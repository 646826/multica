package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// apply_in.go — inbound appliers (Jira → Multica). All Multica mutations run
// through the ratified service paths with real event publication (AD-6);
// appliers own their per-item bookkeeping at apply time (AD-15), and every
// local write refreshes the Link's local snapshot in the same flow
// (signature-forwarding, AD-5) so sync's own writes never echo outward.

// markerKey is the issue.metadata key stamping a mirror with its Jira issue
// id — the AD-15 crash-window resolver looks it up before ever re-creating.
const markerKey = "jira_sync_id"

// importIssue realizes FR-10 for one observed, un-Linked Jira issue:
// claim (pending Link) → resolve (marker adoption) → create (service path)
// → stamp (marker) → finalize (Link + seeded snapshots). Exactly-once effect
// under re-observation; the residual crash window between create-commit and
// marker-stamp is documented and journaled when the resolver later adopts.
func (w *Worker) importIssue(ctx context.Context, conn db.JiraConnection, sm StatusMap, obs ObservedIssue, cycleID pgtype.UUID) error {
	// Non-terminal gate (FR-10): issues whose Jira status category is Done
	// never import — neither at first enable nor when appearing later.
	if obs.StatusCategory == "done" {
		return nil
	}

	link, err := w.Q.UpsertPendingJiraLink(ctx, db.UpsertPendingJiraLinkParams{
		ConnectionID: conn.ID,
		WorkspaceID:  conn.WorkspaceID,
		JiraIssueID:  obs.ID,
		JiraKey:      obs.Key,
	})
	if err != nil {
		return fmt.Errorf("claim link: %w", err)
	}
	if link.State != "pending" {
		return nil // already imported (or dormant/orphaned — not create's business)
	}

	// Resolver: a previous attempt may have created the issue but crashed
	// before finalizing — adopt it by marker instead of duplicating.
	marker, _ := json.Marshal(map[string]string{markerKey: obs.ID})
	if existingID, err := w.Q.FindIssueIDByJiraMarker(ctx, db.FindIssueIDByJiraMarkerParams{
		WorkspaceID: conn.WorkspaceID,
		Column2:     marker,
	}); err == nil && existingID.Valid {
		_ = w.Journal.Record(ctx, conn, cycleID, JournalImportAdopted, existingID, obs.Key, map[string]any{
			"reason": "marker found after interrupted import",
		})
		return w.finalizeImport(ctx, link, existingID, obs, sm)
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("marker resolve: %w", err)
	}

	// Status mapping; unmapped statuses land in backlog, loudly (FR-10).
	status, mapped := sm.In[obs.StatusID]
	if !mapped {
		status = "backlog"
		_ = w.Journal.Record(ctx, conn, cycleID, JournalStatusUnmapped, pgtype.UUID{}, obs.Key, map[string]any{
			"jira_status_id": obs.StatusID, "jira_status": obs.StatusName, "at": "import",
		})
	}

	title := obs.Summary
	if title == "" {
		title = obs.Key
	}
	res, err := w.Issues.Create(ctx, service.IssueCreateParams{
		WorkspaceID:    conn.WorkspaceID,
		Title:          title,
		Description:    pgtype.Text{String: obs.DescriptionMD, Valid: obs.DescriptionMD != ""},
		Status:         status,
		Priority:       "none",
		CreatorType:    "member",
		CreatorID:      conn.ConnectedByID,
		AllowDuplicate: true, // dedupe is by Jira identity (the Link), not title
	}, service.IssueCreateOpts{})
	if err != nil {
		return fmt.Errorf("create mirror issue: %w", err)
	}

	markerValue, _ := json.Marshal(obs.ID)
	if _, err := w.Q.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID:          res.Issue.ID,
		WorkspaceID: conn.WorkspaceID,
		Key:         markerKey,
		Value:       markerValue,
	}); err != nil {
		// Marker failure narrows exactly to the documented residual window;
		// finalize still proceeds (the Link itself is the primary identity).
		_ = w.Journal.Record(ctx, conn, cycleID, JournalImportMarkerFailed, res.Issue.ID, obs.Key, map[string]any{
			"error": redactError(err),
		})
	}

	if err := w.finalizeImport(ctx, link, res.Issue.ID, obs, sm); err != nil {
		return err
	}
	link.IssueID = res.Issue.ID
	if cerr := w.mirrorComments(ctx, conn, link, cycleID); cerr != nil {
		_ = w.Journal.Record(ctx, conn, cycleID, JournalItemDirty, res.Issue.ID, obs.Key, map[string]any{
			"error": redactError(cerr), "at": "import_comments",
		})
	}
	return nil
}

