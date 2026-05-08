/**
 * Benchmark suites and agent profiles — workspace-scoped resources backing
 * the ProgramBench feature. Mirrors `SuiteResponse` / `ProfileResponse` from
 * `server/internal/handler/benchmark.go`; field names and JSON casing must
 * stay aligned with the Go DTOs.
 */

/** Stable identifier for an attached skill captured in a profile snapshot. */
export interface SkillRef {
  slug: string;
  version: string;
}

/**
 * A benchmark suite: a named collection of adapter instances to evaluate
 * against. `instance_ids` opaque to the frontend — adapters interpret them.
 */
export interface BenchmarkSuite {
  id: string;
  workspace_id: string;
  slug: string;
  display_name: string;
  adapter_kind: string;
  instance_ids: string[];
  description: string;
  created_at: string;
  created_by: string;
}

/**
 * Immutable snapshot of an agent's prompt + attached skills at capture time.
 *
 * `duplicate_of` is set (and points to the older profile id in the same
 * workspace) when the captured prompt_hash collides with an existing
 * profile. The capture still succeeds; the field is informational.
 */
export interface BenchmarkProfile {
  id: string;
  workspace_id: string;
  slug: string;
  display_name: string;
  agent_id: string;
  agent_name: string;
  model: string;
  prompt_source: string;
  prompt_hash: string;
  attached_skills: SkillRef[];
  captured_by: string;
  duplicate_of?: string;
}

/** Inbound payload for `POST /api/benchmarks/suites`. */
export interface CreateSuiteRequest {
  slug: string;
  display_name: string;
  adapter_kind: string;
  instance_ids: string[];
  description?: string;
}

/**
 * Inbound payload for `POST /api/benchmarks/profiles`.
 *
 * `agent_name`, `model`, and `prompt_source` are NOT part of the request —
 * the server reads them from the live agent row so the snapshot is
 * authoritative.
 */
export interface CaptureProfileRequest {
  agent_id: string;
  slug: string;
  display_name: string;
}

/**
 * Result of `POST /api/benchmarks/suites/:id/sync`. v1 is informational —
 * the suite is not mutated; the response splits the suite's `instance_ids`
 * into the ones the registered Catalog could resolve vs. the ones it could
 * not. Empty `unresolved` means the suite is in sync with its source.
 */
export interface SuiteSyncResult {
  suite_id: string;
  adapter_kind: string;
  resolved: string[];
  unresolved: string[];
}

/**
 * One row returned by `GET /api/benchmarks/replay/eligible-issues`. Lists
 * completed issues in the workspace that can be replayed as a benchmark
 * instance. Field names mirror the Go DTO.
 */
export interface EligibleIssue {
  id: string;
  number: number;
  title: string;
  status: string;
  updated_at: string;
}

/**
 * Inbound payload for `POST /api/benchmarks/replay/suites`. Each entry in
 * `instances` pins a completed issue plus the reference solution patch the
 * server stores for evaluator scoring.
 */
export interface CreateReplaySuiteRequest {
  slug: string;
  display_name: string;
  description?: string;
  instances: Array<{
    source_issue_id: string;
    reference_solution: string;
    reference_pr_url?: string;
  }>;
}

export interface ListReplayEligibleIssuesResponse {
  items: EligibleIssue[];
}

/** Wrapper returned by list endpoints. */
export interface ListBenchmarkSuitesResponse {
  items: BenchmarkSuite[];
}

export interface ListBenchmarkProfilesResponse {
  items: BenchmarkProfile[];
}

/**
 * Lifecycle of a benchmark run. Mirrors the Go-side state machine; the
 * frontend treats these as opaque labels and renders them via a status map.
 */
export type RunStatus =
  | "queued"
  | "submitting"
  | "evaluating"
  | "complete"
  | "failed"
  | "canceled";

/**
 * A single benchmark execution: pins a suite + profile pair, captures the
 * concrete `suite_instance_ids` at start time, and tracks lifecycle state.
 *
 * `base_run_id` is set when this run was started as a re-run of an earlier
 * one. `evaluator_mode` distinguishes managed (server-driven) vs. imported
 * (results uploaded externally) flows.
 */
export interface BenchmarkRun {
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
  created_at: string;
  started_at?: string;
  completed_at?: string;
}

/** Inbound payload for `POST /api/benchmarks/runs`. */
export interface StartRunRequest {
  suite_id: string;
  profile_id: string;
  base_run_id?: string;
  display_name: string;
  notes?: string;
  evaluator_mode: "managed" | "imported";
  adapter_version?: string;
}

