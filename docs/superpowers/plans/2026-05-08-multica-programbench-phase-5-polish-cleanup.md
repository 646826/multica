# Multica × ProgramBench Phase 5 — Polish & Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Knock out the most-impactful quality/UX follow-ups from Phase 0–4 PR-description backlogs. Each task is small and isolated.

---

## Tasks

### T01 — Expose `created_at`/`started_at`/`completed_at` on RunResponse, render Created column

**Files:**
- `server/internal/handler/benchmark.go` — add 3 timestamp fields to RunResponse + populate
- `packages/core/types/benchmark.ts` — add 3 fields to BenchmarkRun
- `packages/views/benchmarks/RunsList.tsx` — add Created column (timeAgo formatted)

Steps:
1. RunResponse: add `CreatedAt`, `StartedAt` (omitempty), `CompletedAt` (omitempty) JSON fields. Use `util.TimestampToString` (existing helper).
2. TS: `created_at: string`, `started_at?: string`, `completed_at?: string`.
3. RunsList: add column "Created" using `timeAgo` helper from `@multica/core/utils`.
4. i18n key `runs_list.col_created`.
5. Tests + commit.

### T02 — Auto-close benchmark issues on submission accept

**Files:**
- `server/internal/service/benchmark/task_dispatcher.go`

Steps:
1. In `OnAttachmentEvent` happy path (after task → submitted), call existing issue-close service with `close_reason='benchmark_submitted'`.
2. Test verifies issue is closed in submission flow.
3. Commit.

### T03 — Live WS updates on Run detail

**Files:**
- `packages/core/benchmarks/queries.ts` — invalidate cache on bus events
- `packages/views/benchmarks/RunDetail.tsx` — wire WS subscription via existing realtime hub

Steps:
1. Discover existing WS subscription helper (likely `useRealtimeSubscription` or similar).
2. Subscribe to `benchmark_run:status`, `benchmark_task:status`, `benchmark_task:scored`, `benchmark_run:completed` events.
3. On each event, invalidate the relevant TanStack Query cache key so the UI auto-refreshes.
4. Commit.

### T04 — Extract `pgerr.IsUniqueViolation` shared helper

**Files:**
- Create `server/internal/util/pgerr/pgerr.go`
- Migrate ~15 inline call sites across handler + service packages.

Steps:
1. Helper:
   ```go
   func IsUniqueViolation(err error) bool {
       var pg *pgconn.PgError
       return errors.As(err, &pg) && pg.Code == "23505"
   }
   ```
2. Replace inline `errors.As(err, &pgErr); pgErr.Code == "23505"` with `pgerr.IsUniqueViolation(err)` across the codebase.
3. Commit.

### T05 — Add `summary_not_available` to BenchmarkErrorCode + handle in UI

**Files:**
- `packages/core/types/benchmark.ts` — add code
- `packages/core/benchmarks/error.ts` — add to runtime Set
- All view files with exhaustive `messageForCode` switch — add new case
- locale files: en + zh-Hans error keys

Steps:
1. Append `summary_not_available` to union.
2. Add to runtime Set.
3. TS will trip 10 view files exhaustive switches — add the case to each.
4. Locale entries: "Summary not yet computed."
5. Commit.

### T06 — Add new docs pages to meta.json sidebar

**Files:**
- `apps/docs/content/docs/meta.json` (or whatever the file is)

Steps:
1. Find sidebar config.
2. Add `benchmarks`, `benchmarks-evaluator-deploy`, `developers/benchmarks-adapter`.
3. Commit.

### T07 — Final check + push

Standard tests/lint/typecheck + push to fork.

---

## Self-Review

All 7 are small, well-scoped polish items. No architectural changes. The biggest impact: T01 (timestamp visibility), T02 (auto-close — declutters board), T03 (live UI = no manual refresh).