// finalizeImport seeds the two-sided snapshots from the imported values: both
// sides now agree by construction, so the next cycle sees a quiescent pair.
func (w *Worker) finalizeImport(ctx context.Context, link db.JiraLink, issueID pgtype.UUID, obs ObservedIssue, sm StatusMap) error {
	items := ItemsV1{V: 1, Fields: map[string]ItemState{}}
	items.Title = ItemState{RemoteSHA: SHA(obs.Summary), LocalSHA: SHA(obs.Summary)}
	items.Description = ItemState{RemoteSHA: SHA(obs.DescriptionMD), LocalSHA: SHA(obs.DescriptionMD)}
	localStatus := sm.In[obs.StatusID]
	if localStatus == "" {
		localStatus = "backlog"
	}
	items.Status = StatusState{RemoteID: obs.StatusID, Local: localStatus}
	// Labels deliberately start empty: the labels facet (Story 5.1) pulls
	// them on its first cycle through the normal planner path.
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return w.Q.FinalizeJiraLinkInbound(ctx, db.FinalizeJiraLinkInboundParams{
		ID:      link.ID,
		IssueID: issueID,
		JiraKey: obs.Key,
		Items:   raw,
	})
}

// --- Inbound updates (Story 2.4) ---

// dirtyLadder is the transient-failure retry backoff (operational envelope).
var dirtyLadder = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 6 * time.Hour}

func nextRetryAt(now time.Time, retryCount int32) time.Time {
	idx := int(retryCount)
	if idx >= len(dirtyLadder) {
		idx = len(dirtyLadder) - 1
	}
	return now.Add(dirtyLadder[idx])
}

// updateIssue reconciles one Linked pair from a fresh remote observation:
// plan against the two-sided snapshots, apply the inbound actions through the
// system-actor paths, forward the snapshots (AD-5), and journal every skip.
// Outbound actions the planner emits are deferred to the outbound appliers
// (Epic 4) — snapshots stay unchanged for them, so nothing is lost.
// A failure marks the Link dirty (ladder) and never fails the cycle (NFR-3).
func (w *Worker) updateIssue(ctx context.Context, conn db.JiraConnection, sm StatusMap, fieldMap []FieldMapRow, link db.JiraLink, obs ObservedIssue, cycleID pgtype.UUID) {
	if err := w.updateIssueOnce(ctx, conn, sm, fieldMap, link, obs, cycleID); err != nil {
		_ = w.Journal.Record(ctx, conn, cycleID, JournalItemDirty, link.IssueID, obs.Key, map[string]any{
			"error": redactError(err), "retry_count": link.RetryCount + 1,
		})
		if merr := w.Q.MarkJiraLinkDirty(ctx, db.MarkJiraLinkDirtyParams{
			ID:      link.ID,
			RetryAt: pgtype.Timestamptz{Time: nextRetryAt(w.now(), link.RetryCount), Valid: true},
		}); merr != nil {
			slog.Error("jira: mark dirty failed", "link_id", uuidStr(link.ID), "error", merr)
		}
		return
	}
	if link.Dirty {
		if cerr := w.Q.ClearJiraLinkDirty(ctx, link.ID); cerr != nil {
			slog.Error("jira: clear dirty failed", "link_id", uuidStr(link.ID), "error", cerr)
		} else {
			_ = w.Journal.Record(ctx, conn, cycleID, JournalItemRecovered, link.IssueID, obs.Key, nil)
		}
	}
}

