"use client";

import { useCallback, useState } from "react";
import { AlertCircle, Trophy } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  benchmarkLeaderboardOptions,
  benchmarkSuiteListOptions,
  extractBenchmarkErrorCode,
} from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { useWSEvent } from "@multica/core/realtime";
import {
  Alert,
  AlertDescription,
  AlertTitle,
} from "@multica/ui/components/ui/alert";
import { Button } from "@multica/ui/components/ui/button";
import { Label } from "@multica/ui/components/ui/label";
import { NativeSelect, NativeSelectOption } from "@multica/ui/components/ui/native-select";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useNavigation } from "../navigation";
import { PageHeader } from "../layout/page-header";
import { useT } from "../i18n";
import { useBenchmarkErrorMessage } from "./error-message";

/**
 * Per-suite leaderboard. The user picks a suite from the dropdown; the
 * server returns one row per profile that has at least one completed run on
 * the suite, ranked by aggregate pass rate (best run wins). Rows link out
 * to the underlying best run.
 *
 * The suite selector is intentionally local state (no URL param) — phase 2
 * scope. A future iteration may promote it to a `?suite=...` query param.
 */
export default function Leaderboard() {
  const { t } = useT("benchmarks");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();

  const qc = useQueryClient();
  const [suiteSlug, setSuiteSlug] = useState("");
  const { data: suites = [] } = useQuery(benchmarkSuiteListOptions(wsId));
  const lb = useQuery(benchmarkLeaderboardOptions(wsId, suiteSlug));

  // A run completing can change the leaderboard for its suite. We don't have
  // the suite_slug in the payload, so invalidate every leaderboard query for
  // this workspace via a prefix match — the open suite refetches; closed
  // ones stay invalidated and refetch on next mount.
  const invalidateLeaderboards = useCallback(() => {
    qc.invalidateQueries({ queryKey: ["benchmarks", wsId, "leaderboard"] });
  }, [qc, wsId]);
  useWSEvent("benchmark_run:completed", invalidateLeaderboards);

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeader className="px-5">
        <div className="flex items-center gap-2">
          <Trophy className="h-4 w-4 text-muted-foreground" />
          <h1 className="text-sm font-medium">
            {t(($) => $.leaderboard.page_title)}
          </h1>
        </div>
      </PageHeader>

      <div className="flex flex-1 min-h-0 flex-col gap-4 overflow-auto p-6">
        <div className="flex max-w-md flex-col gap-1.5">
          <Label htmlFor="leaderboard-suite-select">
            {t(($) => $.leaderboard.suite_label)}
          </Label>
          <NativeSelect
            id="leaderboard-suite-select"
            className="w-full"
            value={suiteSlug}
            onChange={(e) => setSuiteSlug(e.target.value)}
            disabled={suites.length === 0}
          >
            <NativeSelectOption value="">
              {t(($) => $.leaderboard.suite_placeholder)}
            </NativeSelectOption>
            {suites.map((s) => (
              <NativeSelectOption key={s.id} value={s.slug}>
                {s.display_name}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>

        {!suiteSlug && (
          <p className="text-sm text-muted-foreground">
            {t(($) => $.leaderboard.empty_no_suite)}
          </p>
        )}

        {suiteSlug && lb.isLoading && (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-8 w-64 rounded-md" />
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-10 w-full rounded-md" />
            ))}
          </div>
        )}

        {suiteSlug && lb.error && <ErrorBanner error={lb.error} />}

        {suiteSlug &&
          !lb.isLoading &&
          !lb.error &&
          lb.data &&
          lb.data.length === 0 && (
            <p className="text-sm text-muted-foreground">
              {t(($) => $.leaderboard.empty_no_runs)}
            </p>
          )}

        {suiteSlug && lb.data && lb.data.length > 0 && (
          <div className="overflow-hidden rounded-lg border bg-background">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-xs uppercase tracking-wide text-muted-foreground">
                <tr>
                  <th className="px-4 py-2 text-left font-medium">
                    {t(($) => $.leaderboard.col_rank)}
                  </th>
                  <th className="px-4 py-2 text-left font-medium">
                    {t(($) => $.leaderboard.col_profile)}
                  </th>
                  <th className="px-4 py-2 text-left font-medium">
                    {t(($) => $.leaderboard.col_best_run)}
                  </th>
                  <th className="px-4 py-2 text-right font-medium">
                    {t(($) => $.leaderboard.col_resolved)}
                  </th>
                  <th className="px-4 py-2 text-right font-medium">
                    {t(($) => $.leaderboard.col_avg_pr)}
                  </th>
                  <th className="px-4 py-2 text-right font-medium">
                    {t(($) => $.leaderboard.col_agg_pr)}
                  </th>
                  <th className="px-4 py-2 text-left font-medium">
                    {t(($) => $.leaderboard.col_completed)}
                  </th>
                </tr>
              </thead>
              <tbody>
                {lb.data.map((row) => (
                  <tr
                    key={row.profile_id}
                    className="border-t border-border/60 transition-colors hover:bg-muted/40"
                  >
                    <td className="px-4 py-3 font-mono tabular-nums text-sm">
                      {row.rank}
                    </td>
                    <td className="px-4 py-3">
                      <div className="font-medium">
                        {row.profile_display_name}
                      </div>
                      <code className="text-xs text-muted-foreground">
                        {row.profile_slug}
                      </code>
                    </td>
                    <td className="px-4 py-3">
                      <Button
                        type="button"
                        variant="link"
                        size="sm"
                        className="h-auto px-0"
                        onClick={() =>
                          navigation.push(
                            paths.benchmarkRunDetail(row.best_run_id),
                          )
                        }
                      >
                        {row.best_run_display_name}
                      </Button>
                    </td>
                    <td className="px-4 py-3 text-right font-mono tabular-nums">
                      {row.resolved_count}/{row.total_count}
                    </td>
                    <td className="px-4 py-3 text-right font-mono tabular-nums">
                      {row.average_pass_rate.toFixed(3)}
                    </td>
                    <td className="px-4 py-3 text-right font-mono tabular-nums">
                      {row.aggregate_pass_rate.toFixed(3)}
                    </td>
                    <td className="px-4 py-3 text-xs text-muted-foreground">
                      {row.completed_at}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
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
      : t(($) => $.leaderboard.error_title);
  return (
    <Alert variant="destructive">
      <AlertCircle />
      <AlertTitle>{t(($) => $.leaderboard.error_title)}</AlertTitle>
      <AlertDescription>{message}</AlertDescription>
    </Alert>
  );
}
