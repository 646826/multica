"use client";

import type { RunStatus } from "@multica/core/types";
import { useT } from "../i18n";

/**
 * Visual treatment for each lifecycle state. The class map is exhaustive over
 * `RunStatus`, so a new state added to the union becomes a compile-time error
 * here. Shared between {@link RunsList} (table cell) and {@link RunDetail}
 * (header).
 */
export function StatusPill({ status }: { status: RunStatus }) {
  const { t } = useT("benchmarks");
  const cls: Record<RunStatus, string> = {
    queued: "bg-muted text-muted-foreground",
    submitting: "bg-blue-100 text-blue-800 dark:bg-blue-950 dark:text-blue-200",
    evaluating:
      "bg-yellow-100 text-yellow-800 dark:bg-yellow-950 dark:text-yellow-200",
    complete: "bg-green-100 text-green-800 dark:bg-green-950 dark:text-green-200",
    failed: "bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-200",
    canceled: "bg-muted text-muted-foreground line-through",
  };
  const labels: Record<RunStatus, string> = {
    queued: t(($) => $.status.queued),
    submitting: t(($) => $.status.submitting),
    evaluating: t(($) => $.status.evaluating),
    complete: t(($) => $.status.complete),
    failed: t(($) => $.status.failed),
    canceled: t(($) => $.status.canceled),
  };
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${cls[status]}`}
    >
      {labels[status]}
    </span>
  );
}
