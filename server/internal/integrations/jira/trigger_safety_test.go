package jira

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Trigger-safety pins: agent runs must fire ONLY on genuine post-connect edges.
// History (a pre-existing label, a pre-existing mapped assignee, an @mention in
// an old comment) is context, never an event (RU §25, §26.2). These tests prove
// non-firing — the coverage the round-trip tests never checked.

// A first-connect scan of an EXISTING issue that already carries a rule-matching
// label, a mapped assignee, and a plain-text @agent mention in its history must
// enqueue zero agent runs.
func TestImportNeverTriggersFromHistory(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	f.mux.HandleFunc("/rest/api/3/search/jql", func(wr http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(wr, `{"issues":[{"id":"id-GAME-1","key":"GAME-1","fields":{
			"summary":"Historical","description":{"type":"doc","version":1,"content":[]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"assignee":{"accountId":"acc-boss"},
			"labels":["agent:fixer"],"updated":%q}}],"isLast":true}`, now.Add(-time.Hour).Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-1/comment", func(wr http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(wr, `{"comments":[{"id":"c1","author":{"accountId":"acc-human","displayName":"Ann"},
			"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[
			{"type":"text","text":"@Fixer please look"}]}]},"created":%q}],"startAt":0,"maxResults":100,"total":1}`,
			now.Add(-time.Hour).Format(jiraTimeLayout))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	agentID := makeTestAgent(t, w, conn.WorkspaceID, "Fixer")
	conn = tagRuleConn(t, q, conn, fmt.Sprintf(
		`[{"match_type":"label","match_value":"agent:fixer","agent_id":%q},{"match_type":"assignee","match_value":"acc-boss","agent_id":%q}]`,
		uuidStr(agentID), uuidStr(agentID)))

	rewind := func() db.JiraConnection {
		c, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
			ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-2 * time.Hour), Valid: true}, LocalCursor: c.LocalCursor})
		c, _ = q.GetJiraConnectionByID(ctx, conn.ID)
		return c
	}

	// Cycle 1 imports the issue; cycle 2 re-observes it as a linked pair and
	// runs the tag rules. Neither may fire on the pre-existing signals.
	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import cycle: %v", err)
	}
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	if n := agentTaskCount(t, w, conn.WorkspaceID); n != 0 {
		t.Fatalf("history (label+assignee+@mention present at import) fired %d agent runs, want 0", n)
	}
}

// Two route labels pointing at DIFFERENT agents in one observation are
// ambiguous: sync must assign neither and pick no random winner (RU §11.7).
func TestAmbiguousRouteBlocks(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	labels := atomic.Value{}
	labels.Store(`[]`)
	f.mux.HandleFunc("/rest/api/3/search/jql", func(wr http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(wr, `{"issues":[{"id":"id-AMB","key":"AMB-1","fields":{
			"summary":"Amb","description":{"type":"doc","version":1,"content":[]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":%s,"updated":%q}}],"isLast":true}`, labels.Load(), now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-AMB/comment", func(wr http.ResponseWriter, r *http.Request) {
		wr.Write([]byte(`{"comments":[],"startAt":0,"maxResults":100,"total":0}`))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	a1 := makeTestAgent(t, w, conn.WorkspaceID, "agent-a")
	a2 := makeTestAgent(t, w, conn.WorkspaceID, "agent-b")
	conn = tagRuleConn(t, q, conn, fmt.Sprintf(
		`[{"match_type":"label","match_value":"route-a","agent_id":%q},{"match_type":"label","match_value":"route-b","agent_id":%q}]`,
		uuidStr(a1), uuidStr(a2)))

	rewind := func() db.JiraConnection {
		c, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
			ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}, LocalCursor: c.LocalCursor})
		c, _ = q.GetJiraConnectionByID(ctx, conn.ID)
		return c
	}
	// Import with no labels.
	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-AMB"})

	// Both route labels appear at once → two distinct targets, one observation.
	labels.Store(`["route-a","route-b"]`)
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("ambiguous cycle: %v", err)
	}
	issue, _ := q.GetIssue(ctx, link.IssueID)
	if issue.AssigneeType.Valid && issue.AssigneeType.String == "agent" {
		t.Fatalf("ambiguous route must not assign an agent: %+v", issue)
	}
	if n := agentTaskCount(t, w, conn.WorkspaceID); n != 0 {
		t.Fatalf("ambiguous route fired %d runs, want 0", n)
	}
}

func journalCount(t *testing.T, w *Worker, connID pgtype.UUID, kind string) int {
	t.Helper()
	var n int
	if err := w.Pool.QueryRow(context.Background(),
		"SELECT count(*) FROM jira_journal WHERE connection_id=$1 AND kind=$2", connID, kind).Scan(&n); err != nil {
		t.Fatalf("journal count: %v", err)
	}
	return n
}

func rewindConn(t *testing.T, q *db.Queries, id pgtype.UUID, to time.Time) db.JiraConnection {
	t.Helper()
	ctx := context.Background()
	c, _ := q.GetJiraConnectionByID(ctx, id)
	_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
		ID: id, JiraCursor: pgtype.Timestamptz{Time: to, Valid: true}, LocalCursor: c.LocalCursor})
	c, _ = q.GetJiraConnectionByID(ctx, id)
	return c
}

