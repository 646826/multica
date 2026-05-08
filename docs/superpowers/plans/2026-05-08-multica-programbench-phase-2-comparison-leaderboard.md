# Multica × ProgramBench Phase 2 — Comparison & Leaderboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Add comparison and leaderboard so operators can answer "did my agent change improve or regress?" — both at run level (compare run vs run) and suite level (rank profiles by best run).

**Architecture:** Two pure Go functions (`compare(base, cand)` and `leaderboard(suite)`) live in `service/benchmark/`. Each is exposed via one HTTP endpoint. Frontend adds Compare view (replaces the "Compare with" dropdown placeholder) and Leaderboard view (replaces the disabled tab from Phase 0). All semantics from spec Section 9.

**Reference spec:** `docs/superpowers/specs/2026-05-07-multica-programbench-integration-design.md` Section 9.

---

## File Structure

| Path | Purpose |
|---|---|
| `server/internal/service/benchmark/comparison.go` | Pure `compare(base, cand)` function |
| `server/internal/service/benchmark/comparison_test.go` | Unit tests |
| `server/internal/service/benchmark/leaderboard.go` | Pure `leaderboard(suite_slug)` function |
| `server/internal/service/benchmark/leaderboard_test.go` | Unit tests |
| `server/pkg/db/queries/benchmark_run.sql` (extend) | `ListCompleteRunsBySuite`, `ListProfilesAndBestRunForSuite` |
| `server/pkg/db/queries/benchmark_run_summary.sql` (extend) | `GetBenchmarkRunSummariesByIDs` |
| `server/internal/handler/benchmark.go` (extend) | 2 handlers |
| `server/cmd/server/router.go` (extend) | 2 new routes |
| `packages/core/types/benchmark.ts` (extend) | `ComparisonResult`, `LeaderboardEntry` types |
| `packages/core/api/client.ts` (extend) | 2 ApiClient methods |
| `packages/core/benchmarks/queries.ts` (extend) | 2 query options |
| `packages/views/benchmarks/RunCompare.tsx` | Compare view |
| `packages/views/benchmarks/Leaderboard.tsx` | Leaderboard view |
| `apps/web/app/.../benchmarks/runs/[runId]/compare/[baseId]/page.tsx` | Compare route |
| `apps/web/app/.../benchmarks/leaderboard/page.tsx` | Leaderboard route |
| `apps/web/app/.../benchmarks/layout.tsx` (modify) | Activate Leaderboard tab |
| `packages/views/locales/{en,zh-Hans}/benchmarks.json` (extend) | i18n |

---

## Task 1: sqlc — extend run + summary queries

**Files:**
- Modify `server/pkg/db/queries/benchmark_run.sql`
- Modify `server/pkg/db/queries/benchmark_run_summary.sql`

