package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
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

// outboundMarkerRe recognizes a Jira issue that sync itself created (its
// description footer carries the multica-issue marker). The inbound importer
// skips these so a Multica-origin issue is never re-imported as a duplicate.
var outboundMarkerRe = regexp.MustCompile(`multica-issue-[0-9a-f-]{36}`)

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
	// Skip our own outbound-created issues: their description carries the
	// multica-issue marker, and the outbound adopt pass owns finalizing them.
	// Importing here would create a duplicate mirror of a Multica-origin issue.
	if outboundMarkerRe.MatchString(obs.DescriptionMD) {
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

	// Atomically stamp the adopt marker AND finalize the link: after this tx
	// commits, a re-import finds either the marker (adopt) or a non-pending
	// link (skip) — no duplicate. The only residual is a crash in the gap
	// between the Issues.Create commit and this tx (documented accepted
	// residual, AD-15): a re-import then re-creates once, self-corrected the
	// moment the marker lands. That window is a few statements wide.
	markerValue, _ := json.Marshal(obs.ID)
	itemsRaw, ierr := seededImportItems(obs, sm)
	if ierr != nil {
		return ierr
	}
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	qtx := w.Q.WithTx(tx)
	if _, merr := qtx.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID: res.Issue.ID, WorkspaceID: conn.WorkspaceID, Key: markerKey, Value: markerValue,
	}); merr != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("stamp import marker: %w", merr)
	}
	if ferr := qtx.FinalizeJiraLinkInbound(ctx, db.FinalizeJiraLinkInboundParams{
		ID: link.ID, IssueID: res.Issue.ID, JiraKey: obs.Key, Items: itemsRaw,
	}); ferr != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("finalize import: %w", ferr)
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return cerr
	}
	link.IssueID = res.Issue.ID
	link.State = "ok"
	if cerr := w.mirrorComments(ctx, conn, link, cycleID); cerr != nil {
		_ = w.Journal.Record(ctx, conn, cycleID, JournalItemDirty, res.Issue.ID, obs.Key, map[string]any{
			"error": redactError(cerr), "at": "import_comments",
		})
	}
	return nil
}

// finalizeImport seeds the two-sided snapshots from the imported values: both
// sides now agree by construction, so the next cycle sees a quiescent pair.
// Used by the marker-adoption path (the create path finalizes in its own tx).
func (w *Worker) finalizeImport(ctx context.Context, link db.JiraLink, issueID pgtype.UUID, obs ObservedIssue, sm StatusMap) error {
	raw, err := seededImportItems(obs, sm)
	if err != nil {
		return err
	}
	return w.Q.FinalizeJiraLinkInbound(ctx, db.FinalizeJiraLinkInboundParams{
		ID: link.ID, IssueID: issueID, JiraKey: obs.Key, Items: raw,
	})
}

