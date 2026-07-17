package jira

import (
	"context"
	"fmt"
	"net/http"
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
