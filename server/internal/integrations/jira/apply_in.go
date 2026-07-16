package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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

	return w.finalizeImport(ctx, link, res.Issue.ID, obs, sm)
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
