package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service/benchmark"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// newBenchmarkHandler builds a BenchmarkHandler wired to the same test pool /
// queries used by handler_test.go. Each handler test relies on testHandler /
// testPool / testUserID / testWorkspaceID globals from handler_test.go.
func newBenchmarkHandler(t *testing.T) *BenchmarkHandler {
	t.Helper()
	if testHandler == nil {
		t.Skip("testHandler not initialized (DATABASE_URL unreachable)")
	}
	return NewBenchmarkHandler(BenchmarkDeps{
		Suites:        benchmark.NewSuiteService(testHandler.Queries),
		Profiles:      benchmark.NewProfileService(testHandler.Queries),
		Runs:          benchmark.NewRunService(testHandler.Queries, testPool, events.New()),
		EvaluatorPool: benchmark.NewEvaluatorPoolService(testHandler.Queries),
	})
}

// cleanupBenchmarkRunsForSuite removes benchmark_run rows tied to a suite so
// integration tests don't leak rows across runs. benchmark_task FK is
// ON DELETE CASCADE per the migrations, so deleting the run is sufficient.
func cleanupBenchmarkRunsForSuite(t *testing.T, suiteID string) {
	t.Helper()
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM benchmark_run WHERE workspace_id = $1 AND suite_id = $2`,
			testWorkspaceID, suiteID,
		)
	})
}

// seedBenchmarkRunFixtures creates a workspace-scoped suite + agent + profile
// suitable for run-handler tests. Returns the suite UUID and profile UUID
// (as strings) for the caller to use as request payload values. Cleanup of
// the suite row is registered here; runs created against it are cleaned by
// cleanupBenchmarkRunsForSuite, which the caller invokes once it has the suite id.
func seedBenchmarkRunFixtures(t *testing.T, h *BenchmarkHandler, label string) (suiteID, profileID string) {
	t.Helper()

	suiteSlug := "run-suite-" + label + "-" + uuid.NewString()[:8]
	cleanupSuiteSlug(t, suiteSlug)

	w := httptest.NewRecorder()
	h.CreateSuite(w, newRequest("POST", "/api/benchmarks/suites", map[string]any{
		"slug":         suiteSlug,
		"display_name": "Run suite " + label,
		"adapter_kind": "programbench",
		"instance_ids": []string{"abishekvashok__cmatrix.5c082c6"},
		"description":  "fixture for run handler test",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed suite: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var suite map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &suite); err != nil {
		t.Fatalf("decode seeded suite: %v", err)
	}
	suiteID, _ = suite["id"].(string)
	if suiteID == "" {
		t.Fatalf("seeded suite has no id")
	}

	agentID := createBenchmarkTestAgent(t,
		"Run Agent "+label+" "+uuid.NewString()[:8],
		"Run handler fixture prompt",
		"gpt-4o-mini",
	)

	profileSlug := "run-profile-" + label + "-" + uuid.NewString()[:8]
	w2 := httptest.NewRecorder()
	h.CaptureProfile(w2, newRequest("POST", "/api/benchmarks/profiles", map[string]any{
		"slug":         profileSlug,
		"display_name": "Run profile " + label,
		"agent_id":     agentID,
	}))
	if w2.Code != http.StatusCreated {
		t.Fatalf("seed profile: expected 201, got %d: %s", w2.Code, w2.Body.String())
	}
	var profile map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &profile); err != nil {
		t.Fatalf("decode seeded profile: %v", err)
	}
	profileID, _ = profile["id"].(string)
	if profileID == "" {
		t.Fatalf("seeded profile has no id")
	}

	return suiteID, profileID
}

// cleanupSuiteSlug removes a suite by slug to keep tests isolated when they
// share the package-wide handler workspace.
func cleanupSuiteSlug(t *testing.T, slug string) {
	t.Helper()
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM benchmark_suite WHERE workspace_id = $1 AND slug = $2`,
			testWorkspaceID, slug,
		)
	})
}

