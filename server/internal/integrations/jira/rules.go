package jira

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// rules.go — agent tagging (FR-26/FR-27), the feature's wedge. Tag rules map
// a Jira signal (a label or an assignee accountId) to a workspace agent. When
// an in-scope issue's signal transitions to match a rule, sync assigns the
// mapped agent and, if the issue is parked in backlog, promotes it to todo so
// Multica's native dispatch fires. Rules are edge-triggered (fire on signal
// transitions, tracked in items.Tag) and humans keep precedence: sync never
// overrides an assignment it did not make.

// applyTagRules evaluates the tag rules against one observed issue's current
// signals and applies the mapped agent when a rule newly matches. Returns
// whether the issue's status/assignee changed (so the caller republishes).
func (w *Worker) applyTagRules(ctx context.Context, conn db.JiraConnection, issue db.Issue, items *ItemsV1, obs *ObservedIssue, cycleID pgtype.UUID) (bool, error) {
	if obs == nil {
		return false, nil // tagging is driven by Jira-side signals only
	}
	rules, err := ParseTagRules(conn.TagRules)
	if err != nil || len(rules) == 0 {
		return false, nil
	}

	currentLabels := toSet(obs.Labels)
	firedLabels := toSet(items.Tag.FiredLabels)

	// Determine which rules match NOW and which of those are edge (newly true).
	var edgeAgent string
	var edgeVia map[string]any
	newFiredLabels := map[string]bool{}
	newFiredAssignee := items.Tag.FiredAssignee

	for _, rule := range rules {
		switch rule.MatchType {
		case "label":
			if currentLabels[rule.MatchValue] {
				newFiredLabels[rule.MatchValue] = true
				if !firedLabels[rule.MatchValue] && edgeAgent == "" {
					edgeAgent = rule.AgentID
					edgeVia = map[string]any{"match_type": "label", "match_value": rule.MatchValue}
				}
			}
		case "assignee":
			if obs.AssigneeKey != "" && obs.AssigneeKey == rule.MatchValue {
				if items.Tag.FiredAssignee != rule.MatchValue && edgeAgent == "" {
					edgeAgent = rule.AgentID
					edgeVia = map[string]any{"match_type": "assignee", "match_value": rule.MatchValue}
				}
				newFiredAssignee = rule.MatchValue
			}
		}
	}
	// Assignee signal cleared → allow a future re-fire (never unassigns).
	if obs.AssigneeKey == "" || !assigneeStillMapped(rules, obs.AssigneeKey) {
		newFiredAssignee = ""
	}

	// Persist the edge-tracking sets regardless of whether we assign.
	items.Tag.FiredLabels = setKeys(newFiredLabels)
	items.Tag.FiredAssignee = newFiredAssignee

	if edgeAgent == "" {
		return false, nil
	}

	// Validate the agent exists in the workspace (FR-26).
	agentUUID, perr := parsePropertyUUID(edgeAgent)
	if perr != nil {
		w.journalTagSkip(ctx, conn, issue.ID, obs.Key, cycleID, "invalid agent id", edgeVia)
		return false, nil
	}
	if _, aerr := w.Q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentUUID, WorkspaceID: conn.WorkspaceID}); aerr != nil {
		w.journalTagSkip(ctx, conn, issue.ID, obs.Key, cycleID, "agent not found in workspace", edgeVia)
		return false, nil
	}

	// Human-precedence guard (FR-27): apply when unassigned, or when the issue
	// is already assigned to exactly the agent sync last assigned. Any other
	// assignment (a human's member/agent choice) is respected.
	if !tagAssignAllowed(issue, items.Tag.AssignedAgent) {
		w.journalTagSkip(ctx, conn, issue.ID, obs.Key, cycleID, "human assignment takes precedence", edgeVia)
		return false, nil
	}

	// Assign the agent; promote backlog → todo so native dispatch fires.
	newStatus := issue.Status
	if issue.Status == "backlog" {
		newStatus = "todo"
	}
	updated, uerr := w.Q.UpdateIssue(ctx, db.UpdateIssueParams{
		ID:            issue.ID,
		Title:         pgtype.Text{String: issue.Title, Valid: true},
		Description:   issue.Description,
		Status:        pgtype.Text{String: newStatus, Valid: true},
		AssigneeType:  pgtype.Text{String: "agent", Valid: true},
		AssigneeID:    agentUUID,
		StartDate:     issue.StartDate,
		DueDate:       issue.DueDate,
		ParentIssueID: issue.ParentIssueID,
		ProjectID:     issue.ProjectID,
		Stage:         issue.Stage,
	})
	if uerr != nil {
		return false, fmt.Errorf("tag-assign: %w", uerr)
	}
	items.Tag.AssignedAgent = edgeATag(agentUUID)
	// The activation status change is local-only: exempt from outbound push
	// (do not disturb the Jira status) — mirror the local snapshot so the
	// outbound status arm sees no divergence.
	if newStatus != issue.Status {
		items.Status.Local = newStatus
	}
	edgeVia["agent_id"] = edgeAgent
	_ = w.Journal.Record(ctx, conn, cycleID, JournalTagAssigned, issue.ID, obs.Key, edgeVia)

	// Native dispatch: use the platform's own decision + enqueue (the same
	// path the HTTP assign handler and autopilot use), so agent pickup obeys
	// WillEnqueueRun exactly (backlog parking, agent access, pending-run dedup).
	w.publishIssueUpdated(conn, updated)
	if w.Issues != nil && w.Tasks != nil {
		if trigger, ok := w.Issues.WillEnqueueRun(ctx, service.IssueTriggerInput{
			Issue: updated, PrevStatus: issue.Status, AssigneeChanged: true,
		}, service.IssueTriggerProbe{}); ok {
			_ = trigger
			if _, eerr := w.Tasks.EnqueueTaskForIssue(ctx, updated); eerr != nil {
				_ = w.Journal.Record(ctx, conn, cycleID, JournalTagSkipped, issue.ID, obs.Key, map[string]any{
					"reason": "dispatch enqueue failed", "error": redactError(eerr),
				})
			}
		}
	}
	return true, nil
}

func assigneeStillMapped(rules []TagRule, key string) bool {
	for _, r := range rules {
		if r.MatchType == "assignee" && r.MatchValue == key {
			return true
		}
	}
	return false
}

// tagAssignAllowed enforces human precedence.
func tagAssignAllowed(issue db.Issue, lastAssignedAgent string) bool {
	if !issue.AssigneeType.Valid || issue.AssigneeType.String == "" {
		return true // unassigned
	}
	if issue.AssigneeType.String == "agent" && issue.AssigneeID.Valid && edgeATag(issue.AssigneeID) == lastAssignedAgent && lastAssignedAgent != "" {
		return true // still assigned to the agent sync set
	}
	return false // a human owns this assignment
}

func edgeATag(u pgtype.UUID) string { return uuidStr(u) }

func (w *Worker) journalTagSkip(ctx context.Context, conn db.JiraConnection, issueID pgtype.UUID, key string, cycleID pgtype.UUID, reason string, via map[string]any) {
	detail := map[string]any{"reason": reason}
	for k, v := range via {
		detail[k] = v
	}
	_ = w.Journal.Record(ctx, conn, cycleID, JournalTagSkipped, issueID, key, detail)
}
