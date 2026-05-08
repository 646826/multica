package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service/benchmark"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// evalJobsTestEnv bundles everything an /api/internal/eval-jobs/* test
// needs: a fresh chi router with the auth middleware mounted, the
// plain-text token to put in Authorization, and the workspace-scoped
// run id under which seed tasks/jobs should be created. The fixture
// uses the same DB-backed test pool the rest of the handler suite
// shares (handler_test.go globals); each test seeds its own job rows
// and registers cleanup so parallel runs don't collide.
type evalJobsTestEnv struct {
	router     *chi.Mux
	tokenPlain string
	runID      string
}

// newEvalJobsTestEnv builds the test environment. Mounting through chi
// rather than calling handler methods directly is deliberate: it
// exercises the same middleware chain that production traffic will
// hit (T08 wires the same chain into router.go), so an
// authenticated-route bug won't slip through with green tests.
func newEvalJobsTestEnv(t *testing.T, label string) evalJobsTestEnv {
	t.Helper()
	if testHandler == nil {
		t.Skip("testHandler not initialized (DATABASE_URL unreachable)")
	}

	bench := newBenchmarkHandler(t)
	displayName := "ci-eval-jobs-" + label + "-" + uuid.NewString()[:8]
	cleanupEvaluatorTokens(t, displayName)

	// Mint a real evaluator-pool token so the auth middleware sees
	// matching rows in the DB. Verify() in production hits the same
	// query.
	wc := httptest.NewRecorder()
	bench.CreateEvaluatorToken(wc, newRequest("POST", "/api/benchmarks/evaluator-tokens", map[string]any{
		"display_name": displayName,
	}))
	if wc.Code != http.StatusCreated {
		t.Fatalf("seed evaluator token: expected 201, got %d: %s", wc.Code, wc.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(wc.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode token: %v", err)
	}
	plain, _ := created["plaintext_token"].(string)
	if plain == "" {
		t.Fatalf("seed evaluator token: missing plaintext_token")
	}

	// Seed a run so we have a place to attach benchmark_task /
	// benchmark_eval_job rows. The /complete and /fail tests need both.
	runID := startImportedRun(t, bench, "evjobs-"+label)

	// Build the eval-jobs handler against a NEW EvalJobService — it
	// must hit the same DB pool the seed code used, otherwise Claim
	// won't see the seeded jobs. Using a fresh events.Bus is fine;
	// publish failures don't affect HTTP behavior.
	evpSvc := benchmark.NewEvaluatorPoolService(testHandler.Queries)
	jobSvc := benchmark.NewEvalJobService(testHandler.Queries, testPool, events.New())
	jh := NewEvalJobsHandler(jobSvc)

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireEvaluatorPoolAuth(evpSvc))
		r.Post("/api/internal/eval-jobs/claim", jh.Claim)
		r.Post("/api/internal/eval-jobs/{id}/complete", jh.Complete)
		r.Post("/api/internal/eval-jobs/{id}/fail", jh.Fail)
	})

	return evalJobsTestEnv{
		router:     r,
		tokenPlain: plain,
		runID:      runID,
	}
}

// seedEvalJob inserts a benchmark_task + benchmark_eval_job pair under
// the test run. Returns the job id (string) for use in URLs and the
// task id for downstream assertions on task.status.
func seedEvalJob(t *testing.T, env evalJobsTestEnv, instanceID, adapterKind string) (jobIDStr, taskIDStr string) {
	t.Helper()
	ctx := context.Background()

	runUUID, err := util.ParseUUID(env.runID)
	if err != nil {
		t.Fatalf("parse run id: %v", err)
	}
	wsUUID, err := util.ParseUUID(testWorkspaceID)
	if err != nil {
		t.Fatalf("parse workspace id: %v", err)
	}

	task, err := testHandler.Queries.CreateBenchmarkTask(ctx, db.CreateBenchmarkTaskParams{
		RunID:        runUUID,
		WorkspaceID:  wsUUID,
		InstanceID:   instanceID,
		InstanceMeta: []byte(`{"k":"v"}`),
		Status:       "submitted",
	})
	if err != nil {
		t.Fatalf("seed benchmark_task: %v", err)
	}
	job, err := testHandler.Queries.CreateBenchmarkEvalJob(ctx, db.CreateBenchmarkEvalJobParams{
		TaskID:      task.ID,
		WorkspaceID: wsUUID,
		AdapterKind: adapterKind,
	})
	if err != nil {
		t.Fatalf("seed benchmark_eval_job: %v", err)
	}
	return util.UUIDToString(job.ID), util.UUIDToString(task.ID)
}

