package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/integrations/jira"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// HTTP-contract tests for the Jira connection endpoints (Story 1.3):
// deployment gating, strict body decoding, credential-free DTOs, and
// transactional delete cleanup. Service semantics are covered in
// internal/integrations/jira; these tests pin the edge behavior.

func jiraTestPool(t *testing.T) (*db.Queries, *pgxpool.Pool) {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping DB test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("database not available: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database not reachable: %v", err)
	}
	t.Cleanup(pool.Close)
	return db.New(pool), pool
}

func jiraTestBox(t *testing.T) *secretbox.Box {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func jiraUUID() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }

// jiraRequest builds a request with the chi workspace URL param and a member
// context, the same shape the router middlewares produce.
func jiraRequest(method, target string, body []byte, wsID pgtype.UUID, role string) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	ws := uuidToString(wsID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", ws)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.SetMemberContext(ctx, ws, db.Member{
		ID:          jiraUUID(),
		WorkspaceID: wsID,
		UserID:      jiraUUID(),
		Role:        role,
	})
	return r.WithContext(ctx)
}

func jiraSeedConnection(t *testing.T, q *db.Queries, box *secretbox.Box, ws pgtype.UUID) db.JiraConnection {
	t.Helper()
	sealed, err := box.Seal([]byte("tok"))
	if err != nil {
		t.Fatal(err)
	}
	host := fmt.Sprintf("h-%s.example.test", uuid.NewString()[:8])
	conn, err := q.CreateJiraConnection(context.Background(), db.CreateJiraConnectionParams{
		WorkspaceID: ws, SiteUrl: "https://" + host, SiteHost: host,
		ProjectKey: "GAME", Email: "bot@example.test", TokenEncrypted: sealed,
		ConnectedByID: jiraUUID(), Mode: jira.ModeJiraLeads, LeadingSystem: jira.LeadJira,
		CommentsEnabled: true, LabelsEnabled: true, CustomFieldsEnabled: true,
		CreateFromJira: true, CreateToJira: false, MentionBridgeEnabled: true,
		OutboundIssueType: "Task", StatusMap: []byte(`{}`), CycleIntervalSeconds: 45,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.DeleteJiraConnection(context.Background(), conn.ID) })
	return conn
}

