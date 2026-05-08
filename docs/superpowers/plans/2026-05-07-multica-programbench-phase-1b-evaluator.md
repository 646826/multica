# Multica × ProgramBench Phase 1b — Evaluator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Add the managed-eval pipeline. After 1b: operator deploys an evaluator pod with a workspace evaluator-token; the pod claims pending eval jobs from the server, runs ProgramBench eval inside Docker, and posts results back. Runs in `evaluator_mode='managed'` finalize automatically without operator-side imports.

**Architecture:** New `EvaluatorPoolService` mints/auths/revokes workspace tokens. New `/api/benchmarks/evaluator-tokens` (admin) and `/api/internal/eval-jobs/*` (token-auth) endpoints. ProgramBench `Evaluator` impl runs `uvx programbench eval`. New `cmd/evaluator/` binary is the long-running pull-loop pod.

**Reference precedent:** `server/internal/service/personal_access_token.go` (token pattern), Phase 1a's TaskDispatcher for sqlc transactions, autopilot for goroutine lifecycle.

---

## File Structure

| Path | Purpose |
|---|---|
| `server/internal/service/benchmark/evaluator_pool_service.go` | Mint/list/revoke evaluator tokens; verify-by-hash for middleware |
| `server/internal/service/benchmark/evaluator_pool_service_test.go` | Tests |
| `server/internal/middleware/evaluator_pool_auth.go` | Bearer-token middleware that resolves to workspace + populates ctx |
| `server/internal/service/benchmark/eval_job_service.go` | Claim/Complete/Fail logic; reads attachments, returns download URLs |
| `server/internal/service/benchmark/adapter/programbench_evaluator.go` | Evaluator impl (uvx programbench eval) — registered in evaluator binary, NOT server |
| `server/internal/handler/evaluator_pool.go` | Admin endpoints `/api/benchmarks/evaluator-tokens` |
| `server/internal/handler/eval_jobs.go` | Internal endpoints `/api/internal/eval-jobs/*` |
| `server/cmd/evaluator/main.go` | Binary entrypoint — wire registry, start claim loop |
| `server/cmd/evaluator/runner.go` | Per-job runner: download → eval → post |
| `server/cmd/evaluator/client.go` | HTTP client for talking to multica-server |

---

## Task 1: EvaluatorPoolService (mint/list/revoke + verify)

**Files:**
- `server/internal/service/benchmark/evaluator_pool_service.go`
- `server/internal/service/benchmark/evaluator_pool_service_test.go`

- [ ] **Step 1: Tests**

```go
func TestEvaluatorPoolService_Create_ReturnsTokenOnce(t *testing.T) {
    // Create returns full plain-text token (only once); LookupByHash succeeds.
}
func TestEvaluatorPoolService_Verify_RejectsRevoked(t *testing.T) {
    // Revoke; subsequent Verify returns ErrEvaluatorPoolTokenRevoked.
}
func TestEvaluatorPoolService_List_OmitsHash(t *testing.T) {
    // List returns prefix + display_name + timestamps; no token_hash field.
}
func TestEvaluatorPoolService_Verify_TouchesLastUsed(t *testing.T) {
    // After Verify, last_used_at is populated.
}
```

- [ ] **Step 2: Implement**

```go
type EvaluatorPoolToken struct {
    ID          pgtype.UUID
    WorkspaceID pgtype.UUID
    TokenPrefix string
    DisplayName string
    CreatedBy   pgtype.UUID
    LastUsedAt  pgtype.Timestamptz
    RevokedAt   pgtype.Timestamptz
}

type EvaluatorPoolService struct{ q *db.Queries }

func NewEvaluatorPoolService(q *db.Queries) *EvaluatorPoolService { return &EvaluatorPoolService{q: q} }

// CreateInput: workspace_id, display_name, created_by
// Returns: persisted token + plain-text "evp_<48 random hex>" string (only returned this one time).
func (s *EvaluatorPoolService) Create(ctx context.Context, in CreateEvaluatorPoolTokenInput) (EvaluatorPoolToken, string, error) {
    // generate 24 random bytes; full = "evp_" + hex; prefix = full[:12]; hash = sha256(full)
    // INSERT via CreateEvaluatorPoolToken with token_prefix, token_hash, display_name, etc.
}

func (s *EvaluatorPoolService) List(ctx context.Context, workspaceID pgtype.UUID) ([]EvaluatorPoolToken, error)

func (s *EvaluatorPoolService) Revoke(ctx context.Context, id, workspaceID pgtype.UUID) error
// RevokeEvaluatorPoolToken returns nothing; we treat 0-row update as ErrTokenNotFound.

// Verify is the auth path: hash the bearer + lookup; reject if revoked; touch last_used.
func (s *EvaluatorPoolService) Verify(ctx context.Context, plainToken string) (EvaluatorPoolToken, error) {
    if !strings.HasPrefix(plainToken, "evp_") { return EvaluatorPoolToken{}, ErrEvaluatorPoolTokenInvalid }
    hash := sha256Hex(plainToken)
    row, err := s.q.GetEvaluatorPoolTokenByHash(ctx, hash)
    if errors.Is(err, pgx.ErrNoRows) { return EvaluatorPoolToken{}, ErrEvaluatorPoolTokenInvalid }
    if err != nil { return EvaluatorPoolToken{}, err }
    if row.RevokedAt.Valid { return EvaluatorPoolToken{}, ErrEvaluatorPoolTokenRevoked }
    _ = s.q.TouchEvaluatorPoolToken(ctx, row.ID) // best-effort
    return rowToToken(row), nil
}
```

