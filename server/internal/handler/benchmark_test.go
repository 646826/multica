package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

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
		Suites:   benchmark.NewSuiteService(testHandler.Queries),
		Profiles: benchmark.NewProfileService(testHandler.Queries),
		Runs:     benchmark.NewRunService(testHandler.Queries, testPool, events.New()),
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