// createBenchmarkTestAgent inserts a fresh agent row that ProfileService
// can snapshot. Mirrors createHandlerTestAgent but also fills `instructions`
// and `model` so the captured prompt_hash is not just hashing empty strings.
// Cleanup happens via t.Cleanup; benchmark_agent_profile rows referencing
// this agent are removed first so the agent FK can be deleted.
func createBenchmarkTestAgent(t *testing.T, name, instructions, model string) string {
	t.Helper()

	var modelArg any
	if model == "" {
		modelArg = nil
	} else {
		modelArg = model
	}

	var agentID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id,
			instructions, custom_env, custom_args, mcp_config, model
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 1, $4, $5, '{}'::jsonb, '[]'::jsonb, NULL, $6)
		RETURNING id
	`, testWorkspaceID, name, handlerTestRuntimeID(t), testUserID, instructions, modelArg).Scan(&agentID); err != nil {
		t.Fatalf("failed to create benchmark test agent: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		// benchmark_agent_profile FK references agent(id); delete profile
		// rows first so the agent delete doesn't violate FK.
		testPool.Exec(ctx, `DELETE FROM benchmark_agent_profile WHERE agent_id = $1`, agentID)
		testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, agentID)
	})

	return agentID
}

func TestBenchmarkHandler_CreateSuite_201(t *testing.T) {
	h := newBenchmarkHandler(t)
	slug := "smoke-cli-handler-201-" + uuid.NewString()[:8]
	cleanupSuiteSlug(t, slug)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/benchmarks/suites", map[string]any{
		"slug":         slug,
		"display_name": "Smoke CLI",
		"adapter_kind": "programbench",
		"instance_ids": []string{"abishekvashok__cmatrix.5c082c6"},
		"description":  "test",
	})
	h.CreateSuite(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["slug"] != slug {
		t.Fatalf("slug mismatch: %v", got["slug"])
	}
	if got["workspace_id"] != testWorkspaceID {
		t.Fatalf("workspace_id mismatch: %v", got["workspace_id"])
	}
	if id, ok := got["id"].(string); !ok || id == "" {
		t.Fatalf("id missing or not a string: %v", got["id"])
	}
	instanceIDs, ok := got["instance_ids"].([]any)
	if !ok || len(instanceIDs) != 1 || instanceIDs[0] != "abishekvashok__cmatrix.5c082c6" {
		t.Fatalf("instance_ids mismatch: %v", got["instance_ids"])
	}
}

func TestBenchmarkHandler_CreateSuite_400_OnEmptyInstances(t *testing.T) {
	h := newBenchmarkHandler(t)
	slug := "empty-handler-400-" + uuid.NewString()[:8]
	cleanupSuiteSlug(t, slug)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/benchmarks/suites", map[string]any{
		"slug":         slug,
		"display_name": "Empty",
		"adapter_kind": "programbench",
		"instance_ids": []string{},
	})
	h.CreateSuite(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["error"] != "instance_list_empty" {
		t.Fatalf("expected error=instance_list_empty, got %v", got["error"])
	}
}

func TestBenchmarkHandler_CreateSuite_409_OnDuplicate(t *testing.T) {
	h := newBenchmarkHandler(t)
	slug := "dup-handler-409-" + uuid.NewString()[:8]
	cleanupSuiteSlug(t, slug)

	body := map[string]any{
		"slug":         slug,
		"display_name": "Dup",
		"adapter_kind": "programbench",
		"instance_ids": []string{"a"},
	}

	// First insert succeeds.
	w := httptest.NewRecorder()
	h.CreateSuite(w, newRequest("POST", "/api/benchmarks/suites", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("first create: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Second insert with the same slug must 409.
	w2 := httptest.NewRecorder()
	h.CreateSuite(w2, newRequest("POST", "/api/benchmarks/suites", body))
	if w2.Code != http.StatusConflict {
		t.Fatalf("second create: expected 409, got %d: %s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["error"] != "slug_taken" {
		t.Fatalf("expected error=slug_taken, got %v", got["error"])
	}
}

func TestBenchmarkHandler_ListSuites_200(t *testing.T) {
	h := newBenchmarkHandler(t)
	slug := "list-handler-200-" + uuid.NewString()[:8]
	cleanupSuiteSlug(t, slug)

	// Seed one suite via the handler so the list contains at least our row.
	w := httptest.NewRecorder()
	h.CreateSuite(w, newRequest("POST", "/api/benchmarks/suites", map[string]any{
		"slug":         slug,
		"display_name": "List me",
		"adapter_kind": "programbench",
		"instance_ids": []string{"a"},
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed create: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	w2 := httptest.NewRecorder()
	h.ListSuites(w2, newRequest("GET", "/api/benchmarks/suites", nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	var got struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	found := false
	for _, item := range got.Items {
		if item["slug"] == slug {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("seeded suite %q not found in list", slug)
	}
}

func TestBenchmarkHandler_GetSuite_200_And_404(t *testing.T) {
	h := newBenchmarkHandler(t)
	slug := "get-handler-200-" + uuid.NewString()[:8]
	cleanupSuiteSlug(t, slug)

	// Create.
	w := httptest.NewRecorder()
	h.CreateSuite(w, newRequest("POST", "/api/benchmarks/suites", map[string]any{
		"slug":         slug,
		"display_name": "Get me",
		"adapter_kind": "programbench",
		"instance_ids": []string{"a"},
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	id, _ := created["id"].(string)

	// Get existing → 200.
	w2 := httptest.NewRecorder()
	req := withURLParam(newRequest("GET", "/api/benchmarks/suites/"+id, nil), "id", id)
	h.GetSuite(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got["id"] != id {
		t.Fatalf("get id mismatch: %v vs %s", got["id"], id)
	}

	// Get missing → 404.
	missing := uuid.NewString()
	w3 := httptest.NewRecorder()
	req3 := withURLParam(newRequest("GET", "/api/benchmarks/suites/"+missing, nil), "id", missing)
	h.GetSuite(w3, req3)
	if w3.Code != http.StatusNotFound {
		t.Fatalf("get missing: expected 404, got %d: %s", w3.Code, w3.Body.String())
	}
}

func TestBenchmarkHandler_DeleteSuite_204_And_404(t *testing.T) {
	h := newBenchmarkHandler(t)
	slug := "del-handler-204-" + uuid.NewString()[:8]
	cleanupSuiteSlug(t, slug)

	w := httptest.NewRecorder()
	h.CreateSuite(w, newRequest("POST", "/api/benchmarks/suites", map[string]any{
		"slug":         slug,
		"display_name": "Delete me",
		"adapter_kind": "programbench",
		"instance_ids": []string{"a"},
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	id, _ := created["id"].(string)

	// Delete existing → 204.
	w2 := httptest.NewRecorder()
	req := withURLParam(newRequest("DELETE", "/api/benchmarks/suites/"+id, nil), "id", id)
	h.DeleteSuite(w2, req)
	if w2.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d: %s", w2.Code, w2.Body.String())
	}

	// Delete again → 404.
	w3 := httptest.NewRecorder()
	req3 := withURLParam(newRequest("DELETE", "/api/benchmarks/suites/"+id, nil), "id", id)
	h.DeleteSuite(w3, req3)
	if w3.Code != http.StatusNotFound {
		t.Fatalf("delete missing: expected 404, got %d: %s", w3.Code, w3.Body.String())
	}
}

func TestBenchmarkHandler_DeleteSuite_404(t *testing.T) {
	h := newBenchmarkHandler(t)

	missing := uuid.NewString()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest("DELETE", "/api/benchmarks/suites/"+missing, nil), "id", missing)
	h.DeleteSuite(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBenchmarkHandler_CaptureProfile_201(t *testing.T) {
	h := newBenchmarkHandler(t)
	agentID := createBenchmarkTestAgent(t,
		"Profile Capture 201 "+uuid.NewString()[:8],
		"You are a helpful test agent.",
		"gpt-4o-mini",
	)
	slug := "capture-201-" + uuid.NewString()[:8]

	w := httptest.NewRecorder()
	h.CaptureProfile(w, newRequest("POST", "/api/benchmarks/profiles", map[string]any{
		"slug":         slug,
		"display_name": "Capture 201",
		"agent_id":     agentID,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["slug"] != slug {
		t.Fatalf("slug mismatch: %v", got["slug"])
	}
	if got["agent_id"] != agentID {
		t.Fatalf("agent_id mismatch: %v vs %s", got["agent_id"], agentID)
	}
	hash, _ := got["prompt_hash"].(string)
	if len(hash) != 64 {
		t.Fatalf("prompt_hash should be 64 hex chars, got %q (len %d)", hash, len(hash))
	}
	if _, ok := got["attached_skills"].([]any); !ok {
		t.Fatalf("attached_skills should be an array, got %#v", got["attached_skills"])
	}
	if _, present := got["duplicate_of"]; present {
		t.Fatalf("duplicate_of should be absent on first capture, got %v", got["duplicate_of"])
	}
}

func TestBenchmarkHandler_CaptureProfile_DuplicateHashAllowed(t *testing.T) {
	h := newBenchmarkHandler(t)
	agentID := createBenchmarkTestAgent(t,
		"Profile Dup Hash "+uuid.NewString()[:8],
		"identical instructions for hash collision",
		"gpt-4o-mini",
	)

	slug1 := "dup-hash-a-" + uuid.NewString()[:8]
	slug2 := "dup-hash-b-" + uuid.NewString()[:8]

	w1 := httptest.NewRecorder()
	h.CaptureProfile(w1, newRequest("POST", "/api/benchmarks/profiles", map[string]any{
		"slug":         slug1,
		"display_name": "First",
		"agent_id":     agentID,
	}))
	if w1.Code != http.StatusCreated {
		t.Fatalf("first capture: expected 201, got %d: %s", w1.Code, w1.Body.String())
	}
	var first map[string]any
	if err := json.Unmarshal(w1.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	firstID, _ := first["id"].(string)
	if firstID == "" {
		t.Fatalf("first capture missing id")
	}

	// Second capture against the same agent — same prompt_hash. Different
	// slug, so no 409. Service surfaces the collision in `duplicate_of`.
	w2 := httptest.NewRecorder()
	h.CaptureProfile(w2, newRequest("POST", "/api/benchmarks/profiles", map[string]any{
		"slug":         slug2,
		"display_name": "Second",
		"agent_id":     agentID,
	}))
	if w2.Code != http.StatusCreated {
		t.Fatalf("second capture: expected 201, got %d: %s", w2.Code, w2.Body.String())
	}
	var second map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	dup, ok := second["duplicate_of"].(string)
	if !ok {
		t.Fatalf("duplicate_of should be a UUID string, got %#v", second["duplicate_of"])
	}
	if dup != firstID {
		t.Fatalf("duplicate_of should point at first profile %s, got %s", firstID, dup)
	}
	if first["prompt_hash"] != second["prompt_hash"] {
		t.Fatalf("prompt_hash should match across duplicate captures: %v vs %v",
			first["prompt_hash"], second["prompt_hash"])
	}
}

func TestBenchmarkHandler_CaptureProfile_400_OnMissingAgent(t *testing.T) {
	h := newBenchmarkHandler(t)
	missingAgent := uuid.NewString()

	w := httptest.NewRecorder()
	h.CaptureProfile(w, newRequest("POST", "/api/benchmarks/profiles", map[string]any{
		"slug":         "missing-agent-" + uuid.NewString()[:8],
		"display_name": "Missing",
		"agent_id":     missingAgent,
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["error"] != "agent_not_found" {
		t.Fatalf("expected error=agent_not_found, got %v", got["error"])
	}
}

func TestBenchmarkHandler_CaptureProfile_409_OnDuplicateSlug(t *testing.T) {
	h := newBenchmarkHandler(t)
	agentID := createBenchmarkTestAgent(t,
		"Profile Dup Slug "+uuid.NewString()[:8],
		"prompt for dup slug test",
		"gpt-4o-mini",
	)
	slug := "dup-slug-" + uuid.NewString()[:8]

	body := map[string]any{
		"slug":         slug,
		"display_name": "Dup",
		"agent_id":     agentID,
	}

	w := httptest.NewRecorder()
	h.CaptureProfile(w, newRequest("POST", "/api/benchmarks/profiles", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("first capture: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	w2 := httptest.NewRecorder()
	h.CaptureProfile(w2, newRequest("POST", "/api/benchmarks/profiles", body))
	if w2.Code != http.StatusConflict {
		t.Fatalf("second capture: expected 409, got %d: %s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["error"] != "slug_taken" {
		t.Fatalf("expected error=slug_taken, got %v", got["error"])
	}
}

func TestBenchmarkHandler_ListProfiles_200(t *testing.T) {
	h := newBenchmarkHandler(t)
	agentID := createBenchmarkTestAgent(t,
		"Profile List "+uuid.NewString()[:8],
		"list-me prompt",
		"gpt-4o-mini",
	)
	slug := "list-profile-" + uuid.NewString()[:8]

	w := httptest.NewRecorder()
	h.CaptureProfile(w, newRequest("POST", "/api/benchmarks/profiles", map[string]any{
		"slug":         slug,
		"display_name": "List me",
		"agent_id":     agentID,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed capture: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	w2 := httptest.NewRecorder()
	h.ListProfiles(w2, newRequest("GET", "/api/benchmarks/profiles", nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	var got struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	found := false
	for _, item := range got.Items {
		if item["slug"] == slug {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("seeded profile %q not found in list", slug)
	}
}

func TestBenchmarkHandler_GetProfile_200_And_404(t *testing.T) {
	h := newBenchmarkHandler(t)
	agentID := createBenchmarkTestAgent(t,
		"Profile Get "+uuid.NewString()[:8],
		"get-me prompt",
		"gpt-4o-mini",
	)
	slug := "get-profile-" + uuid.NewString()[:8]

	w := httptest.NewRecorder()
	h.CaptureProfile(w, newRequest("POST", "/api/benchmarks/profiles", map[string]any{
		"slug":         slug,
		"display_name": "Get me",
		"agent_id":     agentID,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("capture: %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	id, _ := created["id"].(string)

	// 200.
	w2 := httptest.NewRecorder()
	req := withURLParam(newRequest("GET", "/api/benchmarks/profiles/"+id, nil), "id", id)
	h.GetProfile(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got["id"] != id {
		t.Fatalf("get id mismatch: %v vs %s", got["id"], id)
	}

	// 404.
	missing := uuid.NewString()
	w3 := httptest.NewRecorder()
	req3 := withURLParam(newRequest("GET", "/api/benchmarks/profiles/"+missing, nil), "id", missing)
	h.GetProfile(w3, req3)
	if w3.Code != http.StatusNotFound {
		t.Fatalf("get missing: expected 404, got %d: %s", w3.Code, w3.Body.String())
	}
	var notFound map[string]any
	if err := json.Unmarshal(w3.Body.Bytes(), &notFound); err != nil {
		t.Fatalf("decode 404: %v", err)
	}
	if notFound["error"] != "profile_not_found" {
		t.Fatalf("expected error=profile_not_found, got %v", notFound["error"])
	}
}

func TestBenchmarkHandler_DeleteProfile_204_And_404(t *testing.T) {
	h := newBenchmarkHandler(t)
	agentID := createBenchmarkTestAgent(t,
		"Profile Delete "+uuid.NewString()[:8],
		"delete-me prompt",
		"gpt-4o-mini",
	)
	slug := "del-profile-" + uuid.NewString()[:8]

	w := httptest.NewRecorder()
	h.CaptureProfile(w, newRequest("POST", "/api/benchmarks/profiles", map[string]any{
		"slug":         slug,
		"display_name": "Delete me",
		"agent_id":     agentID,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("capture: %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	id, _ := created["id"].(string)

	// 204.
	w2 := httptest.NewRecorder()
	req := withURLParam(newRequest("DELETE", "/api/benchmarks/profiles/"+id, nil), "id", id)
	h.DeleteProfile(w2, req)
	if w2.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d: %s", w2.Code, w2.Body.String())
	}

	// Same id again → 404.
	w3 := httptest.NewRecorder()
	req3 := withURLParam(newRequest("DELETE", "/api/benchmarks/profiles/"+id, nil), "id", id)
	h.DeleteProfile(w3, req3)
	if w3.Code != http.StatusNotFound {
		t.Fatalf("delete missing: expected 404, got %d: %s", w3.Code, w3.Body.String())
	}
	var notFound map[string]any
	if err := json.Unmarshal(w3.Body.Bytes(), &notFound); err != nil {
		t.Fatalf("decode 404: %v", err)
	}
	if notFound["error"] != "profile_not_found" {
		t.Fatalf("expected error=profile_not_found, got %v", notFound["error"])
	}
}

func TestBenchmarkHandler_StartRun_201(t *testing.T) {
	h := newBenchmarkHandler(t)
	suiteID, profileID := seedBenchmarkRunFixtures(t, h, "start201")
	cleanupBenchmarkRunsForSuite(t, suiteID)

	w := httptest.NewRecorder()
	h.StartRun(w, newRequest("POST", "/api/benchmarks/runs", map[string]any{
		"suite_id":       suiteID,
		"profile_id":     profileID,
		"display_name":   "Start 201",
		"evaluator_mode": "managed",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["status"] != "queued" {
		t.Fatalf("status: expected queued, got %v", got["status"])
	}
	if got["suite_id"] != suiteID {
		t.Fatalf("suite_id mismatch: %v vs %s", got["suite_id"], suiteID)
	}
	if got["profile_id"] != profileID {
		t.Fatalf("profile_id mismatch: %v vs %s", got["profile_id"], profileID)
	}
	if got["evaluator_mode"] != "managed" {
		t.Fatalf("evaluator_mode mismatch: %v", got["evaluator_mode"])
	}
	if id, ok := got["id"].(string); !ok || id == "" {
		t.Fatalf("id missing: %v", got["id"])
	}
	if _, ok := got["suite_instance_ids"].([]any); !ok {
		t.Fatalf("suite_instance_ids should be an array, got %#v", got["suite_instance_ids"])
	}
}

func TestBenchmarkHandler_StartRun_400_OnBadEvaluatorMode(t *testing.T) {
	h := newBenchmarkHandler(t)
	suiteID, profileID := seedBenchmarkRunFixtures(t, h, "bad-eval")
	cleanupBenchmarkRunsForSuite(t, suiteID)

	w := httptest.NewRecorder()
	h.StartRun(w, newRequest("POST", "/api/benchmarks/runs", map[string]any{
		"suite_id":       suiteID,
		"profile_id":     profileID,
		"display_name":   "Bad eval",
		"evaluator_mode": "bogus",
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["error"] != "invalid_evaluator_mode" {
		t.Fatalf("expected error=invalid_evaluator_mode, got %v", got["error"])
	}
}

func TestBenchmarkHandler_StartRun_404_OnUnknownSuite(t *testing.T) {
	h := newBenchmarkHandler(t)
	suiteID, profileID := seedBenchmarkRunFixtures(t, h, "unknown-suite")
	cleanupBenchmarkRunsForSuite(t, suiteID)

	bogusSuite := uuid.NewString()
	w := httptest.NewRecorder()
	h.StartRun(w, newRequest("POST", "/api/benchmarks/runs", map[string]any{
		"suite_id":       bogusSuite,
		"profile_id":     profileID,
		"display_name":   "Unknown suite",
		"evaluator_mode": "managed",
	}))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["error"] != "suite_or_profile_not_found" {
		t.Fatalf("expected error=suite_or_profile_not_found, got %v", got["error"])
	}
}

func TestBenchmarkHandler_ListRuns_200(t *testing.T) {
	h := newBenchmarkHandler(t)
	suiteID, profileID := seedBenchmarkRunFixtures(t, h, "list")
	cleanupBenchmarkRunsForSuite(t, suiteID)

	wSeed := httptest.NewRecorder()
	h.StartRun(wSeed, newRequest("POST", "/api/benchmarks/runs", map[string]any{
		"suite_id":       suiteID,
		"profile_id":     profileID,
		"display_name":   "List me",
		"evaluator_mode": "managed",
	}))
	if wSeed.Code != http.StatusCreated {
		t.Fatalf("seed run: expected 201, got %d: %s", wSeed.Code, wSeed.Body.String())
	}
	var seeded map[string]any
	if err := json.Unmarshal(wSeed.Body.Bytes(), &seeded); err != nil {
		t.Fatalf("decode seeded run: %v", err)
	}
	seededID, _ := seeded["id"].(string)

	w := httptest.NewRecorder()
	h.ListRuns(w, newRequest("GET", "/api/benchmarks/runs", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	found := false
	for _, item := range got.Items {
		if item["id"] == seededID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("seeded run %q not found in list", seededID)
	}
}

func TestBenchmarkHandler_GetRun_200_And_404(t *testing.T) {
	h := newBenchmarkHandler(t)
	suiteID, profileID := seedBenchmarkRunFixtures(t, h, "get")
	cleanupBenchmarkRunsForSuite(t, suiteID)

	wSeed := httptest.NewRecorder()
	h.StartRun(wSeed, newRequest("POST", "/api/benchmarks/runs", map[string]any{
		"suite_id":       suiteID,
		"profile_id":     profileID,
		"display_name":   "Get me",
		"evaluator_mode": "managed",
	}))
	if wSeed.Code != http.StatusCreated {
		t.Fatalf("seed run: %d %s", wSeed.Code, wSeed.Body.String())
	}
	var seeded map[string]any
	if err := json.Unmarshal(wSeed.Body.Bytes(), &seeded); err != nil {
		t.Fatalf("decode seeded run: %v", err)
	}
	id, _ := seeded["id"].(string)

	// 200.
	w2 := httptest.NewRecorder()
	req := withURLParam(newRequest("GET", "/api/benchmarks/runs/"+id, nil), "id", id)
	h.GetRun(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if got["id"] != id {
		t.Fatalf("get id mismatch: %v vs %s", got["id"], id)
	}

	// 404.
	missing := uuid.NewString()
	w3 := httptest.NewRecorder()
	req3 := withURLParam(newRequest("GET", "/api/benchmarks/runs/"+missing, nil), "id", missing)
	h.GetRun(w3, req3)
	if w3.Code != http.StatusNotFound {
		t.Fatalf("get missing: expected 404, got %d: %s", w3.Code, w3.Body.String())
	}
	var nf map[string]any
	if err := json.Unmarshal(w3.Body.Bytes(), &nf); err != nil {
		t.Fatalf("decode 404: %v", err)
	}
	if nf["error"] != "run_not_found" {
		t.Fatalf("expected error=run_not_found, got %v", nf["error"])
	}
}

func TestBenchmarkHandler_CancelRun_204_And_404(t *testing.T) {
	h := newBenchmarkHandler(t)
	suiteID, profileID := seedBenchmarkRunFixtures(t, h, "cancel")
	cleanupBenchmarkRunsForSuite(t, suiteID)

	wSeed := httptest.NewRecorder()
	h.StartRun(wSeed, newRequest("POST", "/api/benchmarks/runs", map[string]any{
		"suite_id":       suiteID,
		"profile_id":     profileID,
		"display_name":   "Cancel me",
		"evaluator_mode": "managed",
	}))
	if wSeed.Code != http.StatusCreated {
		t.Fatalf("seed run: %d %s", wSeed.Code, wSeed.Body.String())
	}
	var seeded map[string]any
	if err := json.Unmarshal(wSeed.Body.Bytes(), &seeded); err != nil {
		t.Fatalf("decode seeded run: %v", err)
	}
	id, _ := seeded["id"].(string)

	// 204.
	w2 := httptest.NewRecorder()
	req := withURLParam(newRequest("DELETE", "/api/benchmarks/runs/"+id, nil), "id", id)
	h.CancelRun(w2, req)
	if w2.Code != http.StatusNoContent {
		t.Fatalf("cancel: expected 204, got %d: %s", w2.Code, w2.Body.String())
	}

	// Cancel a never-existed run id → 404.
	missing := uuid.NewString()
	w3 := httptest.NewRecorder()
	req3 := withURLParam(newRequest("DELETE", "/api/benchmarks/runs/"+missing, nil), "id", missing)
	h.CancelRun(w3, req3)
	if w3.Code != http.StatusNotFound {
		t.Fatalf("cancel missing: expected 404, got %d: %s", w3.Code, w3.Body.String())
	}
	var nf map[string]any
	if err := json.Unmarshal(w3.Body.Bytes(), &nf); err != nil {
		t.Fatalf("decode 404: %v", err)
	}
	if nf["error"] != "run_not_found" {
		t.Fatalf("expected error=run_not_found, got %v", nf["error"])
	}
}

// startImportedRun seeds a suite + profile + run in 'imported' evaluator
// mode and returns the run UUID (string). Cleanup of the suite + run rows
// is registered via t.Cleanup. The run starts with no benchmark_task rows;
// callers create tasks themselves to drive whatever code path they exercise.
func startImportedRun(t *testing.T, h *BenchmarkHandler, label string) string {
	t.Helper()
	suiteID, profileID := seedBenchmarkRunFixtures(t, h, label)
	cleanupBenchmarkRunsForSuite(t, suiteID)

	w := httptest.NewRecorder()
	h.StartRun(w, newRequest("POST", "/api/benchmarks/runs", map[string]any{
		"suite_id":       suiteID,
		"profile_id":     profileID,
		"display_name":   "Imported " + label,
		"evaluator_mode": "imported",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed imported run: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var seeded map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &seeded); err != nil {
		t.Fatalf("decode seeded run: %v", err)
	}
	id, _ := seeded["id"].(string)
	if id == "" {
		t.Fatalf("seeded run has no id")
	}
	return id
}

func TestBenchmarkHandler_ImportEvalResult_204(t *testing.T) {
	h := newBenchmarkHandler(t)
	runID := startImportedRun(t, h, "import204")

	// Seed a benchmark_task in 'submitted' state — TaskDispatcher would
	// normally do this, but we don't run the dispatcher in handler tests.
	runUUID, err := util.ParseUUID(runID)
	if err != nil {
		t.Fatalf("parse run id: %v", err)
	}
	wsUUID, err := util.ParseUUID(testWorkspaceID)
	if err != nil {
		t.Fatalf("parse workspace id: %v", err)
	}
	const instanceID = "abishekvashok__cmatrix.5c082c6"
	task, err := testHandler.Queries.CreateBenchmarkTask(context.Background(), db.CreateBenchmarkTaskParams{
		RunID:        runUUID,
		WorkspaceID:  wsUUID,
		InstanceID:   instanceID,
		InstanceMeta: []byte("{}"),
		Status:       "submitted",
		StatusReason: "",
	})
	if err != nil {
		t.Fatalf("seed benchmark_task: %v", err)
	}

	body := map[string]any{
		"resolved":          true,
		"passed_tests":      5,
		"total_tests":       5,
		"pass_rate":         1.0,
		"raw_eval_json":     map[string]any{},
		"failed_categories": []string{},
	}
	w := httptest.NewRecorder()
	req := withURLParams(
		newRequest("POST", "/api/benchmarks/runs/"+runID+"/eval-results/"+instanceID, body),
		"id", runID,
		"instance_id", instanceID,
	)
	h.ImportEvalResult(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the task advanced to 'scored'.
	updated, err := testHandler.Queries.GetBenchmarkTask(context.Background(), db.GetBenchmarkTaskParams{
		ID:          task.ID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if updated.Status != "scored" {
		t.Fatalf("task status: expected scored, got %q", updated.Status)
	}

	// Verify the eval_result row was upserted.
	er, err := testHandler.Queries.GetBenchmarkEvalResult(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get eval result: %v", err)
	}
	if !er.Resolved {
		t.Fatalf("eval_result.resolved: expected true")
	}
	if er.PassedTests != 5 || er.TotalTests != 5 {
		t.Fatalf("eval_result tests: expected 5/5, got %d/%d", er.PassedTests, er.TotalTests)
	}
}

func TestBenchmarkHandler_ImportEvalResult_404_OnUnknownInstance(t *testing.T) {
	h := newBenchmarkHandler(t)
	runID := startImportedRun(t, h, "import404")

	// No benchmark_task row was created for this instance_id, so the
	// service-layer lookup will return ErrTaskNotFoundForInstance.
	const unknownInstance = "ghost__pkg.0000000"
	body := map[string]any{
		"resolved":          false,
		"passed_tests":      0,
		"total_tests":       1,
		"pass_rate":         0.0,
		"raw_eval_json":     map[string]any{},
		"failed_categories": []string{},
	}
	w := httptest.NewRecorder()
	req := withURLParams(
		newRequest("POST", "/api/benchmarks/runs/"+runID+"/eval-results/"+unknownInstance, body),
		"id", runID,
		"instance_id", unknownInstance,
	)
	h.ImportEvalResult(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode 404: %v", err)
	}
	if got["error"] != "task_not_found_for_instance" {
		t.Fatalf("expected error=task_not_found_for_instance, got %v", got["error"])
	}
}

// cleanupEvaluatorTokens removes evaluator_pool_token rows the test created
// in the package-wide workspace. Each test uses a unique display_name so we
// can drop just our rows without disturbing siblings running in parallel.
func cleanupEvaluatorTokens(t *testing.T, displayName string) {
	t.Helper()
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM evaluator_pool_token WHERE workspace_id = $1 AND display_name = $2`,
			testWorkspaceID, displayName,
		)
	})
}

