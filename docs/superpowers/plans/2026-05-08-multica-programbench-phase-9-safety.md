# Multica × ProgramBench Phase 9 — Safety & Multi-tenancy Implementation Plan

**Goal:** Two production-safety items: per-task advisory lock for multi-replica TaskDispatcher correctness, and workspace-scoped filtering on EvalJobService.Claim so multi-workspace deployments don't cross-leak benchmark eval jobs.

---

## Tasks

### T01 — Per-task advisory lock in TaskDispatcher

**Files:** `server/internal/service/benchmark/task_dispatcher.go`

In the per-task issue-creation block, take a tx-scoped advisory lock keyed on `benchmark_task:<task.id>`. Skip silently if not acquired (another replica owns this task this tick).

```go
var locked bool
if err := tx.QueryRow(ctx,
    "SELECT pg_try_advisory_xact_lock(hashtext($1))",
    "benchmark_task:"+util.UUIDToString(task.ID),
).Scan(&locked); err != nil { return err }
if !locked { continue }
```

Test: spawn two TaskDispatcher.Tick concurrently against the same workspace; assert no double-issue.

Commit: `fix(benchmark): per-task advisory lock for multi-replica TaskDispatcher safety`.

### T02 — Workspace-scoped EvalJobService.Claim

**Files:** `server/internal/service/benchmark/eval_job_service.go`, `server/pkg/db/queries/benchmark_eval_job.sql`, evaluator binary client + handler

Currently `EvalJobService.Claim(evaluatorID, adapterKinds, max)` doesn't filter by workspace. The evaluator-pool token IS workspace-scoped (Phase 1b T01), so the handler has the workspace id in ctx. Pass it down.

Steps:
1. Add `workspace_id = $1` filter to the sqlc claim query.
2. Update service signature: `Claim(ctx, workspaceID, evaluatorID, adapterKinds, max)`.
3. Handler reads `workspaceID` from `EvaluatorTokenFromContext`.

```sql
-- name: ClaimBenchmarkEvalJobs :many
WITH claimed AS (
    SELECT id FROM benchmark_eval_job
    WHERE state = 'pending'
      AND workspace_id = $1                  -- NEW
      AND adapter_kind = ANY($2::text[])
    ORDER BY enqueued_at ASC
    LIMIT $3
    FOR UPDATE SKIP LOCKED
)
UPDATE benchmark_eval_job
SET state = 'claimed', claimed_by = $4, claimed_at = now()
WHERE id IN (SELECT id FROM claimed)
RETURNING *;
```

Tests: claim from one workspace shouldn't return jobs of another workspace.

Commit: `fix(benchmark): EvalJobService.Claim filters by token's workspace`.

### T03 — Final check + push

Standard. Push to fork. Don't push to upstream.

---

## Self-Review

Both items are pure correctness: T01 fixes a multi-replica race, T02 fixes a multi-tenant leak. Together they make the feature production-safe.
