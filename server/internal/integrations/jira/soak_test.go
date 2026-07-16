package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Story 3.3 — echo & churn immunity (FR-20/FR-21, SM-3 core). A stateful fake
// Jira lets comments flow both ways across many cycles and a mid-soak
// "restart" (fresh Worker instance): totals must come out exact, quiescent
// cycles must issue zero write calls, and Breadcrumbs must stay inert.

type soakJira struct {
	mu       sync.Mutex
	issueKey string
	issueID  string
	summary  string
	updated  time.Time
	comments []map[string]any
	posts    int
	writes   int
}

func newSoakJira(t *testing.T, f *fakeJira, issueID, key string) *soakJira {
	s := &soakJira{issueID: issueID, issueKey: key, summary: "Soak issue", updated: time.Now().UTC()}
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		fmt.Fprintf(w, `{"issues":[{"id":%q,"key":%q,"fields":{
			"summary":%q,
			"description":{"type":"doc","version":1,"content":[
				{"type":"paragraph","content":[{"type":"text","text":"soak body"}]},
				{"type":"mediaSingle","content":[{"type":"media","attrs":{"id":"x"}}]}
			]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":["keep-me"],
			"updated":%q
		}}],"isLast":true}`, s.issueID, s.issueKey, s.summary, s.updated.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/"+issueID+"/comment", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			s.posts++
			s.writes++
			id := fmt.Sprintf("posted-%d", s.posts)
			var payload struct {
				Body json.RawMessage `json:"body"`
			}
			_ = json.Unmarshal(body, &payload)
			s.comments = append(s.comments, map[string]any{
				"id":      id,
				"author":  map[string]any{"accountId": "acc-bot", "displayName": "Sync Bot"},
				"body":    json.RawMessage(payload.Body),
				"created": time.Now().UTC().Format(jiraTimeLayout),
			})
			fmt.Fprintf(w, `{"id":%q}`, id)
			return
		}
		out := map[string]any{"comments": s.comments, "startAt": 0, "maxResults": 1000, "total": len(s.comments)}
		_ = json.NewEncoder(w).Encode(out)
	})
	t.Cleanup(func() {})
	return s
}

func (s *soakJira) addHumanComment(i int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.comments = append(s.comments, map[string]any{
		"id":      fmt.Sprintf("human-%d", i),
		"author":  map[string]any{"accountId": "acc-human", "displayName": "Marco"},
		"body":    json.RawMessage(fmt.Sprintf(`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"jira says %d"}]}]}`, i)),
		"created": time.Now().UTC().Format(jiraTimeLayout),
	})
	s.updated = time.Now().UTC()
}

func rewindCursor(t *testing.T, q *db.Queries, connID pgtype.UUID) db.JiraConnection {
	t.Helper()
	c, err := q.GetJiraConnectionByID(context.Background(), connID)
	if err != nil {
		t.Fatal(err)
	}
	_ = q.UpdateJiraConnectionCursors(context.Background(), db.UpdateJiraConnectionCursorsParams{
		ID: connID, JiraCursor: pgtype.Timestamptz{Time: time.Now().UTC().Add(-time.Hour), Valid: true}, LocalCursor: c.LocalCursor,
	})
	c, _ = q.GetJiraConnectionByID(context.Background(), connID)
	return c
}

func TestSoakAlternatingCommentsAcrossRestartExactTotals(t *testing.T) {
	f := newFakeJira(t)
	soak := newSoakJira(t, f, "soak-1", "GAME-40")
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	if err := q.UpdateJiraConnectionServiceAccount(ctx, db.UpdateJiraConnectionServiceAccountParams{
		ID: conn.ID, ServiceAccountID: "acc-bot",
	}); err != nil {
		t.Fatal(err)
	}

	const rounds = 20 // 20 jira + 20 local = 40 comments total each side
	if _, err := w.runCycle(ctx, rewindCursor(t, q, conn.ID)); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "soak-1"})

	for i := 0; i < rounds; i++ {
		if i == rounds/2 {
			// Mid-soak restart: a brand-new worker with empty in-memory state.
			w = NewWorker(w.Pool, w.Q, w.Svc, w.Issues, nil)
			w.sleep = func(time.Duration) {}
		}
		soak.addHumanComment(i)
		if _, err := q.CreateComment(ctx, db.CreateCommentParams{
			IssueID: link.IssueID, WorkspaceID: conn.WorkspaceID,
			AuthorType: "member", AuthorID: testUUID(),
			Content: fmt.Sprintf("multica says %d", i), Type: "comment",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.runCycle(ctx, rewindCursor(t, q, conn.ID)); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
	}
	// Two settle cycles: everything already synced — totals must not move.
	for i := 0; i < 2; i++ {
		if _, err := w.runCycle(ctx, rewindCursor(t, q, conn.ID)); err != nil {
			t.Fatalf("settle: %v", err)
		}
	}

	if soak.posts != rounds {
		t.Fatalf("jira must receive exactly %d posted comments, got %d", rounds, soak.posts)
	}
	rows, _ := q.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{IssueID: link.IssueID, WorkspaceID: conn.WorkspaceID, Limit: 500})
	var mirrors, locals int
	for _, c := range rows {
		switch {
		case c.AuthorType == "system" && strings.Contains(c.Content, "From Jira"):
			mirrors++
		case c.AuthorType == "member":
			locals++
		}
	}
	if mirrors != rounds || locals != rounds {
		t.Fatalf("exact totals violated: mirrors=%d locals=%d want %d/%d", mirrors, locals, rounds, rounds)
	}
}

