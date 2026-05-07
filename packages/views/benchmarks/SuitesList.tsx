/* eslint-disable i18next/no-literal-string -- T22 will wrap user-facing strings in t(...) */
"use client";

import { useMemo } from "react";
import { AlertCircle, ClipboardList, Plus, Search, Trash2 } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import {
  benchmarkSuiteListOptions,
  extractBenchmarkErrorCode,
  useBenchmarksUI,
  useDeleteBenchmarkSuite,
} from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { BenchmarkErrorCode, BenchmarkSuite } from "@multica/core/types";
import { timeAgo } from "@multica/core/utils";
import { Alert, AlertDescription, AlertTitle } from "@multica/ui/components/ui/alert";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useNavigation } from "../navigation";
import { PageHeader } from "../layout/page-header";

/**
 * Map a benchmark error code to a user-facing message. The list endpoint can
 * realistically only fail with auth / workspace-context errors; everything
 * else (slug_taken, bad_body, …) is a write-side concern. We still cover the
 * full union so the type checker keeps us honest if new codes appear.
 */
function messageForCode(code: BenchmarkErrorCode): string {
  switch (code) {
    case "unauthenticated":
      return "Please sign in.";
    case "workspace_required":
    case "bad_workspace_id":
    case "bad_user_id":
      return "Workspace context is missing — try reloading the page.";
    case "internal_error":
      return "The server hit an internal error. Try again in a moment.";
    case "suite_not_found":
      return "Suite not found.";
    case "profile_not_found":
      return "Profile not found.";
    case "agent_not_found":
      return "Agent not found.";
    case "slug_taken":
      return "That slug is already used by another suite.";
    case "instance_list_empty":
      return "Suite must include at least one task instance.";
    case "bad_body":
      return "The request body was malformed.";
    case "bad_id":
      return "Invalid identifier.";
  }
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
        <ClipboardList className="h-4 w-4 text-muted-foreground" />
        <h1 className="text-sm font-medium">Suites</h1>
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
        Create suite
      </Button>
    </PageHeader>
  );
}

function EmptyState({ createHref }: { createHref: string }) {
  const navigation = useNavigation();
  return (
    <div className="flex flex-1 flex-col items-center justify-center px-6 py-16 text-center">
      <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
        <ClipboardList className="h-6 w-6 text-muted-foreground" />
      </div>
      <h2 className="mt-4 text-base font-semibold">No suites yet</h2>
      <p className="mt-1 max-w-md text-sm text-muted-foreground">
        Create one to define a stable benchmark task set.
      </p>
      <Button
        type="button"
        size="sm"
        className="mt-5"
        onClick={() => navigation.push(createHref)}
      >
        <Plus className="h-3 w-3" />
        Create suite
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
      : "Failed to load suites.";
  return (
    <Alert variant="destructive">
      <AlertCircle />
      <AlertTitle>Couldn&apos;t load suites</AlertTitle>
      <AlertDescription>{message}</AlertDescription>
    </Alert>
  );
}

interface SuiteRowProps {
  suite: BenchmarkSuite;
  onOpen: () => void;
  onDelete: () => void;
  deleting: boolean;
}

function SuiteRow({ suite, onOpen, onDelete, deleting }: SuiteRowProps) {
  return (
    <tr
      className="cursor-pointer border-t border-border/60 transition-colors hover:bg-muted/40"
      onClick={onOpen}
    >
      <td className="px-4 py-3 font-mono text-xs">{suite.slug}</td>
      <td className="px-4 py-3 text-sm">{suite.display_name}</td>
      <td className="px-4 py-3 text-sm text-muted-foreground">
        {suite.adapter_kind}
      </td>
      <td className="px-4 py-3 text-sm tabular-nums text-muted-foreground">
        {suite.instance_ids.length}
      </td>
      <td className="px-4 py-3 text-sm text-muted-foreground">
        {timeAgo(suite.created_at)}
      </td>
      <td className="px-2 py-2 text-right">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={`Delete suite ${suite.slug}`}
          disabled={deleting}
          onClick={(e) => {
            e.stopPropagation();
            onDelete();
          }}
        >
          <Trash2 className="h-4 w-4" />
        </Button>
      </td>
    </tr>
  );
}

export default function SuitesList() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const suiteFilter = useBenchmarksUI((s) => s.suiteFilter);
  const setSuiteFilter = useBenchmarksUI((s) => s.setSuiteFilter);

  const suitesBase = paths.benchmarkSuites();
  const createHref = `${suitesBase}/new`;

  const {
    data: suites = [],
    isLoading,
    error,
  } = useQuery(benchmarkSuiteListOptions(wsId));

  const deleteSuite = useDeleteBenchmarkSuite();

  const filtered = useMemo(() => {
    const q = suiteFilter.trim().toLowerCase();
    if (!q) return suites;
    return suites.filter(
      (s) =>
        s.slug.toLowerCase().includes(q) ||
        s.display_name.toLowerCase().includes(q),
    );
  }, [suites, suiteFilter]);

  if (isLoading) {
    return <LoadingState createHref={createHref} />;
  }

  const totalCount = suites.length;
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
                  value={suiteFilter}
                  onChange={(e) => setSuiteFilter(e.target.value)}
                  placeholder="Filter by slug or name"
                  className="h-8 w-64 pl-8 text-sm"
                />
              </div>
            </div>
            {filtered.length === 0 ? (
              <div className="flex flex-1 flex-col items-center justify-center gap-2 px-4 py-16 text-center text-muted-foreground">
                <Search className="h-8 w-8 text-muted-foreground/40" />
                <p className="text-sm">No suites match your filter.</p>
              </div>
            ) : (
              <div className="flex-1 overflow-auto">
                <table className="w-full text-left">
                  <thead className="sticky top-0 z-10 bg-muted/40 text-xs uppercase tracking-wide text-muted-foreground">
                    <tr>
                      <th className="px-4 py-2 font-medium">Slug</th>
                      <th className="px-4 py-2 font-medium">Display name</th>
                      <th className="px-4 py-2 font-medium">Adapter</th>
                      <th className="px-4 py-2 font-medium">Tasks</th>
                      <th className="px-4 py-2 font-medium">Created</th>
                      <th className="px-4 py-2 font-medium" aria-label="Actions" />
                    </tr>
                  </thead>
                  <tbody>
                    {filtered.map((suite) => (
                      <SuiteRow
                        key={suite.id}
                        suite={suite}
                        onOpen={() =>
                          navigation.push(`${suitesBase}/${suite.id}`)
                        }
                        onDelete={() => deleteSuite.mutate(suite.id)}
                        deleting={
                          deleteSuite.isPending &&
                          deleteSuite.variables === suite.id
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
