package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Import-path tests (Story 2.3): the FR-10 exactly-once protocol against a
// fake Jira and the real IssueService (live Postgres). Issues created here
// are unassigned, so no agent dispatch can fire (FR-28 precondition).

func importFixture(t *testing.T, f *fakeJira) (*Worker, db.JiraConnection, *db.Queries) {
	t.Helper()
	w, conn, q, pool := workerFixture(t, f)
	w.Issues = service.NewIssueService(q, pool, events.New(), analytics.NoopClient{}, nil)

	// Imports create real issues: the workspace row must exist (issue counter
	// lives on it). Rebind the connection to a real workspace.
	ws, err := q.CreateWorkspace(context.Background(), db.CreateWorkspaceParams{
		Name:        "jira-import-test",
		Slug:        fmt.Sprintf("jira-imp-%s", uuidStr(testUUID())[:8]),
		IssuePrefix: "JIT",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.DeleteWorkspace(context.Background(), ws.ID) })
	if _, err := pool.Exec(context.Background(),
		"UPDATE jira_connection SET workspace_id = $2 WHERE id = $1", conn.ID, ws.ID); err != nil {
		t.Fatal(err)
	}

	refreshed, err := q.GetJiraConnectionByID(context.Background(), conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	conn = refreshed

	// The import gate needs a usable status map.
	upd, err := q.UpdateJiraConnectionConfig(context.Background(), db.UpdateJiraConnectionConfigParams{
		ID: conn.ID, Enabled: true, Mode: conn.Mode, LeadingSystem: conn.LeadingSystem,
		CommentsEnabled: true, LabelsEnabled: true, CustomFieldsEnabled: true,
		CreateFromJira: true, CreateToJira: false,
		JqlFilter: "", LabelPrefix: "", MentionBridgeEnabled: true,
		OutboundIssueType: "Task",
		StatusMap:         []byte(`{"in":{"100":"todo","200":"in_progress"},"out":{"todo":"100","in_progress":"200"}}`),
		FieldMap:          []byte(`[]`), TagRules: []byte(`[]`),
		CycleIntervalSeconds: 45,
	})
	if err != nil {
		t.Fatal(err)
	}
	return w, upd, q
}

func searchIssueWithCategory(key string, updated time.Time, statusID, category string) string {
	return fmt.Sprintf(`{"id":"id-%s","key":%q,"fields":{
		"summary":"Imported %s",
		"description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"desc %s"}]}]},
		"status":{"id":%q,"name":"S","statusCategory":{"key":%q}},
		"labels":[],
		"updated":%q
	}}`, key, key, key, key, statusID, category, updated.Format(jiraTimeLayout))
}

func TestImportCreatesNonTerminalExactlyOnce(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[%s,%s,%s],"isLast":true}`,
			searchIssueWithCategory("GAME-1", now.Add(-3*time.Minute), "100", "new"),
			searchIssueWithCategory("GAME-2", now.Add(-2*time.Minute), "999", "indeterminate"), // unmapped status
			searchIssueWithCategory("GAME-3", now.Add(-1*time.Minute), "300", "done"),          // terminal: skipped
		)
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()

	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	link1, err := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-1"})
	if err != nil || link1.State != "ok" || !link1.IssueID.Valid {
		t.Fatalf("GAME-1 link: %v %+v", err, link1)
	}
	issue1, err := q.GetIssue(ctx, link1.IssueID)
	if err != nil || issue1.Title != "Imported GAME-1" || issue1.Status != "todo" {
		t.Fatalf("GAME-1 issue: %v %+v", err, issue1)
	}
	if !strings.Contains(string(issue1.Metadata), markerKey) {
		t.Fatalf("marker not stamped: %s", issue1.Metadata)
	}

	link2, err := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-2"})
	if err != nil {
		t.Fatalf("GAME-2 link: %v", err)
	}
	issue2, err := q.GetIssue(ctx, link2.IssueID)
	if err != nil || issue2.Status != "backlog" {
		t.Fatalf("unmapped status must import into backlog: %v %+v", err, issue2)
	}
	rows, _ := q.ListJiraJournalRecent(ctx, db.ListJiraJournalRecentParams{ConnectionID: conn.ID, Limit: 20})
	foundUnmapped := false
	for _, rrow := range rows {
		if rrow.Kind == string(JournalStatusUnmapped) {
			foundUnmapped = true
		}
	}
	if !foundUnmapped {
		t.Fatal("unmapped import status must journal")
	}

	if _, err := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-3"}); err == nil {
		t.Fatal("done-category issue must not import (FR-10 non-terminal gate)")
	}

	// Cursor advanced to the newest observation.
	got, _ := q.GetJiraConnectionByID(ctx, conn.ID)
	if !got.JiraCursor.Valid || got.JiraCursor.Time.Before(now.Add(-90*time.Second)) {
		t.Fatalf("cursor must advance to max observed updated: %+v", got.JiraCursor)
	}

	// Idempotency: replay the same window (fresh conn row → old cursor).
	if _, err := w.runCycle(ctx, got); err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	links := 0
	for _, id := range []string{"id-GAME-1", "id-GAME-2"} {
		if _, err := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: id}); err == nil {
			links++
		}
	}
	if links != 2 {
		t.Fatalf("replay must not change link count: %d", links)
	}
	after1, _ := q.GetIssue(ctx, link1.IssueID)
	if after1.Title != issue1.Title || after1.Status != issue1.Status {
		t.Fatalf("replay mutated the mirror: %+v", after1)
	}
}

func TestImportRespectsCreateFromJiraOff(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[%s],"isLast":true}`,
			searchIssueWithCategory("GAME-9", now, "100", "new"))
	})
	w, conn, q := importFixture(t, f)
	conn.CreateFromJira = false

	if _, err := w.runCycle(context.Background(), conn); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if _, err := q.GetJiraLinkByJiraIssueID(context.Background(), db.GetJiraLinkByJiraIssueIDParams{
		ConnectionID: conn.ID, JiraIssueID: "id-GAME-9",
	}); err == nil {
		t.Fatal("creation flow OFF must import nothing (FR-9/FR-10 gate)")
	}
}

