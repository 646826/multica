# Multica × ProgramBench Phase 3 — Polish & Docs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Fill Phase 1c UI gaps (per-task results, summary card in run detail), add a "Sync from catalog" affordance for suites, and write the operator-facing documentation. After Phase 3, the feature is complete per the original spec.

---

## File Structure

| Path | Purpose |
|---|---|
| `server/pkg/db/queries/benchmark_task.sql` (already has ListBenchmarkTasksByRun) | sqlc — no new queries needed |
| `server/internal/service/benchmark/run_service.go` (extend) | `ListTasksForRun`, `GetRunSummary` (already may exist) |
| `server/internal/handler/benchmark.go` (extend) | 2 new handlers + 1 catalog-sync handler |
| `server/cmd/server/router.go` (extend) | Mount routes |
| `packages/core/types/benchmark.ts` (extend) | `BenchmarkTask`, `BenchmarkRunSummary`, `BenchmarkEvalResult` types |
| `packages/core/api/client.ts` (extend) | API methods |
| `packages/core/benchmarks/queries.ts` (extend) | Query options |
| `packages/views/benchmarks/RunDetail.tsx` (modify) | Real task table + summary card replacing placeholders |
| `packages/views/benchmarks/SuiteDetail.tsx` (modify) | "Sync from catalog" button |
| `packages/views/locales/{en,zh-Hans}/benchmarks.json` (extend) | i18n |
| `apps/docs/content/docs/operator/benchmarks-getting-started.mdx` | Operator guide |
| `apps/docs/content/docs/operator/benchmarks-evaluator-deploy.mdx` | Deploy guide |
| `apps/docs/content/docs/developers/benchmarks-adapter.mdx` | Adapter dev guide |

---

## Task 1: Server — list tasks + get summary endpoints

**Files:**
- Modify `server/internal/service/benchmark/run_service.go`
- Modify `server/internal/handler/benchmark.go`
- Modify `server/cmd/server/router.go`

- [ ] **Step 1:** Add to RunService (likely both methods need adding):

```go
type RunTaskView struct {
    ID            string
    InstanceID    string
    Status        string
    StatusReason  string
    IssueID       string
    Resolved      bool
    PassedTests   int
    TotalTests    int
    PassRate      float64
    FailedCategories []string
}

func (s *RunService) ListTasksForRun(ctx context.Context, runID, workspaceID pgtype.UUID) ([]RunTaskView, error)
func (s *RunService) GetRunSummary(ctx context.Context, runID, workspaceID pgtype.UUID) (*BenchmarkRunSummaryView, error)
```

`ListTasksForRun`:
1. Verify run exists in workspace via GetBenchmarkRun.
2. ListBenchmarkTasksByRun → tasks.
3. ListBenchmarkEvalResultsForRun → eval results, indexed by task_id.
4. Build joined RunTaskView slice; missing eval results → zeroed pass-rate/resolved=false.

`GetRunSummary`:
1. Verify run exists (workspace-scoped).
2. GetBenchmarkRunSummary; if no row → return nil (run not finalized yet).
3. Map to view type.

```go
type BenchmarkRunSummaryView struct {
    RunID             string
    ResolvedCount     int
    TotalCount        int
    AggregatePassRate float64
    AveragePassRate   float64
    ErroredCount      int
    FailureCategories []FailureCategoryView // {name, count}
}

type FailureCategoryView struct {
    Name  string `json:"name"`
    Count int    `json:"count"`
}
```

- [ ] **Step 2:** Handlers:

```go
// GET /api/benchmarks/runs/{id}/tasks
func (h *BenchmarkHandler) ListRunTasks(w http.ResponseWriter, r *http.Request)

// GET /api/benchmarks/runs/{id}/summary
func (h *BenchmarkHandler) GetRunSummary(w http.ResponseWriter, r *http.Request)
```

Both workspace-scoped. ListRunTasks returns `{items: []RunTaskResponse}`. GetRunSummary returns 404 if no summary yet.

- [ ] **Step 3:** Tests for both service methods + both handlers (real DB).
- [ ] **Step 4:** Wire routes in router.go inside `r.Route("/runs/{id}", ...)`:
  ```go
  r.Get("/tasks", benchmarkHandler.ListRunTasks)
  r.Get("/summary", benchmarkHandler.GetRunSummary)
  ```
- [ ] **Step 5:** Commit `feat(benchmark): list tasks + get summary endpoints`.

---

## Task 2: Frontend types + queries for tasks + summary

**Files:**
- Modify `packages/core/types/benchmark.ts`
- Modify `packages/core/types/index.ts`
- Modify `packages/core/api/client.ts`
- Modify `packages/core/benchmarks/queries.ts`

