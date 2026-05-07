package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/service/benchmark"
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
	})
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
