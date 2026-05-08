"use client";

// English literal strings live here intentionally for Phase 1c-T04. The
// dedicated i18n pass for runs UI is tracked as P1c-T06 and will replace
// every hard-coded label below with `useT("benchmarks").t(...)` lookups.
/* eslint-disable i18next/no-literal-string */

import { AlertCircle, ArrowLeft } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import {
  benchmarkRunDetailOptions,
  extractBenchmarkErrorCode,
  useCancelBenchmarkRun,
} from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { BenchmarkErrorCode, BenchmarkRun } from "@multica/core/types";
import {
  Alert,
  AlertDescription,
  AlertTitle,
} from "@multica/ui/components/ui/alert";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useNavigation, AppLink } from "../navigation";
import { PageHeader } from "../layout/page-header";
import { StatusPill } from "./StatusPill";

/**
 * Map a benchmark error code to a user-facing English message for the run
 * detail view. The full union is covered so a new code becomes a compile
 * error. T06 will swap this for the shared `useT` translator pattern.
 */
function messageForCode(code: BenchmarkErrorCode): string {
  switch (code) {
    case "unauthenticated":
      return "You must be signed in to view this run.";
    case "workspace_required":
    case "bad_workspace_id":
    case "bad_user_id":
      return "Workspace context is missing or invalid.";
    case "bad_id":
      return "Invalid run id.";
    case "internal_error":
      return "Something went wrong on the server.";
    case "suite_not_found":
      return "Suite not found.";
    case "profile_not_found":
      return "Profile not found.";
    case "agent_not_found":
      return "Agent not found.";
    case "slug_taken":
      return "That slug is already taken.";
    case "instance_list_empty":
      return "Suite has no instances.";
    case "bad_body":
      return "Invalid request body.";
  }
}

function errorMessage(err: unknown): string {
  const code = extractBenchmarkErrorCode(err);
  if (code) return messageForCode(code);
  if (err instanceof Error && err.message) return err.message;
  return "Failed to load run.";
}

/**
 * Format `submission_timeout_seconds` as a compact `Hh Mm` string. Hours are
 * dropped when zero so common short timeouts read as `15m` instead of `0h 15m`.
 */
function formatTimeout(s: number): string {
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

function HeaderBar({
  title,
  onBack,
  action,
}: {
  title: string;
  onBack: () => void;
  action?: React.ReactNode;
}) {
  return (
    <PageHeader className="justify-between gap-2 px-5">
      <div className="flex items-center gap-2">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label="Back to runs"
          onClick={onBack}
        >
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <h1 className="text-sm font-medium">{title}</h1>
      </div>
      {action}
    </PageHeader>
  );
}

function LoadingState({ onBack }: { onBack: () => void }) {
  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <HeaderBar title="Run" onBack={onBack} />
      <div className="flex flex-1 min-h-0 flex-col gap-4 p-6">
        <Skeleton className="h-8 w-64 rounded-md" />
        <Skeleton className="h-4 w-40 rounded-md" />
        <div className="space-y-2 pt-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-6 w-full rounded-md" />
          ))}
        </div>
      </div>
    </div>
  );
}

function ModeBadge({ mode }: { mode: BenchmarkRun["evaluator_mode"] }) {
  const cls =
    mode === "managed"
      ? "bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-200"
      : "bg-slate-100 text-slate-800 dark:bg-slate-800 dark:text-slate-200";
  return (
    <span
      className={`inline-flex items-center rounded px-1.5 py-0.5 text-xs font-medium ${cls}`}
    >
      {mode}
    </span>
  );
}

function MetadataRow({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="grid grid-cols-[160px_1fr] items-center gap-3 py-1.5">
      <span className="text-xs uppercase tracking-wide text-muted-foreground">
        {label}
      </span>
      <div className="text-sm">{children}</div>
    </div>
  );
}