Sentinels: `ErrEvaluatorPoolTokenInvalid`, `ErrEvaluatorPoolTokenRevoked`, `ErrEvaluatorPoolTokenNotFound`.

- [ ] **Step 3: Run tests, all 4 pass.**
- [ ] **Step 4: Commit `feat(benchmark): EvaluatorPoolService — mint/list/revoke/verify`.**

---

## Task 2: Evaluator-pool middleware

**Files:**
- `server/internal/middleware/evaluator_pool_auth.go`

- [ ] **Step 1: Implement.**

```go
type ctxKey int
const evaluatorTokenKey ctxKey = 0

func RequireEvaluatorPoolAuth(svc *benchmark.EvaluatorPoolService) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            authz := r.Header.Get("Authorization")
            if !strings.HasPrefix(authz, "Bearer ") {
                http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
                return
            }
            tok := strings.TrimPrefix(authz, "Bearer ")
            evp, err := svc.Verify(r.Context(), tok)
            if err != nil {
                http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
                return
            }
            ctx := context.WithValue(r.Context(), evaluatorTokenKey, evp)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

func EvaluatorTokenFromContext(ctx context.Context) (benchmark.EvaluatorPoolToken, bool) {
    v, ok := ctx.Value(evaluatorTokenKey).(benchmark.EvaluatorPoolToken)
    return v, ok
}
```

- [ ] **Step 2: Tests for unauth header / bogus token / revoked / valid.**
- [ ] **Step 3: Commit `feat(benchmark): evaluator-pool auth middleware`.**

---

## Task 3: EvalJobService (server-side)

**Files:**
- `server/internal/service/benchmark/eval_job_service.go`
- `server/internal/service/benchmark/eval_job_service_test.go`

This is the orchestration helper for the internal endpoints.

- [ ] **Step 1: Methods.**

```go
type EvalJobService struct {
    q *db.Queries; pool *pgxpool.Pool; bus Publisher
}

// Claim picks up to N pending jobs for the given adapter kinds, atomically marks them claimed.
// Returns each job + the task's submission attachment download URL (relative path).
func (s *EvalJobService) Claim(ctx context.Context, evaluatorID string, adapterKinds []string, max int32) ([]ClaimedJob, error)

type ClaimedJob struct {
    JobID            pgtype.UUID
    TaskID           pgtype.UUID
    InstanceID       string
    InstanceMeta     json.RawMessage
    AdapterKind      string
    AttachmentID     pgtype.UUID
    SubmissionDownloadURL string // e.g. "/api/attachments/<id>/download"
}

// Complete records the eval result, advances task → 'scored', marks job 'done'.
func (s *EvalJobService) Complete(ctx context.Context, in CompleteEvalJobInput) error

type CompleteEvalJobInput struct {
    JobID            pgtype.UUID
    Resolved         bool
    PassedTests      int
    TotalTests       int
    PassRate         float64
    RawEvalJSON      json.RawMessage
    FailedCategories []string
}

// Fail bumps attempt; if attempt < maxAttempts → state='pending' (returns to queue); else 'failed' + task → 'errored'.
func (s *EvalJobService) Fail(ctx context.Context, jobID pgtype.UUID, lastError string) error
```

Use sqlc `ClaimBenchmarkEvalJobs`, `CompleteBenchmarkEvalJob`, `FailBenchmarkEvalJob` from Phase 1a.

`Complete` runs in a single transaction: upsert eval_result → update task status → mark job done → publish `EventBenchmarkTaskScored`. (Same shape as `RunService.ImportEvalResult`.)

`Fail` runs `FailBenchmarkEvalJob` (which auto-handles attempt+1 → pending or failed), and if it returned `state='failed'`, updates task to 'errored' and publishes `EventBenchmarkTaskStatus`.

- [ ] **Step 2: Tests** — Claim picks N pending jobs, Complete advances task + creates result, Fail retries then errors out after maxAttempts.
- [ ] **Step 3: Commit `feat(benchmark): EvalJobService — claim/complete/fail`.**

