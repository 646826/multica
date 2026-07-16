package jira

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// apply_labels.go — label appliers (FR-22). Inbound reconciles the Multica
// label set (create-missing by name, attach/detach), removing only labels
// sync itself propagated so native labels survive; outbound folds the label
// set into the coalesced field PUT. Jira labels cannot contain spaces, so
// outbound names are transformed and the transform is journaled once.

// applyInLabels reconciles labels onto the Multica issue and updates the
// propagated-set snapshot (AD-5 materialized sets).
func (w *Worker) applyInLabels(ctx context.Context, conn db.JiraConnection, issueID pgtype.UUID, items *ItemsV1, act Action) error {
	nameToID, err := w.labelIndex(ctx, conn.WorkspaceID)
	if err != nil {
		return err
	}
	for _, name := range act.Add {
		id, ok := nameToID[strings.ToLower(name)]
		if !ok {
			created, cerr := w.Q.CreateLabel(ctx, db.CreateLabelParams{
				WorkspaceID: conn.WorkspaceID, ResourceType: "issue", Name: name, Color: "gray",
			})
			if cerr != nil {
				return fmt.Errorf("create label %q: %w", name, cerr)
			}
			id = created.ID
			nameToID[strings.ToLower(name)] = id
		}
		if aerr := w.Q.AttachLabelToIssue(ctx, db.AttachLabelToIssueParams{
			IssueID: issueID, LabelID: id, WorkspaceID: conn.WorkspaceID,
		}); aerr != nil {
			return fmt.Errorf("attach label %q: %w", name, aerr)
		}
	}
	for _, name := range act.Remove {
		id, ok := nameToID[strings.ToLower(name)]
		if !ok {
			continue
		}
		if derr := w.Q.DetachLabelFromIssue(ctx, db.DetachLabelFromIssueParams{
			IssueID: issueID, LabelID: id, WorkspaceID: conn.WorkspaceID,
		}); derr != nil {
			return fmt.Errorf("detach label %q: %w", name, derr)
		}
	}
	items.Labels = nextLabelState(items.Labels, act)
	return nil
}

// labelIndex builds a case-insensitive name→id map for the workspace.
func (w *Worker) labelIndex(ctx context.Context, workspaceID pgtype.UUID) (map[string]pgtype.UUID, error) {
	rows, err := w.Q.ListLabels(ctx, db.ListLabelsParams{WorkspaceID: workspaceID, ResourceType: "issue"})
	if err != nil {
		return nil, err
	}
	out := make(map[string]pgtype.UUID, len(rows))
	for _, r := range rows {
		out[strings.ToLower(r.Name)] = r.ID
	}
	return out, nil
}

// nextLabelState folds an apply into the materialized last-synced/propagated
// sets: added labels join both; removed labels leave both.
func nextLabelState(prev LabelsState, act Action) LabelsState {
	last := toSet(prev.LastSynced)
	prop := toSet(prev.Propagated)
	for _, a := range act.Add {
		last[a] = true
		prop[a] = true
	}
	for _, r := range act.Remove {
		delete(last, r)
		delete(prop, r)
	}
	return LabelsState{LastSynced: setKeys(last), Propagated: setKeys(prop)}
}

func setKeys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	return normalizeSet(mapKeys(m))
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// jiraLabelSafe replaces spaces (Jira labels forbid them); reports whether it
// changed so the applier journals the transform once.
func jiraLabelSafe(name string) (string, bool) {
	safe := strings.ReplaceAll(name, " ", "-")
	return safe, safe != name
}
