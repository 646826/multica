package jira

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Story 6.1/6.3 — tag rules with edge-triggering and human precedence, and
// the no-spurious-runs guarantee.

func makeTestAgent(t *testing.T, w *Worker, wsID pgtype.UUID, name string) pgtype.UUID {
	t.Helper()
	rid := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	if _, err := w.Pool.Exec(context.Background(),
		"INSERT INTO agent_runtime (id, workspace_id, name, runtime_mode, provider) VALUES ($1,$2,$3,'local','claude')",
		rid, wsID, name+"-rt"); err != nil {
		t.Fatal(err)
	}
	id := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	if _, err := w.Pool.Exec(context.Background(),
		"INSERT INTO agent (id, workspace_id, name, kind, runtime_mode, runtime_id) VALUES ($1,$2,$3,'user','local',$4)",
		id, wsID, name, rid); err != nil {
		t.Fatal(err)
	}
	return id
}

func tagRuleConn(t *testing.T, q *db.Queries, conn db.JiraConnection, rulesJSON string) db.JiraConnection {
	t.Helper()
	upd, err := q.UpdateJiraConnectionConfig(context.Background(), db.UpdateJiraConnectionConfigParams{
		ID: conn.ID, Enabled: true, Mode: conn.Mode, LeadingSystem: conn.LeadingSystem,
		CommentsEnabled: true, LabelsEnabled: true, CustomFieldsEnabled: true,
		CreateFromJira: true, CreateToJira: false, JqlFilter: "", LabelPrefix: "",
		MentionBridgeEnabled: true, OutboundIssueType: "Task",
		StatusMap: []byte(`{"in":{"100":"todo"},"out":{"todo":"100"}}`),
		FieldMap:  []byte(`[]`), TagRules: []byte(rulesJSON), CycleIntervalSeconds: 45,
	})
	if err != nil {
		t.Fatal(err)
	}
	return upd
}