func TestBenchmarkHandler_CreateEvaluatorToken_201(t *testing.T) {
	h := newBenchmarkHandler(t)
	displayName := "ci-create-201-" + uuid.NewString()[:8]
	cleanupEvaluatorTokens(t, displayName)

	w := httptest.NewRecorder()
	h.CreateEvaluatorToken(w, newRequest("POST", "/api/benchmarks/evaluator-tokens", map[string]any{
		"display_name": displayName,
	}))

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	plain, ok := got["plaintext_token"].(string)
	if !ok || plain == "" {
		t.Fatalf("plaintext_token missing or not a string: %v", got["plaintext_token"])
	}
	if len(plain) < 4 || plain[:4] != "evp_" {
		t.Fatalf("plaintext_token does not start with evp_: %q", plain)
	}
	if got["display_name"] != displayName {
		t.Fatalf("display_name mismatch: %v", got["display_name"])
	}
	prefix, ok := got["prefix"].(string)
	if !ok || prefix == "" {
		t.Fatalf("prefix missing: %v", got["prefix"])
	}
	if id, ok := got["id"].(string); !ok || id == "" {
		t.Fatalf("id missing or not a string: %v", got["id"])
	}
	if got["created_by"] != testUserID {
		t.Fatalf("created_by mismatch: %v", got["created_by"])
	}
}

