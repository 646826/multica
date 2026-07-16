package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// apply_out.go — outbound appliers (Multica → Jira). Every remote creation is
// intent-first (AD-15): a pending identity row with a deterministic marker is
// committed BEFORE the Jira write; the marker travels inside the posted body
// so an interrupted run adopts its own artifact instead of re-posting.

// commentMarker derives the opaque adopt token for one Multica comment.
func commentMarker(commentID pgtype.UUID) string {
	return "mc-" + uuidStr(commentID)[:13]
}

// markerRe extracts adopt tokens from observed self-authored Jira comments.
var markerRe = regexp.MustCompile(`\[(mc-[0-9a-f-]{13})\]`)

// outboundCommentBatch bounds one cycle's comment pushes (budget safety).
const outboundCommentBatch = 100

// syncOutboundComments pushes new human/agent Multica comments on Linked
// pairs to Jira, exactly once across crashes (FR-19). System-authored rows
// (mirrors, Breadcrumbs) never appear here — the source query filters
// author_type to member|agent, so echo is impossible by construction.
func (w *Worker) syncOutboundComments(ctx context.Context, conn db.JiraConnection, cycleID pgtype.UUID) error {
	if !conn.CommentsEnabled {
		return nil
	}
	if dir := EffectiveDirection(settingsFromConnection(conn), FacetComments); dir != DirPush && dir != DirTwoWay {
		return nil
	}
	pending, err := w.Q.ListUnsyncedCommentsForConnection(ctx, db.ListUnsyncedCommentsForConnectionParams{
		ConnectionID: conn.ID, Limit: outboundCommentBatch,
	})
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	client, err := w.Svc.ClientFor(conn)
	if err != nil {
		return err
	}

	for _, row := range pending {
		marker := commentMarker(row.ID)
		link, err := w.Q.ClaimJiraCommentLinkOutbound(ctx, db.ClaimJiraCommentLinkOutboundParams{
			ConnectionID: conn.ID, WorkspaceID: conn.WorkspaceID,
			IssueID: row.IssueID, CommentID: row.ID, Marker: marker,
		})
		if err != nil {
			return fmt.Errorf("claim outbound comment: %w", err)
		}
		if link.State != "pending" {
			continue // finished by an earlier run
		}

		attribution := w.commentAttribution(ctx, conn, row.AuthorType, row.AuthorID)
		body := fmt.Sprintf("%s\n\n%s\n\n[%s]", attribution, row.Content, marker)
		jiraID, perr := client.AddComment(ctx, row.JiraIssueID, MarkdownToADF(body))
		if perr != nil {
			// Leave the intent pending: the next cycle re-observes; if the
			// POST actually landed, the adopt-scan finalizes without a
			// duplicate (AD-15).
			_ = w.Journal.Record(ctx, conn, cycleID, JournalItemDirty, row.IssueID, row.JiraKey, map[string]any{
				"error": redactError(perr), "at": "outbound_comment",
			})
			continue
		}
		if err := w.Q.FinalizeJiraCommentLinkOutbound(ctx, db.FinalizeJiraCommentLinkOutboundParams{
			ID: link.ID, JiraCommentID: jiraID,
		}); err != nil {
			return fmt.Errorf("finalize outbound comment: %w", err)
		}
	}
	return nil
}

// commentAttribution renders the author line Jira users see (NFR-5): agents
// are visibly distinct from members; lookups degrade to the role name.
func (w *Worker) commentAttribution(ctx context.Context, conn db.JiraConnection, authorType string, authorID pgtype.UUID) string {
	name := ""
	switch authorType {
	case "agent":
		if agent, err := w.Q.GetAgent(ctx, authorID); err == nil {
			name = agent.Name
		}
		if name == "" {
			name = "agent"
		}
		return fmt.Sprintf("*%s (agent) via Multica*", name)
	default:
		if user, err := w.Q.GetUser(ctx, authorID); err == nil {
			name = strings.TrimSpace(user.Name)
		}
		if name == "" {
			name = "member"
		}
		return fmt.Sprintf("*%s (member) via Multica*", name)
	}
}

