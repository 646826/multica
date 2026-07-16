package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

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