// Pin (RU §26.2): a live comment with no mention does not trigger, and editing
// it to ADD a mention (same Jira comment id) still does not — mirroring keys on
// the id and skips an already-linked comment.
func TestLiveCommentAndLaterMentionEditDoNotTrigger(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	state := atomic.Value{}
	state.Store("none")
	f.mux.HandleFunc("/rest/api/3/search/jql", func(wr http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(wr, `{"issues":[{"id":"id-CE","key":"CE-1","fields":{
			"summary":"C","description":{"type":"doc","version":1,"content":[]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":[],"updated":%q}}],"isLast":true}`, now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-CE/comment", func(wr http.ResponseWriter, r *http.Request) {
		switch state.Load() {
		case "plain":
			fmt.Fprintf(wr, `{"comments":[{"id":"c-e","author":{"accountId":"acc-h","displayName":"H"},"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"hello team"}]}]},"created":%q}],"total":1}`, now.Format(jiraTimeLayout))
		case "edited":
			fmt.Fprintf(wr, `{"comments":[{"id":"c-e","author":{"accountId":"acc-h","displayName":"H"},"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"@Fixer hello team"}]}]},"created":%q}],"total":1}`, now.Format(jiraTimeLayout))
		default:
			wr.Write([]byte(`{"comments":[],"total":0}`))
		}
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	makeTestAgent(t, w, conn.WorkspaceID, "Fixer")
	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	state.Store("plain")
	if _, err := w.runCycle(ctx, rewindConn(t, q, conn.ID, now.Add(-time.Hour))); err != nil {
		t.Fatalf("plain: %v", err)
	}
	state.Store("edited")
	if _, err := w.runCycle(ctx, rewindConn(t, q, conn.ID, now.Add(-time.Hour))); err != nil {
		t.Fatalf("edited: %v", err)
	}
	if n := agentTaskCount(t, w, conn.WorkspaceID); n != 0 {
		t.Fatalf("plain comment + later mention-edit fired %d runs, want 0", n)
	}
}

// Pin (RU §10.8): a restricted Jira comment is dropped, never mirrored, never
// fed to an agent — even with a plain @mention in its body.
func TestRestrictedCommentNeverWakes(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	present := atomic.Value{}
	present.Store(false)
	f.mux.HandleFunc("/rest/api/3/search/jql", func(wr http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(wr, `{"issues":[{"id":"id-RC","key":"RC-1","fields":{
			"summary":"R","description":{"type":"doc","version":1,"content":[]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":[],"updated":%q}}],"isLast":true}`, now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-RC/comment", func(wr http.ResponseWriter, r *http.Request) {
		if present.Load() == true {
			fmt.Fprintf(wr, `{"comments":[{"id":"c-r","author":{"accountId":"acc-h","displayName":"H"},"visibility":{"type":"role","value":"Administrators"},"body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"@Fixer secret"}]}]},"created":%q}],"total":1}`, now.Format(jiraTimeLayout))
			return
		}
		wr.Write([]byte(`{"comments":[],"total":0}`))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	makeTestAgent(t, w, conn.WorkspaceID, "Fixer")
	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	present.Store(true)
	if _, err := w.runCycle(ctx, rewindConn(t, q, conn.ID, now.Add(-time.Hour))); err != nil {
		t.Fatalf("restricted: %v", err)
	}
	if n := agentTaskCount(t, w, conn.WorkspaceID); n != 0 {
		t.Fatalf("restricted comment fired %d runs, want 0", n)
	}
	if journalCount(t, w, conn.ID, "restricted_comment_dropped") == 0 {
		t.Fatal("restricted comment must be journaled as dropped")
	}
}

// Pin (RU §26.2): an ordinary label (matching no tag rule) never triggers, even
// when route rules exist for other labels.
func TestOrdinaryLabelNeverTriggers(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	labels := atomic.Value{}
	labels.Store(`[]`)
	f.mux.HandleFunc("/rest/api/3/search/jql", func(wr http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(wr, `{"issues":[{"id":"id-OL","key":"OL-1","fields":{
			"summary":"O","description":{"type":"doc","version":1,"content":[]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":%s,"updated":%q}}],"isLast":true}`, labels.Load(), now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-OL/comment", func(wr http.ResponseWriter, r *http.Request) {
		wr.Write([]byte(`{"comments":[],"total":0}`))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	agentID := makeTestAgent(t, w, conn.WorkspaceID, "Fixer")
	conn = tagRuleConn(t, q, conn, fmt.Sprintf(`[{"match_type":"label","match_value":"route-x","agent_id":%q}]`, uuidStr(agentID)))
	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	labels.Store(`["frontend"]`) // ordinary label — not a configured route
	if _, err := w.runCycle(ctx, rewindConn(t, q, conn.ID, now.Add(-time.Hour))); err != nil {
		t.Fatalf("label: %v", err)
	}
	if n := agentTaskCount(t, w, conn.WorkspaceID); n != 0 {
		t.Fatalf("ordinary label fired %d runs, want 0", n)
	}
}

// Pin (RU §25): a disabled connection is never handed to the worker —
// ListEnabledJiraConnections (the only source of cycle work) excludes it.
func TestDisabledConnectionNotListed(t *testing.T) {
	f := newFakeJira(t)
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	if _, err := w.Pool.Exec(ctx, "UPDATE jira_connection SET enabled=false WHERE id=$1", conn.ID); err != nil {
		t.Fatal(err)
	}
	enabled, err := q.ListEnabledJiraConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range enabled {
		if c.ID == conn.ID {
			t.Fatal("disabled connection must not be listed as enabled")
		}
	}
}

// End-to-end (RU §12.2): a human moves Jira to a terminal status, then the agent
// completes locally. The next cycle takes the local-only outbound path (Jira not
// re-observed) — the guard must suppress the transition off the human terminal.
func TestNoOutboundTransitionOffHumanTerminal(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	phase := atomic.Value{}
	phase.Store("open")
	var transitions atomic.Int64
	f.mux.HandleFunc("/rest/api/3/search/jql", func(wr http.ResponseWriter, r *http.Request) {
		switch phase.Load() {
		case "open":
			fmt.Fprintf(wr, `{"issues":[{"id":"id-T","key":"T-1","fields":{
				"summary":"T","description":{"type":"doc","version":1,"content":[]},
				"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
				"labels":[],"updated":%q}}],"isLast":true}`, now.Add(-2*time.Hour).Format(jiraTimeLayout))
		case "cancelled":
			fmt.Fprintf(wr, `{"issues":[{"id":"id-T","key":"T-1","fields":{
				"summary":"T","description":{"type":"doc","version":1,"content":[]},
				"status":{"id":"900","name":"Cancelled","statusCategory":{"key":"done"}},
				"labels":[],"updated":%q}}],"isLast":true}`, now.Add(-time.Hour).Format(jiraTimeLayout))
		default:
			wr.Write([]byte(`{"issues":[],"isLast":true}`))
		}
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-T/comment", func(wr http.ResponseWriter, r *http.Request) {
		wr.Write([]byte(`{"comments":[],"total":0}`))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-T/transitions", func(wr http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			transitions.Add(1)
			wr.WriteHeader(http.StatusNoContent)
			return
		}
		wr.Write([]byte(`{"transitions":[{"id":"t-done","to":{"id":"800","name":"Done"}}]}`))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	conn, _ = q.UpdateJiraConnectionConfig(ctx, db.UpdateJiraConnectionConfigParams{
		ID: conn.ID, Enabled: true, Mode: "two_way", LeadingSystem: "jira",
		CommentsEnabled: true, LabelsEnabled: true, CustomFieldsEnabled: true,
		CreateFromJira: true, CreateToJira: false, JqlFilter: "", LabelPrefix: "",
		MentionBridgeEnabled: true, OutboundIssueType: "Task",
		StatusMap: []byte(`{"in":{"100":"todo","900":"cancelled"},"out":{"todo":"100","cancelled":"900","done":"800"}}`),
		FieldMap:  []byte(`[]`), TagRules: []byte(`[]`), CycleIntervalSeconds: 45,
	})
	setCursors := func(to time.Time) db.JiraConnection {
		_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
			ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: to, Valid: true}, LocalCursor: pgtype.Timestamptz{Time: to, Valid: true}})
		c, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		return c
	}
	// Cycle 1: import at status 100.
	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-T"})
	// Cycle 2: a human moves Jira to Cancelled (done category) → pulled in.
	phase.Store("cancelled")
	if _, err := w.runCycle(ctx, setCursors(now.Add(-90*time.Minute))); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if issue, _ := q.GetIssue(ctx, link.IssueID); issue.Status != "cancelled" {
		t.Fatalf("human cancel must be pulled, got %q", issue.Status)
	}
	// The agent completes locally; Jira stays quiet.
	if _, err := q.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: link.IssueID, Status: "done", WorkspaceID: conn.WorkspaceID}); err != nil {
		t.Fatal(err)
	}
	phase.Store("quiet")
	// Cycle 3: local-only outbound path — the guard must suppress the transition.
	if _, err := w.runCycle(ctx, setCursors(now.Add(-30*time.Minute))); err != nil {
		t.Fatalf("local-only: %v", err)
	}
	if transitions.Load() != 0 {
		t.Fatalf("agent completion must NOT transition Jira off a human terminal, got %d", transitions.Load())
	}
	if journalCount(t, w, conn.ID, "status_terminal_guard") == 0 {
		t.Fatal("terminal guard must be journaled")
	}
}

// A single agent reached by BOTH a label rule and an assignee rule in one
// observation is not ambiguous (dedup by target agent) — it assigns normally.
func TestSingleAgentViaTwoRulesNotAmbiguous(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	sig := atomic.Value{}
	sig.Store("none")
	f.mux.HandleFunc("/rest/api/3/search/jql", func(wr http.ResponseWriter, r *http.Request) {
		if sig.Load() == "both" {
			fmt.Fprintf(wr, `{"issues":[{"id":"id-SA","key":"SA-1","fields":{
				"summary":"S","description":{"type":"doc","version":1,"content":[]},
				"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
				"assignee":{"accountId":"acc-x"},"labels":["route-x"],"updated":%q}}],"isLast":true}`, now.Format(jiraTimeLayout))
			return
		}
		fmt.Fprintf(wr, `{"issues":[{"id":"id-SA","key":"SA-1","fields":{
			"summary":"S","description":{"type":"doc","version":1,"content":[]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":[],"updated":%q}}],"isLast":true}`, now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-SA/comment", func(wr http.ResponseWriter, r *http.Request) {
		wr.Write([]byte(`{"comments":[],"total":0}`))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	agentID := makeTestAgent(t, w, conn.WorkspaceID, "Solo")
	conn = tagRuleConn(t, q, conn, fmt.Sprintf(
		`[{"match_type":"label","match_value":"route-x","agent_id":%q},{"match_type":"assignee","match_value":"acc-x","agent_id":%q}]`,
		uuidStr(agentID), uuidStr(agentID)))
	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-SA"})
	sig.Store("both")
	if _, err := w.runCycle(ctx, rewindConn(t, q, conn.ID, now.Add(-time.Hour))); err != nil {
		t.Fatalf("both: %v", err)
	}
	issue, _ := q.GetIssue(ctx, link.IssueID)
	if issue.AssigneeType.String != "agent" || issue.AssigneeID != agentID {
		t.Fatalf("one agent via two signals must assign, not block: %+v", issue)
	}
	if journalCount(t, w, conn.ID, "tag_ambiguous") != 0 {
		t.Fatal("single agent via two rules must not be flagged ambiguous")
	}
}