// authedRequest builds a JSON request with the evaluator-pool bearer
// token already attached. We bypass newRequest() because that helper
// sets the user-JWT-style X-User-ID / X-Workspace-ID headers, which
// are unused (and ignored) on the internal eval-jobs routes.
func authedRequest(method, path, token string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestEvalJobsHandler_Claim_AuthorizedReturnsJobs(t *testing.T) {
	env := newEvalJobsTestEnv(t, "claim-200")
	jobID, _ := seedEvalJob(t, env, "alpha__one.aaa", "programbench")

	body := map[string]any{
		"evaluator_id":   "evaluator-A",
		"adapter_kinds":  []string{"programbench"},
		"max_concurrent": 5,
	}
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, authedRequest("POST", "/api/internal/eval-jobs/claim", env.tokenPlain, body))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, w.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 job, got %d: %v", len(got), got)
	}
	if got[0]["job_id"] != jobID {
		t.Fatalf("job_id mismatch: got %v want %s", got[0]["job_id"], jobID)
	}
	if got[0]["adapter_kind"] != "programbench" {
		t.Fatalf("adapter_kind mismatch: %v", got[0]["adapter_kind"])
	}
	if got[0]["instance_id"] != "alpha__one.aaa" {
		t.Fatalf("instance_id mismatch: %v", got[0]["instance_id"])
	}
}

func TestEvalJobsHandler_Claim_RejectsMissingToken_401(t *testing.T) {
	env := newEvalJobsTestEnv(t, "claim-401")

	body := map[string]any{
		"evaluator_id":  "evaluator-A",
		"adapter_kinds": []string{"programbench"},
	}
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, authedRequest("POST", "/api/internal/eval-jobs/claim", "", body))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEvalJobsHandler_Claim_BadRequest_OnMissingEvaluatorID(t *testing.T) {
	env := newEvalJobsTestEnv(t, "claim-400-evid")

	body := map[string]any{
		"adapter_kinds": []string{"programbench"},
	}
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, authedRequest("POST", "/api/internal/eval-jobs/claim", env.tokenPlain, body))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["error"] != "evaluator_id_required" {
		t.Fatalf("expected evaluator_id_required, got %v", got["error"])
	}
}

func TestEvalJobsHandler_Claim_BadRequest_OnEmptyAdapterKinds(t *testing.T) {
	env := newEvalJobsTestEnv(t, "claim-400-kinds")

	body := map[string]any{
		"evaluator_id": "evaluator-A",
	}
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, authedRequest("POST", "/api/internal/eval-jobs/claim", env.tokenPlain, body))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["error"] != "adapter_kinds_required" {
		t.Fatalf("expected adapter_kinds_required, got %v", got["error"])
	}
}

