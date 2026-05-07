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
