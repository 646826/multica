package jira

import (
	"context"
	"fmt"
	"net/http"
	"sync"
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

// --- Story 4.2 / 4.3: outbound fields + divergence breadcrumbs ---

func TestOutboundFieldsPushOnceWithReadBackFixpoint(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	var puts atomic.Int64
	summary := atomic.Value{}
	summary.Store("Imported GAME-60")
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[{"id":"id-GAME-60","key":"GAME-60","fields":{
			"summary":%q,
			"description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"body"}]}]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":[],"updated":%q}}],"isLast":true}`, summary.Load(), now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-60", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			puts.Add(1)
			summary.Store("Multica renamed it")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// GET read-back returns the just-written value.
		fmt.Fprintf(w, `{"id":"id-GAME-60","key":"GAME-60","fields":{
			"summary":%q,
			"description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"body"}]}]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":[],"updated":%q}}`, summary.Load(), now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-60/comment", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"comments":[],"startAt":0,"maxResults":100,"total":0}`))
	})
	// Multica-leads: local fields push to Jira.
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	c, _ := q.UpdateJiraConnectionConfig(ctx, db.UpdateJiraConnectionConfigParams{
		ID: conn.ID, Enabled: true, Mode: "multica_leads", LeadingSystem: "multica",
		CommentsEnabled: true, LabelsEnabled: true, CustomFieldsEnabled: true,
		CreateFromJira: true, CreateToJira: true, JqlFilter: "", LabelPrefix: "",
		MentionBridgeEnabled: true, OutboundIssueType: "Task",
		StatusMap: []byte(`{"in":{"100":"todo"},"out":{"todo":"100"}}`),
		FieldMap:  []byte(`[]`), TagRules: []byte(`[]`), CycleIntervalSeconds: 45,
	})
	if _, err := w.runCycle(ctx, c); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-60"})

	cur, _ := q.GetIssue(ctx, link.IssueID)
	if _, err := q.UpdateIssue(ctx, db.UpdateIssueParams{
		ID: cur.ID, Title: pgtype.Text{String: "Multica renamed it", Valid: true},
		Description: cur.Description, AssigneeType: cur.AssigneeType, AssigneeID: cur.AssigneeID,
		StartDate: cur.StartDate, DueDate: cur.DueDate, ParentIssueID: cur.ParentIssueID,
		ProjectID: cur.ProjectID, Stage: cur.Stage,
	}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		cc, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
			ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true},
			LocalCursor: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true},
		})
		cc, _ = q.GetJiraConnectionByID(ctx, conn.ID)
		if _, err := w.runCycle(ctx, cc); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}
	if puts.Load() != 1 {
		t.Fatalf("outbound field write must fire exactly once then reach fixpoint (FR-20): %d PUTs", puts.Load())
	}
}

func TestOutboundDivergenceBreadcrumbOnRemoteSide(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	var comments atomic.Int64
	summary := atomic.Value{}
	summary.Store("Imported GAME-61")
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[{"id":"id-GAME-61","key":"GAME-61","fields":{
			"summary":%q,
			"description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"body"}]}]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":[],"updated":%q}}],"isLast":true}`, summary.Load(), now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-61", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			summary.Store("Multica wins")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		fmt.Fprintf(w, `{"id":"id-GAME-61","key":"GAME-61","fields":{"summary":%q,
			"description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"body"}]}]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},"labels":[],"updated":%q}}`, summary.Load(), now.Format(jiraTimeLayout))
	})
	var mu sync.Mutex
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-61/comment", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			comments.Add(1)
			mu.Lock()
			mu.Unlock()
			w.Write([]byte(`{"id":"jc-bc"}`))
			return
		}
		w.Write([]byte(`{"comments":[],"startAt":0,"maxResults":100,"total":0}`))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	c, _ := q.UpdateJiraConnectionConfig(ctx, db.UpdateJiraConnectionConfigParams{
		ID: conn.ID, Enabled: true, Mode: "multica_leads", LeadingSystem: "multica",
		CommentsEnabled: true, LabelsEnabled: true, CustomFieldsEnabled: true,
		CreateFromJira: true, CreateToJira: true, JqlFilter: "", LabelPrefix: "",
		MentionBridgeEnabled: true, OutboundIssueType: "Task",
		StatusMap: []byte(`{"in":{"100":"todo"},"out":{"todo":"100"}}`),
		FieldMap:  []byte(`[]`), TagRules: []byte(`[]`), CycleIntervalSeconds: 45,
	})
	if _, err := w.runCycle(ctx, c); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-61"})

	// Both sides change the title → multica leads → Multica wins, Jira side
	// gets exactly one breadcrumb comment.
	summary.Store("Jira also edited")
	cur, _ := q.GetIssue(ctx, link.IssueID)
	if _, err := q.UpdateIssue(ctx, db.UpdateIssueParams{
		ID: cur.ID, Title: pgtype.Text{String: "Multica wins", Valid: true},
		Description: cur.Description, AssigneeType: cur.AssigneeType, AssigneeID: cur.AssigneeID,
		StartDate: cur.StartDate, DueDate: cur.DueDate, ParentIssueID: cur.ParentIssueID,
		ProjectID: cur.ProjectID, Stage: cur.Stage,
	}); err != nil {
		t.Fatal(err)
	}

	rewind := func() db.JiraConnection {
		cc, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
			ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true},
			LocalCursor: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true},
		})
		cc, _ = q.GetJiraConnectionByID(ctx, conn.ID)
		return cc
	}
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("divergence cycle: %v", err)
	}
	if comments.Load() != 1 {
		t.Fatalf("exactly one outbound breadcrumb, got %d", comments.Load())
	}
	// Replay: no second breadcrumb.
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if comments.Load() != 1 {
		t.Fatalf("breadcrumb must not repeat: %d", comments.Load())
	}
}
