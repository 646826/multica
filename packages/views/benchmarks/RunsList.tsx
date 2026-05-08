"use client";

import { useCallback, useMemo } from "react";
import { AlertCircle, Activity, Plus, Search } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  benchmarkKeys,
  benchmarkRunListOptions,
  extractBenchmarkErrorCode,
  useBenchmarksUI,
} from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useWSEvent } from "@multica/core/realtime";
import type { BenchmarkRun } from "@multica/core/types";
import { timeAgo } from "@multica/core/utils";
import { Alert, AlertDescription, AlertTitle } from "@multica/ui/components/ui/alert";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useNavigation } from "../navigation";
import { PageHeader } from "../layout/page-header";
import { useT } from "../i18n";
import { StatusPill } from "./StatusPill";
import { useBenchmarkErrorMessage } from "./error-message";

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
    <span className={`inline-flex items-center rounded px-1.5 py-0.5 text-xs font-medium ${cls}`}>
      {label}
    </span>
  );
}

function PageHeaderBar({
  totalCount,
  createHref,
}: {
  totalCount: number;
  createHref: string;
}) {
  const navigation = useNavigation();
  const { t } = useT("benchmarks");
  return (
    <PageHeader className="justify-between px-5">
      <div className="flex items-center gap-2">
        <Activity className="h-4 w-4 text-muted-foreground" />
        <h1 className="text-sm font-medium">{t(($) => $.runs_list.page_title)}</h1>
        {totalCount > 0 && (
          <span className="font-mono text-xs tabular-nums text-muted-foreground/70">
            {totalCount}
          </span>
        )}
      </div>
      <Button
        type="button"
        size="sm"
        onClick={() => navigation.push(createHref)}
      >
        <Plus className="h-3 w-3" />
        {t(($) => $.runs_list.create_cta)}
      </Button>
    </PageHeader>
  );
}

function EmptyState({ createHref }: { createHref: string }) {
  const navigation = useNavigation();
  const { t } = useT("benchmarks");
  return (
    <div className="flex flex-1 flex-col items-center justify-center px-6 py-16 text-center">
      <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
        <Activity className="h-6 w-6 text-muted-foreground" />
      </div>
      <h2 className="mt-4 text-base font-semibold">
        {t(($) => $.runs_list.empty_title)}
      </h2>
      <p className="mt-1 max-w-md text-sm text-muted-foreground">
        {t(($) => $.runs_list.empty_description)}
      </p>
      <Button
        type="button"
        size="sm"
        className="mt-5"
        onClick={() => navigation.push(createHref)}
      >
        <Plus className="h-3 w-3" />
        {t(($) => $.runs_list.create_cta)}
      </Button>
    </div>
  );
}

function LoadingState({ createHref }: { createHref: string }) {
  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeaderBar totalCount={0} createHref={createHref} />
      <div className="flex flex-1 min-h-0 flex-col gap-4 p-6">
        <Skeleton className="h-8 w-64 rounded-md" />
        <div className="space-y-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-12 w-full rounded-md" />
          ))}
        </div>
      </div>
    </div>
  );
}

function ErrorBanner({ error }: { error: unknown }) {
  const { t } = useT("benchmarks");
  const messageFn = useBenchmarkErrorMessage({
    slug_taken: (t) => t(($) => $.errors.slug_taken_suite),
  });
  const code = extractBenchmarkErrorCode(error);
  const message = code
    ? messageFn(code)
    : error instanceof Error
      ? error.message
      : t(($) => $.runs_list.error_title);
  return (
    <Alert variant="destructive">
      <AlertCircle />
      <AlertTitle>{t(($) => $.runs_list.error_title)}</AlertTitle>
      <AlertDescription>{message}</AlertDescription>
    </Alert>
  );
}

interface RunRowProps {
  run: BenchmarkRun;
  onOpen: () => void;
}

