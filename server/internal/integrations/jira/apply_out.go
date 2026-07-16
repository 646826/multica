package jira

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

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