func TestJiraConnectRefusesWhenUnconfigured(t *testing.T) {
	q, pool := jiraTestPool(t)
	h := &Handler{Queries: q, TxStarter: pool, Jira: jira.NewService(q, nil)}

	req := jiraRequest(http.MethodPost, "/api/workspaces/x/jira/connect",
		[]byte(`{"site_url":"https://x.atlassian.net","email":"a","token":"b","project_key":"GAME"}`),
		jiraUUID(), "admin")
	rec := httptest.NewRecorder()
	h.ConnectJira(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when key unset, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestJiraConnectRejectsUnknownFields(t *testing.T) {
	q, pool := jiraTestPool(t)
	h := &Handler{Queries: q, TxStarter: pool, Jira: jira.NewService(q, jiraTestBox(t))}

	req := jiraRequest(http.MethodPost, "/api/workspaces/x/jira/connect",
		[]byte(`{"site_url":"https://x.atlassian.net","email":"a","token":"b","project_key":"GAME","surprise":1}`),
		jiraUUID(), "admin")
	rec := httptest.NewRecorder()
	h.ConnectJira(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown field") {
		t.Fatalf("want 400 unknown field, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGetJiraConnectionDTOOmitsCredentials(t *testing.T) {
	q, pool := jiraTestPool(t)
	box := jiraTestBox(t)
	ws := jiraUUID()
	jiraSeedConnection(t, q, box, ws)
	h := &Handler{Queries: q, TxStarter: pool, Jira: jira.NewService(q, box)}

	req := jiraRequest(http.MethodGet, "/api/workspaces/x/jira", nil, ws, "member")
	rec := httptest.NewRecorder()
	h.GetJiraConnection(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, banned := range []string{"token", "encrypted"} {
		if strings.Contains(body, `"`+banned) {
			t.Fatalf("DTO leaks credential-ish field %q: %s", banned, body)
		}
	}
	var payload struct {
		Configured bool             `json:"configured"`
		CanManage  bool             `json:"can_manage"`
		Connection *json.RawMessage `json:"connection"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Configured || payload.CanManage || payload.Connection == nil {
		t.Fatalf("payload gating wrong: %+v body=%s", payload, body)
	}

	// Admin sees can_manage true.
	req = jiraRequest(http.MethodGet, "/api/workspaces/x/jira", nil, ws, "admin")
	rec = httptest.NewRecorder()
	h.GetJiraConnection(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || !payload.CanManage {
		t.Fatalf("admin can_manage: err=%v body=%s", err, rec.Body.String())
	}
}

func TestPatchJiraConnectionValidatesAndJournals(t *testing.T) {
	q, pool := jiraTestPool(t)
	box := jiraTestBox(t)
	ws := jiraUUID()
	conn := jiraSeedConnection(t, q, box, ws)
	h := &Handler{Queries: q, TxStarter: pool, Jira: jira.NewService(q, box)}

	// Invalid: mirror + create_to_jira.
	req := jiraRequest(http.MethodPatch, "/api/workspaces/x/jira",
		[]byte(`{"mode":"mirror","create_to_jira":true}`), ws, "admin")
	rec := httptest.NewRecorder()
	h.PatchJiraConnection(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "mirror") {
		t.Fatalf("want mirror validation 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// Valid: enable + tighten cadence.
	req = jiraRequest(http.MethodPatch, "/api/workspaces/x/jira",
		[]byte(`{"enabled":true,"cycle_interval_seconds":60}`), ws, "admin")
	rec = httptest.NewRecorder()
	h.PatchJiraConnection(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	var resp JiraConnectionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || !resp.Enabled || resp.CycleIntervalSeconds != 60 {
		t.Fatalf("patch not applied: err=%v resp=%+v", err, resp)
	}

	rows, err := q.ListJiraJournalRecent(context.Background(), db.ListJiraJournalRecentParams{
		ConnectionID: conn.ID, Limit: 5,
	})
	if err != nil || len(rows) == 0 || rows[0].Kind != "config_changed" {
		t.Fatalf("config change not journaled: err=%v rows=%d", err, len(rows))
	}
}

func TestDeleteJiraConnectionCleansBookkeepingOnly(t *testing.T) {
	q, pool := jiraTestPool(t)
	box := jiraTestBox(t)
	ws := jiraUUID()
	conn := jiraSeedConnection(t, q, box, ws)
	h := &Handler{Queries: q, TxStarter: pool, Jira: jira.NewService(q, box)}

	if _, err := q.InsertJiraJournal(context.Background(), db.InsertJiraJournalParams{
		ConnectionID: conn.ID, WorkspaceID: ws, Kind: "config_changed", Detail: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	req := jiraRequest(http.MethodDelete, "/api/workspaces/x/jira", nil, ws, "admin")
	rec := httptest.NewRecorder()
	h.DeleteJiraConnection(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := q.GetJiraConnectionByID(context.Background(), conn.ID); err == nil {
		t.Fatal("connection survived delete")
	}
	rows, err := q.ListJiraJournalRecent(context.Background(), db.ListJiraJournalRecentParams{
		ConnectionID: conn.ID, Limit: 5,
	})
	if err != nil || len(rows) != 0 {
		t.Fatalf("journal not cleaned: err=%v rows=%d", err, len(rows))
	}

	// Second delete → 404.
	req = jiraRequest(http.MethodDelete, "/api/workspaces/x/jira", nil, ws, "admin")
	rec = httptest.NewRecorder()
	h.DeleteJiraConnection(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 on repeat delete, got %d", rec.Code)
	}
}