export interface ListBenchmarkRunsResponse {
  items: BenchmarkRun[];
}

/**
 * Result of comparing two benchmark runs (`GET
 * /api/benchmarks/runs/:cand/compare?base=:base`). Mirrors the Go
 * `CompareResponse` DTO. `partial` is true when the two runs cover
 * different instance sets — `missing_in_base` / `missing_in_cand`
 * itemize the asymmetric instances and the deltas only consider the
 * intersection.
 */
export interface ComparisonInstance {
  instance_id: string;
  base_pass_rate: number;
  cand_pass_rate: number;
}

export interface ComparisonDelta {
  resolved: number;
  avg_pass_rate: number;
  agg_pass_rate: number;
  errored: number;
}

export interface ComparisonCategories {
  added: string[];
  cleared: string[];
}

export interface ComparisonResult {
  base_run_id: string;
  cand_run_id: string;
  partial: boolean;
  delta: ComparisonDelta;
  improved: ComparisonInstance[];
  regressed: ComparisonInstance[];
  newly_resolved: ComparisonInstance[];
  lost_resolved: ComparisonInstance[];
  categories: ComparisonCategories;
  missing_in_base?: string[];
  missing_in_cand?: string[];
}

/**
 * One row in the per-suite leaderboard (`GET
 * /api/benchmarks/leaderboard?suite=:slug`). Each row pins a profile's
 * best completed run on the suite, ranked by aggregate pass rate.
 */
export interface LeaderboardRow {
  rank: number;
  profile_id: string;
  profile_slug: string;
  profile_display_name: string;
  best_run_id: string;
  best_run_display_name: string;
  resolved_count: number;
  total_count: number;
  average_pass_rate: number;
  aggregate_pass_rate: number;
  errored_count: number;
  completed_at: string;
}

export interface ListLeaderboardResponse {
  items: LeaderboardRow[];
}

/**
 * One row in the per-run task list (`GET
 * /api/benchmarks/runs/:id/tasks`). Mirrors `RunTaskView` from the Go
 * service projected to snake_case by the handler. Scoring fields
 * (`resolved`, `passed_tests`, `total_tests`, `pass_rate`,
 * `failed_categories`) are zero / empty when the task has not been
 * scored yet — the eval_result row is absent. `issue_id` is omitted
 * (not present on the JSON object) when the task is not linked to an
 * issue.
 */
export interface BenchmarkRunTask {
  id: string;
  instance_id: string;
  status: string;
  status_reason: string;
  issue_id?: string;
  resolved: boolean;
  passed_tests: number;
  total_tests: number;
  pass_rate: number;
  failed_categories: string[];
}

/**
 * Per-category aggregate emitted in `BenchmarkRunSummary.failure_categories`.
 * Mirrors `FailureCategoryView` on the server.
 */
export interface FailureCategory {
  name: string;
  count: number;
}

/**
 * Persisted summary row for a finalized run (`GET
 * /api/benchmarks/runs/:id/summary`). Mirrors `BenchmarkRunSummaryView`
 * from the Go service — snake_case is set explicitly via `json:"..."`
 * tags. The endpoint returns 404 `summary_not_available` when the run
 * exists but has not been finalized yet, distinguished from
 * `run_not_found` by the error code in the body.
 */
export interface BenchmarkRunSummary {
  run_id: string;
  resolved_count: number;
  total_count: number;
  aggregate_pass_rate: number;
  average_pass_rate: number;
  errored_count: number;
  failure_categories: FailureCategory[];
}

export interface ListBenchmarkRunTasksResponse {
  items: BenchmarkRunTask[];
}

/**
 * Machine-readable error codes returned in the JSON body of failed
 * `/api/benchmarks/*` responses (see the comment block above the handler
 * methods in `packages/core/api/client.ts`). UI views map these to
 * user-facing strings; the union exists so a missing case is a type error
 * rather than a silent fall-through.
 */
export type BenchmarkErrorCode =
  | "instance_list_empty"
  | "bad_body"
  | "bad_id"
  | "bad_user_id"
  | "bad_workspace_id"
  | "workspace_required"
  | "agent_not_found"
  | "unauthenticated"
  | "suite_not_found"
  | "profile_not_found"
  | "slug_taken"
  | "internal_error"
  | "invalid_evaluator_mode"
  | "suite_or_profile_not_found"
  | "task_not_found_for_instance"
  | "run_not_found"
  | "display_name_required"
  | "evaluator_id_required"
  | "adapter_kinds_required"
  | "eval_job_not_found"
  | "adapter_unknown"
  | "summary_not_available";
