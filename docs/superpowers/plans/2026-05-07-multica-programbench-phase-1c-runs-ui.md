# Multica × ProgramBench Phase 1c — Runs UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Activate the disabled "Runs" tab from Phase 0; deliver list + detail views and a start-run wizard so operators can drive benchmark runs from the web UI.

**Architecture:** Mirrors Phase 0 frontend patterns (`packages/views/benchmarks/<Component>.tsx` + `apps/web/...benchmarks/<route>/page.tsx` re-exports). New ApiClient methods + TanStack Query hooks for runs. Wizard is a 3-step drawer. WS live updates deferred to future iteration unless trivial.

**Reference precedent:** Phase 0 `SuitesList.tsx` / `SuiteCreate.tsx` / `SuiteDetail.tsx`. Phase 1a server-side ran handlers expose `/api/benchmarks/runs/*` already.

---

## File Structure

| Path | Purpose |
|---|---|
| `packages/core/types/benchmark.ts` (extend) | `BenchmarkRun`, `StartRunRequest`, `RunStatus`, `RunSummary`, list-response types |
| `packages/core/api/client.ts` (extend) | `listBenchmarkRuns`, `getBenchmarkRun`, `startBenchmarkRun`, `cancelBenchmarkRun` |
| `packages/core/benchmarks/queries.ts` (extend) | `benchmarkRunListOptions`, `benchmarkRunDetailOptions` |
| `packages/core/benchmarks/mutations.ts` (extend) | `useStartBenchmarkRun`, `useCancelBenchmarkRun` |
| `packages/views/benchmarks/RunsList.tsx` | List view with status pills + filters |
| `packages/views/benchmarks/RunDetail.tsx` | Header + summary + tasks table |
| `packages/views/benchmarks/StartRunWizard.tsx` | 3-step drawer (suite → profile → optional baseline) |
| `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/runs/page.tsx` | Re-export RunsList |
| `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/runs/new/page.tsx` | Re-export StartRunWizard (or trigger from list) |
| `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/runs/[runId]/page.tsx` | Re-export RunDetail |
| `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/layout.tsx` (modify) | Activate Runs tab (remove disabled) |
| `packages/core/paths/paths.ts` (extend) | `benchmarkRuns()`, `benchmarkRunDetail()` |
| `packages/views/locales/en/benchmarks.json` (extend) | New keys for runs section |
| `packages/views/locales/zh-Hans/benchmarks.json` (extend) | Mirror keys |

---

## Task 1: Extend frontend types + ApiClient methods

**Files:**
- Modify `packages/core/types/benchmark.ts`
- Modify `packages/core/types/index.ts` (re-exports)
- Modify `packages/core/api/client.ts`

- [ ] **Step 1: Add types** mirroring server DTOs `RunResponse` / list response.

```ts
export type RunStatus =
  | "queued" | "submitting" | "evaluating" | "complete"
  | "failed" | "canceled";

export type BenchmarkRun = {
  id: string;
  workspace_id: string;
  suite_id: string;
  suite_instance_ids: string[];
  profile_id: string;
  base_run_id?: string;
  display_name: string;
  status: RunStatus;
  status_reason: string;
  notes: string;
  evaluator_mode: "managed" | "imported";
  adapter_version: string;
  submission_timeout_seconds: number;
  created_by: string;
};

export type StartRunRequest = {
  suite_id: string;
  profile_id: string;
  base_run_id?: string;
  display_name: string;
  notes?: string;
  evaluator_mode: "managed" | "imported";
  adapter_version?: string;
};

export type ListBenchmarkRunsResponse = { items: BenchmarkRun[] };
```

- [ ] **Step 2: Add 4 ApiClient methods** matching `/api/benchmarks/runs` routes from Phase 1a T13.

```ts
async listBenchmarkRuns(limit = 50): Promise<ListBenchmarkRunsResponse> {
  return this.fetch(`/api/benchmarks/runs?limit=${limit}`);
}
async getBenchmarkRun(id: string): Promise<BenchmarkRun> {
  return this.fetch(`/api/benchmarks/runs/${id}`);
}
async startBenchmarkRun(input: StartRunRequest): Promise<BenchmarkRun> {
  return this.fetch(`/api/benchmarks/runs`, {
    method: "POST", body: JSON.stringify(input),
  });
}
async cancelBenchmarkRun(id: string): Promise<void> {
  await this.fetch(`/api/benchmarks/runs/${id}/cancel`, { method: "POST" });
}
```

