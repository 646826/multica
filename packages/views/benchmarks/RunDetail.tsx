"use client";

import { AlertCircle, ArrowLeft } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import {
  benchmarkRunDetailOptions,
  benchmarkRunListOptions,
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
import { Label } from "@multica/ui/components/ui/label";
import {
  NativeSelect,
  NativeSelectOption,
} from "@multica/ui/components/ui/native-select";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useNavigation, AppLink } from "../navigation";
import { PageHeader } from "../layout/page-header";
import { useT } from "../i18n";
import { StatusPill } from "./StatusPill";

type Translator = ReturnType<typeof useT<"benchmarks">>["t"];

/**
 * Map a benchmark error code to a user-facing message for the run detail
 * view. The full union is covered exhaustively so a new code in the union
 * becomes a type error rather than a silent fall-through.
 */
function messageForCode(t: Translator, code: BenchmarkErrorCode): string {
  switch (code) {
    case "unauthenticated":
      return t(($) => $.errors.unauthenticated);
    case "workspace_required":
    case "bad_workspace_id":
    case "bad_user_id":
      return t(($) => $.errors.workspace_context_missing);
    case "bad_id":
      return t(($) => $.errors.bad_id);
    case "internal_error":
      return t(($) => $.errors.internal_error);
    case "suite_not_found":
      return t(($) => $.errors.suite_not_found);
    case "profile_not_found":
      return t(($) => $.errors.profile_not_found);
    case "agent_not_found":
      return t(($) => $.errors.agent_not_found);
    case "slug_taken":
      return t(($) => $.errors.slug_taken_suite);
    case "instance_list_empty":
      return t(($) => $.errors.instance_list_empty);
    case "bad_body":
      return t(($) => $.errors.bad_body);
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
  }
}

function errorMessage(t: Translator, err: unknown): string {
  const code = extractBenchmarkErrorCode(err);
  if (code) return messageForCode(t, code);
  if (err instanceof Error && err.message) return err.message;
  return t(($) => $.run_detail.error_title);
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
  const { t } = useT("benchmarks");
  return (
    <PageHeader className="justify-between gap-2 px-5">
      <div className="flex items-center gap-2">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={t(($) => $.run_detail.back)}
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
  const { t } = useT("benchmarks");
  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <HeaderBar title={t(($) => $.run_detail.loading)} onBack={onBack} />
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
  const { t } = useT("benchmarks");
  const cls =
    mode === "managed"
      ? "bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-200"
      : "bg-slate-100 text-slate-800 dark:bg-slate-800 dark:text-slate-200";
  const label =
    mode === "managed"
      ? t(($) => $.run_detail.mode_managed)
      : t(($) => $.run_detail.mode_imported);
  return (
    <span
      className={`inline-flex items-center rounded px-1.5 py-0.5 text-xs font-medium ${cls}`}
    >
      {label}
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
  const { t } = useT("benchmarks");
  return (
    <section className="rounded-lg border bg-background p-4">
      <MetadataRow label={t(($) => $.run_detail.metadata_suite_id)}>
        <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
          {run.suite_id}
        </code>
      </MetadataRow>
      <MetadataRow label={t(($) => $.run_detail.metadata_profile_id)}>
        <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
          {run.profile_id}
        </code>
      </MetadataRow>
      <MetadataRow label={t(($) => $.run_detail.metadata_mode)}>
        <ModeBadge mode={run.evaluator_mode} />
      </MetadataRow>
      <MetadataRow label={t(($) => $.run_detail.metadata_timeout)}>
        <span className="font-mono tabular-nums text-xs">
          {formatTimeout(run.submission_timeout_seconds)}
        </span>
      </MetadataRow>
      {run.adapter_version && (
        <MetadataRow label={t(($) => $.run_detail.metadata_adapter_version)}>
          <Badge variant="outline">{run.adapter_version}</Badge>
        </MetadataRow>
      )}
      {run.base_run_id && baseRunHref && (
        <MetadataRow label={t(($) => $.run_detail.metadata_base_run)}>
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

/**
 * "Compare with…" picker: lists other completed runs of the same suite,
 * excluding the current run. Navigates to the compare route on selection.
 * Hidden when there are no eligible candidates so the empty native select
 * doesn't sit dead on the page.
 */
function ComparePicker({ run }: { run: BenchmarkRun }) {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const { t } = useT("benchmarks");

  const { data: runs = [] } = useQuery(benchmarkRunListOptions(wsId));

  const candidates = runs.filter(
    (r) =>
      r.id !== run.id &&
      r.status === "complete" &&
      r.suite_id === run.suite_id,
  );

  if (candidates.length === 0) return null;

  const selectId = `compare-with-${run.id}`;

  return (
    <section className="rounded-lg border bg-background p-4">
      <Label htmlFor={selectId} className="text-sm font-medium">
        {t(($) => $.run_detail.compare_with)}
      </Label>
      <p className="mb-2 text-xs text-muted-foreground">
        {t(($) => $.run_detail.compare_with_help)}
      </p>
      <NativeSelect
        id={selectId}
        value=""
        onChange={(e) => {
          const baseId = e.target.value;
          if (!baseId) return;
          navigation.push(paths.benchmarkRunCompare(run.id, baseId));
        }}
      >
        <NativeSelectOption value="">
          {t(($) => $.run_detail.compare_with_placeholder)}
        </NativeSelectOption>
        {candidates.map((r) => (
          <NativeSelectOption key={r.id} value={r.id}>
            {r.display_name}
          </NativeSelectOption>
        ))}
      </NativeSelect>
    </section>
  );
}

export default function RunDetail({ runId }: { runId: string }) {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const { t } = useT("benchmarks");

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
    const message = error
      ? errorMessage(t, error)
      : t(($) => $.errors.run_not_found);
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <HeaderBar
          title={t(($) => $.run_detail.error_title)}
          onBack={goBack}
        />
        <div className="flex flex-1 min-h-0 flex-col gap-4 p-6">
          <Alert variant="destructive">
            <AlertCircle />
            <AlertTitle>{t(($) => $.run_detail.error_title)}</AlertTitle>
            <AlertDescription>{message}</AlertDescription>
          </Alert>
          <div>
            <Button type="button" variant="ghost" size="sm" onClick={goBack}>
              <ArrowLeft className="h-3 w-3" />
              {t(($) => $.run_detail.back)}
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
    if (window.confirm(t(($) => $.run_detail.cancel_confirm))) {
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
              {cancelMut.isPending
                ? t(($) => $.run_detail.cancel_button_pending)
                : t(($) => $.run_detail.cancel_button)}
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

        <ComparePicker run={run} />

        {notes && (
          <section>
            <h3 className="mb-1 text-sm font-medium">
              {t(($) => $.run_detail.notes_title)}
            </h3>
            <p className="whitespace-pre-wrap text-sm text-muted-foreground">
              {run.notes}
            </p>
          </section>
        )}

        <section className="rounded-md border bg-muted/30 p-4 text-sm text-muted-foreground">
          {t(($) => $.run_detail.tasks_placeholder)}
        </section>

        {run.status === "complete" && (
          <section className="rounded-md border bg-muted/30 p-4 text-sm text-muted-foreground">
            {t(($) => $.run_detail.summary_placeholder)}
          </section>
        )}
      </div>
    </div>
  );
}