func (w *Worker) updateIssueOnce(ctx context.Context, conn db.JiraConnection, sm StatusMap, fieldMap []FieldMapRow, link db.JiraLink, obs ObservedIssue, cycleID pgtype.UUID) error {
	items, err := ParseItems(link.Items)
	if err != nil {
		return err
	}
	issue, err := w.Q.GetIssue(ctx, link.IssueID)
	if err != nil {
		return fmt.Errorf("load local issue: %w", err)
	}
	settings := settingsFromConnection(conn)
	loc := &LocalIssue{
		Title:         issue.Title,
		DescriptionMD: CanonicalMarkdown(issue.Description.String),
		Status:        issue.Status,
	}

	actions := PlanIssue(PlanInput{
		Settings:  settings,
		StatusMap: sm,
		FieldMap:  fieldMap,
		Items:     items,
		Remote:    &obs,
		Local:     loc,
	})

	changedFields := false
	newTitle, newDescription := issue.Title, issue.Description
	for _, act := range actions {
		switch act.Kind {
		case ActInTitle:
			newTitle = act.Value
			changedFields = true
			items.Title = ItemState{RemoteSHA: SHA(act.Value), LocalSHA: SHA(act.Value), BreadcrumbFor: items.Title.BreadcrumbFor}
		case ActInDescription:
			newDescription = pgtype.Text{String: act.Value, Valid: act.Value != ""}
			changedFields = true
			items.Description = ItemState{RemoteSHA: SHA(act.Value), LocalSHA: SHA(act.Value), BreadcrumbFor: items.Description.BreadcrumbFor}
		case ActInStatus:
			updated, uerr := w.Q.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
				ID: issue.ID, Status: act.Target, WorkspaceID: conn.WorkspaceID,
			})
			if uerr != nil {
				return fmt.Errorf("apply status: %w", uerr)
			}
			w.publishIssueUpdated(conn, updated)
			items.Status = StatusState{RemoteID: act.Value, Local: act.Target, BreadcrumbFor: items.Status.BreadcrumbFor}
			issue.Status = act.Target
		case ActBreadcrumbIn:
			if berr := w.postLocalBreadcrumb(ctx, conn, issue, act); berr != nil {
				return fmt.Errorf("breadcrumb: %w", berr)
			}
			switch act.Item {
			case "title":
				items.Title.BreadcrumbFor = SHA(act.Old)
			case "description":
				items.Description.BreadcrumbFor = SHA(act.Old)
			case "status":
				items.Status.BreadcrumbFor = SHA(act.Old)
			}
		case ActSkip:
			_ = w.Journal.Record(ctx, conn, cycleID, act.Journal, link.IssueID, obs.Key, act.Detail)
		default:
			// Outbound kinds (out_*, breadcrumb_remote) are Epic 4's job and
			// label/field inbound applies land with Epic 5; snapshots for
			// those items stay untouched so their appliers see the same
			// divergence on their cycle.
		}
	}

	if changedFields {
		if _, uerr := w.Q.UpdateIssue(ctx, db.UpdateIssueParams{
			ID:          issue.ID,
			Title:       pgtype.Text{String: newTitle, Valid: true},
			Description: newDescription,
			// Non-COALESCE columns must be passed through, or the update
			// would clear them (UpdateIssue overwrites nargs verbatim).
			AssigneeType:  issue.AssigneeType,
			AssigneeID:    issue.AssigneeID,
			StartDate:     issue.StartDate,
			DueDate:       issue.DueDate,
			ParentIssueID: issue.ParentIssueID,
			ProjectID:     issue.ProjectID,
			Stage:         issue.Stage,
		}); uerr != nil {
			return fmt.Errorf("apply fields: %w", uerr)
		}
		w.publishIssueUpdated(conn, issue)
	}

	// Track the remote status pair even when nothing was applied this cycle
	// (change-driven detection depends on the last-observed remote id).
	if obs.StatusID != "" && items.Status.RemoteID != obs.StatusID {
		// Status changed remotely but was not applied (skip/unmapped/out-mode):
		// record the observation so the same change does not re-plan forever.
		items.Status.RemoteID = obs.StatusID
	}

	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return w.Q.UpdateJiraLinkItems(ctx, db.UpdateJiraLinkItemsParams{
		ID: link.ID, Items: raw, JiraKey: obs.Key,
	})
}

// publishIssueUpdated mirrors the ratified system-actor publication
// (handler/github.go:1364-1384) so realtime/notification listeners see the
// change exactly as they would a GitHub-driven one.
func (w *Worker) publishIssueUpdated(conn db.JiraConnection, issue db.Issue) {
	if w.Bus == nil {
		return
	}
	w.Bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: uuidStr(conn.WorkspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload: map[string]any{
			"issue_id": uuidStr(issue.ID),
			"status":   issue.Status,
			"source":   "jira_sync",
		},
	})
}

// postLocalBreadcrumb records an overwrite on the Multica side as an inert
// system comment (FR-8/FR-21): author_type=system, zero author, type=system —
// it never enters trigger paths and is not realtime-published in v1
// (visible on the issue thread; realtime wiring arrives with the comment
// bridge in Epic 3).
func (w *Worker) postLocalBreadcrumb(ctx context.Context, conn db.JiraConnection, issue db.Issue, act Action) error {
	content := fmt.Sprintf("Jira sync: %s was overwritten by the leading system.\n\nPrevious value:\n\n%s", act.Item, truncateForComment(act.Old))
	_, err := w.Q.CreateComment(ctx, db.CreateCommentParams{
		IssueID:     issue.ID,
		WorkspaceID: conn.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true}, // zero UUID: the platform system actor
		Content:     content,
		Type:        "system",
	})
	return err
}

func truncateForComment(s string) string {
	if len(s) > 2000 {
		return s[:2000] + "\n…(truncated)"
	}
	return s
}