func TestEvalJobsHandler_Complete_AdvancesTask_204(t *testing.T) {
	env := newEvalJobsTestEnv(t, "complete-204")
	jobID, taskIDStr := seedEvalJob(t, env, "beta__two.bbb", "programbench")

	// Claim first — Complete operates on a 'claimed' job.
	wClaim := httptest.NewRecorder()
	env.router.ServeHTTP(wClaim, authedRequest("POST", "/api/internal/eval-jobs/claim", env.tokenPlain, map[string]any{
		"evaluator_id":  "evaluator-A",
		"adapter_kinds": []string{"programbench"},
	}))
	if wClaim.Code != http.StatusOK {
		t.Fatalf("seed claim: expected 200, got %d: %s", wClaim.Code, wClaim.Body.String())
	}

	body := map[string]any{
		"resolved":          true,
		"passed_tests":      4,
		"total_tests":       5,
		"pass_rate":         0.8,
		"raw_eval_json":     map[string]any{"verdict": "pass"},
		"failed_categories": []string{},
	}
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, authedRequest("POST", "/api/internal/eval-jobs/"+jobID+"/complete", env.tokenPlain, body))

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the task advanced to 'scored'.
	taskUUID, err := util.ParseUUID(taskIDStr)
	if err != nil {
		t.Fatalf("parse task id: %v", err)
	}
	wsUUID, err := util.ParseUUID(testWorkspaceID)
	if err != nil {
		t.Fatalf("parse workspace id: %v", err)
	}
	updated, err := testHandler.Queries.GetBenchmarkTask(context.Background(), db.GetBenchmarkTaskParams{
		ID:          taskUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if updated.Status != "scored" {
		t.Fatalf("task.status: expected scored, got %q", updated.Status)
	}
}

func TestEvalJobsHandler_Complete_GoneOnUnknownJob_410(t *testing.T) {
	env := newEvalJobsTestEnv(t, "complete-410")

	bogus := uuid.NewString()
	body := map[string]any{
		"resolved":          true,
		"passed_tests":      1,
		"total_tests":       1,
		"pass_rate":         1.0,
		"raw_eval_json":     map[string]any{},
		"failed_categories": []string{},
	}
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, authedRequest("POST", "/api/internal/eval-jobs/"+bogus+"/complete", env.tokenPlain, body))

	if w.Code != http.StatusGone {
		t.Fatalf("expected 410, got %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["error"] != "eval_job_not_found" {
		t.Fatalf("expected eval_job_not_found, got %v", got["error"])
	}
}

func TestEvalJobsHandler_Fail_ReturnsToPending_204(t *testing.T) {
	env := newEvalJobsTestEnv(t, "fail-204")
	jobID, _ := seedEvalJob(t, env, "gamma__three.ccc", "programbench")

	// Claim first — Fail also operates on a 'claimed' job in the
	// real evaluator loop, and we want to assert the row returns
	// to 'pending' afterwards.
	wClaim := httptest.NewRecorder()
	env.router.ServeHTTP(wClaim, authedRequest("POST", "/api/internal/eval-jobs/claim", env.tokenPlain, map[string]any{
		"evaluator_id":  "evaluator-A",
		"adapter_kinds": []string{"programbench"},
	}))
	if wClaim.Code != http.StatusOK {
		t.Fatalf("seed claim: expected 200, got %d: %s", wClaim.Code, wClaim.Body.String())
	}

	body := map[string]any{"last_error": "transient boom"}
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, authedRequest("POST", "/api/internal/eval-jobs/"+jobID+"/fail", env.tokenPlain, body))

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the job is back in 'pending' (transient failure).
	jobUUID, err := util.ParseUUID(jobID)
	if err != nil {
		t.Fatalf("parse job id: %v", err)
	}
	row, err := testHandler.Queries.GetBenchmarkEvalJob(context.Background(), jobUUID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if row.State != "pending" {
		t.Fatalf("job.state: expected pending, got %q", row.State)
	}
	if row.LastError != "transient boom" {
		t.Fatalf("job.last_error: expected %q, got %q", "transient boom", row.LastError)
	}
}

func TestEvalJobsHandler_Fail_GoneOnUnknownJob_410(t *testing.T) {
	env := newEvalJobsTestEnv(t, "fail-410")

	bogus := uuid.NewString()
	body := map[string]any{"last_error": "boom"}
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, authedRequest("POST", "/api/internal/eval-jobs/"+bogus+"/fail", env.tokenPlain, body))

	if w.Code != http.StatusGone {
		t.Fatalf("expected 410, got %d: %s", w.Code, w.Body.String())
	}
}