- [ ] **Step 1: benchmark_run.sql — replace existing `ListBenchmarkRunsBySuite` (it's complete-only) with a more flexible version. Actually keep that one but ADD a new query for the leaderboard:**

Append to `benchmark_run.sql`:

```sql
-- name: ListCompleteRunsBySuiteSlug :many
SELECT r.* FROM benchmark_run r
JOIN benchmark_suite s ON s.id = r.suite_id
WHERE r.workspace_id = $1
  AND s.slug = $2
  AND r.status = 'complete'
ORDER BY r.completed_at DESC
LIMIT $3;

-- name: GetBenchmarkSuiteByWorkspaceAndSlug :one
SELECT * FROM benchmark_suite WHERE workspace_id = $1 AND slug = $2;
```

(The 2nd query is needed by leaderboard handler to validate the slug + return the suite id.)

- [ ] **Step 2: benchmark_run_summary.sql — append**

```sql
-- name: ListBenchmarkRunSummariesByRunIDs :many
SELECT * FROM benchmark_run_summary WHERE run_id = ANY($1::uuid[]);
```

- [ ] **Step 3:** `cd server && sqlc generate`. Revert unrelated header churn. `go build ./...` clean.
- [ ] **Step 4:** Commit `feat(benchmark): sqlc queries for comparison and leaderboard`.

---

## Task 2: Comparison pure function + tests

**Files:**
- Create `server/internal/service/benchmark/comparison.go`
- Create `server/internal/service/benchmark/comparison_test.go`

- [ ] **Step 1:** Implement per spec Section 9 Comparison subsection:

```go
package benchmark

import (
    "encoding/json"
    "math"
    "sort"
)

const passRateEpsilon = 0.001

type ComparisonResult struct {
    BaseRunID  string `json:"base_run_id"`
    CandRunID  string `json:"cand_run_id"`
    Partial    bool   `json:"partial"`

    Delta struct {
        Resolved        int     `json:"resolved"`
        AvgPassRate     float64 `json:"avg_pass_rate"`
        AggPassRate     float64 `json:"agg_pass_rate"`
        Errored         int     `json:"errored"`
    } `json:"delta"`

    Improved      []ComparisonInstance `json:"improved"`
    Regressed     []ComparisonInstance `json:"regressed"`
    NewlyResolved []ComparisonInstance `json:"newly_resolved"`
    LostResolved  []ComparisonInstance `json:"lost_resolved"`

    Categories struct {
        Added   []string `json:"added"`
        Cleared []string `json:"cleared"`
    } `json:"categories"`

    MissingInBase []string `json:"missing_in_base,omitempty"`
    MissingInCand []string `json:"missing_in_cand,omitempty"`
}

type ComparisonInstance struct {
    InstanceID   string  `json:"instance_id"`
    BasePassRate float64 `json:"base_pass_rate"`
    CandPassRate float64 `json:"cand_pass_rate"`
}

type RunSummaryView struct {
    RunID             string
    ResolvedCount     int
    TotalCount        int
    ErroredCount      int
    AggregatePassRate float64
    AveragePassRate   float64
    FailureCategories []string
}

type EvalResultView struct {
    InstanceID  string
    Resolved    bool
    PassRate    float64
}

// Compare is a pure function: given two RunSummaryView and full eval-result lists
// (each indexed by instance_id), produce a ComparisonResult.
func Compare(
    base, cand RunSummaryView,
    baseResults, candResults map[string]EvalResultView,
) ComparisonResult {
    out := ComparisonResult{
        BaseRunID: base.RunID, CandRunID: cand.RunID,
    }
    out.Delta.Resolved = cand.ResolvedCount - base.ResolvedCount
    out.Delta.AvgPassRate = cand.AveragePassRate - base.AveragePassRate
    out.Delta.AggPassRate = cand.AggregatePassRate - base.AggregatePassRate
    out.Delta.Errored = cand.ErroredCount - base.ErroredCount

    // Build instance sets.
    baseSet, candSet := map[string]struct{}{}, map[string]struct{}{}
    for k := range baseResults { baseSet[k] = struct{}{} }
    for k := range candResults { candSet[k] = struct{}{} }

    shared := []string{}
    for k := range baseSet {
        if _, ok := candSet[k]; ok { shared = append(shared, k) }
    }
    sort.Strings(shared)

    for _, id := range shared {
        b := baseResults[id]
        c := candResults[id]
        diff := c.PassRate - b.PassRate
        switch {
        case diff > passRateEpsilon:
            out.Improved = append(out.Improved, ComparisonInstance{
                InstanceID: id, BasePassRate: b.PassRate, CandPassRate: c.PassRate,
            })
        case diff < -passRateEpsilon:
            out.Regressed = append(out.Regressed, ComparisonInstance{
                InstanceID: id, BasePassRate: b.PassRate, CandPassRate: c.PassRate,
            })
        }
        if !b.Resolved && c.Resolved {
            out.NewlyResolved = append(out.NewlyResolved, ComparisonInstance{
                InstanceID: id, BasePassRate: b.PassRate, CandPassRate: c.PassRate,
            })
        } else if b.Resolved && !c.Resolved {
            out.LostResolved = append(out.LostResolved, ComparisonInstance{
                InstanceID: id, BasePassRate: b.PassRate, CandPassRate: c.PassRate,
            })
        }
    }

    // Categories.
    out.Categories.Added = stringSetDiff(cand.FailureCategories, base.FailureCategories)
    out.Categories.Cleared = stringSetDiff(base.FailureCategories, cand.FailureCategories)

    // Partial flag + missing lists.
    for k := range baseSet {
        if _, ok := candSet[k]; !ok { out.MissingInCand = append(out.MissingInCand, k) }
    }
    for k := range candSet {
        if _, ok := baseSet[k]; !ok { out.MissingInBase = append(out.MissingInBase, k) }
    }
    sort.Strings(out.MissingInBase); sort.Strings(out.MissingInCand)
    out.Partial = len(out.MissingInBase) > 0 || len(out.MissingInCand) > 0
    return out
}

func stringSetDiff(a, b []string) []string {
    seen := map[string]struct{}{}; for _, x := range b { seen[x] = struct{}{} }
    out := []string{}
    for _, x := range a {
        if _, ok := seen[x]; !ok { out = append(out, x) }
    }
    sort.Strings(out)
    return out
}

// Helper: round to 5 decimal places (matches numeric(6,5) precision).
func round5(f float64) float64 { return math.Round(f*1e5) / 1e5 }

// Marshal helpers for endpoint use.
var _ = json.Marshal  // keep import if needed
```

- [ ] **Step 2: Tests:**
  - `TestCompare_AllShared_AllImproved`
  - `TestCompare_NewlyResolved`
  - `TestCompare_LostResolved`
  - `TestCompare_PartialDifferentInstances` — base and cand have non-overlapping instances → `partial: true`, `missing_in_*` populated
  - `TestCompare_FailureCategoriesAddedAndCleared`
- [ ] **Step 3:** `go test ./server/internal/service/benchmark/ -run TestCompare -v` → all pass.
- [ ] **Step 4:** Commit `feat(benchmark): pure compare function with tests`.

---

## Task 3: Leaderboard pure function + tests

**Files:**
- Create `server/internal/service/benchmark/leaderboard.go`
- Create `server/internal/service/benchmark/leaderboard_test.go`

- [ ] **Step 1:** Implement per spec Section 9 Leaderboard subsection:

```go
type LeaderboardRow struct {
    Rank              int     `json:"rank"`
    ProfileID         string  `json:"profile_id"`
    ProfileSlug       string  `json:"profile_slug"`
    ProfileDisplayName string `json:"profile_display_name"`
    BestRunID         string  `json:"best_run_id"`
    BestRunDisplayName string `json:"best_run_display_name"`
    ResolvedCount     int     `json:"resolved_count"`
    TotalCount        int     `json:"total_count"`
    AveragePassRate   float64 `json:"average_pass_rate"`
    AggregatePassRate float64 `json:"aggregate_pass_rate"`
    ErroredCount      int     `json:"errored_count"`
    CompletedAt       string  `json:"completed_at"`
}

type LeaderboardInput struct {
    SuiteID   string
    SuiteSlug string
    Runs      []LeaderboardRunRow // each carries profile + summary fields
}

type LeaderboardRunRow struct {
    RunID              string
    RunDisplayName     string
    ProfileID          string
    ProfileSlug        string
    ProfileDisplayName string
    ResolvedCount      int
    TotalCount         int
    AveragePassRate    float64
    AggregatePassRate  float64
    ErroredCount       int
    CompletedAt        string // RFC3339
}

func Leaderboard(in LeaderboardInput) []LeaderboardRow {
    // Group by ProfileID; for each group, pick best by (resolved DESC, avg_pr DESC, -errored, -completed_at ASC).
    groups := map[string][]LeaderboardRunRow{}
    for _, r := range in.Runs { groups[r.ProfileID] = append(groups[r.ProfileID], r) }

    bestPerProfile := []LeaderboardRunRow{}
    for _, runs := range groups {
        sort.Slice(runs, func(i, j int) bool { return betterRun(runs[i], runs[j]) })
        bestPerProfile = append(bestPerProfile, runs[0])
    }
    sort.Slice(bestPerProfile, func(i, j int) bool { return betterRun(bestPerProfile[i], bestPerProfile[j]) })

    out := make([]LeaderboardRow, 0, len(bestPerProfile))
    rank := 0
    var prev *LeaderboardRunRow
    for i, r := range bestPerProfile {
        if i == 0 || !sameTier(r, *prev) { rank = i + 1 }
        out = append(out, LeaderboardRow{
            Rank: rank, ProfileID: r.ProfileID, ProfileSlug: r.ProfileSlug,
            ProfileDisplayName: r.ProfileDisplayName,
            BestRunID: r.RunID, BestRunDisplayName: r.RunDisplayName,
            ResolvedCount: r.ResolvedCount, TotalCount: r.TotalCount,
            AveragePassRate: r.AveragePassRate, AggregatePassRate: r.AggregatePassRate,
            ErroredCount: r.ErroredCount, CompletedAt: r.CompletedAt,
        })
        rr := r; prev = &rr
    }
    return out
}

// betterRun returns true if a is better than b per the spec's ordering.
func betterRun(a, b LeaderboardRunRow) bool {
    if a.ResolvedCount != b.ResolvedCount { return a.ResolvedCount > b.ResolvedCount }
    if a.AveragePassRate != b.AveragePassRate { return a.AveragePassRate > b.AveragePassRate }
    if a.AggregatePassRate != b.AggregatePassRate { return a.AggregatePassRate > b.AggregatePassRate }
    if a.ErroredCount != b.ErroredCount { return a.ErroredCount < b.ErroredCount }
    return a.CompletedAt < b.CompletedAt // earlier wins ties
}

func sameTier(a, b LeaderboardRunRow) bool {
    return a.ResolvedCount == b.ResolvedCount &&
           a.AveragePassRate == b.AveragePassRate &&
           a.AggregatePassRate == b.AggregatePassRate
}
```

- [ ] **Step 2: Tests:**
  - `TestLeaderboard_RanksByResolvedThenAvgPR`
  - `TestLeaderboard_BestRunPerProfile` — same profile with 2 runs → only the better one appears, ranked correctly
  - `TestLeaderboard_DenseRankOnTies`
  - `TestLeaderboard_EmptyInput_EmptyOutput`
- [ ] **Step 3:** Commit `feat(benchmark): pure leaderboard function with tests`.

---

## Task 4: Service-layer wrappers (DB → pure function)

**Files:**
- Modify `server/internal/service/benchmark/run_service.go` (or add `run_service_compare.go`)

- [ ] **Step 1:** Add methods to `*RunService`:

```go
// CompareRuns fetches both summaries + eval results, calls Compare(), returns the result.
func (s *RunService) CompareRuns(ctx context.Context, baseID, candID, workspaceID pgtype.UUID) (ComparisonResult, error)

// LeaderboardForSuite fetches all complete runs for a suite slug, calls Leaderboard().
func (s *RunService) LeaderboardForSuite(ctx context.Context, workspaceID pgtype.UUID, suiteSlug string) ([]LeaderboardRow, error)
```

`CompareRuns`:
1. GetBenchmarkRun for both ids (workspace-scoped). Both must be `status='complete'`; otherwise `ErrRunNotComplete`.
2. GetBenchmarkRunSummary for both.
3. ListBenchmarkEvalResultsForRun for both → build `map[instance_id]EvalResultView`.
4. Call Compare(); return.

`LeaderboardForSuite`:
1. GetBenchmarkSuiteByWorkspaceAndSlug → ErrSuiteResolution if not found.
2. ListCompleteRunsBySuiteSlug.
3. ListBenchmarkRunSummariesByRunIDs (batch lookup).
4. For each run, look up its profile via GetBenchmarkProfile; attach profile_slug/display_name.
5. Build LeaderboardRunRow slice; call Leaderboard(); return.

- [ ] **Step 2: Tests** — integration tests against real DB seeding 2-3 runs of varying outcomes.
- [ ] **Step 3:** Commit `feat(benchmark): RunService.CompareRuns + LeaderboardForSuite`.

---

## Task 5: HTTP — comparison + leaderboard endpoints

**Files:**
- Modify `server/internal/handler/benchmark.go`
- Modify `server/internal/handler/benchmark_test.go`

- [ ] **Step 1:** Two new methods on `*BenchmarkHandler`:

```go
// GET /api/benchmarks/runs/{id}/compare?base=<base-id>
// 200 ComparisonResult
// 404 if either run missing/not complete
func (h *BenchmarkHandler) CompareRun(w http.ResponseWriter, r *http.Request) {
    workspaceID, _, ok := h.resolveBenchmarkContext(w, r)
    if !ok { return }
    candID, ok := parseBenchmarkURLID(w, chi.URLParam(r, "id"))
    if !ok { return }
    baseStr := r.URL.Query().Get("base")
    if baseStr == "" { writeError(w, http.StatusBadRequest, "base_required"); return }
    baseID, ok := parseBenchmarkURLID(w, baseStr)
    if !ok { return }
    out, err := h.deps.Runs.CompareRuns(r.Context(), baseID, candID, workspaceID)
    // map errors → 404 / 400 / 500
    writeJSON(w, http.StatusOK, out)
}

// GET /api/benchmarks/leaderboard?suite=<suite-slug>
// 200 {items: [LeaderboardRow]}
// 404 if suite missing
func (h *BenchmarkHandler) Leaderboard(w http.ResponseWriter, r *http.Request) { ... }
```

- [ ] **Step 2:** Tests — happy path + 404 missing run + 400 missing base + leaderboard with 2 profiles.
- [ ] **Step 3:** Commit `feat(benchmark): HTTP routes for compare + leaderboard`.

---

## Task 6: Wire routes

**Files:**
- Modify `server/cmd/server/router.go`

- [ ] **Step 1:** Add to existing `r.Route("/api/benchmarks", ...)` block:

```go
r.Route("/runs/{id}", func(r chi.Router) {
    // existing get/cancel routes...
    r.Get("/compare", benchmarkHandler.CompareRun)
})
r.Get("/leaderboard", benchmarkHandler.Leaderboard)
```

(Take care to integrate with existing `/runs/{id}` block — ADD `compare` to that, not duplicate.)

- [ ] **Step 2:** Build + tests pass.
- [ ] **Step 3:** Commit `feat(benchmark): mount compare + leaderboard routes`.

---

## Task 7: Frontend types + ApiClient

**Files:**
- Modify `packages/core/types/benchmark.ts`
- Modify `packages/core/types/index.ts`
- Modify `packages/core/api/client.ts`
- Modify `packages/core/benchmarks/queries.ts`

- [ ] **Step 1:** Add types `ComparisonResult`, `ComparisonInstance`, `LeaderboardRow`, `ListLeaderboardResponse` matching server JSON.
- [ ] **Step 2:** Add ApiClient methods `compareBenchmarkRun(candID, baseID)`, `getBenchmarkLeaderboard(suiteSlug)`.
- [ ] **Step 3:** Add query options `benchmarkCompareOptions`, `benchmarkLeaderboardOptions`.
- [ ] **Step 4:** Commit `feat(benchmark): frontend types/api/queries for compare + leaderboard`.

---

## Task 8: Compare view

**Files:**
- Create `packages/views/benchmarks/RunCompare.tsx`
- Create `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/runs/[runId]/compare/[baseId]/page.tsx`
- Modify `packages/views/benchmarks/RunDetail.tsx` — add "Compare with…" dropdown (queries other complete runs of same suite, links to compare route)
- Modify `packages/views/benchmarks/index.ts`

- [ ] **Step 1:** RunCompare component:
  - Header with both run display names + "← Back to <cand>".
  - Top summary table:
    | | base | candidate | Δ |
    |---|---|---|---|
    | resolved | 12/30 | 14/30 | +2 |
    | avg pass-rate | 0.612 | 0.683 | +0.071 |
    | aggregate pass-rate | … | … | … |
    | errored | 1 | 0 | -1 |
  - 4 sections: Improved (N), Regressed (N), Newly resolved (N), Lost resolved (N) — each with instance + base→cand pass-rate arrows.
  - Failure categories: Added / Cleared.
  - Partial banner if `partial=true`.

- [ ] **Step 2:** Update RunDetail to add a "Compare with" dropdown showing other complete runs of the same suite. On select → navigate to compare route.

- [ ] **Step 3:** Commit `feat(benchmark): compare view + run-detail compare picker`.

---

## Task 9: Leaderboard view

**Files:**
- Create `packages/views/benchmarks/Leaderboard.tsx`
- Create `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/leaderboard/page.tsx`
- Modify `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/layout.tsx` — un-disable Leaderboard tab
- Modify `packages/views/benchmarks/index.ts`

- [ ] **Step 1:** Leaderboard component:
  - Suite picker (Select populated from suites list).
  - On suite select: query leaderboard.
  - Table: Rank, Profile (display_name), Best run (link), Resolved, Avg pass-rate, Agg pass-rate, Last completed.
  - Empty state if no complete runs for suite.

- [ ] **Step 2:** Activate Leaderboard tab.
- [ ] **Step 3:** Commit `feat(benchmark): leaderboard view + activate tab`.

---

## Task 10: i18n + final check + push

**Files:**
- Modify `packages/views/locales/{en,zh-Hans}/benchmarks.json` — add `compare`, `leaderboard` sections
- All new view files use `t(...)` from the start (no eslint-disable needed)

- [ ] **Step 1:** i18n keys.
- [ ] **Step 2:** Parity test passes.
- [ ] **Step 3:** Final pre-PR check: tests, lint, typecheck, push to fork.
- [ ] **Step 4:** Commit `feat(benchmark): i18n for compare + leaderboard` (if not done in T8/T9).

---

## Self-Review

**Spec coverage (Phase 2 in spec):**
- ✅ Comparison & leaderboard via Section 9 semantics.
- ✅ Endpoints + UI views.
- ✅ Activates Leaderboard tab.
- ✅ Wires Compare with dropdown in run detail.

**Out of scope:** WS-driven live updates of leaderboard / comparison (refresh triggers manually via React Query staleness).

**Type consistency:** ComparisonResult / LeaderboardRow / RunSummaryView / EvalResultView used consistently across tasks.
