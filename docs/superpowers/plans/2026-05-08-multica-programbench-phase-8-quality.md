# Multica × ProgramBench Phase 8 — Quality Wins Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Knock out the most impactful remaining backlog: real ADO PR diff fetching (the P7 stub), optimistic delete mutations, and workspace-level benchmark concurrency cap.

---

## Tasks

### T01 — Real ADO PR diff fetching

**Files:**
- `server/internal/service/benchmark/reference_fetcher.go` — replace ADO stub with real impl

ADO REST flow:
1. Parse URL: extract `org`, `project`, `repo`, `prID`.
2. GET `https://dev.azure.com/<org>/<project>/_apis/git/repositories/<repo>/pullrequests/<id>?api-version=7.0` with Basic auth `:<pat>`. Returns JSON with `lastMergeSourceCommit.commitId` and `lastMergeTargetCommit.commitId`.
3. GET `https://dev.azure.com/<org>/<project>/_apis/git/repositories/<repo>/diffs/commits?baseVersion=<targetCommit>&baseVersionType=commit&targetVersion=<sourceCommit>&targetVersionType=commit&api-version=7.0`. Returns paths changed, but NOT the unified diff text (ADO doesn't expose unified diff directly).
4. **Fallback**: GET each changed file's content at both commits via `/items?path=<>&versionDescriptor.version=<commit>&api-version=7.0`, then compute diff client-side using `text/diffmatchpatch` style.

The full implementation is involved; for v1, a pragmatic alternative: ADO has an undocumented "iterations.diffs" endpoint that returns a JSON structure with line-level changes, but it's still not unified-diff text. Easiest path:

**Pragmatic v1**: GET a flat list of changed files via the diffs endpoint, then for each file `GET /items` at both commits, call out to local `diff -u` to produce unified diff text.

Even simpler **v1 MVP**: only support PRs whose source repository is also accessible via `git clone`. We could use a Git library to clone the bare repo + diff. But that's heavy.

**Compromise**: Implement just enough ADO API access to fetch the JSON description of changes, and emit a proto-diff (file-level summary) labeled clearly. Operators can use this as a starting point but should still paste a real unified diff for accuracy.

```go
func (f *ReferenceFetcher) fetchAzureDevOpsPR(ctx context.Context, u *url.URL) (FetchedPatch, error) {
    if f.adoToken == "" {
        return FetchedPatch{}, fmt.Errorf("%w: ADO token not configured (set MULTICA_ADO_PAT)", ErrReferenceFetchFailed)
    }
    parts := parseADOURL(u)
    if parts == nil {
        return FetchedPatch{}, fmt.Errorf("%w: cannot parse ADO PR URL", ErrUnsupportedReferenceURL)
    }
    pr, err := f.adoGetPR(ctx, parts)
    if err != nil { return FetchedPatch{}, err }

    diffs, err := f.adoGetCommitDiffs(ctx, parts, pr.targetCommit, pr.sourceCommit)
    if err != nil { return FetchedPatch{}, err }

    return FetchedPatch{
        Patch:     buildSummaryPatch(diffs, pr),
        SourceURL: u.String(),
    }, nil
}

// buildSummaryPatch produces a unified-diff-shaped summary of the PR changes.
// Each changed file gets:
//   diff --git a/<path> b/<path>
//   index 0000000..0000000 100644
//   --- a/<path>     (or /dev/null if added)
//   +++ b/<path>     (or /dev/null if deleted)
//   @@ <ado_change_summary> @@
//
// This is NOT byte-equivalent to a real unified diff. It signals "what changed"
// for similarity scoring but is not directly applyable. Operators are advised
// to paste a real diff for accurate replay-benchmark grading.
```

Tests: stub the ADO HTTP flow with httptest server returning canned JSON; verify the produced patch text contains the expected file paths.

Commit: `feat(benchmark): real ADO PR diff fetching (file-summary level)`.

### T02 — Optimistic deletes for suite + profile + run

**Files:**
- `packages/core/benchmarks/mutations.ts`

Currently `useDeleteBenchmarkSuite`, `useDeleteBenchmarkProfile`, `useCancelBenchmarkRun` only invalidate-on-success — UI shows row until refetch. Add `onMutate` rollback patterns:

```ts
export function useDeleteBenchmarkSuite() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteBenchmarkSuite(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: benchmarkKeys.suites(wsId) });
      const prev = qc.getQueryData<BenchmarkSuite[]>(benchmarkKeys.suites(wsId));
      qc.setQueryData<BenchmarkSuite[]>(
        benchmarkKeys.suites(wsId),
        (old) => old?.filter(s => s.id !== id) ?? [],
      );
      return { prev };
    },
    onError: (_e, _id, ctx) => {
      if (ctx?.prev) qc.setQueryData(benchmarkKeys.suites(wsId), ctx.prev);
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: benchmarkKeys.suites(wsId) });
    },
  });
}
```

Mirror for delete-profile.

For cancel-run, optimistic = flip status to "canceled" in cache:
```ts
qc.setQueryData(benchmarkKeys.runs(wsId), (old: BenchmarkRun[]) =>
  old?.map(r => r.id === id ? {...r, status: "canceled"} : r) ?? []
);
```

Commit: `feat(benchmark): optimistic delete + cancel mutations`.

### T03 — Workspace-level benchmark concurrency cap

**Files:**
- `server/internal/service/benchmark/task_dispatcher.go` — read cap from env or workspace settings
- For v1 MVP: read from env `MULTICA_BENCHMARK_MAX_PARALLEL` (default 4).

```go
type TaskDispatcher struct {
    // ... existing fields
    maxParallel int
}

func NewTaskDispatcher(...) *TaskDispatcher {
    // ...
    cap := 4
    if v := os.Getenv("MULTICA_BENCHMARK_MAX_PARALLEL"); v != "" {
        if n, err := strconv.Atoi(v); err == nil && n > 0 { cap = n }
    }
    return &TaskDispatcher{..., maxParallel: cap}
}
```

In the per-run dispatch loop, count currently `issued` benchmark tasks across the workspace; if at cap, skip creating more. Use existing sqlc query `CountActiveTasksForRun` at the run level + a new workspace-level query.

Add sqlc query:
```sql
-- name: CountActiveBenchmarkTasksByWorkspace :one
SELECT COUNT(*) FROM benchmark_task
WHERE workspace_id = $1 AND status IN ('issued', 'submitted', 'evaluating');
```

Generate, build, test.

Commit: `feat(benchmark): workspace-level concurrency cap for benchmark issues`.

### T04 — Final check + push

Standard pattern.

---

## Self-Review

**Scope:**
- ✅ Real ADO fetching (file-summary level — operators get a starting point even if not byte-perfect).
- ✅ Optimistic UI for delete + cancel.
- ✅ Server-side back-pressure on benchmark task creation.
- ⏭ Multi-replica advisory locks (TaskDispatcher per-task) — defer; current single-leader pattern is OK for typical deploys.
- ⏭ Cross-workspace evaluator filtering — defer.
- ⏭ Test-execution-based eval — defer (separate large project).