- [ ] **Step 3: Re-export new types from `types/index.ts`.**
- [ ] **Step 4: `pnpm -F @multica/core typecheck` clean. Commit `feat(benchmark): frontend types and API methods for runs`.**

---

## Task 2: Path helpers + TanStack hooks for runs

**Files:**
- Modify `packages/core/paths/paths.ts`
- Modify `packages/core/paths/paths.test.ts` + `consistency.test.ts`
- Modify `packages/core/benchmarks/queries.ts`
- Modify `packages/core/benchmarks/mutations.ts`
- Modify `packages/core/benchmarks/index.ts` (if barrel)

- [ ] **Step 1: Path helpers**

```ts
benchmarkRuns: () => `${workspace}/benchmarks/runs`,
benchmarkRunDetail: (id: string) => `${workspace}/benchmarks/runs/${id}`,
benchmarkRunNew:    () => `${workspace}/benchmarks/runs/new`,
```

Mirror in tests.

- [ ] **Step 2: Query options**

```ts
export function benchmarkRunListOptions(wsId: string) {
  return queryOptions({
    queryKey: benchmarkKeys.runs(wsId),
    queryFn: () => api.listBenchmarkRuns().then(r => r.items),
  });
}
export function benchmarkRunDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: benchmarkKeys.run(wsId, id),
    queryFn: () => api.getBenchmarkRun(id),
    enabled: Boolean(id),
  });
}
```

Add `runs(wsId)` and `run(wsId, id)` to the `benchmarkKeys` factory.

- [ ] **Step 3: Mutation hooks**

```ts
export function useStartBenchmarkRun() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: StartRunRequest) => api.startBenchmarkRun(input),
    onSuccess: () => qc.invalidateQueries({ queryKey: benchmarkKeys.runs(wsId) }),
  });
}
export function useCancelBenchmarkRun() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.cancelBenchmarkRun(id),
    onSuccess: (_, id) => {
      qc.invalidateQueries({ queryKey: benchmarkKeys.runs(wsId) });
      qc.invalidateQueries({ queryKey: benchmarkKeys.run(wsId, id) });
    },
  });
}
```

- [ ] **Step 4:** `pnpm -F @multica/core typecheck` clean.
- [ ] **Step 5:** Commit `feat(benchmark): query/mutation hooks for runs`.

---

## Task 3: Activate Runs tab + Runs list view

**Files:**
- Modify `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/layout.tsx` (un-disable Runs tab)
- Create `packages/views/benchmarks/RunsList.tsx`
- Create `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/runs/page.tsx` (re-export)
- Modify `packages/views/benchmarks/index.ts` (export RunsList)

- [ ] **Step 1: Update layout.tsx to enable Runs**

In the `tabs` array, change Runs from `{ disabled: true }` to `{ disabled: false, href: paths.workspace(slug).benchmarkRuns() }`. Remove the "Available after Phase 1" tooltip for Runs (keep for Leaderboard).

- [ ] **Step 2: RunsList component** following SuitesList pattern.

Columns: Display name, Suite (display via separate query? or just show suite id), Profile (similar), Status (pill with color), Tasks (`scored/total` if summary exists, otherwise "—"), Created.

For Phase 1c v1: don't worry about pulling related suite/profile names; use ids. Future polish can join.

CTA "Start run" → navigate to `/benchmarks/runs/new`.

Empty state: "No runs yet. Start a run to benchmark your agent against a suite."

Status pills with Tailwind classes:
- queued → `bg-gray-100`
- submitting → `bg-blue-100`
- evaluating → `bg-yellow-100`
- complete → `bg-green-100`
- failed → `bg-red-100`
- canceled → `bg-gray-200`

- [ ] **Step 3: Re-export and route**

```tsx
// apps/web/.../benchmarks/runs/page.tsx
export { RunsList as default } from "@multica/views/benchmarks";
```

- [ ] **Step 4: Typecheck + lint clean.**
- [ ] **Step 5: Commit `feat(benchmark): runs list view + activate runs tab`.**

---

## Task 4: Run detail view (no live WS yet)

**Files:**
- Create `packages/views/benchmarks/RunDetail.tsx`
- Create `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/runs/[runId]/page.tsx`

- [ ] **Step 1: RunDetail component**

