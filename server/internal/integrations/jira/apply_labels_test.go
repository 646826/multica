package jira

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Story 5.1 — labels with native-label protection.

func TestInboundLabelsPullAndProtectNativeLabels(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	labels := "[\"bug\",\"urgent\"]"
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[{"id":"id-GAME-70","key":"GAME-70","fields":{
			"summary":"Labeled","description":{"type":"doc","version":1,"content":[]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":%s,"updated":%q}}],"isLast":true}`, labels, now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-70/comment", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"comments":[],"startAt":0,"maxResults":100,"total":0}`))
	})
	w, conn, q := importFixture(t, f) // jira_leads: labels facet pulls
	ctx := context.Background()

	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-70"})

	rewind := func() db.JiraConnection {
		c, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
			ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}, LocalCursor: c.LocalCursor,
		})
		c, _ = q.GetJiraConnectionByID(ctx, conn.ID)
		return c
	}
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("labels cycle: %v", err)
	}

	names := issueLabelNames(t, q, link.IssueID, conn.WorkspaceID)
	if !names["bug"] || !names["urgent"] {
		t.Fatalf("jira labels must pull: %v", names)
	}

	// A user adds a native Multica label; Jira later drops "urgent".
	nativeID, err := q.CreateLabel(ctx, db.CreateLabelParams{WorkspaceID: conn.WorkspaceID, ResourceType: "issue", Name: "mine", Color: "blue"})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.AttachLabelToIssue(ctx, db.AttachLabelToIssueParams{IssueID: link.IssueID, LabelID: nativeID.ID, WorkspaceID: conn.WorkspaceID}); err != nil {
		t.Fatal(err)
	}
	labels = "[\"bug\"]"
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("removal cycle: %v", err)
	}

	names = issueLabelNames(t, q, link.IssueID, conn.WorkspaceID)
	if names["urgent"] {
		t.Fatalf("propagated label 'urgent' must be removed when Jira drops it: %v", names)
	}
	if !names["bug"] {
		t.Fatalf("still-present jira label 'bug' must remain: %v", names)
	}
	if !names["mine"] {
		t.Fatalf("NATIVE label 'mine' must survive (FR-22): %v", names)
	}
}

func TestOutboundLabelsTransformSpaces(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[{"id":"id-GAME-71","key":"GAME-71","fields":{
			"summary":"L","description":{"type":"doc","version":1,"content":[]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":[],"updated":%q}}],"isLast":true}`, now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-71", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		fmt.Fprintf(w, `{"id":"id-GAME-71","key":"GAME-71","fields":{"summary":"L",
			"description":{"type":"doc","version":1,"content":[]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},"labels":[],"updated":%q}}`, now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-71/comment", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"comments":[],"startAt":0,"maxResults":100,"total":0}`))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	conn, _ = q.UpdateJiraConnectionConfig(ctx, db.UpdateJiraConnectionConfigParams{
		ID: conn.ID, Enabled: true, Mode: "two_way", LeadingSystem: "multica",
		CommentsEnabled: true, LabelsEnabled: true, CustomFieldsEnabled: true,
		CreateFromJira: true, CreateToJira: false, JqlFilter: "", LabelPrefix: "",
		MentionBridgeEnabled: true, OutboundIssueType: "Task",
		StatusMap: []byte(`{"in":{"100":"todo"},"out":{"todo":"100"}}`),
		FieldMap:  []byte(`[]`), TagRules: []byte(`[]`), CycleIntervalSeconds: 45,
	})
	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-71"})

	lab, _ := q.CreateLabel(ctx, db.CreateLabelParams{WorkspaceID: conn.WorkspaceID, ResourceType: "issue", Name: "needs review", Color: "red"})
	if err := q.AttachLabelToIssue(ctx, db.AttachLabelToIssueParams{IssueID: link.IssueID, LabelID: lab.ID, WorkspaceID: conn.WorkspaceID}); err != nil {
		t.Fatal(err)
	}
	// bump issue updated_at so the local pass sees it
	cur, _ := q.GetIssue(ctx, link.IssueID)
	_, _ = q.UpdateIssue(ctx, db.UpdateIssueParams{ID: cur.ID, Title: pgtype.Text{String: cur.Title, Valid: true},
		Description: cur.Description, AssigneeType: cur.AssigneeType, AssigneeID: cur.AssigneeID,
		StartDate: cur.StartDate, DueDate: cur.DueDate, ParentIssueID: cur.ParentIssueID, ProjectID: cur.ProjectID, Stage: cur.Stage})

	c, _ := q.GetJiraConnectionByID(ctx, conn.ID)
	_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
		ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}, LocalCursor: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}})
	c, _ = q.GetJiraConnectionByID(ctx, conn.ID)
	if _, err := w.runCycle(ctx, c); err != nil {
		t.Fatalf("push cycle: %v", err)
	}

	rows, _ := q.ListJiraJournalRecent(ctx, db.ListJiraJournalRecentParams{ConnectionID: conn.ID, Limit: 30})
	transformed := false
	for _, r := range rows {
		if r.Kind == string(JournalLabelTransformed) {
			transformed = true
		}
	}
	if !transformed {
		t.Fatal("space-containing label must journal a transform")
	}
}

func issueLabelNames(t *testing.T, q *db.Queries, issueID, wsID pgtype.UUID) map[string]bool {
	t.Helper()
	rows, err := q.ListLabelsByIssue(context.Background(), db.ListLabelsByIssueParams{IssueID: issueID, WorkspaceID: wsID})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, l := range rows {
		out[strings.ToLower(l.Name)] = true
	}
	return out
}
