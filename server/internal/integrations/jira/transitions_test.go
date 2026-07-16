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

// Story 4.1 — outbound status transitions (FR-17).

func transitionsFixture(t *testing.T, reachable *atomic.Bool) (*fakeJira, *atomic.Int64) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	var posts atomic.Int64
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[%s],"isLast":true}`,
			searchIssueWithCategory("GAME-50", now.Add(-5*time.Minute), "100", "new"))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-50/comment", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"comments":[],"startAt":0,"maxResults":100,"total":0}`))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-50/transitions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if reachable.Load() {
			w.Write([]byte(`{"transitions":[{"id":"t-7","to":{"id":"200","name":"In Progress"}}]}`))
			return
		}
		w.Write([]byte(`{"transitions":[{"id":"t-9","to":{"id":"300","name":"Done"}}]}`))
	})
	return f, &posts
}

func TestOutboundTransitionFiresOnceAndForwards(t *testing.T) {
	var reachable atomic.Bool
	reachable.Store(true)
	f, posts := transitionsFixture(t, &reachable)
	w, conn, q := importFixture(t, f)
	ctx := context.Background()

	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	if posts.Load() != 0 {
		t.Fatalf("import/inbound must never trigger outbound transitions: %d", posts.Load())
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-50"})

	// Local status change — the same write path GitHub-driven changes use.
	if _, err := q.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID: link.IssueID, Status: "in_progress", WorkspaceID: conn.WorkspaceID,
	}); err != nil {
		t.Fatal(err)
	}
	c2, _ := q.GetJiraConnectionByID(ctx, conn.ID)
	if _, err := w.runCycle(ctx, c2); err != nil {
		t.Fatalf("outbound cycle: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("want exactly one transition POST, got %d", posts.Load())
	}
	link, _ = q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-50"})
	items, _ := ParseItems(link.Items)
	if items.Status.RemoteID != "200" || items.Status.Local != "in_progress" {
		t.Fatalf("status snapshot not forwarded: %+v", items.Status)
	}

	// Replay cycles: mapped-equivalence + forwarded snapshot ⇒ no more POSTs.
	for i := 0; i < 2; i++ {
		c3, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		if _, err := w.runCycle(ctx, c3); err != nil {
			t.Fatalf("replay: %v", err)
		}
	}
	if posts.Load() != 1 {
		t.Fatalf("transition must not repeat: %d", posts.Load())
	}
}

func TestOutboundTransitionUnreachableJournalsOnceThenRecovers(t *testing.T) {
	var reachable atomic.Bool
	reachable.Store(false)
	f, posts := transitionsFixture(t, &reachable)
	w, conn, q := importFixture(t, f)
	ctx := context.Background()

	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-50"})
	if _, err := q.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID: link.IssueID, Status: "in_progress", WorkspaceID: conn.WorkspaceID,
	}); err != nil {
		t.Fatal(err)
	}

	countKind := func(kind JournalKind) int {
		rows, _ := q.ListJiraJournalRecent(ctx, db.ListJiraJournalRecentParams{ConnectionID: conn.ID, Limit: 50})
		n := 0
		for _, r := range rows {
			if r.Kind == string(kind) {
				n++
			}
		}
		return n
	}

	for i := 0; i < 3; i++ {
		c2, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		// keep the local change in the observation window
		_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
			ID: conn.ID, JiraCursor: c2.JiraCursor, LocalCursor: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		})
		c2, _ = q.GetJiraConnectionByID(ctx, conn.ID)
		if _, err := w.runCycle(ctx, c2); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}
	if posts.Load() != 0 {
		t.Fatalf("unreachable target must not POST: %d", posts.Load())
	}
	if got := countKind(JournalTransitionUnreachable); got != 1 {
		t.Fatalf("unreachable must journal exactly once per occurrence, got %d", got)
	}

	// The workflow gains the edge → next cycle transitions and pairs recovery.
	reachable.Store(true)
	c3, _ := q.GetJiraConnectionByID(ctx, conn.ID)
	_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
		ID: conn.ID, JiraCursor: c3.JiraCursor, LocalCursor: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
	})
	c3, _ = q.GetJiraConnectionByID(ctx, conn.ID)
	if _, err := w.runCycle(ctx, c3); err != nil {
		t.Fatalf("recovery cycle: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("recovered transition must fire once: %d", posts.Load())
	}
	if got := countKind(JournalTransitionRecovered); got != 1 {
		t.Fatalf("recovery must journal transition_recovered once, got %d", got)
	}
}