Sections:
- Header: display_name + back arrow + status pill + cancel button (only enabled if status in queued/submitting/evaluating).
- Metadata block: suite_id (mono), profile_id (mono), evaluator_mode badge, base_run_id (link if present), submission_timeout_seconds.
- Notes (if non-empty).
- Tasks table — for v1 just say "Task list coming in a future iteration" since we don't have a list-tasks-by-run query exposed. (Or: add a quick `listBenchmarkTasksByRun` API endpoint if it doesn't exist? Check Phase 1a. The sqlc query exists; HTTP endpoint does NOT. Defer to follow-up.)
- Summary block: "Available after run completes" placeholder.

Cancel button onClick: `useCancelBenchmarkRun().mutate(id)`. Confirmation dialog if available; else `window.confirm`.

- [ ] **Step 2: Route**

```tsx
"use client";
import { use } from "react";
import { RunDetail } from "@multica/views/benchmarks";

export default function Page({ params }: { params: Promise<{ workspaceSlug: string; runId: string }> }) {
  const { runId } = use(params);
  return <RunDetail runId={runId} />;
}
```

- [ ] **Step 3: Typecheck clean. Commit `feat(benchmark): run detail view (basic)`.**

---

## Task 5: Start-run wizard

**Files:**
- Create `packages/views/benchmarks/StartRunWizard.tsx`
- Create `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/runs/new/page.tsx`

- [ ] **Step 1: StartRunWizard component**

Three-step form (linear; not a fancy stepper — just a single form with grouped sections):
1. **Suite** — `<select>` populated from `useQuery(benchmarkSuiteListOptions(wsId))`. Required.
2. **Profile** — `<select>` populated from `useQuery(benchmarkProfileListOptions(wsId))`. Required.
3. **Display name** + optional **Notes**.
4. **Evaluator mode** — radio `managed | imported`. Default `imported` (works without evaluator pod).
5. **Compare with (optional)** — `<select>` of recent complete runs from same suite (re-query when suite changes; client-side filter on `status==="complete" && suite_id===selectedSuite`).

Submit: `useStartBenchmarkRun().mutateAsync({suite_id, profile_id, display_name, notes, evaluator_mode, base_run_id})`. On success: navigate to `/benchmarks/runs/<id>`. Error: alert with `extractBenchmarkErrorCode`-derived message.

Cancel button: navigate back to `/benchmarks/runs`.

- [ ] **Step 2: Route page** (one-line re-export).
- [ ] **Step 3: Typecheck clean. Commit `feat(benchmark): start-run wizard`.**

---

## Task 6: i18n keys

**Files:**
- Modify `packages/views/locales/en/benchmarks.json`
- Modify `packages/views/locales/zh-Hans/benchmarks.json`

- [ ] **Step 1: Add `runs_*`, `run_detail_*`, `start_run_*` sections.**

Mirror the structure of existing `suites_list` / `profiles_list` keys. Status pill labels:

```json
"status": {
  "queued": "Queued",
  "submitting": "Submitting",
  "evaluating": "Evaluating",
  "complete": "Complete",
  "failed": "Failed",
  "canceled": "Canceled"
}
```

Chinese (zh-Hans):
```json
"status": {
  "queued": "排队中",
  "submitting": "提交中",
  "evaluating": "评估中",
  "complete": "完成",
  "failed": "失败",
  "canceled": "已取消"
}
```

- [ ] **Step 2: Replace any English literals in T03–T05 component files with `t(...)` calls. Remove any file-level eslint-disable.**
- [ ] **Step 3: parity test passes.**
- [ ] **Step 4: Commit `feat(benchmark): i18n keys for runs UI`.**

---

## Task 7: Final pre-PR check + push

- [ ] Tests green.
- [ ] gofmt + vet (no Go changes in 1c, but a sanity check).
- [ ] pnpm typecheck + lint clean.
- [ ] TODO/TBD scan in new view files.
- [ ] Push to fork.

---

## Self-Review

**Spec coverage (1c):**
- ✅ Runs list view + activate disabled tab.
- ✅ Run detail (basic; tasks table deferred — needs server query).
- ✅ Start-run wizard.
- ✅ Cancel button.
- ⏭ Live WS updates deferred (existing realtime hub supports it; trivial follow-up).
- ⏭ Tasks-per-run table requires a new sqlc/HTTP endpoint; defer to follow-up.
- ⏭ Comparison view is Phase 2.
- ⏭ Leaderboard is Phase 2.

**Caveats:** Run detail without per-task drill-down is a v1 limitation. Comparison and leaderboard are Phase 2.