- [ ] **Step 1:** Add types `BenchmarkRunTask`, `BenchmarkRunSummary`, `FailureCategory`, `ListBenchmarkRunTasksResponse`.
- [ ] **Step 2:** Add ApiClient methods `listBenchmarkRunTasks(runID)`, `getBenchmarkRunSummary(runID)`.
- [ ] **Step 3:** Add query options `benchmarkRunTasksOptions`, `benchmarkRunSummaryOptions`.
- [ ] **Step 4:** Commit `feat(benchmark): types and queries for run tasks + summary`.

---

## Task 3: RunDetail — real task table + summary card

**Files:**
- Modify `packages/views/benchmarks/RunDetail.tsx`
- Modify `packages/views/locales/en/benchmarks.json` + `zh-Hans/benchmarks.json` (extend run_detail keys)

- [ ] **Step 1:** Replace tasks placeholder with a virtualized table (or plain table) rendering RunTaskView rows. Columns: Instance ID, Status, Pass rate, "Open issue" link (if issue_id set).
- [ ] **Step 2:** Replace summary placeholder with a Card showing: Resolved (X/Y), Average pass-rate, Aggregate pass-rate, Errored count, Top failure categories list.
- [ ] **Step 3:** i18n new keys.
- [ ] **Step 4:** Commit `feat(benchmark): real task table and summary card in run detail`.

---

## Task 4: SuiteDetail — Sync from catalog

**Files:**
- Modify `server/internal/handler/benchmark.go` — add `POST /api/benchmarks/suites/{id}/sync`
- Modify `server/internal/service/benchmark/suite_service.go` — add `SyncFromCatalog`
- Modify `packages/views/benchmarks/SuiteDetail.tsx` — add "Sync from catalog" button
- Add i18n keys

- [ ] **Step 1:** Server-side: `POST /api/benchmarks/suites/{id}/sync`:
  - Get suite.
  - For each instance_id in suite.instance_ids, call `Catalog.Resolve()`. Returns resolution status per instance.
  - Returns `{added: 0, removed: 0, resolved: N}` summary. (No mutation in v1 — just informational; full suite-edit-from-catalog deferred.)
- [ ] **Step 2:** Frontend: Sync button on SuiteDetail. On click → POST → toast / inline result.
- [ ] **Step 3:** Tests.
- [ ] **Step 4:** Commit `feat(benchmark): suite sync-from-catalog endpoint and UI`.

---

## Task 5: Documentation

**Files:**
- Create `apps/docs/content/docs/operator/benchmarks-getting-started.mdx`
- Create `apps/docs/content/docs/operator/benchmarks-evaluator-deploy.mdx`
- Create `apps/docs/content/docs/developers/benchmarks-adapter.mdx`
- Modify `apps/docs/content/docs/meta.json` or whatever the docs index is — add new pages

- [ ] **Step 1: Getting-started guide.** Cover:
  - What benchmarks do, when to use them.
  - Create your first suite (via UI).
  - Capture an agent profile.
  - Start a run, observe progress.
  - Imported vs managed mode (one paragraph each).
  - Compare runs.
  - Leaderboard.
  - Troubleshooting common issues.

- [ ] **Step 2: Evaluator deploy guide.** Cover:
  - When you need it (managed mode).
  - Helm chart values (point at multica-helm-chart for the config block — Helm chart itself is a separate repo's task).
  - Mint a workspace evaluator-pool token via the UI.
  - Set token + server URL env vars in the pod.
  - Validate by checking /api/internal/eval-jobs/claim returns 200.

- [ ] **Step 3: Adapter developer guide.** Cover:
  - The 4 interfaces (Catalog, IssueComposer, SubmissionParser, Evaluator).
  - Where to register (server registry vs evaluator binary).
  - ProgramBench impl as a reference.
  - Testing strategy (parser unit tests, integration with a stubbed catalog).

- [ ] **Step 4:** Add to docs index.
- [ ] **Step 5:** Commit `docs(benchmark): operator + developer guides`.

---

## Task 6: Final check + push

- [ ] All tests green.
- [ ] gofmt + vet + typecheck + lint clean.
- [ ] TODO scan clean.
- [ ] Push to fork.

---

## Self-Review

**Spec coverage (Phase 3):**
- ✅ Imported-eval flow polished (already in Phase 1a; Task 1 surfaces results in UI).
- ✅ Sync from catalog (basic informational version).
- ✅ Failure-category display in summary.
- ✅ Documentation: 3 guides.
- ⏭ Helm chart evaluator Deployment lives in a separate repo (`multica-helm-chart/`); a follow-up dispatch can add it. Documenting the values shape in the deploy guide is enough for v1.

After Phase 3 the spec's Phase 0–3 surface is complete and shipped to fork.