// adoptOutboundComment resolves a pending intent when sync's own posted
// comment is observed back (crash between POST and finalize): the marker in
// the body identifies the intent; adoption finalizes it without re-posting.
func (w *Worker) adoptOutboundComment(ctx context.Context, conn db.JiraConnection, rc RemoteComment, cycleID pgtype.UUID, issueID pgtype.UUID, jiraKey string) (bool, error) {
	md, _ := ADFToMarkdown(rc.BodyADF)
	m := markerRe.FindStringSubmatch(md)
	if m == nil {
		return false, nil
	}
	link, err := w.Q.GetPendingOutboundCommentLinkByMarker(ctx, db.GetPendingOutboundCommentLinkByMarkerParams{
		ConnectionID: conn.ID, Marker: m[1],
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err := w.Q.FinalizeJiraCommentLinkOutbound(ctx, db.FinalizeJiraCommentLinkOutboundParams{
		ID: link.ID, JiraCommentID: rc.ID,
	}); err != nil {
		return false, err
	}
	_ = w.Journal.Record(ctx, conn, cycleID, JournalCommentAdopted, issueID, jiraKey, map[string]any{
		"jira_comment_id": rc.ID, "marker": m[1],
	})
	return true, nil
}

// issueMarker stamps a Multica issue's identity into a Jira-safe token used to
// adopt a Jira issue created by an interrupted run (AD-15).
func issueMarker(issueID pgtype.UUID) string {
	return "multica-issue-" + uuidStr(issueID)
}

// syncOutboundCreates creates Jira issues for new Multica issues when the
// Multica→Jira creation flow is on (FR-9). Intent-first: a pending Link with
// the marker is committed before the POST; the marker rides in the created
// issue's description footer so an interrupted run adopts instead of
// re-creating. Mirror mode short-circuits (defense in depth).
func (w *Worker) syncOutboundCreates(ctx context.Context, conn db.JiraConnection, cycleID pgtype.UUID) error {
	if conn.Mode == ModeMirror || !conn.CreateToJira {
		return nil
	}
	if strings.TrimSpace(conn.ProjectKey) == "" {
		return nil
	}
	cut := conn.LocalCursor
	if !cut.Valid {
		cut = pgtype.Timestamptz{Time: w.now().Add(-time.Hour), Valid: true}
	}
	issues, err := w.Q.ListUnlinkedLocalIssues(ctx, db.ListUnlinkedLocalIssuesParams{
		WorkspaceID: conn.WorkspaceID,
		CreatedAt:   pgtype.Timestamptz{Time: cut.Time.Add(-observeOverlap), Valid: true},
		Limit:       int32(outboundCommentBatch),
	})
	if err != nil {
		return err
	}
	if len(issues) == 0 {
		return nil
	}
	client, err := w.Svc.ClientFor(conn)
	if err != nil {
		return err
	}
	for _, issue := range issues {
		// Skip mirrors we created inbound (they carry the jira_sync_id marker
		// and would already have a link; the NOT EXISTS covers finalized ones,
		// this guards the create-race window).
		if hasInboundMarker(issue.Metadata) {
			continue
		}
		if err := w.createOneJiraIssue(ctx, conn, client, issue, cycleID); err != nil {
			_ = w.Journal.Record(ctx, conn, cycleID, JournalItemDirty, issue.ID, "", map[string]any{
				"error": redactError(err), "at": "outbound_create",
			})
		}
	}
	return nil
}

func hasInboundMarker(metadata []byte) bool {
	return strings.Contains(string(metadata), markerKey)
}

func (w *Worker) createOneJiraIssue(ctx context.Context, conn db.JiraConnection, client *Client, issue db.Issue, cycleID pgtype.UUID) error {
	marker := issueMarker(issue.ID)
	// Intent: pending Link keyed by a synthetic external id (the marker) until
	// finalized with the real Jira id.
	link, err := w.Q.UpsertPendingJiraLink(ctx, db.UpsertPendingJiraLinkParams{
		ConnectionID: conn.ID, WorkspaceID: conn.WorkspaceID,
		JiraIssueID: marker, JiraKey: "",
	})
	if err != nil {
		return fmt.Errorf("claim create intent: %w", err)
	}
	if link.State != "pending" {
		return nil
	}
	if link.IssueID.Valid && link.IssueID != issue.ID {
		return nil // marker already bound to another issue (shouldn't happen)
	}

	// Adopt: an interrupted run may have created the Jira issue already.
	if existing, _, serr := client.SearchJQLIDs(ctx, fmt.Sprintf("project = %q AND description ~ %q", conn.ProjectKey, marker), 1); serr == nil && len(existing) > 0 {
		return w.finalizeCreate(ctx, conn, link, issue, existing[0], "")
	}

	desc := issue.Description.String + "\n\n[" + marker + "]"
	created, cerr := client.CreateIssue(ctx, conn.ProjectKey, conn.OutboundIssueType, map[string]any{
		"summary":     truncateSummary(issue.Title),
		"description": MarkdownToADFAny(desc),
	})
	if cerr != nil {
		var apiErr *APIError
		if errors.As(cerr, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 {
			// Required-field / validation rejection: loud, no blind retry.
			_ = w.Journal.Record(ctx, conn, cycleID, JournalCreateRejected, issue.ID, "", map[string]any{
				"error": redactError(cerr),
			})
			return nil
		}
		return cerr // transient: intent stays pending, retried next cycle
	}
	return w.finalizeCreate(ctx, conn, link, issue, created.ID, created.Key)
}

func (w *Worker) finalizeCreate(ctx context.Context, conn db.JiraConnection, link db.JiraLink, issue db.Issue, jiraID, jiraKey string) error {
	items := ItemsV1{V: 1, Fields: map[string]ItemState{}}
	items.Title = ItemState{RemoteSHA: SHA(issue.Title), LocalSHA: SHA(issue.Title)}
	localDesc := CanonicalMarkdown(issue.Description.String)
	items.Description = ItemState{RemoteSHA: SHA(localDesc), LocalSHA: SHA(localDesc)}
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	if err := w.Q.FinalizeJiraLinkCreate(ctx, db.FinalizeJiraLinkCreateParams{
		ID: link.ID, JiraIssueID: jiraID, IssueID: issue.ID, JiraKey: jiraKey, Items: raw,
	}); err != nil {
		return err
	}
	_ = w.Journal.Record(ctx, conn, pgtype.UUID{}, JournalOutboundCreated, issue.ID, jiraKey, nil)
	return nil
}

func truncateSummary(s string) string {
	if len(s) > 255 {
		return s[:255]
	}
	if s == "" {
		return "(untitled)"
	}
	return s
}