func agentTaskCount(t *testing.T, w *Worker, wsID pgtype.UUID) int {
	t.Helper()
	var n int
	if err := w.Pool.QueryRow(context.Background(),
		"SELECT count(*) FROM agent_task_queue q JOIN issue i ON i.id = q.issue_id WHERE i.workspace_id = $1", wsID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTagRuleAssignsAgentEdgeTriggeredWithHumanPrecedence(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	labels := atomic.Value{}
	labels.Store(`[]`)
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[{"id":"id-GAME-90","key":"GAME-90","fields":{
			"summary":"Bug","description":{"type":"doc","version":1,"content":[]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":%s,"updated":%q}}],"isLast":true}`, labels.Load(), now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-90/comment", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"comments":[],"startAt":0,"maxResults":100,"total":0}`))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	agentID := makeTestAgent(t, w, conn.WorkspaceID, "Fixer")
	conn = tagRuleConn(t, q, conn, fmt.Sprintf(`[{"match_type":"label","match_value":"agent:fixer","agent_id":%q}]`, uuidStr(agentID)))

	rewind := func() db.JiraConnection {
		c, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
			ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}, LocalCursor: c.LocalCursor})
		c, _ = q.GetJiraConnectionByID(ctx, conn.ID)
		return c
	}

	// Import with no label → no assignment, no run.
	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-90"})
	if n := agentTaskCount(t, w, conn.WorkspaceID); n != 0 {
		t.Fatalf("no rule matched at import → zero runs (FR-28), got %d", n)
	}

	// The label appears → edge fires: agent assigned, backlog→todo, run enqueued.
	labels.Store(`["agent:fixer"]`)
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("tag cycle: %v", err)
	}
	issue, _ := q.GetIssue(ctx, link.IssueID)
	if issue.AssigneeType.String != "agent" || issue.AssigneeID != agentID {
		t.Fatalf("agent must be assigned: %+v", issue)
	}
	if issue.Status != "todo" {
		t.Fatalf("backlog/todo issue must be dispatch-eligible: %s", issue.Status)
	}
	if n := agentTaskCount(t, w, conn.WorkspaceID); n != 1 {
		t.Fatalf("native dispatch must enqueue exactly one run, got %d", n)
	}

	// Same label present next cycle → edge already fired, no re-assign/run.
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("steady cycle: %v", err)
	}
	if n := agentTaskCount(t, w, conn.WorkspaceID); n != 1 {
		t.Fatalf("unchanged signal must not re-fire, got %d runs", n)
	}

	// A human reassigns to a member → the guard must not steal it back.
	memberID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	cur, _ := q.GetIssue(ctx, link.IssueID)
	if _, err := q.UpdateIssue(ctx, db.UpdateIssueParams{ID: cur.ID, Title: pgtype.Text{String: cur.Title, Valid: true},
		Description: cur.Description, Status: pgtype.Text{String: cur.Status, Valid: true},
		AssigneeType: pgtype.Text{String: "member", Valid: true}, AssigneeID: memberID,
		StartDate: cur.StartDate, DueDate: cur.DueDate, ParentIssueID: cur.ParentIssueID, ProjectID: cur.ProjectID, Stage: cur.Stage}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("human-precedence cycle: %v", err)
	}
	issue, _ = q.GetIssue(ctx, link.IssueID)
	if issue.AssigneeType.String != "member" {
		t.Fatalf("human assignment must be respected (FR-27): %+v", issue)
	}

	// Label removed then re-added → re-fires (edge), but the human still owns
	// the assignment, so it stays guarded.
	labels.Store(`[]`)
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("label-removed cycle: %v", err)
	}
	issue, _ = q.GetIssue(ctx, link.IssueID)
	if issue.AssigneeType.String != "member" {
		t.Fatalf("label disappearing must never unassign (FR-27): %+v", issue)
	}
}

// Story 6.2 — mention bridge.

func TestMentionBridgeWakesAgentAndProtectsHumans(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	comments := atomic.Value{}
	comments.Store("initial")
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[{"id":"id-GAME-95","key":"GAME-95","fields":{
			"summary":"Talk","description":{"type":"doc","version":1,"content":[]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":[],"updated":%q}}],"isLast":true}`, now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-95/comment", func(w http.ResponseWriter, r *http.Request) {
		if comments.Load() == "with" {
			// A human comment mentioning the agent (plain text) AND a real
			// Jira user-mention of a human named like the agent (ADF node).
			fmt.Fprintf(w, `{"comments":[
				{"id":"c-m1","author":{"accountId":"acc-human","displayName":"Marco"},
				 "body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[
					{"type":"text","text":"hey @Fixer please look, cc "},
					{"type":"mention","attrs":{"id":"acc-max","text":"@Fixer"}},
					{"type":"text","text":" the human"}
				 ]}]},"created":%q}
			],"startAt":0,"maxResults":100,"total":1}`, now.Format(jiraTimeLayout))
			return
		}
		w.Write([]byte(`{"comments":[],"startAt":0,"maxResults":100,"total":0}`))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	makeTestAgent(t, w, conn.WorkspaceID, "Fixer")

	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-95"})

	comments.Store("with")
	c2, _ := q.GetJiraConnectionByID(ctx, conn.ID)
	_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
		ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}, LocalCursor: c2.LocalCursor})
	c2, _ = q.GetJiraConnectionByID(ctx, conn.ID)
	if _, err := w.runCycle(ctx, c2); err != nil {
		t.Fatalf("mention cycle: %v", err)
	}

	rows, _ := q.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{IssueID: link.IssueID, WorkspaceID: conn.WorkspaceID, Limit: 100})
	var mirrored string
	for _, c := range rows {
		if c.AuthorType == "system" && strings.Contains(c.Content, "From Jira") {
			mirrored = c.Content
		}
	}
	if !strings.Contains(mirrored, "mention://agent/") {
		t.Fatalf("plain-text @Fixer must become a native mention: %s", mirrored)
	}
	// The human Jira mention node rendered WITHOUT @ ("Fixer the human") must
	// NOT have been converted into a second agent mention.
	if strings.Count(mirrored, "mention://agent/") != 1 {
		t.Fatalf("real Jira user-mention must never convert to an agent mention: %s", mirrored)
	}
	// The mention must wake the agent (native mention enqueue).
	if n := agentTaskCount(t, w, conn.WorkspaceID); n != 1 {
		t.Fatalf("mention must enqueue exactly one run, got %d", n)
	}
}

func TestMentionBridgeToggleOff(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[{"id":"id-GAME-96","key":"GAME-96","fields":{
			"summary":"Q","description":{"type":"doc","version":1,"content":[]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":[],"updated":%q}}],"isLast":true}`, now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-96/comment", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"comments":[{"id":"c-x","author":{"accountId":"acc-human","displayName":"Marco"},
			"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"@Fixer hi"}]}]},
			"created":%q}],"startAt":0,"maxResults":100,"total":1}`, now.Format(jiraTimeLayout))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	makeTestAgent(t, w, conn.WorkspaceID, "Fixer")
	conn, _ = q.UpdateJiraConnectionConfig(ctx, db.UpdateJiraConnectionConfigParams{
		ID: conn.ID, Enabled: true, Mode: conn.Mode, LeadingSystem: conn.LeadingSystem,
		CommentsEnabled: true, LabelsEnabled: true, CustomFieldsEnabled: true,
		CreateFromJira: true, CreateToJira: false, JqlFilter: "", LabelPrefix: "",
		MentionBridgeEnabled: false, OutboundIssueType: "Task",
		StatusMap: []byte(`{"in":{"100":"todo"},"out":{"todo":"100"}}`),
		FieldMap:  []byte(`[]`), TagRules: []byte(`[]`), CycleIntervalSeconds: 45,
	})
	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-96"})
	rows, _ := q.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{IssueID: link.IssueID, WorkspaceID: conn.WorkspaceID, Limit: 100})
	for _, c := range rows {
		if strings.Contains(c.Content, "mention://agent/") {
			t.Fatalf("toggle off must not rewrite mentions: %s", c.Content)
		}
	}
	if n := agentTaskCount(t, w, conn.WorkspaceID); n != 0 {
		t.Fatalf("toggle off must not wake agents: %d", n)
	}
}
