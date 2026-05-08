"use client";

import type { BenchmarkErrorCode } from "@multica/core/types";
import { extractBenchmarkErrorCode } from "@multica/core/benchmarks";
import { useT } from "../i18n";

type Translator = ReturnType<typeof useT<"benchmarks">>["t"];

/**
 * Per-view overrides for the codes whose user-facing message depends on
 * which page the error surfaced on.
 *
 * - `slug_taken` — different copy for suite vs. profile create vs. forms.
 * - `bad_id` — some pages know whether the id is a suite or profile id.
 * - `bad_body` — forms prefer "form data was rejected".
 * - `instance_list_empty` — the create form wants "add at least one
 *   instance id"; list pages want the read-side wording.
 * - `agent_not_found` — capture-form scopes to "in this workspace".
 *
 * Each override is a thunk that calls `t(...)` itself so the consumer
 * picks the exact resource key for their context.
 */
export interface BenchmarkErrorMessageOverrides {
  slug_taken?: (t: Translator) => string;
  bad_id?: (t: Translator) => string;
  bad_body?: (t: Translator) => string;
  instance_list_empty?: (t: Translator) => string;
  agent_not_found?: (t: Translator) => string;
}

/**
 * Map a `BenchmarkErrorCode` to a user-facing string, using the
 * benchmarks i18n namespace. Pass `overrides` for codes whose message
 * varies per view (see {@link BenchmarkErrorMessageOverrides}).
 *
 * The switch is exhaustive: adding a new member to `BenchmarkErrorCode`
 * without updating this function is a compile error rather than a silent
 * fall-through to the default copy.
 */
export function useBenchmarkErrorMessage(
  overrides: BenchmarkErrorMessageOverrides = {},
): (code: BenchmarkErrorCode) => string {
  const { t } = useT("benchmarks");
  return (code: BenchmarkErrorCode): string => {
    switch (code) {
      case "unauthenticated":
        return t(($) => $.errors.unauthenticated);
      case "workspace_required":
      case "bad_workspace_id":
      case "bad_user_id":
        return t(($) => $.errors.workspace_context_missing);
      case "internal_error":
        return t(($) => $.errors.internal_error);
      case "suite_not_found":
        return t(($) => $.errors.suite_not_found);
      case "profile_not_found":
        return t(($) => $.errors.profile_not_found);
      case "agent_not_found":
        return overrides.agent_not_found
          ? overrides.agent_not_found(t)
          : t(($) => $.errors.agent_not_found);
      case "slug_taken":
        return overrides.slug_taken
          ? overrides.slug_taken(t)
          : t(($) => $.errors.slug_taken_pick_different);
      case "instance_list_empty":
        return overrides.instance_list_empty
          ? overrides.instance_list_empty(t)
          : t(($) => $.errors.instance_list_empty);
      case "bad_body":
        return overrides.bad_body
          ? overrides.bad_body(t)
          : t(($) => $.errors.bad_body);
      case "bad_id":
        return overrides.bad_id
          ? overrides.bad_id(t)
          : t(($) => $.errors.bad_id);
      case "invalid_evaluator_mode":
        return t(($) => $.errors.invalid_evaluator_mode);
      case "suite_or_profile_not_found":
        return t(($) => $.errors.suite_or_profile_not_found);
      case "task_not_found_for_instance":
        return t(($) => $.errors.task_not_found_for_instance);
      case "run_not_found":
        return t(($) => $.errors.run_not_found);
      case "display_name_required":
        return t(($) => $.errors.display_name_required);
      case "evaluator_id_required":
        return t(($) => $.errors.evaluator_id_required);
      case "adapter_kinds_required":
        return t(($) => $.errors.adapter_kinds_required);
      case "eval_job_not_found":
        return t(($) => $.errors.eval_job_not_found);
      case "adapter_unknown":
        return t(($) => $.errors.adapter_unknown);
      case "summary_not_available":
        return t(($) => $.errors.summary_not_available);
      case "unsupported_reference_url":
        return t(($) => $.errors.unsupported_reference_url);
      case "reference_fetch_failed":
        return t(($) => $.errors.reference_fetch_failed);
      case "url_required":
        return t(($) => $.errors.url_required);
    }
  };
}

/**
 * Wraps `useBenchmarkErrorMessage` with a generic-error fallback so
 * call sites can map any thrown value (network error, etc.) without
 * repeating the `extractBenchmarkErrorCode` dance.
 *
 * `fallback` is the default string returned when the error has no
 * benchmark code and isn't a normal `Error`.
 */
export function useBenchmarkErrorFallback(
  overrides: BenchmarkErrorMessageOverrides = {},
): (err: unknown, fallback: string) => string {
  const messageForCode = useBenchmarkErrorMessage(overrides);
  return (err: unknown, fallback: string): string => {
    const code = extractBenchmarkErrorCode(err);
    if (code) return messageForCode(code);
    if (err instanceof Error && err.message) return err.message;
    return fallback;
  };
}