---

## Task 4: HTTP — `/api/benchmarks/evaluator-tokens` admin endpoints

**Files:**
- `server/internal/handler/evaluator_pool.go`
- `server/internal/handler/evaluator_pool_test.go`

- [ ] **Step 1:** Add to existing `*BenchmarkHandler` (or new struct — match Phase 0/1a pattern). Methods:

```go
POST /api/benchmarks/evaluator-tokens   {display_name} → 201 {id, prefix, plaintext_token (only here!), display_name, created_at}
GET  /api/benchmarks/evaluator-tokens   → 200 {items: [...]} (no plaintext, no hash)
DELETE /api/benchmarks/evaluator-tokens/{id} → 204 / 404
```

- [ ] **Step 2: Tests.**
- [ ] **Step 3: Commit `feat(benchmark): HTTP routes for evaluator-pool token mgmt`.**

---

## Task 5: HTTP — `/api/internal/eval-jobs/*`

**Files:**
- `server/internal/handler/eval_jobs.go`
- `server/internal/handler/eval_jobs_test.go`

- [ ] **Step 1:** Methods on a small new `*EvalJobsHandler` struct:

```go
POST /api/internal/eval-jobs/claim      body {evaluator_id, adapter_kinds, max_concurrent} → [{job_id, task_id, instance_id, instance_meta, adapter_kind, submission_download_url}]
POST /api/internal/eval-jobs/{id}/complete  body {resolved, passed_tests, total_tests, pass_rate, raw_eval_json, failed_categories} → 204
POST /api/internal/eval-jobs/{id}/fail      body {last_error} → 204
```

All routes guarded by `RequireEvaluatorPoolAuth`. Workspace-scoping derived from the token's WorkspaceID via `EvaluatorTokenFromContext`. Reject claims/completes/fails for jobs not in the token's workspace with 403.

- [ ] **Step 2: Tests.**
- [ ] **Step 3: Commit `feat(benchmark): /api/internal/eval-jobs endpoints (claim/complete/fail)`.**

---

## Task 6: ProgramBench Evaluator impl

**Files:**
- `server/internal/service/benchmark/adapter/programbench_evaluator.go`

This is registered in the evaluator binary, NOT the server. Defining it in the same package keeps types together.

- [ ] **Step 1:** Implement.

```go
type ProgramBenchEvaluator struct {
    runArgs func(ctx context.Context, dir string, args ...string) ([]byte, error)
}

func NewProgramBenchEvaluator() *ProgramBenchEvaluator {
    return &ProgramBenchEvaluator{
        runArgs: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
            cctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
            defer cancel()
            cmd := exec.CommandContext(cctx, args[0], args[1:]...)
            cmd.Dir = dir
            var out bytes.Buffer
            cmd.Stdout = &out; cmd.Stderr = &out
            if err := cmd.Run(); err != nil {
                return out.Bytes(), fmt.Errorf("uvx eval: %w (output=%s)", err, strings.TrimSpace(out.String()))
            }
            return out.Bytes(), nil
        },
    }
}

func (e *ProgramBenchEvaluator) Kind() string { return "programbench" }

func (e *ProgramBenchEvaluator) Evaluate(ctx context.Context, in EvaluateInput) (EvaluateOutput, error) {
    // 1. Validate SubmissionPath exists and is named submission.tar.gz.
    // 2. Set up the run dir layout that programbench eval expects:
    //    workdir/run/<instance_id>/submission.tar.gz
    runDir := filepath.Join(in.WorkDir, "run", in.Task.InstanceID)
    if err := os.MkdirAll(runDir, 0o755); err != nil { return EvaluateOutput{}, err }
    if err := copyFile(in.SubmissionPath, filepath.Join(runDir, "submission.tar.gz")); err != nil { return ..., err }

    // 3. Run `uvx programbench eval <workdir>/run` (the path containing per-instance subdirs).
    if _, err := e.runArgs(ctx, in.WorkDir, "uvx", "programbench", "eval", "run"); err != nil {
        return EvaluateOutput{}, err
    }

    // 4. Read and parse <runDir>/<instance_id>.eval.json.
    raw, err := os.ReadFile(filepath.Join(runDir, in.Task.InstanceID + ".eval.json"))
    if err != nil { return EvaluateOutput{}, fmt.Errorf("read eval json: %w", err) }

    // 5. Parse: extract resolved/passed/total/categories per the ProgramBench eval shape.
    return parseProgramBenchEval(raw)
}

func parseProgramBenchEval(raw json.RawMessage) (EvaluateOutput, error) {
    // Adapter-specific JSON parsing. ProgramBench's eval JSON has tests[] entries
    // with pass/fail; aggregate; extract failure categories from test names.
    // For v1, a minimal correct parse is enough — refine as we test against real data.
}
```

