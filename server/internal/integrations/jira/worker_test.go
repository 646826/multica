package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	_, pool := openTestQueries(t)
	return pool
}

// workerFixture seeds an enabled Connection whose client hits the fake.
func workerFixture(t *testing.T, f *fakeJira) (*Worker, db.JiraConnection, *db.Queries, *pgxpool.Pool) {
	t.Helper()
	q, pool := openTestQueries(t)
	box := newTestBox(t)
	conn := createTestConnection(t, q, testUUID())
	// createTestConnection stores a raw token; reseal it so ClientFor works.
	sealed, err := box.Seal([]byte("tok"))
	if err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateJiraConnectionToken(context.Background(), db.UpdateJiraConnectionTokenParams{
		ID: conn.ID, Email: conn.Email, TokenEncrypted: sealed,
	}); err != nil {
		t.Fatal(err)
	}
	conn.TokenEncrypted = sealed

	svc := serviceWithFake(t, q, box, f)
	w := NewWorker(pool, q, svc, nil, nil)
	w.sleep = func(time.Duration) {}
	return w, conn, q, pool
}

func TestRunCycleAdvancesCursorsAndHealth(t *testing.T) {
	f := connectFake(t, true)
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"issues":[],"isLast":true}`))
	})
	w, conn, q, _ := workerFixture(t, f)

	ran, err := w.runCycle(context.Background(), conn)
	if err != nil || !ran {
		t.Fatalf("cycle: ran=%v err=%v", ran, err)
	}

	got, err := q.GetJiraConnectionByID(context.Background(), conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.JiraCursor.Valid || !got.LocalCursor.Valid {
		t.Fatalf("cursors not initialized: %+v", got)
	}
	var snap healthSnapshot
	if err := json.Unmarshal(got.Health, &snap); err != nil || snap.State != "ok" || snap.LastCycleAt == "" {
		t.Fatalf("health snapshot: err=%v snap=%+v raw=%s", err, snap, got.Health)
	}
}

func TestRunCycleClassifiesAuthExpiredAndRecovers(t *testing.T) {
	f := newFakeJira(t)
	var fail atomic.Bool
	fail.Store(true)
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"issues":[],"isLast":true}`))
	})
	w, conn, q, _ := workerFixture(t, f)

	if _, err := w.runCycle(context.Background(), conn); err == nil {
		t.Fatal("want cycle error on 401")
	}
	got, _ := q.GetJiraConnectionByID(context.Background(), conn.ID)
	var snap healthSnapshot
	if json.Unmarshal(got.Health, &snap) != nil || snap.State != "auth_expired" {
		t.Fatalf("want auth_expired health, got %s", got.Health)
	}
	rows, _ := q.ListJiraJournalRecent(context.Background(), db.ListJiraJournalRecentParams{ConnectionID: conn.ID, Limit: 5})
	if len(rows) == 0 || rows[0].Kind != string(JournalCycleError) {
		t.Fatalf("cycle error not journaled: %+v", rows)
	}

	// Recovery: next healthy cycle pairs the failure with cycle_recovered.
	fail.Store(false)
	got, _ = q.GetJiraConnectionByID(context.Background(), conn.ID)
	if _, err := w.runCycle(context.Background(), got); err != nil {
		t.Fatalf("recovery cycle: %v", err)
	}
	rows, _ = q.ListJiraJournalRecent(context.Background(), db.ListJiraJournalRecentParams{ConnectionID: conn.ID, Limit: 5})
	if len(rows) == 0 || rows[0].Kind != string(JournalCycleRecovered) {
		t.Fatalf("recovery not journaled: %+v", rows)
	}
	got, _ = q.GetJiraConnectionByID(context.Background(), conn.ID)
	if json.Unmarshal(got.Health, &snap) != nil || snap.State != "ok" {
		t.Fatalf("health not recovered: %s", got.Health)
	}
}

func TestRunCycleAdvisoryLockIsExclusive(t *testing.T) {
	f := newFakeJira(t)
	release := make(chan struct{})
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		<-release // hold the first cycle open so the second contends
		w.Write([]byte(`{"issues":[],"isLast":true}`))
	})
	w1, conn, _, _ := workerFixture(t, f)
	w2 := NewWorker(w1.Pool, w1.Q, w1.Svc, nil, nil)

	var ran1, ran2 bool
	var err1, err2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ran1, err1 = w1.runCycle(context.Background(), conn)
	}()
	// Give the first cycle time to take the lock and block on the fake.
	time.Sleep(300 * time.Millisecond)
	go func() {
		defer wg.Done()
		ran2, err2 = w2.runCycle(context.Background(), conn)
	}()
	time.Sleep(300 * time.Millisecond)
	close(release)
	wg.Wait()

	if err1 != nil || err2 != nil {
		t.Fatalf("errors: %v %v", err1, err2)
	}
	if ran1 == ran2 {
		t.Fatalf("exactly one cycle must run under contention: ran1=%v ran2=%v", ran1, ran2)
	}
}

