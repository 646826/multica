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
