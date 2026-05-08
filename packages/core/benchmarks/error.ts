import type { BenchmarkErrorCode } from "../types";

const BENCHMARK_ERROR_CODES: ReadonlySet<BenchmarkErrorCode> = new Set([
  "instance_list_empty",
  "bad_body",
  "bad_id",
  "bad_user_id",
  "bad_workspace_id",
  "workspace_required",
  "agent_not_found",
  "unauthenticated",
  "suite_not_found",
  "profile_not_found",
  "slug_taken",
  "internal_error",
  "invalid_evaluator_mode",
  "suite_or_profile_not_found",
  "task_not_found_for_instance",
  "run_not_found",
  "display_name_required",
  "evaluator_id_required",
  "adapter_kinds_required",
  "eval_job_not_found",
]);

/**
 * Pull a {@link BenchmarkErrorCode} out of an unknown thrown value if the
 * server attached one to the JSON body. Returns `undefined` for non-ApiError
 * throws, network failures, or unrecognised codes — callers that need to
 * branch on a specific code should fall back to a generic error message in
 * that case.
 *
 * Kept dependency-free of `ApiError` (duck-types on `body.code`) so this
 * module can sit alongside the queries/mutations without an api-package
 * import in the types-adjacent leaf. ApiError already carries the matching
 * shape, and any future error class with the same shape is intentionally
 * compatible.
 */
export function extractBenchmarkErrorCode(
  err: unknown,
): BenchmarkErrorCode | undefined {
  if (!err || typeof err !== "object") return undefined;
  const body = (err as { body?: unknown }).body;
  if (!body || typeof body !== "object") return undefined;
  const code = (body as { code?: unknown }).code;
  if (typeof code !== "string") return undefined;
  return BENCHMARK_ERROR_CODES.has(code as BenchmarkErrorCode)
    ? (code as BenchmarkErrorCode)
    : undefined;
}