// settingsFromConnection projects the persisted row into planner Settings.
func settingsFromConnection(conn db.JiraConnection) Settings {
	return Settings{
		Enabled:              conn.Enabled,
		Mode:                 conn.Mode,
		LeadingSystem:        conn.LeadingSystem,
		CommentsEnabled:      conn.CommentsEnabled,
		LabelsEnabled:        conn.LabelsEnabled,
		CustomFieldsEnabled:  conn.CustomFieldsEnabled,
		CreateFromJira:       conn.CreateFromJira,
		CreateToJira:         conn.CreateToJira,
		JQLFilter:            conn.JqlFilter,
		LabelPrefix:          conn.LabelPrefix,
		MentionBridgeEnabled: conn.MentionBridgeEnabled,
		OutboundIssueType:    conn.OutboundIssueType,
		StatusMap:            conn.StatusMap,
		FieldMap:             conn.FieldMap,
		TagRules:             conn.TagRules,
		CycleIntervalSeconds: conn.CycleIntervalSeconds,
	}
}

// --- Inbound comments (Story 3.1) ---

// mirrorComments pulls new public Jira comments onto the Multica twin,
// exactly once (identity = jira comment id in jira_comment_link). Restricted
// comments were already stripped of their bodies by the client (fail-closed,
// FR-18); here they only count into Health. Service-account comments are
// never mirrored as content — they are sync's own writes (actor filter),
// scanned for outbound intent markers by the Epic-3 outbound story.
func (w *Worker) mirrorComments(ctx context.Context, conn db.JiraConnection, link db.JiraLink, cycleID pgtype.UUID) error {
	if !conn.CommentsEnabled {
		return nil
	}
	if dir := EffectiveDirection(settingsFromConnection(conn), FacetComments); dir != DirPull && dir != DirTwoWay {
		return nil
	}
	client, err := w.Svc.ClientFor(conn)
	if err != nil {
		return err
	}
	comments, err := client.ListComments(ctx, link.JiraIssueID)
	if err != nil {
		return err
	}
	for _, rc := range comments {
		if _, lerr := w.Q.GetJiraCommentLinkByJiraID(ctx, db.GetJiraCommentLinkByJiraIDParams{
			ConnectionID: conn.ID, JiraCommentID: rc.ID,
		}); lerr == nil {
			continue // already mirrored (or ours)
		} else if !errors.Is(lerr, pgx.ErrNoRows) {
			return lerr
		}

		if rc.Restricted {
			_ = w.Journal.Record(ctx, conn, cycleID, JournalRestrictedCommentDropped, link.IssueID, link.JiraKey, map[string]any{
				"jira_comment_id": rc.ID,
			})
			// Record the identity so the drop is decided once, not per cycle.
			if err := w.recordCommentLink(ctx, conn, link, pgtype.UUID{}, rc.ID); err != nil {
				return err
			}
			continue
		}
		if rc.AuthorID != "" && rc.AuthorID == conn.ServiceAccountID {
			// Sync's own Jira comment observed back: adopt a pending outbound
			// intent by its marker (crash between POST and finalize, AD-15),
			// else record identity only. Never mirrored as content.
			adopted, aerr := w.adoptOutboundComment(ctx, conn, rc, cycleID, link.IssueID, link.JiraKey)
			if aerr != nil {
				return aerr
			}
			if !adopted {
				if err := w.recordCommentLink(ctx, conn, link, pgtype.UUID{}, rc.ID); err != nil {
					return err
				}
			}
			continue
		}

		body, _ := ADFToMarkdown(rc.BodyADF)
		author := rc.AuthorName
		if author == "" {
			author = "unknown"
		}
		content := fmt.Sprintf("**From Jira — %s**\n\n%s", author, body)

		tx, err := w.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		qtx := w.Q.WithTx(tx)
		comment, cerr := qtx.CreateComment(ctx, db.CreateCommentParams{
			IssueID:     link.IssueID,
			WorkspaceID: conn.WorkspaceID,
			AuthorType:  "system",
			AuthorID:    pgtype.UUID{Valid: true}, // zero UUID: platform system actor
			Content:     content,
			Type:        "comment",
		})
		if cerr == nil {
			_, cerr = qtx.CreateJiraCommentLinkInbound(ctx, db.CreateJiraCommentLinkInboundParams{
				ConnectionID: conn.ID, WorkspaceID: conn.WorkspaceID,
				IssueID: link.IssueID, CommentID: comment.ID, JiraCommentID: rc.ID,
			})
		}
		if cerr != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("mirror comment %s: %w", rc.ID, cerr)
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// recordCommentLink stores an identity-only row (restricted or self-authored
// comments): decided once, never re-examined, never mirrored.
func (w *Worker) recordCommentLink(ctx context.Context, conn db.JiraConnection, link db.JiraLink, commentID pgtype.UUID, jiraCommentID string) error {
	_, err := w.Q.CreateJiraCommentLinkInbound(ctx, db.CreateJiraCommentLinkInboundParams{
		ConnectionID: conn.ID, WorkspaceID: conn.WorkspaceID,
		IssueID: link.IssueID, CommentID: commentID, JiraCommentID: jiraCommentID,
	})
	return err
}