func TestBenchmarkHandler_CreateEvaluatorToken_400_OnEmptyName(t *testing.T) {
	h := newBenchmarkHandler(t)

	w := httptest.NewRecorder()
	h.CreateEvaluatorToken(w, newRequest("POST", "/api/benchmarks/evaluator-tokens", map[string]any{
		"display_name": "   ",
	}))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode 400: %v", err)
	}
	if got["error"] != "display_name_required" {
		t.Fatalf("expected error=display_name_required, got %v", got["error"])
	}
}

func TestBenchmarkHandler_ListEvaluatorTokens_200(t *testing.T) {
	h := newBenchmarkHandler(t)
	displayName := "ci-list-200-" + uuid.NewString()[:8]
	cleanupEvaluatorTokens(t, displayName)

	// Seed one token so the list is non-empty for shape assertions.
	wc := httptest.NewRecorder()
	h.CreateEvaluatorToken(wc, newRequest("POST", "/api/benchmarks/evaluator-tokens", map[string]any{
		"display_name": displayName,
	}))
	if wc.Code != http.StatusCreated {
		t.Fatalf("seed create: expected 201, got %d: %s", wc.Code, wc.Body.String())
	}

	w := httptest.NewRecorder()
	h.ListEvaluatorTokens(w, newRequest("GET", "/api/benchmarks/evaluator-tokens", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode 200: %v", err)
	}
	items, ok := got["items"].([]any)
	if !ok {
		t.Fatalf("items missing or wrong type: %v", got["items"])
	}
	var seeded map[string]any
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["display_name"] == displayName {
			seeded = m
			break
		}
	}
	if seeded == nil {
		t.Fatalf("seeded token not in list, items=%v", items)
	}
	if _, present := seeded["plaintext_token"]; present {
		t.Fatalf("List response must not include plaintext_token, got %v", seeded)
	}
	if _, present := seeded["token_hash"]; present {
		t.Fatalf("List response must not include token_hash, got %v", seeded)
	}
	if seeded["prefix"] == "" || seeded["prefix"] == nil {
		t.Fatalf("prefix missing in list item: %v", seeded)
	}
}