- [ ] **Step 2:** Unit-test `parseProgramBenchEval` with a stubbed JSON sample. Don't test the real `Evaluate` end-to-end (requires Docker). Mark with `t.Skip` if `EVALUATOR_E2E` env var unset.
- [ ] **Step 3:** Commit `feat(benchmark): ProgramBench Evaluator impl`.

---

## Task 7: cmd/evaluator/ binary — main + client + runner

**Files:**
- `server/cmd/evaluator/main.go`
- `server/cmd/evaluator/client.go`
- `server/cmd/evaluator/runner.go`

- [ ] **Step 1: Client.** Small HTTPS client wrapping the 3 internal endpoints (claim/complete/fail).
- [ ] **Step 2: Runner.** Per-job:
  1. Create temp work dir.
  2. Download submission via the URL from the claim response.
  3. Resolve adapter via `registry.Evaluator(kind)`.
  4. Call `evaluator.Evaluate(ctx, ...)`.
  5. POST `/complete` with parsed result, OR `/fail` with last_error on failure.
  6. Cleanup work dir.
- [ ] **Step 3: Main.** Long-running claim loop:
  - Read flags / env: `MULTICA_SERVER_URL`, `MULTICA_EVALUATOR_TOKEN`, `MULTICA_EVALUATOR_ID`, `MULTICA_MAX_CONCURRENT`, `MULTICA_ADAPTER_KINDS`.
  - Build `*adapter.Registry` and register `NewProgramBenchEvaluator()`.
  - Tick every 5s: claim N jobs; for each spawn a goroutine running runner. Wait on a sema bounded by max-concurrent.
  - SIGTERM: stop claiming; wait for in-flight runners up to deadline.
- [ ] **Step 4:** `go build ./server/cmd/evaluator/` clean.
- [ ] **Step 5:** Skip end-to-end tests (require Docker + uvx); just lint.
- [ ] **Step 6:** Commit `feat(benchmark): multica-evaluator binary`.

---

## Task 8: Wire admin token routes + internal eval-job routes

**Files:**
- `server/cmd/server/router.go`

- [ ] **Step 1:** Inside the existing `r.Route("/api/benchmarks", ...)` block, add:

```go
r.Route("/evaluator-tokens", func(r chi.Router) {
    r.Get("/", evaluatorPoolHandler.List)
    r.Post("/", evaluatorPoolHandler.Create)
    r.Delete("/{id}", evaluatorPoolHandler.Revoke)
})
```

- [ ] **Step 2:** Mount internal routes at TOP-LEVEL (NOT under workspace middleware), guarded by RequireEvaluatorPoolAuth:

```go
r.Route("/api/internal/eval-jobs", func(r chi.Router) {
    r.Use(middleware.RequireEvaluatorPoolAuth(evalPoolService))
    r.Post("/claim", evalJobsHandler.Claim)
    r.Post("/{id}/complete", evalJobsHandler.Complete)
    r.Post("/{id}/fail", evalJobsHandler.Fail)
})
```

- [ ] **Step 3:** Construct `*EvaluatorPoolService`, `*EvalJobService`, `*EvaluatorPoolHandler`, `*EvalJobsHandler` in router.go alongside existing benchmark wiring.
- [ ] **Step 4:** `go build && go vet && go test ./...` clean.
- [ ] **Step 5:** Commit `feat(benchmark): mount evaluator-token + internal eval-job routes`.

---

## Task 9: Final pre-PR check + push to fork

- [ ] Tests green for benchmark + handler packages.
- [ ] gofmt + go vet clean.
- [ ] TODO/TBD/FIXME scan clean in new code.
- [ ] Push to fork.

---

## Helm chart (separate repo) — defer to a follow-up dispatch

The `multica-helm-chart` repo at `/Volumes/kingston/repo_v3/Ark-AI/multica-helm-chart/` is a separate Git repo. Adding the evaluator Deployment is its own task list and PR. Dispatch separately after Phase 1b core lands and the binary's image is buildable.

---

## Self-Review

**Spec coverage (1b):**
- ✅ Evaluator-pool token mgmt (mint/list/revoke + verify).
- ✅ Internal /api/internal/eval-jobs/* (claim/complete/fail).
- ✅ ProgramBench Evaluator impl (uvx programbench eval).
- ✅ multica-evaluator binary.
- ⏭ Helm chart deferred to separate dispatch.

**Caveats:** Tasks 6–7 (Evaluator + binary) cannot be exercised end-to-end without Docker + a populated ProgramBench cleanroom image. Tests cover unit-level parsing; full e2e is operator-side.

**Type consistency:** `EvaluatorPoolToken`, `ClaimedJob`, `CompleteEvalJobInput`, `EvaluateInput/Output` defined in earlier tasks and used consistently.
