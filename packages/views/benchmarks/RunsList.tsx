"use client";

// English literal strings live here intentionally for Phase 1c-T03; the
// dedicated i18n pass for runs UI is tracked as P1c-T06 and will replace
// every hard-coded label below with `useT("benchmarks").t(...)` lookups.
/* eslint-disable i18next/no-literal-string */

import { useMemo } from "react";
import { AlertCircle, Activity, Plus, Search } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import {
  benchmarkRunListOptions,
  extractBenchmarkErrorCode,
  useBenchmarksUI,
} from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { BenchmarkErrorCode, BenchmarkRun } from "@multica/core/types";
import { Alert, AlertDescription, AlertTitle } from "@multica/ui/components/ui/alert";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useNavigation } from "../navigation";
import { PageHeader } from "../layout/page-header";
import { StatusPill } from "./StatusPill";

/**
 * Map a benchmark error code to a user-facing English message. The list
 * endpoint can realistically only fail with auth / workspace-context errors;
 * write-side codes (slug_taken, bad_body, …) are still covered so a missing
 * case becomes a type error if the union grows.
 *
 * T06 will replace this with the shared `messageForCode` translator pattern
 * used in SuitesList / ProfilesList.
 */
function messageForCode(code: BenchmarkErrorCode): string {
  switch (code) {
    case "unauthenticated":
      return "You must be signed in to view runs.";
    case "workspace_required":
    case "bad_workspace_id":
    case "bad_user_id":
      return "Workspace context is missing or invalid.";
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
    case "bad_id":
      return "Invalid id.";
  }
}

function ModeBadge({ mode }: { mode: BenchmarkRun["evaluator_mode"] }) {
  const cls =
    mode === "managed"
      ? "bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-200"
      : "bg-slate-100 text-slate-800 dark:bg-slate-800 dark:text-slate-200";
  return (
    <span className={`inline-flex items-center rounded px-1.5 py-0.5 text-xs font-medium ${cls}`}>
      {mode}
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
  return (
    <PageHeader className="justify-between px-5">
      <div className="flex items-center gap-2">
        <Activity className="h-4 w-4 text-muted-foreground" />
        <h1 className="text-sm font-medium">Runs</h1>
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
        Start run
      </Button>
    </PageHeader>
  );
}

function EmptyState({ createHref }: { createHref: string }) {
  const navigation = useNavigation();
  return (
    <div className="flex flex-1 flex-col items-center justify-center px-6 py-16 text-center">
      <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
        <Activity className="h-6 w-6 text-muted-foreground" />
      </div>
      <h2 className="mt-4 text-base font-semibold">No runs yet</h2>
      <p className="mt-1 max-w-md text-sm text-muted-foreground">
        Start a run to benchmark your agent against a suite.
      </p>
      <Button
        type="button"
        size="sm"
        className="mt-5"
        onClick={() => navigation.push(createHref)}
      >
        <Plus className="h-3 w-3" />
        Start run
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
  const code = extractBenchmarkErrorCode(error);
  const message = code
    ? messageForCode(code)
    : error instanceof Error
      ? error.message
      : "Failed to load runs.";
  return (
    <Alert variant="destructive">
      <AlertCircle />
      <AlertTitle>Failed to load runs</AlertTitle>
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
    </tr>
  );
}

export default function RunsList() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const runFilter = useBenchmarksUI((s) => s.runFilter);
  const setRunFilter = useBenchmarksUI((s) => s.setRunFilter);

  const createHref = paths.benchmarkRunNew();

  const {
    data: runs = [],
    isLoading,
    error,
  } = useQuery(benchmarkRunListOptions(wsId));

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
                  placeholder="Filter by name…"
                  className="h-8 w-64 pl-8 text-sm"
                />
              </div>
            </div>
            {filtered.length === 0 ? (
              <div className="flex flex-1 flex-col items-center justify-center gap-2 px-4 py-16 text-center text-muted-foreground">
                <Search className="h-8 w-8 text-muted-foreground/40" />
                <p className="text-sm">No runs match your filter.</p>
              </div>
            ) : (
              <div className="flex-1 overflow-auto">
                <table className="w-full text-left">
                  <thead className="sticky top-0 z-10 bg-muted/40 text-xs uppercase tracking-wide text-muted-foreground">
                    <tr>
                      <th className="px-4 py-2 font-medium">Display name</th>
                      <th className="px-4 py-2 font-medium">Status</th>
                      <th className="px-4 py-2 font-medium">Suite</th>
                      <th className="px-4 py-2 font-medium">Profile</th>
                      <th className="px-4 py-2 font-medium">Mode</th>
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