func TestSoakQuiescentPairMakesZeroWrites(t *testing.T) {
	f := newFakeJira(t)
	soak := newSoakJira(t, f, "soak-2", "GAME-41")
	w, conn, q := importFixture(t, f)
	ctx := context.Background()

	if _, err := w.runCycle(ctx, rewindCursor(t, q, conn.ID)); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "soak-2"})
	before, _ := q.GetIssue(ctx, link.IssueID)
	writesAfterImport := soak.writes

	for i := 0; i < 10; i++ {
		if _, err := w.runCycle(ctx, rewindCursor(t, q, conn.ID)); err != nil {
			t.Fatalf("quiescent cycle %d: %v", i, err)
		}
	}
	if soak.writes != writesAfterImport {
		t.Fatalf("quiescent pair must issue ZERO write calls (FR-20): %d extra", soak.writes-writesAfterImport)
	}
	after, _ := q.GetIssue(ctx, link.IssueID)
	if !after.UpdatedAt.Time.Equal(before.UpdatedAt.Time) {
		t.Fatalf("local mirror must not churn either: %v vs %v", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestSoakBreadcrumbWithMentionStaysInert(t *testing.T) {
	f := newFakeJira(t)
	soak := newSoakJira(t, f, "soak-3", "GAME-42")
	w, conn, q := importFixture(t, f)
	ctx := context.Background()

	if _, err := w.runCycle(ctx, rewindCursor(t, q, conn.ID)); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "soak-3"})

	// Human edits the local title to something containing an agent mention,
	// then Jira edits the same item → divergence → breadcrumb quoting the
	// discarded "@Fixer" value.
	cur, _ := q.GetIssue(ctx, link.IssueID)
	if _, err := q.UpdateIssue(ctx, db.UpdateIssueParams{
		ID: cur.ID, Title: pgtype.Text{String: "ask @Fixer about this", Valid: true},
		Description: cur.Description, AssigneeType: cur.AssigneeType, AssigneeID: cur.AssigneeID,
		StartDate: cur.StartDate, DueDate: cur.DueDate, ParentIssueID: cur.ParentIssueID,
		ProjectID: cur.ProjectID, Stage: cur.Stage,
	}); err != nil {
		t.Fatal(err)
	}
	soak.mu.Lock()
	soak.summary = "Jira renamed it"
	soak.updated = time.Now().UTC()
	soak.mu.Unlock()
	postsBefore := soak.posts

	if _, err := w.runCycle(ctx, rewindCursor(t, q, conn.ID)); err != nil {
		t.Fatalf("divergence cycle: %v", err)
	}

	rows, _ := q.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{IssueID: link.IssueID, WorkspaceID: conn.WorkspaceID, Limit: 100})
	foundBreadcrumb := false
	for _, c := range rows {
		if c.AuthorType == "system" && strings.Contains(c.Content, "@Fixer") {
			foundBreadcrumb = true
		}
	}
	if !foundBreadcrumb {
		t.Fatal("expected a breadcrumb quoting the discarded value")
	}
	if soak.posts != postsBefore {
		t.Fatalf("breadcrumbs must never mirror onward (FR-21): %d extra posts", soak.posts-postsBefore)
	}
	var tasks int
	if err := w.Pool.QueryRow(ctx,
		"SELECT count(*) FROM agent_task_queue q JOIN issue i ON i.id = q.issue_id WHERE i.workspace_id = $1",
		conn.WorkspaceID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if tasks != 0 {
		t.Fatalf("breadcrumb mentioning an agent must not enqueue any run (FR-21): %d tasks", tasks)
	}
}