function Metadata({
  run,
  baseRunHref,
}: {
  run: BenchmarkRun;
  baseRunHref: string | null;
}) {
  return (
    <section className="rounded-lg border bg-background p-4">
      <MetadataRow label="Suite ID">
        <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
          {run.suite_id}
        </code>
      </MetadataRow>
      <MetadataRow label="Profile ID">
        <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
          {run.profile_id}
        </code>
      </MetadataRow>
      <MetadataRow label="Mode">
        <ModeBadge mode={run.evaluator_mode} />
      </MetadataRow>
      <MetadataRow label="Submission timeout">
        <span className="font-mono tabular-nums text-xs">
          {formatTimeout(run.submission_timeout_seconds)}
        </span>
      </MetadataRow>
      {run.adapter_version && (
        <MetadataRow label="Adapter version">
          <Badge variant="outline">{run.adapter_version}</Badge>
        </MetadataRow>
      )}
      {run.base_run_id && baseRunHref && (
        <MetadataRow label="Base run">
          <AppLink
            href={baseRunHref}
            className="font-mono text-xs text-primary underline-offset-2 hover:underline"
          >
            {run.base_run_id}
          </AppLink>
        </MetadataRow>
      )}
    </section>
  );
}

export default function RunDetail({ runId }: { runId: string }) {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();

  const runsBase = paths.benchmarkRuns();
  const goBack = () => navigation.push(runsBase);

  const {
    data: run,
    isLoading,
    error,
  } = useQuery(benchmarkRunDetailOptions(wsId, runId));

  const cancelMut = useCancelBenchmarkRun();

  if (isLoading) {
    return <LoadingState onBack={goBack} />;
  }

  if (error || !run) {
    const message = error ? errorMessage(error) : "Run not found.";
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <HeaderBar title="Run" onBack={goBack} />
        <div className="flex flex-1 min-h-0 flex-col gap-4 p-6">
          <Alert variant="destructive">
            <AlertCircle />
            <AlertTitle>Failed to load run</AlertTitle>
            <AlertDescription>{message}</AlertDescription>
          </Alert>
          <div>
            <Button type="button" variant="ghost" size="sm" onClick={goBack}>
              <ArrowLeft className="h-3 w-3" />
              Back to runs
            </Button>
          </div>
        </div>
      </div>
    );
  }

  const isActive =
    run.status === "queued" ||
    run.status === "submitting" ||
    run.status === "evaluating";

  const onCancel = () => {
    if (
      window.confirm("Cancel this run? In-flight tasks will be aborted.")
    ) {
      cancelMut.mutate(run.id);
    }
  };

  const baseRunHref = run.base_run_id
    ? paths.benchmarkRunDetail(run.base_run_id)
    : null;

  const notes = run.notes.trim();

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <HeaderBar
        title={run.display_name}
        onBack={goBack}
        action={
          isActive ? (
            <Button
              type="button"
              variant="destructive"
              size="sm"
              disabled={cancelMut.isPending}
              onClick={onCancel}
            >
              {cancelMut.isPending ? "Canceling…" : "Cancel run"}
            </Button>
          ) : null
        }
      />

      <div className="flex flex-1 min-h-0 flex-col gap-6 overflow-auto p-6">
        <div className="flex flex-wrap items-center gap-2">
          <StatusPill status={run.status} />
          {run.status_reason && (
            <span className="text-sm text-muted-foreground">
              {run.status_reason}
            </span>
          )}
        </div>

        <Metadata run={run} baseRunHref={baseRunHref} />

        {notes && (
          <section>
            <h3 className="mb-1 text-sm font-medium">Notes</h3>
            <p className="whitespace-pre-wrap text-sm text-muted-foreground">
              {run.notes}
            </p>
          </section>
        )}

        <section className="rounded-md border bg-muted/30 p-4 text-sm text-muted-foreground">
          Per-task results will be available in a future iteration.
        </section>

        {run.status === "complete" && (
          <section className="rounded-md border bg-muted/30 p-4 text-sm text-muted-foreground">
            Summary computed; rendering will be added in a future iteration.
          </section>
        )}
      </div>
    </div>
  );
}
