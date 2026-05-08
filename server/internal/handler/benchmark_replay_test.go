package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// seedReplayDoneIssue inserts a `done` issue scoped to the package-wide
// handler workspace and registers cleanup. Returns the issue id (string)
// so tests can pass it through the handler payload as-is.
func seedReplayDoneIssue(t *testing.T, title, description string) (issueID string, number int32) {
	t.Helper()
	ctx := context.Background()

	// Pick a unique issue.number per insert (workspace_id, number) is unique.
	// COALESCE(MAX(number),0)+1 is enough for test isolation.
	if err := testPool.QueryRow(ctx, `
		WITH next AS (
			SELECT COALESCE(MAX(number), 0) + 1 AS n FROM issue WHERE workspace_id = $1
		)
		INSERT INTO issue (
			workspace_id, title, description, status, priority,
			creator_type, creator_id, number, position
		)
		VALUES ($1, $2, $3, 'done', 'none', 'member', $4, (SELECT n FROM next), 0)
		RETURNING id, number
	`, testWorkspaceID, title, description, testUserID).Scan(&issueID, &number); err != nil {
		t.Fatalf("seed done issue: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})
	return issueID, number
}

func TestBenchmarkHandler_ListReplayEligibleIssues_200(t *testing.T) {
	h := newBenchmarkHandler(t)

	id1, num1 := seedReplayDoneIssue(t, "Replay eligible A "+uuid.NewString()[:6], "body A")
	id2, num2 := seedReplayDoneIssue(t, "Replay eligible B "+uuid.NewString()[:6], "body B")

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/benchmarks/replay/eligible-issues?limit=5", nil)
	h.ListReplayEligibleIssues(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items, ok := got["items"].([]any)
	if !ok {
		t.Fatalf("items not an array: %v", got)
	}
	// Newest first — id2 was inserted after id1.
	foundIDs := map[string]int32{}
	for _, it := range items {
		m := it.(map[string]any)
		idStr, _ := m["id"].(string)
		foundIDs[idStr] = int32(m["number"].(float64))
		if m["status"] != "done" {
			t.Fatalf("status not 'done': %v", m["status"])
		}
	}
	if foundIDs[id1] != num1 {
		t.Fatalf("expected id1=%s with number=%d, got %d", id1, num1, foundIDs[id1])
	}
	if foundIDs[id2] != num2 {
		t.Fatalf("expected id2=%s with number=%d, got %d", id2, num2, foundIDs[id2])
	}
}

func TestBenchmarkHandler_CreateReplaySuite_201(t *testing.T) {
	h := newBenchmarkHandler(t)
	slug := "replay-suite-201-" + uuid.NewString()[:8]
	cleanupSuiteSlug(t, slug)

	issueID, _ := seedReplayDoneIssue(t, "Replay create 201 "+uuid.NewString()[:6], "desc")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/benchmarks/replay/suites", map[string]any{
		"slug":         slug,
		"display_name": "Replay 201",
		"description":  "test",
		"instances": []map[string]any{{
			"source_issue_id":    issueID,
			"reference_solution": "diff --git a/x b/x\n+ok",
			"reference_pr_url":   "https://example.test/pr/1",
		}},
	})
	h.CreateReplaySuite(w, req)

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
	if got["adapter_kind"] != "multica_replay" {
		t.Fatalf("adapter_kind = %v; want multica_replay", got["adapter_kind"])
	}
	instances, ok := got["instance_ids"].([]any)
	if !ok || len(instances) != 1 {
		t.Fatalf("instance_ids: %v", got["instance_ids"])
	}
	expectedInst := "multica-issue:" + issueID
	if instances[0] != expectedInst {
		t.Fatalf("instance id = %v; want %s", instances[0], expectedInst)
	}
}

func TestBenchmarkHandler_CreateReplaySuite_400_OnEmpty(t *testing.T) {
	h := newBenchmarkHandler(t)
	slug := "replay-empty-" + uuid.NewString()[:8]
	cleanupSuiteSlug(t, slug)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/benchmarks/replay/suites", map[string]any{
		"slug":         slug,
		"display_name": "Empty",
		"instances":    []map[string]any{},
	})
	h.CreateReplaySuite(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["error"] != "instance_list_empty" {
		t.Fatalf("error = %v; want instance_list_empty", got["error"])
	}
}

func TestBenchmarkHandler_CreateReplaySuite_400_OnMissingReference(t *testing.T) {
	h := newBenchmarkHandler(t)
	slug := "replay-noref-" + uuid.NewString()[:8]
	cleanupSuiteSlug(t, slug)

	issueID, _ := seedReplayDoneIssue(t, "Replay no-ref "+uuid.NewString()[:6], "")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/benchmarks/replay/suites", map[string]any{
		"slug":         slug,
		"display_name": "No Ref",
		"instances": []map[string]any{{
			"source_issue_id":    issueID,
			"reference_solution": "",
		}},
	})
	h.CreateReplaySuite(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["error"] != "reference_solution_required" {
		t.Fatalf("error = %v; want reference_solution_required", got["error"])
	}
}

func TestBenchmarkHandler_CreateReplaySuite_404_OnUnknownIssue(t *testing.T) {
	h := newBenchmarkHandler(t)
	slug := "replay-unknown-" + uuid.NewString()[:8]
	cleanupSuiteSlug(t, slug)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/benchmarks/replay/suites", map[string]any{
		"slug":         slug,
		"display_name": "Unknown",
		"instances": []map[string]any{{
			"source_issue_id":    "00000000-0000-0000-0000-000000000000",
			"reference_solution": "diff --solution",
		}},
	})
	h.CreateReplaySuite(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["error"] != "replay_source_issue_not_found" {
		t.Fatalf("error = %v; want replay_source_issue_not_found", got["error"])
	}
}