// seededImportItems builds the initial two-sided snapshot for an imported
// issue: both sides agree by construction so the next cycle is quiescent.
func seededImportItems(obs ObservedIssue, sm StatusMap) ([]byte, error) {
	items := ItemsV1{V: 1, Fields: map[string]ItemState{}}
	items.Title = ItemState{RemoteSHA: SHA(obs.Summary), LocalSHA: SHA(obs.Summary)}
	items.Description = ItemState{RemoteSHA: SHA(obs.DescriptionMD), LocalSHA: SHA(obs.DescriptionMD)}
	localStatus := sm.In[obs.StatusID]
	if localStatus == "" {
		localStatus = "backlog"
	}
	items.Status = StatusState{RemoteID: obs.StatusID, Local: localStatus}
	return json.Marshal(items)
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
func (w *Worker) updateIssue(ctx context.Context, conn db.JiraConnection, sm StatusMap, fieldMap []FieldMapRow, link db.JiraLink, obs *ObservedIssue, cycleID pgtype.UUID) {
	if err := w.updateIssueOnce(ctx, conn, sm, fieldMap, link, obs, cycleID); err != nil {
		_ = w.Journal.Record(ctx, conn, cycleID, JournalItemDirty, link.IssueID, link.JiraKey, map[string]any{
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
			_ = w.Journal.Record(ctx, conn, cycleID, JournalItemRecovered, link.IssueID, link.JiraKey, nil)
		}
	}
}

func (w *Worker) updateIssueOnce(ctx context.Context, conn db.JiraConnection, sm StatusMap, fieldMap []FieldMapRow, link db.JiraLink, obs *ObservedIssue, cycleID pgtype.UUID) error {
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
	labelRows, lerr := w.Q.ListLabelsByIssue(ctx, db.ListLabelsByIssueParams{IssueID: issue.ID, WorkspaceID: conn.WorkspaceID})
	if lerr != nil {
		// A read failure here would leave loc.Labels empty and the planner
		// could remove every propagated label — fail into the dirty ladder.
		return fmt.Errorf("load issue labels: %w", lerr)
	}
	for _, l := range labelRows {
		loc.Labels = append(loc.Labels, l.Name)
	}
	loc.Fields = map[string]string{}
	if len(issue.Properties) > 0 {
		var props map[string]json.RawMessage
		if json.Unmarshal(issue.Properties, &props) == nil {
			for _, row := range fieldMap {
				if v, ok := props[row.PropertyID]; ok {
					loc.Fields[row.PropertyID] = canonicalPropertyValue(v)
				}
			}
		}
	}

	actions := PlanIssue(PlanInput{
		Settings:  settings,
		StatusMap: sm,
		FieldMap:  fieldMap,
		Items:     items,
		Remote:    obs,
		Local:     loc,
	})

	changedFields := false
	statusTouched := false
	outFields := map[string]any{}
	var outApply []func(*ObservedIssue)
	var outBreadcrumbs []Action
	var outLabelsAct *Action
	var outFieldActs []Action
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
			statusTouched = true
		case ActBreadcrumbIn:
			if berr := w.postLocalBreadcrumb(ctx, conn, issue, act); berr != nil {
				return fmt.Errorf("breadcrumb: %w", berr)
			}
			markBreadcrumb(&items, act)
		case ActOutTransition:
			if err := w.applyOutTransition(ctx, conn, link, &items, act, cycleID); err != nil {
				return err
			}
			statusTouched = true
		case ActOutTitle:
			outFields["summary"] = act.Value
			outApply = append(outApply, func(refreshed *ObservedIssue) {
				v := act.Value
				if refreshed != nil {
					v = refreshed.Summary
				}
				items.Title = ItemState{RemoteSHA: SHA(v), LocalSHA: SHA(act.Value), BreadcrumbFor: items.Title.BreadcrumbFor}
			})
		case ActOutDesc:
			outFields["description"] = MarkdownToADFAny(act.Value)
			outApply = append(outApply, func(refreshed *ObservedIssue) {
				v := act.Value
				if refreshed != nil {
					v = refreshed.DescriptionMD
				}
				items.Description = ItemState{RemoteSHA: SHA(v), LocalSHA: SHA(act.Value), BreadcrumbFor: items.Description.BreadcrumbFor}
			})
		case ActBreadcrumbOut:
			outBreadcrumbs = append(outBreadcrumbs, act)
		case ActInLabels:
			if lerr := w.applyInLabels(ctx, conn, issue.ID, &items, act); lerr != nil {
				return lerr
			}
		case ActOutLabels:
			outLabelsAct = &act
		case ActInField:
			if ferr := w.applyInField(ctx, conn, issue.ID, &items, act, cycleID); ferr != nil {
				return ferr
			}
		case ActOutField:
			a := act
			outFieldActs = append(outFieldActs, a)
		case ActSkip:
			_ = w.Journal.Record(ctx, conn, cycleID, act.Journal, link.IssueID, link.JiraKey, act.Detail)
		default:
			// Outbound kinds (out_*, breadcrumb_remote) are Epic 4's job and
			// label/field inbound applies land with Epic 5; snapshots for
			// those items stay untouched so their appliers see the same
			// divergence on their cycle.
		}
	}

	// Outbound labels fold into the same PUT (FR-22): compute the target Jira
	// set from the current propagated set plus/minus the planned changes, with
	// space-to-dash transforms journaled once.
	if outLabelsAct != nil {
		targetState := nextLabelState(items.Labels, *outLabelsAct)
		var jiraLabels []string
		for _, name := range targetState.Propagated {
			safe, changed := jiraLabelSafe(name)
			if changed {
				_ = w.Journal.Record(ctx, conn, cycleID, JournalLabelTransformed, issue.ID, link.JiraKey, map[string]any{
					"from": name, "to": safe,
				})
			}
			jiraLabels = append(jiraLabels, safe)
		}
		outFields["labels"] = jiraLabels
		items.Labels = targetState
	}

	// Outbound mapped custom fields fold into the same PUT (FR-24).
	fieldMapByExternal := map[string]FieldMapRow{}
	for _, r := range fieldMap {
		fieldMapByExternal[r.ExternalField] = r
	}
	for _, act := range outFieldActs {
		external, _ := act.Detail["external_field"].(string)
		row, ok := fieldMapByExternal[external]
		if !ok {
			continue
		}
		jt, jok := w.jiraFieldType(ctx, conn, external)
		if !jok {
			_ = w.Journal.Record(ctx, conn, cycleID, JournalFieldSkipped, issue.ID, link.JiraKey, map[string]any{
				"external_field": external, "reason": "jira field catalog unavailable",
			})
			continue
		}
		wire, wok := PropertyToJiraRaw(json.RawMessage(act.Value), jt)
		if !wok {
			_ = w.Journal.Record(ctx, conn, cycleID, JournalFieldSkipped, issue.ID, link.JiraKey, map[string]any{
				"external_field": external, "reason": "value not mappable to jira type " + jt,
			})
			continue
		}
		outFields[external] = wire
		itemState := items.Fields[external]
		itemState.LocalSHA = SHA(act.Value)
		itemState.RemoteSHA = SHA(act.Value)
		items.Fields[external] = itemState
		_ = row
	}

	// Coalesced outbound field PUT (FR-12 outbound / FR-20): at most one write
	// per issue per cycle; refresh the remote snapshots by read-back so lossy
	// ADF round-trips reach a fixpoint instead of churning.
	if len(outFields) > 0 || len(outBreadcrumbs) > 0 {
		client, cerr := w.Svc.ClientFor(conn)
		if cerr != nil {
			return cerr
		}
		for _, bc := range outBreadcrumbs {
			body := fmt.Sprintf("*Multica sync:* %s was overwritten by the leading system.\n\nPrevious value:\n\n%s", bc.Item, truncateForComment(bc.Old))
			if _, perr := client.AddComment(ctx, link.JiraIssueID, MarkdownToADF(body)); perr != nil {
				return fmt.Errorf("outbound breadcrumb: %w", perr)
			}
		}
		if len(outFields) > 0 {
			if perr := client.UpdateIssueFields(ctx, link.JiraIssueID, outFields); perr != nil {
				return fmt.Errorf("outbound fields: %w", perr)
			}
		}
		var refreshed *ObservedIssue
		var refreshedFields map[string]string
		needFieldReadback := false
		for k := range outFields {
			if k != "summary" && k != "description" && k != "labels" {
				needFieldReadback = true
			}
		}
		if len(outApply) > 0 || needFieldReadback {
			var readbackIDs []string
			for _, r := range fieldMap {
				readbackIDs = append(readbackIDs, r.ExternalField)
			}
			if ri, gerr := client.GetIssue(ctx, link.JiraIssueID, readbackIDs); gerr == nil {
				md, _ := ADFToMarkdown(ri.DescriptionADF)
				refreshed = &ObservedIssue{Summary: ri.Summary, DescriptionMD: md, StatusID: ri.StatusID}
				refreshedFields = map[string]string{}
				for k, v := range ri.Fields {
					refreshedFields[k] = string(v)
				}
			}
		}
		for _, apply := range outApply {
			apply(refreshed)
		}
		// Forward pushed custom-field RemoteSHA from the actual Jira value so
		// the next observation sees no phantom remote change (FR-20 fixpoint).
		for external := range outFields {
			if external == "summary" || external == "description" || external == "labels" {
				continue
			}
			st := items.Fields[external]
			if refreshedFields != nil {
				if rv, ok := refreshedFields[external]; ok {
					st.RemoteSHA = SHA(rv)
				}
			}
			items.Fields[external] = st
		}
		for _, bc := range outBreadcrumbs {
			markBreadcrumb(&items, bc)
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
	// (change-driven detection depends on the last-observed remote id) — but
	// never clobber a snapshot an applier just forwarded.
	if !statusTouched && obs != nil && obs.StatusID != "" && items.Status.RemoteID != obs.StatusID {
		// Status changed remotely but was not applied (skip/unmapped/out-mode):
		// record the observation so the same change does not re-plan forever.
		items.Status.RemoteID = obs.StatusID
	}

	// Agent tagging (FR-27): evaluate rules against the observed signals.
	// Runs after inbound applies so the issue reflects the current state; the
	// activation status change it may make is local-only (exempt from push).
	if _, terr := w.applyTagRules(ctx, conn, issue, &items, obs, cycleID); terr != nil {
		return terr
	}

	jiraKey := link.JiraKey
	if obs != nil && obs.Key != "" {
		jiraKey = obs.Key
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return w.Q.UpdateJiraLinkItems(ctx, db.UpdateJiraLinkItemsParams{
		ID: link.ID, Items: raw, JiraKey: jiraKey,
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
		// Mention bridge (FR-29): rewrite @AgentName in the human body before
		// mirroring so the native trigger machinery can wake the agent.
		bridged, wake, berr := w.bridgeMentions(ctx, conn, body)
		if berr != nil {
			return berr
		}
		content := fmt.Sprintf("**From Jira — %s**\n\n%s", author, bridged)

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
		// Wake each mentioned agent through the native mention-enqueue path
		// (explicit mentions only; the mirrored comment is system-authored so
		// implicit routing never fires — AD-6). A denied invocation is
		// journaled, never silently dropped and never author-faked.
		w.wakeMentionedAgents(ctx, conn, link, comment.ID, wake, cycleID)
	}
	return nil
}

// wakeMentionedAgents enqueues one native mention task per mentioned agent.
func (w *Worker) wakeMentionedAgents(ctx context.Context, conn db.JiraConnection, link db.JiraLink, commentID pgtype.UUID, agentIDs []pgtype.UUID, cycleID pgtype.UUID) {
	if len(agentIDs) == 0 || w.Tasks == nil {
		return
	}
	issue, err := w.Q.GetIssue(ctx, link.IssueID)
	if err != nil {
		_ = w.Journal.Record(ctx, conn, cycleID, JournalMentionDenied, link.IssueID, link.JiraKey, map[string]any{
			"reason": "load issue for mention wake failed", "error": redactError(err),
		})
		return
	}
	for _, agentID := range agentIDs {
		if _, eerr := w.Tasks.EnqueueTaskForMention(ctx, issue, agentID, commentID); eerr != nil {
			_ = w.Journal.Record(ctx, conn, cycleID, JournalMentionDenied, link.IssueID, link.JiraKey, map[string]any{
				"agent_id": uuidStr(agentID), "error": redactError(eerr),
			})
		}
	}
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

// applyOutTransition executes one planned Jira transition (FR-17): resolve
// the available edges, execute the one landing on the mapped target, refresh
// the status snapshot; an unreachable target journals loudly exactly once per
// occurrence (marker) and pairs with transition_recovered on later success.
func (w *Worker) applyOutTransition(ctx context.Context, conn db.JiraConnection, link db.JiraLink, items *ItemsV1, act Action, cycleID pgtype.UUID) error {
	attemptSig := SHA(act.Value + "->" + act.Target)
	client, err := w.Svc.ClientFor(conn)
	if err != nil {
		return err
	}
	transitions, err := client.GetTransitions(ctx, link.JiraIssueID)
	if err != nil {
		return err
	}
	var transitionID string
	for _, tr := range transitions {
		if tr.ToID == act.Target {
			transitionID = tr.ID
			break
		}
	}
	if transitionID == "" {
		// Loud exactly once per occurrence: the marker suppresses repeat
		// journaling, but the cheap probe above keeps running so a workflow
		// change recovers automatically (FR-17).
		if items.Status.UnreachableFor != attemptSig {
			items.Status.UnreachableFor = attemptSig
			_ = w.Journal.Record(ctx, conn, cycleID, JournalTransitionUnreachable, link.IssueID, link.JiraKey, map[string]any{
				"multica_status": act.Value, "target_jira_status_id": act.Target,
			})
		}
		return nil
	}
	if err := client.DoTransition(ctx, link.JiraIssueID, transitionID); err != nil {
		return fmt.Errorf("transition: %w", err)
	}
	if items.Status.UnreachableFor != "" {
		_ = w.Journal.Record(ctx, conn, cycleID, JournalTransitionRecovered, link.IssueID, link.JiraKey, nil)
	}
	items.Status = StatusState{RemoteID: act.Target, Local: act.Value, BreadcrumbFor: items.Status.BreadcrumbFor}
	return nil
}

// markBreadcrumb records that a breadcrumb has been posted for a discarded
// value so it never re-posts (FR-8 once).
func markBreadcrumb(items *ItemsV1, act Action) {
	switch {
	case act.Item == "title":
		items.Title.BreadcrumbFor = SHA(act.Old)
	case act.Item == "description":
		items.Description.BreadcrumbFor = SHA(act.Old)
	case act.Item == "status":
		items.Status.BreadcrumbFor = SHA(act.Old)
	case strings.HasPrefix(act.Item, "field:"):
		external := strings.TrimPrefix(act.Item, "field:")
		st := items.Fields[external]
		st.BreadcrumbFor = SHA(act.Old)
		items.Fields[external] = st
	}
}
