package jira

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Story 4.4 — Multica→Jira creation flows (FR-9, AD-15).

func multicaLeadsFixture(t *testing.T, f *fakeJira) (*Worker, db.JiraConnection, *db.Queries) {
	w, conn, q := importFixture(t, f)
	upd, err := q.UpdateJiraConnectionConfig(context.Background(), db.UpdateJiraConnectionConfigParams{
		ID: conn.ID, Enabled: true, Mode: "multica_leads", LeadingSystem: "multica",
		CommentsEnabled: true, LabelsEnabled: true, CustomFieldsEnabled: true,
		CreateFromJira: false, CreateToJira: true, JqlFilter: "", LabelPrefix: "",
		MentionBridgeEnabled: true, OutboundIssueType: "Task",
		StatusMap: []byte(`{"in":{"100":"todo"},"out":{"todo":"100"}}`),
		FieldMap:  []byte(`[]`), TagRules: []byte(`[]`), CycleIntervalSeconds: 45,
	})
	if err != nil {
		t.Fatal(err)
	}
	return w, upd, q
}

func TestOutboundCreateExactlyOnceWithMarkerAdoption(t *testing.T) {
	f := newFakeJira(t)
	var creates atomic.Int64
	var lastBody atomic.Value
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		// adopt-scan: return the created issue when its marker is queried
		jql := r.URL.Query().Get("jql")
		if strings.Contains(jql, "multica-issue-") && creates.Load() > 0 {
			w.Write([]byte(`{"issues":[{"id":"jira-created-1"}],"isLast":true}`))
			return
		}
		w.Write([]byte(`{"issues":[],"isLast":true}`))
	})
	f.mux.HandleFunc("/rest/api/3/issue", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			lastBody.Store(string(body))
			creates.Add(1)
			w.Write([]byte(`{"id":"jira-created-1","key":"GAME-100"}`))
		}
	})
	w, conn, q := multicaLeadsFixture(t, f)
	ctx := context.Background()

	// A new local Multica issue (not a mirror).
	issue, err := q.CreateIssue(ctx, db.CreateIssueParams{
		WorkspaceID: conn.WorkspaceID, Title: "Built in Multica", Status: "todo",
		Priority: "none", CreatorType: "member", CreatorID: conn.ConnectedByID, Number: 9001,
	})
	if err != nil {
		t.Fatal(err)
	}

	rewind := func() db.JiraConnection {
		c, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
			ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
			LocalCursor: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		})
		c, _ = q.GetJiraConnectionByID(ctx, conn.ID)
		return c
	}
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("create cycle: %v", err)
	}
	if creates.Load() != 1 {
		t.Fatalf("want exactly one Jira create, got %d", creates.Load())
	}
	body, _ := lastBody.Load().(string)
	if !strings.Contains(body, "multica-issue-") || !strings.Contains(body, "Built in Multica") {
		t.Fatalf("create body must carry marker + title: %s", body)
	}
	link, err := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "jira-created-1"})
	if err != nil || link.State != "ok" || link.IssueID != issue.ID || link.JiraKey != "GAME-100" {
		t.Fatalf("link not finalized to real jira id: %v %+v", err, link)
	}

	// Replay: the issue is now linked → no second create.
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if creates.Load() != 1 {
		t.Fatalf("replay must not re-create: %d", creates.Load())
	}
}

func TestOutboundCreateRejectionIsLoudNoRetry(t *testing.T) {
	f := newFakeJira(t)
	var creates atomic.Int64
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"issues":[],"isLast":true}`))
	})
	f.mux.HandleFunc("/rest/api/3/issue", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			creates.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"errorMessages":[],"errors":{"customfield_1":"Field required"}}`))
		}
	})
	w, conn, q := multicaLeadsFixture(t, f)
	ctx := context.Background()
	if _, err := q.CreateIssue(ctx, db.CreateIssueParams{
		WorkspaceID: conn.WorkspaceID, Title: "Needs a required field", Status: "todo",
		Priority: "none", CreatorType: "member", CreatorID: conn.ConnectedByID, Number: 9002,
	}); err != nil {
		t.Fatal(err)
	}

	rewind := func() db.JiraConnection {
		c, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
			ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
			LocalCursor: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
		})
		c, _ = q.GetJiraConnectionByID(ctx, conn.ID)
		return c
	}
	for i := 0; i < 3; i++ {
		if _, err := w.runCycle(ctx, rewind()); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}
	// Each cycle re-attempts the still-pending intent, but a 4xx is loud +
	// no blind retry within the attempt: exactly one create call per cycle,
	// journaled create_rejected. (The item is not silently lost — it stays
	// pending and visible; a fixed required-field config makes it succeed.)
	rows, _ := q.ListJiraJournalRecent(ctx, db.ListJiraJournalRecentParams{ConnectionID: conn.ID, Limit: 30})
	rejected := 0
	for _, r := range rows {
		if r.Kind == string(JournalCreateRejected) {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatal("required-field rejection must journal create_rejected")
	}
}

func TestMirrorModeShortCircuitsOutboundCreate(t *testing.T) {
	f := newFakeJira(t)
	var creates atomic.Int64
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"issues":[],"isLast":true}`))
	})
	f.mux.HandleFunc("/rest/api/3/issue", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			creates.Add(1)
		}
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()
	// Force the row to mirror with create_to_jira somehow set (defense in
	// depth: apply must short-circuit even if the flag leaked on).
	if _, err := q.CreateIssue(ctx, db.CreateIssueParams{
		WorkspaceID: conn.WorkspaceID, Title: "should not leave", Status: "todo",
		Priority: "none", CreatorType: "member", CreatorID: conn.ConnectedByID, Number: 9003,
	}); err != nil {
		t.Fatal(err)
	}
	conn.Mode = "mirror"
	conn.CreateToJira = true // simulate a leaked flag
	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if creates.Load() != 0 {
		t.Fatalf("mirror mode must never create in Jira: %d", creates.Load())
	}
	_ = fmt.Sprint
}