func TestBenchmarkHandler_RevokeEvaluatorToken_204(t *testing.T) {
	h := newBenchmarkHandler(t)
	displayName := "ci-revoke-204-" + uuid.NewString()[:8]
	cleanupEvaluatorTokens(t, displayName)

	// Seed a token so we have a real id to revoke.
	wc := httptest.NewRecorder()
	h.CreateEvaluatorToken(wc, newRequest("POST", "/api/benchmarks/evaluator-tokens", map[string]any{
		"display_name": displayName,
	}))
	if wc.Code != http.StatusCreated {
		t.Fatalf("seed create: expected 201, got %d: %s", wc.Code, wc.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(wc.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	tokenID, _ := created["id"].(string)
	if tokenID == "" {
		t.Fatalf("seeded token has no id")
	}

	w := httptest.NewRecorder()
	req := withURLParams(
		newRequest("DELETE", "/api/benchmarks/evaluator-tokens/"+tokenID, nil),
		"id", tokenID,
	)
	h.RevokeEvaluatorToken(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// List again — the seeded token should now have a non-empty revoked_at.
	wl := httptest.NewRecorder()
	h.ListEvaluatorTokens(wl, newRequest("GET", "/api/benchmarks/evaluator-tokens", nil))
	if wl.Code != http.StatusOK {
		t.Fatalf("post-revoke list: expected 200, got %d: %s", wl.Code, wl.Body.String())
	}
	var listed map[string]any
	if err := json.Unmarshal(wl.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode listed: %v", err)
	}
	items, _ := listed["items"].([]any)
	var found map[string]any
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["id"] == tokenID {
			found = m
			break
		}
	}
	if found == nil {
		t.Fatalf("revoked token not in list, items=%v", items)
	}
	revokedAt, _ := found["revoked_at"].(string)
	if revokedAt == "" {
		t.Fatalf("expected revoked_at to be populated, got %v", found)
	}
}

// seedCompareSuite creates a suite (with two instances), an agent, and a
// profile against it via the handler, then returns (suiteID, profileID) as
// strings. Used by Compare/Leaderboard handler tests that need a stable
// 2-instance suite. Cleanup of the suite + downstream rows is registered.
func seedCompareSuite(t *testing.T, h *BenchmarkHandler, label string) (suiteID, profileID string) {
	t.Helper()
	suiteSlug := "cmp-suite-" + label + "-" + uuid.NewString()[:8]
	cleanupSuiteSlug(t, suiteSlug)

	w := httptest.NewRecorder()
	h.CreateSuite(w, newRequest("POST", "/api/benchmarks/suites", map[string]any{
		"slug":         suiteSlug,
		"display_name": "Compare suite " + label,
		"adapter_kind": "programbench",
		"instance_ids": []string{"alpha__one.aaa", "beta__two.bbb"},
		"description":  "fixture for compare/leaderboard handler test",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed compare suite: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var suite map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &suite); err != nil {
		t.Fatalf("decode seeded suite: %v", err)
	}
	suiteID, _ = suite["id"].(string)
	if suiteID == "" {
		t.Fatalf("seeded compare suite has no id")
	}

	profileID = seedCompareProfile(t, h, label+"-default")
	return suiteID, profileID
}

// seedCompareProfile inserts a fresh agent + captured profile pair scoped to
// the package-wide test workspace. The slug suffix keeps profiles unique
// across sibling tests; cleanup is handled by createBenchmarkTestAgent's
// t.Cleanup hook on the underlying agent row.
func seedCompareProfile(t *testing.T, h *BenchmarkHandler, slugSuffix string) string {
	t.Helper()
	agentID := createBenchmarkTestAgent(t,
		"Cmp Agent "+slugSuffix+" "+uuid.NewString()[:8],
		"Compare handler fixture prompt",
		"gpt-4o-mini",
	)
	profileSlug := "cmp-profile-" + slugSuffix + "-" + uuid.NewString()[:8]
	w := httptest.NewRecorder()
	h.CaptureProfile(w, newRequest("POST", "/api/benchmarks/profiles", map[string]any{
		"slug":         profileSlug,
		"display_name": "Cmp profile " + slugSuffix,
		"agent_id":     agentID,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed compare profile: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var profile map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &profile); err != nil {
		t.Fatalf("decode seeded profile: %v", err)
	}
	id, _ := profile["id"].(string)
	if id == "" {
		t.Fatalf("seeded compare profile has no id")
	}
	return id
}

// completeRunHandlerSpec mirrors completeRunSpec from the service-package
// fixtures but is package-local so the handler tests don't depend on
// benchmark_test internals. Each evalSpec becomes a benchmark_task +
// benchmark_eval_result pair, and a benchmark_run_summary row is written
// matching the headline counters.
type completeRunHandlerSpec struct {
	DisplayName       string
	SuiteID           string
	ProfileID         string
	Resolved          int32
	Total             int32
	Errored           int32
	AveragePassRate   float64
	AggregatePassRate float64
	Evals             []handlerEvalSpec
}

type handlerEvalSpec struct {
	InstanceID string
	Resolved   bool
	PassRate   float64
}

// seedCompleteRunHandler starts a run via the handler, seeds the per-instance
// task + eval_result rows, drives the run to status='complete', and writes
// the matching run_summary. Returns the run UUID string. Cleanup is the
// caller's responsibility — typically via cleanupBenchmarkRunsForSuite on
// the parent suite, which cascades to tasks/eval_results.
func seedCompleteRunHandler(t *testing.T, h *BenchmarkHandler, spec completeRunHandlerSpec) string {
	t.Helper()
	ctx := context.Background()

	w := httptest.NewRecorder()
	h.StartRun(w, newRequest("POST", "/api/benchmarks/runs", map[string]any{
		"suite_id":        spec.SuiteID,
		"profile_id":      spec.ProfileID,
		"display_name":    spec.DisplayName,
		"evaluator_mode":  "imported",
		"adapter_version": "programbench@v1",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed complete run start: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var seeded map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &seeded); err != nil {
		t.Fatalf("decode seeded run: %v", err)
	}
	runID, _ := seeded["id"].(string)
	if runID == "" {
		t.Fatalf("seeded run has no id")
	}
	runUUID, err := util.ParseUUID(runID)
	if err != nil {
		t.Fatalf("parse run uuid: %v", err)
	}
	wsUUID, err := util.ParseUUID(testWorkspaceID)
	if err != nil {
		t.Fatalf("parse ws uuid: %v", err)
	}

	for _, ev := range spec.Evals {
		task, err := testHandler.Queries.CreateBenchmarkTask(ctx, db.CreateBenchmarkTaskParams{
			RunID:        runUUID,
			WorkspaceID:  wsUUID,
			InstanceID:   ev.InstanceID,
			InstanceMeta: []byte(`{}`),
			Status:       "scored",
		})
		if err != nil {
			t.Fatalf("create benchmark_task: %v", err)
		}
		var pr pgtype.Numeric
		if err := pr.Scan(fmt.Sprintf("%.5f", ev.PassRate)); err != nil {
			t.Fatalf("scan pass_rate: %v", err)
		}
		if _, err := testHandler.Queries.UpsertBenchmarkEvalResult(ctx, db.UpsertBenchmarkEvalResultParams{
			TaskID:           task.ID,
			WorkspaceID:      wsUUID,
			Resolved:         ev.Resolved,
			PassedTests:      0,
			TotalTests:       1,
			PassRate:         pr,
			RawEvalJson:      []byte(`{}`),
			FailedCategories: []byte(`[]`),
		}); err != nil {
			t.Fatalf("upsert eval_result: %v", err)
		}
	}

	// Drive the run through 'submitting' → 'complete' so completed_at is
	// populated; the leaderboard query orders by completed_at DESC.
	if _, err := testHandler.Queries.UpdateBenchmarkRunStatus(ctx, db.UpdateBenchmarkRunStatusParams{
		ID:          runUUID,
		WorkspaceID: wsUUID,
		Status:      "submitting",
	}); err != nil {
		t.Fatalf("update status submitting: %v", err)
	}
	if _, err := testHandler.Queries.UpdateBenchmarkRunStatus(ctx, db.UpdateBenchmarkRunStatusParams{
		ID:          runUUID,
		WorkspaceID: wsUUID,
		Status:      "complete",
	}); err != nil {
		t.Fatalf("update status complete: %v", err)
	}

	var agg, avg pgtype.Numeric
	if err := agg.Scan(fmt.Sprintf("%.5f", spec.AggregatePassRate)); err != nil {
		t.Fatalf("scan agg: %v", err)
	}
	if err := avg.Scan(fmt.Sprintf("%.5f", spec.AveragePassRate)); err != nil {
		t.Fatalf("scan avg: %v", err)
	}
	if _, err := testHandler.Queries.UpsertBenchmarkRunSummary(ctx, db.UpsertBenchmarkRunSummaryParams{
		RunID:             runUUID,
		WorkspaceID:       wsUUID,
		ResolvedCount:     spec.Resolved,
		TotalCount:        spec.Total,
		AggregatePassRate: agg,
		AveragePassRate:   avg,
		ErroredCount:      spec.Errored,
		FailureCategories: []byte(`[]`),
	}); err != nil {
		t.Fatalf("upsert run_summary: %v", err)
	}
	return runID
}

func TestBenchmarkHandler_CompareRun_200(t *testing.T) {
	h := newBenchmarkHandler(t)
	suiteID, baseProfileID := seedCompareSuite(t, h, "cmp200")
	cleanupBenchmarkRunsForSuite(t, suiteID)
	candProfileID := seedCompareProfile(t, h, "cmp200-cand")

	baseID := seedCompleteRunHandler(t, h, completeRunHandlerSpec{
		DisplayName:       "Base",
		SuiteID:           suiteID,
		ProfileID:         baseProfileID,
		Resolved:          0,
		Total:             2,
		AveragePassRate:   0.5,
		AggregatePassRate: 0.5,
		Evals: []handlerEvalSpec{
			{InstanceID: "alpha__one.aaa", PassRate: 0.5, Resolved: false},
			{InstanceID: "beta__two.bbb", PassRate: 0.5, Resolved: false},
		},
	})
	candID := seedCompleteRunHandler(t, h, completeRunHandlerSpec{
		DisplayName:       "Candidate",
		SuiteID:           suiteID,
		ProfileID:         candProfileID,
		Resolved:          1,
		Total:             2,
		AveragePassRate:   0.75,
		AggregatePassRate: 0.75,
		Evals: []handlerEvalSpec{
			{InstanceID: "alpha__one.aaa", PassRate: 1.0, Resolved: true},
			{InstanceID: "beta__two.bbb", PassRate: 0.5, Resolved: false},
		},
	})

	w := httptest.NewRecorder()
	req := withURLParam(
		newRequest("GET", "/api/benchmarks/runs/"+candID+"/compare", nil),
		"id", candID,
	)
	// httptest's NewRequest strips query strings unless we set RawQuery
	// directly; the handler reads ?base= via r.URL.Query().
	req.URL.RawQuery = "base=" + baseID
	h.CompareRun(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got benchmark.ComparisonResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.BaseRunID != baseID {
		t.Fatalf("base_run_id mismatch: got %s, want %s", got.BaseRunID, baseID)
	}
	if got.CandRunID != candID {
		t.Fatalf("cand_run_id mismatch: got %s, want %s", got.CandRunID, candID)
	}
	if got.Delta.Resolved != 1 {
		t.Fatalf("delta.resolved: got %d, want 1", got.Delta.Resolved)
	}
}

func TestBenchmarkHandler_CompareRun_400_OnMissingBase(t *testing.T) {
	h := newBenchmarkHandler(t)
	candID := uuid.NewString()
	w := httptest.NewRecorder()
	req := withURLParam(newRequest("GET", "/api/benchmarks/runs/"+candID+"/compare", nil), "id", candID)
	h.CompareRun(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode 400: %v", err)
	}
	if got["error"] != "base_required" {
		t.Fatalf("expected error=base_required, got %v", got["error"])
	}
}

func TestBenchmarkHandler_CompareRun_404_OnUnknownRun(t *testing.T) {
	h := newBenchmarkHandler(t)
	suiteID, profileID := seedCompareSuite(t, h, "cmp404")
	cleanupBenchmarkRunsForSuite(t, suiteID)

	// Real complete run as base; candidate is a bogus UUID so the service
	// returns ErrRunNotFound and the handler maps to 404.
	baseID := seedCompleteRunHandler(t, h, completeRunHandlerSpec{
		DisplayName:       "Base for 404",
		SuiteID:           suiteID,
		ProfileID:         profileID,
		Resolved:          1,
		Total:             1,
		AveragePassRate:   1.0,
		AggregatePassRate: 1.0,
		Evals: []handlerEvalSpec{
			{InstanceID: "alpha__one.aaa", PassRate: 1.0, Resolved: true},
		},
	})
	bogusCand := uuid.NewString()

	w := httptest.NewRecorder()
	req := withURLParam(
		newRequest("GET", "/api/benchmarks/runs/"+bogusCand+"/compare", nil),
		"id", bogusCand,
	)
	req.URL.RawQuery = "base=" + baseID
	h.CompareRun(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode 404: %v", err)
	}
	if got["error"] != "run_not_found" {
		t.Fatalf("expected error=run_not_found, got %v", got["error"])
	}
}

func TestBenchmarkHandler_Leaderboard_200(t *testing.T) {
	h := newBenchmarkHandler(t)
	suiteID, profA := seedCompareSuite(t, h, "lb200")
	cleanupBenchmarkRunsForSuite(t, suiteID)
	profB := seedCompareProfile(t, h, "lb200-b")

	// Fetch the suite slug — the helper only returns the UUID, but the
	// Leaderboard endpoint takes ?suite=<slug>.
	wGet := httptest.NewRecorder()
	getReq := withURLParam(newRequest("GET", "/api/benchmarks/suites/"+suiteID, nil), "id", suiteID)
	h.GetSuite(wGet, getReq)
	if wGet.Code != http.StatusOK {
		t.Fatalf("get suite: %d %s", wGet.Code, wGet.Body.String())
	}
	var suiteRow map[string]any
	if err := json.Unmarshal(wGet.Body.Bytes(), &suiteRow); err != nil {
		t.Fatalf("decode suite: %v", err)
	}
	suiteSlug, _ := suiteRow["slug"].(string)
	if suiteSlug == "" {
		t.Fatalf("suite slug empty")
	}

	_ = seedCompleteRunHandler(t, h, completeRunHandlerSpec{
		DisplayName:       "A best",
		SuiteID:           suiteID,
		ProfileID:         profA,
		Resolved:          2,
		Total:             2,
		AveragePassRate:   1.0,
		AggregatePassRate: 1.0,
		Evals: []handlerEvalSpec{
			{InstanceID: "alpha__one.aaa", PassRate: 1.0, Resolved: true},
			{InstanceID: "beta__two.bbb", PassRate: 1.0, Resolved: true},
		},
	})
	// Sleep so completed_at differs across runs; the leaderboard ORDER BY
	// uses completed_at DESC and the tiebreaker fires on identical
	// millisecond timestamps.
	time.Sleep(1100 * time.Millisecond)
	_ = seedCompleteRunHandler(t, h, completeRunHandlerSpec{
		DisplayName:       "B only",
		SuiteID:           suiteID,
		ProfileID:         profB,
		Resolved:          1,
		Total:             2,
		AveragePassRate:   0.5,
		AggregatePassRate: 0.5,
		Evals: []handlerEvalSpec{
			{InstanceID: "alpha__one.aaa", PassRate: 1.0, Resolved: true},
			{InstanceID: "beta__two.bbb", PassRate: 0.0, Resolved: false},
		},
	})

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/benchmarks/leaderboard", nil)
	req.URL.RawQuery = "suite=" + suiteSlug
	h.Leaderboard(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Items []benchmark.LeaderboardRow `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode 200: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("expected 2 leaderboard rows, got %d: %v", len(got.Items), got.Items)
	}
	if got.Items[0].Rank != 1 || got.Items[1].Rank != 2 {
		t.Fatalf("ranks: got %d/%d, want 1/2", got.Items[0].Rank, got.Items[1].Rank)
	}
	if got.Items[0].ResolvedCount != 2 {
		t.Fatalf("rank-1 resolved_count: got %d, want 2", got.Items[0].ResolvedCount)
	}
}

func TestBenchmarkHandler_Leaderboard_404_OnUnknownSuite(t *testing.T) {
	h := newBenchmarkHandler(t)
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/benchmarks/leaderboard", nil)
	req.URL.RawQuery = "suite=bogus-no-such-" + uuid.NewString()[:8]
	h.Leaderboard(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode 404: %v", err)
	}
	if got["error"] != "suite_not_found" {
		t.Fatalf("expected error=suite_not_found, got %v", got["error"])
	}
}

func TestBenchmarkHandler_Leaderboard_400_OnMissingSuite(t *testing.T) {
	h := newBenchmarkHandler(t)
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/benchmarks/leaderboard", nil)
	h.Leaderboard(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode 400: %v", err)
	}
	if got["error"] != "suite_required" {
		t.Fatalf("expected error=suite_required, got %v", got["error"])
	}
}

// TestBenchmarkHandler_ListRunTasks_200 seeds a complete run with two tasks
// (one resolved, one not) and asserts that the handler returns both with
// the eval_result fields merged in.
func TestBenchmarkHandler_ListRunTasks_200(t *testing.T) {
	h := newBenchmarkHandler(t)
	suiteID, profileID := seedCompareSuite(t, h, "lt200")
	cleanupBenchmarkRunsForSuite(t, suiteID)

	runID := seedCompleteRunHandler(t, h, completeRunHandlerSpec{
		DisplayName:       "List tasks 200",
		SuiteID:           suiteID,
		ProfileID:         profileID,
		Resolved:          1,
		Total:             2,
		AveragePassRate:   0.75,
		AggregatePassRate: 0.75,
		Evals: []handlerEvalSpec{
			{InstanceID: "alpha__one.aaa", PassRate: 1.0, Resolved: true},
			{InstanceID: "beta__two.bbb", PassRate: 0.5, Resolved: false},
		},
	})

	w := httptest.NewRecorder()
	req := withURLParam(newRequest("GET", "/api/benchmarks/runs/"+runID+"/tasks", nil), "id", runID)
	h.ListRunTasks(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode 200: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("expected 2 task rows, got %d: %v", len(got.Items), got.Items)
	}
	// ListBenchmarkTasksByRun orders by instance_id ASC, so alpha precedes beta.
	first := got.Items[0]
	if first["instance_id"] != "alpha__one.aaa" {
		t.Fatalf("expected first instance alpha__one.aaa, got %v", first["instance_id"])
	}
	if first["resolved"] != true {
		t.Fatalf("expected first resolved=true, got %v", first["resolved"])
	}
	if pr, _ := first["pass_rate"].(float64); pr < 0.999 {
		t.Fatalf("expected first pass_rate~1.0, got %v", first["pass_rate"])
	}
	second := got.Items[1]
	if second["instance_id"] != "beta__two.bbb" {
		t.Fatalf("expected second instance beta__two.bbb, got %v", second["instance_id"])
	}
	if second["resolved"] != false {
		t.Fatalf("expected second resolved=false, got %v", second["resolved"])
	}
	if second["status"] != "scored" {
		t.Fatalf("expected second status=scored, got %v", second["status"])
	}
}

// TestBenchmarkHandler_ListRunTasks_404 asserts that a bogus run id within
// the workspace surfaces as 404 run_not_found rather than a 200 with an
// empty list (which would mask cross-workspace lookups).
func TestBenchmarkHandler_ListRunTasks_404(t *testing.T) {
	h := newBenchmarkHandler(t)
	bogus := uuid.NewString()

	w := httptest.NewRecorder()
	req := withURLParam(newRequest("GET", "/api/benchmarks/runs/"+bogus+"/tasks", nil), "id", bogus)
	h.ListRunTasks(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode 404: %v", err)
	}
	if got["error"] != "run_not_found" {
		t.Fatalf("expected error=run_not_found, got %v", got["error"])
	}
}

// TestBenchmarkHandler_GetRunSummary_200 seeds a finalized run with a
// summary row and asserts that the handler returns the projected fields.
func TestBenchmarkHandler_GetRunSummary_200(t *testing.T) {
	h := newBenchmarkHandler(t)
	suiteID, profileID := seedCompareSuite(t, h, "sum200")
	cleanupBenchmarkRunsForSuite(t, suiteID)

	runID := seedCompleteRunHandler(t, h, completeRunHandlerSpec{
		DisplayName:       "Summary 200",
		SuiteID:           suiteID,
		ProfileID:         profileID,
		Resolved:          2,
		Total:             3,
		Errored:           0,
		AveragePassRate:   0.66667,
		AggregatePassRate: 0.66667,
		Evals: []handlerEvalSpec{
			{InstanceID: "alpha__one.aaa", PassRate: 1.0, Resolved: true},
			{InstanceID: "beta__two.bbb", PassRate: 1.0, Resolved: true},
			{InstanceID: "gamma__three.ccc", PassRate: 0.0, Resolved: false},
		},
	})

	w := httptest.NewRecorder()
	req := withURLParam(newRequest("GET", "/api/benchmarks/runs/"+runID+"/summary", nil), "id", runID)
	h.GetRunSummary(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got benchmark.BenchmarkRunSummaryView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode 200: %v", err)
	}
	if got.RunID != runID {
		t.Fatalf("run_id mismatch: got %s, want %s", got.RunID, runID)
	}
	if got.ResolvedCount != 2 {
		t.Fatalf("resolved_count: got %d, want 2", got.ResolvedCount)
	}
	if got.TotalCount != 3 {
		t.Fatalf("total_count: got %d, want 3", got.TotalCount)
	}
	if got.AveragePassRate < 0.6 || got.AveragePassRate > 0.7 {
		t.Fatalf("average_pass_rate: got %v, want ~0.66667", got.AveragePassRate)
	}
}

// TestBenchmarkHandler_GetRunSummary_404_NoSummaryYet asserts the handler
// distinguishes "run exists, no summary yet" (running run) from "run not
// found" — the former returns 404 summary_not_available so the frontend
// can render an in-progress placeholder.
func TestBenchmarkHandler_GetRunSummary_404_NoSummaryYet(t *testing.T) {
	h := newBenchmarkHandler(t)
	suiteID, profileID := seedBenchmarkRunFixtures(t, h, "sum404nosummary")
	cleanupBenchmarkRunsForSuite(t, suiteID)

	// StartRun creates a queued run with no summary row yet — exactly the
	// state we need to exercise the (run exists, summary missing) branch.
	w0 := httptest.NewRecorder()
	h.StartRun(w0, newRequest("POST", "/api/benchmarks/runs", map[string]any{
		"suite_id":       suiteID,
		"profile_id":     profileID,
		"display_name":   "No summary yet",
		"evaluator_mode": "imported",
	}))
	if w0.Code != http.StatusCreated {
		t.Fatalf("start run: %d %s", w0.Code, w0.Body.String())
	}
	var run map[string]any
	if err := json.Unmarshal(w0.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	runID, _ := run["id"].(string)
	if runID == "" {
		t.Fatalf("run id empty")
	}

	w := httptest.NewRecorder()
	req := withURLParam(newRequest("GET", "/api/benchmarks/runs/"+runID+"/summary", nil), "id", runID)
	h.GetRunSummary(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode 404: %v", err)
	}
	if got["error"] != "summary_not_available" {
		t.Fatalf("expected error=summary_not_available, got %v", got["error"])
	}
}