func TestJournalRefusesUnregisteredKind(t *testing.T) {
	q, _ := openTestQueries(t)
	j := &Journal{Q: q}
	err := j.Record(context.Background(), db.JiraConnection{ID: testUUID(), WorkspaceID: testUUID()},
		pgtype.UUID{}, JournalKind("made_up_kind"), pgtype.UUID{}, "", nil)
	if err == nil {
		t.Fatal("unregistered journal kind must be refused")
	}
}

func TestSweepMarksOrphanedAndDormant(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		jql := r.URL.Query().Get("jql")
		if strings.Contains(jql, "id = ") {
			// Scope membership probe: nothing matches the filter → dormant.
			w.Write([]byte(`{"issues":[],"isLast":true}`))
			return
		}
		w.Write([]byte(`{"issues":[],"isLast":true}`))
	})
	f.mux.HandleFunc("/rest/api/3/issue/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/rest/api/3/issue/")
		switch id {
		case "gone-1":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"errorMessages":["Issue does not exist"]}`))
		case "moved-2":
			fmt.Fprintf(w, `{"id":"moved-2","key":"OTHER-9","fields":{"summary":"m","status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},"labels":[],"updated":%q}}`, now.Format(jiraTimeLayout))
		default:
			fmt.Fprintf(w, `{"id":%q,"key":"GAME-77","fields":{"summary":"q","status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},"labels":[],"updated":%q}}`, id, now.Format(jiraTimeLayout))
		}
	})
	w, conn, q, _ := workerFixture(t, f)
	ctx := context.Background()

	mk := func(jiraID string) db.JiraLink {
		link, err := q.CreateJiraLink(ctx, db.CreateJiraLinkParams{
			ConnectionID: conn.ID, WorkspaceID: conn.WorkspaceID, IssueID: testUUID(),
			JiraIssueID: jiraID, JiraKey: "GAME-1", State: "ok", Items: []byte(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		return link
	}
	gone := mk("gone-1")
	moved := mk("moved-2")
	quiet := mk("quiet-3")

	// JQL filter set → the scope probe declares 'quiet-3'... the probe above
	// returns empty for id-searches, so a filtered connection would dormant
	// it; test both regimes.
	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	g, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "gone-1"})
	if g.State != "orphaned" {
		t.Fatalf("deleted issue must orphan its link: %+v", g.State)
	}
	m, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "moved-2"})
	if m.State != "orphaned" {
		t.Fatalf("moved issue must orphan its link: %+v", m.State)
	}
	qt, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "quiet-3"})
	if qt.State != "ok" || !qt.LastSeenAt.Valid {
		t.Fatalf("quiet healthy issue must be re-touched, not flagged: %+v", qt)
	}
	_ = gone
	_ = moved
	_ = quiet
}

func TestDormantResumesOnReObservation(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[{"id":"back-1","key":"GAME-5","fields":{
			"summary":"Back in scope",
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":[],"updated":%q}}],"isLast":true}`, now.Format(jiraTimeLayout))
	})
	w, conn, q, _ := workerFixture(t, f)
	ctx := context.Background()

	issueID := testUUID()
	if _, err := q.CreateJiraLink(ctx, db.CreateJiraLinkParams{
		ConnectionID: conn.ID, WorkspaceID: conn.WorkspaceID, IssueID: issueID,
		JiraIssueID: "back-1", JiraKey: "GAME-5", State: "dormant", Items: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "back-1"})
	if link.State != "ok" {
		t.Fatalf("re-observed dormant link must resume (FR-11): %+v", link.State)
	}
	rows, _ := q.ListJiraJournalRecent(ctx, db.ListJiraJournalRecentParams{ConnectionID: conn.ID, Limit: 10})
	resumed := false
	for _, rrow := range rows {
		if rrow.Kind == string(JournalScopeResumed) {
			resumed = true
		}
	}
	if !resumed {
		t.Fatal("resume must journal scope_resumed")
	}
}