function RunRow({ run, onOpen }: RunRowProps) {
  return (
    <tr
      className="cursor-pointer border-t border-border/60 transition-colors hover:bg-muted/40"
      onClick={onOpen}
    >
      <td className="px-4 py-3 text-sm">{run.display_name}</td>
      <td className="px-4 py-3">
        <StatusPill status={run.status} />
      </td>
      <td className="px-4 py-3 font-mono text-xs text-muted-foreground">
        {run.suite_id}
      </td>
      <td className="px-4 py-3 font-mono text-xs text-muted-foreground">
        {run.profile_id}
      </td>
      <td className="px-4 py-3">
        <ModeBadge mode={run.evaluator_mode} />
      </td>
      <td className="px-4 py-3 text-xs text-muted-foreground">
        {timeAgo(run.created_at)}
      </td>
    </tr>
  );
}

export default function RunsList() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const { t } = useT("benchmarks");
  const qc = useQueryClient();
  const runFilter = useBenchmarksUI((s) => s.runFilter);
  const setRunFilter = useBenchmarksUI((s) => s.setRunFilter);

  const createHref = paths.benchmarkRunNew();

  const {
    data: runs = [],
    isLoading,
    error,
  } = useQuery(benchmarkRunListOptions(wsId));

  // Live updates: any run lifecycle event in the workspace can change a row
  // (new row on `created`, status badge on `status` / `completed`). Single
  // invalidate hits the list cache; per-event payload filtering isn't useful
  // here because the list shows every run.
  const invalidateRuns = useCallback(() => {
    qc.invalidateQueries({ queryKey: benchmarkKeys.runs(wsId) });
  }, [qc, wsId]);
  useWSEvent("benchmark_run:created", invalidateRuns);
  useWSEvent("benchmark_run:status", invalidateRuns);
  useWSEvent("benchmark_run:completed", invalidateRuns);

  const filtered = useMemo(() => {
    const q = runFilter.trim().toLowerCase();
    if (!q) return runs;
    return runs.filter((r) => r.display_name.toLowerCase().includes(q));
  }, [runs, runFilter]);

  if (isLoading) {
    return <LoadingState createHref={createHref} />;
  }

  const totalCount = runs.length;
  const showEmpty = !error && totalCount === 0;

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeaderBar totalCount={totalCount} createHref={createHref} />

      <div className="flex flex-1 min-h-0 flex-col gap-4 p-6">
        {error && <ErrorBanner error={error} />}

        {showEmpty ? (
          <EmptyState createHref={createHref} />
        ) : !error ? (
          <div className="flex flex-1 min-h-0 flex-col overflow-hidden rounded-lg border bg-background">
            <div className="flex h-12 shrink-0 items-center gap-2 border-b px-4">
              <div className="relative">
                <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={runFilter}
                  onChange={(e) => setRunFilter(e.target.value)}
                  placeholder={t(($) => $.runs_list.filter_placeholder)}
                  className="h-8 w-64 pl-8 text-sm"
                />
              </div>
            </div>
            {filtered.length === 0 ? (
              <div className="flex flex-1 flex-col items-center justify-center gap-2 px-4 py-16 text-center text-muted-foreground">
                <Search className="h-8 w-8 text-muted-foreground/40" />
                <p className="text-sm">
                  {t(($) => $.runs_list.filter_no_match_title)}
                </p>
                <p className="text-xs">
                  {t(($) => $.runs_list.filter_no_match_description)}
                </p>
              </div>
            ) : (
              <div className="flex-1 overflow-auto">
                <table className="w-full text-left">
                  <thead className="sticky top-0 z-10 bg-muted/40 text-xs uppercase tracking-wide text-muted-foreground">
                    <tr>
                      <th className="px-4 py-2 font-medium">
                        {t(($) => $.runs_list.col_display_name)}
                      </th>
                      <th className="px-4 py-2 font-medium">
                        {t(($) => $.runs_list.col_status)}
                      </th>
                      <th className="px-4 py-2 font-medium">
                        {t(($) => $.runs_list.col_suite)}
                      </th>
                      <th className="px-4 py-2 font-medium">
                        {t(($) => $.runs_list.col_profile)}
                      </th>
                      <th className="px-4 py-2 font-medium">
                        {t(($) => $.runs_list.col_mode)}
                      </th>
                      <th className="px-4 py-2 font-medium">
                        {t(($) => $.runs_list.col_created)}
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {filtered.map((run) => (
                      <RunRow
                        key={run.id}
                        run={run}
                        onOpen={() =>
                          navigation.push(paths.benchmarkRunDetail(run.id))
                        }
                      />
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        ) : null}
      </div>
    </div>
  );
}