func TestImportAdoptsByMarkerAfterInterruptedImport(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[%s],"isLast":true}`,
			searchIssueWithCategory("GAME-7", now, "100", "new"))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()

	// Simulate the interrupted import: the issue exists with its marker, the
	// Link is still pending (crash before finalize).
	res, err := w.Issues.Create(ctx, service.IssueCreateParams{
		WorkspaceID: conn.WorkspaceID, Title: "Imported GAME-7", Status: "todo",
		Priority: "none", CreatorType: "member", CreatorID: conn.ConnectedByID,
		AllowDuplicate: true,
	}, service.IssueCreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	markerValue, _ := json.Marshal("id-GAME-7")
	if _, err := q.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID: res.Issue.ID, WorkspaceID: conn.WorkspaceID, Key: markerKey, Value: markerValue,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertPendingJiraLink(ctx, db.UpsertPendingJiraLinkParams{
		ConnectionID: conn.ID, WorkspaceID: conn.WorkspaceID, JiraIssueID: "id-GAME-7", JiraKey: "GAME-7",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	link, err := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-7"})
	if err != nil || link.State != "ok" || link.IssueID != res.Issue.ID {
		t.Fatalf("must adopt the marked issue, not re-create: %v %+v", err, link)
	}
	rows, _ := q.ListJiraJournalRecent(ctx, db.ListJiraJournalRecentParams{ConnectionID: conn.ID, Limit: 10})
	adopted := false
	for _, rrow := range rows {
		if rrow.Kind == string(JournalImportAdopted) {
			adopted = true
		}
	}
	if !adopted {
		t.Fatal("adoption must journal import_adopted")
	}
}

// --- Story 2.4: inbound updates ---

func TestInboundUpdateAppliesAndForwardsSnapshots(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	var phase atomic.Int64
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		if phase.Load() == 0 {
			fmt.Fprintf(w, `{"issues":[%s],"isLast":true}`,
				searchIssueWithCategory("GAME-10", now.Add(-5*time.Minute), "100", "new"))
			return
		}
		// Remote edit: title, description, status all changed.
		fmt.Fprintf(w, `{"issues":[{"id":"id-GAME-10","key":"GAME-10","fields":{
			"summary":"Renamed remotely",
			"description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"new body"}]}]},
			"status":{"id":"200","name":"In Progress","statusCategory":{"key":"indeterminate"}},
			"labels":[],
			"updated":%q
		}}],"isLast":true}`, now.Add(-1*time.Minute).Format(jiraTimeLayout))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()

	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import cycle: %v", err)
	}
	phase.Store(1)
	conn2, _ := q.GetJiraConnectionByID(ctx, conn.ID)
	// Rewind cursor so the edited issue re-enters the window.
	if err := q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
		ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-10 * time.Minute), Valid: true}, LocalCursor: conn2.LocalCursor,
	}); err != nil {
		t.Fatal(err)
	}
	conn2, _ = q.GetJiraConnectionByID(ctx, conn.ID)
	if _, err := w.runCycle(ctx, conn2); err != nil {
		t.Fatalf("update cycle: %v", err)
	}

	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-10"})
	issue, err := q.GetIssue(ctx, link.IssueID)
	if err != nil || issue.Title != "Renamed remotely" || issue.Status != "in_progress" || issue.Description.String != "new body" {
		t.Fatalf("inbound update not applied: %v %+v", err, issue)
	}
	items, err := ParseItems(link.Items)
	if err != nil || items.Title.RemoteSHA != SHA("Renamed remotely") || items.Title.LocalSHA != SHA("Renamed remotely") {
		t.Fatalf("snapshots not forwarded: %v %+v", err, items)
	}
	if items.Status.RemoteID != "200" || items.Status.Local != "in_progress" {
		t.Fatalf("status snapshot wrong: %+v", items.Status)
	}
}

func TestInboundDivergenceBreadcrumbOnceAndLocalActivationSafe(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	var remoteTitle atomic.Value
	remoteTitle.Store("Imported GAME-11")
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-11/transitions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Write([]byte(`{"transitions":[{"id":"t-2","to":{"id":"200","name":"In Progress"}}]}`))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-11/comment", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"comments":[],"startAt":0,"maxResults":100,"total":0}`))
	})
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[{"id":"id-GAME-11","key":"GAME-11","fields":{
			"summary":%q,
			"description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"body"}]}]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":[],
			"updated":%q
		}}],"isLast":true}`, remoteTitle.Load(), now.Format(jiraTimeLayout))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()

	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-11"})

	// Human edits the title AND an agent-ish local status change happens.
	if _, err := q.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: link.IssueID, Status: "in_progress", WorkspaceID: conn.WorkspaceID}); err != nil {
		t.Fatal(err)
	}
	cur, _ := q.GetIssue(ctx, link.IssueID)
	if _, err := q.UpdateIssue(ctx, db.UpdateIssueParams{
		ID: cur.ID, Title: pgtype.Text{String: "Local human title", Valid: true},
		Description: cur.Description, AssigneeType: cur.AssigneeType, AssigneeID: cur.AssigneeID,
		StartDate: cur.StartDate, DueDate: cur.DueDate, ParentIssueID: cur.ParentIssueID,
		ProjectID: cur.ProjectID, Stage: cur.Stage,
	}); err != nil {
		t.Fatal(err)
	}

	// Remote title changes too → divergence; jira_leads ⇒ Jira wins,
	// breadcrumb posted exactly once; local status must NOT be reverted
	// (unchanged remote status is change-driven, FR-16).
	remoteTitle.Store("Jira wins title")
	rewind := func() db.JiraConnection {
		c, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
			ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-10 * time.Minute), Valid: true}, LocalCursor: c.LocalCursor,
		})
		c, _ = q.GetJiraConnectionByID(ctx, conn.ID)
		return c
	}
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("divergence cycle: %v", err)
	}

	issue, _ := q.GetIssue(ctx, link.IssueID)
	if issue.Title != "Jira wins title" {
		t.Fatalf("leading side must win: %+v", issue.Title)
	}
	if issue.Status != "in_progress" {
		t.Fatalf("unchanged remote status must not revert local activation (FR-16/FR-27): %s", issue.Status)
	}
	countBreadcrumbs := func() int {
		rows, err := q.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{IssueID: link.IssueID, WorkspaceID: conn.WorkspaceID, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, c := range rows {
			if c.AuthorType == "system" && strings.Contains(c.Content, "overwritten") {
				n++
			}
		}
		return n
	}
	if got := countBreadcrumbs(); got != 1 {
		t.Fatalf("want exactly one breadcrumb, got %d", got)
	}

	// Replay the same divergence window: no second breadcrumb, no rewrites.
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("replay cycle: %v", err)
	}
	if got := countBreadcrumbs(); got != 1 {
		t.Fatalf("breadcrumb must not repeat, got %d", got)
	}
}

func TestInboundLossyFixpointNoChurn(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[{"id":"id-GAME-12","key":"GAME-12","fields":{
			"summary":"Lossy",
			"description":{"type":"doc","version":1,"content":[
				{"type":"paragraph","content":[{"type":"text","text":"before"}]},
				{"type":"mediaSingle","content":[{"type":"media","attrs":{"id":"x"}}]}
			]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":[],
			"updated":%q
		}}],"isLast":true}`, now.Format(jiraTimeLayout))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()

	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-12"})
	before, _ := q.GetIssue(ctx, link.IssueID)

	for i := 0; i < 3; i++ {
		c, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
			ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-10 * time.Minute), Valid: true}, LocalCursor: c.LocalCursor,
		})
		c, _ = q.GetJiraConnectionByID(ctx, conn.ID)
		if _, err := w.runCycle(ctx, c); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}
	after, _ := q.GetIssue(ctx, link.IssueID)
	if !after.UpdatedAt.Time.Equal(before.UpdatedAt.Time) {
		t.Fatalf("lossy conversion must reach a fixpoint (no churn writes): %v vs %v", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestDirtyLadderIsolatesPoisonAndRecovers(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[%s,%s],"isLast":true}`,
			searchIssueWithCategory("GAME-13", now.Add(-2*time.Minute), "100", "new"),
			searchIssueWithCategory("GAME-14", now.Add(-1*time.Minute), "100", "new"))
	})
	f.mux.HandleFunc("/rest/api/3/issue/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"id":"id-GAME-13","key":"GAME-13","fields":{
			"summary":"Imported GAME-13",
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":[],
			"updated":%q
		}}`, now.Format(jiraTimeLayout))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()

	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	link13, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-13"})

	// Poison one link: unknown items version makes its update fail.
	if err := q.UpdateJiraLinkItems(ctx, db.UpdateJiraLinkItemsParams{
		ID: link13.ID, Items: []byte(`{"v":2}`), JiraKey: link13.JiraKey,
	}); err != nil {
		t.Fatal(err)
	}

	rewound, _ := q.GetJiraConnectionByID(ctx, conn.ID)
	_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
		ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-10 * time.Minute), Valid: true}, LocalCursor: rewound.LocalCursor,
	})
	rewound, _ = q.GetJiraConnectionByID(ctx, conn.ID)
	if _, err := w.runCycle(ctx, rewound); err != nil {
		t.Fatalf("poisoned cycle must still succeed (NFR-3): %v", err)
	}

	link13, _ = q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-13"})
	if !link13.Dirty || link13.RetryCount != 1 {
		t.Fatalf("poison item must go dirty with ladder: %+v", link13)
	}
	got, _ := q.GetJiraConnectionByID(ctx, conn.ID)
	if !got.JiraCursor.Valid || got.JiraCursor.Time.Before(now.Add(-90*time.Second)) {
		t.Fatalf("cursor must still advance past the poison (AD-2): %+v", got.JiraCursor)
	}

	// Heal the items and make the retry due: the dirty rescan path refreshes
	// via GET /issue/{id} and recovers.
	healthy, _ := json.Marshal(ItemsV1{V: 1, Fields: map[string]ItemState{}})
	if err := q.UpdateJiraLinkItems(ctx, db.UpdateJiraLinkItemsParams{ID: link13.ID, Items: healthy, JiraKey: link13.JiraKey}); err != nil {
		t.Fatal(err)
	}
	if err := q.MarkJiraLinkDirty(ctx, db.MarkJiraLinkDirtyParams{ID: link13.ID, RetryAt: pgtype.Timestamptz{Time: now.Add(-time.Minute), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	got, _ = q.GetJiraConnectionByID(ctx, conn.ID)
	if _, err := w.runCycle(ctx, got); err != nil {
		t.Fatalf("recovery cycle: %v", err)
	}
	link13, _ = q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-13"})
	if link13.Dirty {
		t.Fatalf("recovered link must clear dirty: %+v", link13)
	}
}

// --- Story 3.1: inbound comments ---

func TestInboundCommentsMirrorExactlyOnceWithPrivacyAndActorFilter(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[%s],"isLast":true}`,
			searchIssueWithCategory("GAME-20", now, "100", "new"))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-20/comment", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"comments":[
			{"id":"c-1","author":{"accountId":"acc-human","displayName":"Marco"},
			 "body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"public one"}]}]},
			 "created":%q},
			{"id":"c-2","author":{"accountId":"acc-human","displayName":"Marco"},
			 "visibility":{"type":"role","value":"Developers"},
			 "body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"SECRET"}]}]},
			 "created":%q},
			{"id":"c-3","author":{"accountId":"acc-bot","displayName":"Sync Bot"},
			 "body":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"our own mirror"}]}]},
			 "created":%q}
		],"startAt":0,"maxResults":100,"total":3}`,
			now.Format(jiraTimeLayout), now.Format(jiraTimeLayout), now.Format(jiraTimeLayout))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()

	// The actor filter keys off the stored service-account id.
	if err := q.UpdateJiraConnectionServiceAccount(ctx, db.UpdateJiraConnectionServiceAccountParams{
		ID: conn.ID, ServiceAccountID: "acc-bot",
	}); err != nil {
		t.Fatal(err)
	}
	conn, _ = q.GetJiraConnectionByID(ctx, conn.ID)

	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-20"})

	rows, err := q.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{IssueID: link.IssueID, WorkspaceID: conn.WorkspaceID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var mirrored []string
	for _, c := range rows {
		if c.AuthorType == "system" && strings.Contains(c.Content, "From Jira") {
			mirrored = append(mirrored, c.Content)
		}
		if strings.Contains(c.Content, "SECRET") {
			t.Fatalf("restricted comment content leaked: %s", c.Content)
		}
		if strings.Contains(c.Content, "our own mirror") {
			t.Fatalf("service-account comment must never mirror back (echo): %s", c.Content)
		}
	}
	if len(mirrored) != 1 || !strings.Contains(mirrored[0], "Marco") || !strings.Contains(mirrored[0], "public one") {
		t.Fatalf("want exactly one attributed mirror, got %v", mirrored)
	}

	journal, _ := q.ListJiraJournalRecent(ctx, db.ListJiraJournalRecentParams{ConnectionID: conn.ID, Limit: 30})
	droppedLogged := false
	for _, j := range journal {
		if j.Kind == string(JournalRestrictedCommentDropped) {
			droppedLogged = true
		}
	}
	if !droppedLogged {
		t.Fatal("restricted drop must journal")
	}

	// Replay: identical window → zero new comments (idempotent by identity).
	c2, _ := q.GetJiraConnectionByID(ctx, conn.ID)
	_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
		ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-10 * time.Minute), Valid: true}, LocalCursor: c2.LocalCursor,
	})
	c2, _ = q.GetJiraConnectionByID(ctx, conn.ID)
	if _, err := w.runCycle(ctx, c2); err != nil {
		t.Fatalf("replay: %v", err)
	}
	rows2, _ := q.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{IssueID: link.IssueID, WorkspaceID: conn.WorkspaceID, Limit: 100})
	if len(rows2) != len(rows) {
		t.Fatalf("replay must not duplicate comments: %d vs %d", len(rows2), len(rows))
	}
}
