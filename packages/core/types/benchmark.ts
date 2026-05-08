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
  | "eval_job_not_found";
