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

// Story 6.3 — UJ-2 round-trip acceptance: Jira label → agent works → results
// land back in Jira, zero Multica-side human actions.

func TestRoundTripLabelToAgentToJira(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	labels := atomic.Value{}
	labels.Store(`[]`)
	var postedComments atomic.Int64
	var transitions atomic.Int64
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[{"id":"id-GAME-99","key":"GAME-99","fields":{
			"summary":"Crash on iOS","description":{"type":"doc","version":1,"content":[]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":%s,"updated":%q}}],"isLast":true}`, labels.Load(), now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-99/comment", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postedComments.Add(1)
			w.Write([]byte(`{"id":"jc-agent"}`))
			return
		}
		w.Write([]byte(`{"comments":[],"startAt":0,"maxResults":100,"total":0}`))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-99/transitions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			transitions.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Write([]byte(`{"transitions":[{"id":"t-rev","to":{"id":"300","name":"In Review"}}]}`))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	agentID := makeTestAgent(t, w, conn.WorkspaceID, "Fixer")
	conn = tagRuleConn(t, q, conn, fmt.Sprintf(`[{"match_type":"label","match_value":"agent:fixer","agent_id":%q}]`, uuidStr(agentID)))
	// Map in_review → 300 for the outbound transition.
	conn, _ = q.UpdateJiraConnectionConfig(ctx, db.UpdateJiraConnectionConfigParams{
		ID: conn.ID, Enabled: true, Mode: conn.Mode, LeadingSystem: conn.LeadingSystem,
		CommentsEnabled: true, LabelsEnabled: true, CustomFieldsEnabled: true,
		CreateFromJira: true, CreateToJira: false, JqlFilter: "", LabelPrefix: "",
		MentionBridgeEnabled: true, OutboundIssueType: "Task",
		StatusMap: []byte(`{"in":{"100":"todo","300":"in_review"},"out":{"todo":"100","in_review":"300"}}`),
		FieldMap:  []byte(`[]`), TagRules: conn.TagRules, CycleIntervalSeconds: 45,
	})

	rewind := func() db.JiraConnection {
		c, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
			ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true},
			LocalCursor: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}})
		c, _ = q.GetJiraConnectionByID(ctx, conn.ID)
		return c
	}

	// 1) Import (no label) — nothing happens.
	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-99"})

	// 2) The Jira label appears → agent assigned + dispatched (≤1 cycle).
	labels.Store(`["agent:fixer"]`)
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("tag cycle: %v", err)
	}
	issue, _ := q.GetIssue(ctx, link.IssueID)
	if issue.AssigneeType.String != "agent" || issue.AssigneeID != agentID {
		t.Fatalf("label must summon the agent: %+v", issue)
	}
	if n := agentTaskCount(t, w, conn.WorkspaceID); n != 1 {
		t.Fatalf("agent run must be enqueued: %d", n)
	}

	// 3) The agent does its work (simulated): comments + moves to in_review.
	if _, err := q.CreateComment(ctx, db.CreateCommentParams{
		IssueID: link.IssueID, WorkspaceID: conn.WorkspaceID,
		AuthorType: "agent", AuthorID: agentID, Content: "Fixed the touch handler; pushed a branch.", Type: "comment",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: link.IssueID, Status: "in_review", WorkspaceID: conn.WorkspaceID}); err != nil {
		t.Fatal(err)
	}

	// 4) Next cycle pushes the agent's comment + transition to Jira (≤1 cycle).
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("round-trip cycle: %v", err)
	}
	if postedComments.Load() != 1 {
		t.Fatalf("agent comment must appear in Jira exactly once, got %d", postedComments.Load())
	}
	if transitions.Load() != 1 {
		t.Fatalf("agent status move must transition Jira exactly once, got %d", transitions.Load())
	}
	// No Multica-side human ever touched this issue: the whole loop ran on
	// the Jira label alone. (Assertion is structural — the test performed no
	// member action.)
}

// Regression (adversarial review #5): a rule whose agent does not yet exist
// must NOT consume the edge — it fires once the agent is created. The signal
// is a LIVE post-connect edge (a label added after import): pre-existing labels
// present at import are history and never auto-fire (RU §13, §25), so this test
// adds the label after the initial import to exercise the real edge path.
func TestTagRuleReFiresAfterAgentAppears(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	labels := atomic.Value{}
	labels.Store(`[]`)
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[{"id":"id-GAME-A1","key":"GAME-A1","fields":{
			"summary":"B","description":{"type":"doc","version":1,"content":[]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":%s,"updated":%q}}],"isLast":true}`, labels.Load(), now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-A1/comment", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"comments":[],"startAt":0,"maxResults":100,"total":0}`))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	// Rule references an agent id that does not exist yet.
	missing := uuidStr(pgtype.UUID{Bytes: uuid.New(), Valid: true})
	conn = tagRuleConn(t, q, conn, fmt.Sprintf(`[{"match_type":"label","match_value":"agent:later","agent_id":%q}]`, missing))

	rewind := func() db.JiraConnection {
		c, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
			ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}, LocalCursor: c.LocalCursor})
		c, _ = q.GetJiraConnectionByID(ctx, conn.ID)
		return c
	}
	// Import with no label — nothing fires.
	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-A1"})

	// The label is added post-connect → a genuine live edge — but the agent is
	// missing, so the edge must NOT be consumed.
	labels.Store(`["agent:later"]`)
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("missing-agent cycle: %v", err)
	}
	issue, _ := q.GetIssue(ctx, link.IssueID)
	if issue.AssigneeType.String == "agent" {
		t.Fatal("must not assign a non-existent agent")
	}

	// Create the agent with the SAME id the rule references, re-run: the edge
	// was not consumed, so it fires now.
	if _, err := w.Pool.Exec(ctx, "INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider) VALUES ($1,'rt','local','claude')", conn.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	var rid pgtype.UUID
	_ = w.Pool.QueryRow(ctx, "SELECT id FROM agent_runtime WHERE workspace_id=$1 LIMIT 1", conn.WorkspaceID).Scan(&rid)
	if _, err := w.Pool.Exec(ctx, "INSERT INTO agent (id, workspace_id, name, kind, runtime_mode, runtime_id) VALUES ($1,$2,'Later','user','local',$3)",
		pgtype.UUID{Bytes: mustParseUUID(t, missing), Valid: true}, conn.WorkspaceID, rid); err != nil {
		t.Fatal(err)
	}
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("re-fire cycle: %v", err)
	}
	issue, _ = q.GetIssue(ctx, link.IssueID)
	if issue.AssigneeType.String != "agent" {
		t.Fatalf("rule must fire once the agent exists (edge not consumed): %+v", issue)
	}
}

func mustParseUUID(t *testing.T, s string) [16]byte {
	t.Helper()
	u, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
