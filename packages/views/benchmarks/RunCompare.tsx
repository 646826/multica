"use client";

import { AlertCircle, ArrowLeft } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import {
  benchmarkCompareOptions,
  benchmarkRunDetailOptions,
} from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { ComparisonInstance } from "@multica/core/types";
import {
  Alert,
  AlertDescription,
  AlertTitle,
} from "@multica/ui/components/ui/alert";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useNavigation } from "../navigation";
import { PageHeader } from "../layout/page-header";
import { useT } from "../i18n";

/**
 * Compare two completed benchmark runs of the same suite. Shows a delta
 * summary, partitions instances into improved/regressed/newly-resolved/
 * lost-resolved buckets, and lists added/cleared failure categories.
 *
 * The route is `/benchmarks/runs/<candID>/compare/<baseID>` — `candID` is
 * the "current" run and `baseID` is the baseline being compared against.
 */
export default function RunCompare({
  candID,
  baseID,
}: {
  candID: string;
  baseID: string;
}) {
  const { t } = useT("benchmarks");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();

  const {
    data: comparison,
    isLoading,
    error,
  } = useQuery(benchmarkCompareOptions(wsId, candID, baseID));
  const { data: candRun } = useQuery(benchmarkRunDetailOptions(wsId, candID));
  const { data: baseRun } = useQuery(benchmarkRunDetailOptions(wsId, baseID));

  const goBack = () => navigation.push(paths.benchmarkRunDetail(candID));

  if (isLoading) {
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <HeaderBar
          title={t(($) => $.compare.loading)}
          onBack={goBack}
          backLabel={t(($) => $.run_detail.back)}
        />
        <div className="flex flex-1 min-h-0 flex-col gap-4 p-6">
          <Skeleton className="h-8 w-64 rounded-md" />
          <Skeleton className="h-32 w-full rounded-md" />
          <Skeleton className="h-32 w-full rounded-md" />
        </div>
      </div>
    );
  }

  if (error || !comparison) {
    const message =
      error instanceof Error && error.message
        ? error.message
        : t(($) => $.compare.error_title);
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <HeaderBar
          title={t(($) => $.compare.error_title)}
          onBack={goBack}
          backLabel={t(($) => $.run_detail.back)}
        />
        <div className="flex flex-1 min-h-0 flex-col gap-4 p-6">
          <Alert variant="destructive" role="alert">
            <AlertCircle />
            <AlertTitle>{t(($) => $.compare.error_title)}</AlertTitle>
            <AlertDescription>{message}</AlertDescription>
          </Alert>
        </div>
      </div>
    );
  }

  const candName = candRun?.display_name ?? candID;
  const baseName = baseRun?.display_name ?? baseID;

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <HeaderBar
        title={t(($) => $.compare.title)}
        onBack={goBack}
        backLabel={t(($) => $.compare.back, { name: candName })}
      />

      <div className="flex flex-1 min-h-0 flex-col gap-6 overflow-auto p-6">
        <p className="text-sm text-muted-foreground">
          <span className="font-mono">{baseName}</span>{" "}
          <span aria-hidden="true">→</span>{" "}
          <span className="font-mono">{candName}</span>
        </p>

        {comparison.partial && (
          <Alert variant="default" role="status">
            <AlertTitle>{t(($) => $.compare.partial_title)}</AlertTitle>
            <AlertDescription>
              {t(($) => $.compare.partial_description)}
            </AlertDescription>
          </Alert>
        )}

        <section className="rounded-lg border bg-background p-4">
          <h2 className="mb-3 text-sm font-medium">
            {t(($) => $.compare.summary_title)}
          </h2>
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b text-xs uppercase tracking-wide text-muted-foreground">
                <th className="py-2 text-left font-normal"></th>
                <th className="py-2 text-right font-normal">
                  {t(($) => $.compare.col_delta)}
                </th>
              </tr>
            </thead>
            <tbody>
              <DeltaRow
                label={t(($) => $.compare.row_resolved)}
                delta={comparison.delta.resolved}
              />
              <DeltaRow
                label={t(($) => $.compare.row_avg_pr)}
                delta={comparison.delta.avg_pass_rate}
                format="rate"
              />
              <DeltaRow
                label={t(($) => $.compare.row_agg_pr)}
                delta={comparison.delta.agg_pass_rate}
                format="rate"
              />
              <DeltaRow
                label={t(($) => $.compare.row_errored)}
                delta={comparison.delta.errored}
                invertGood
              />
            </tbody>
          </table>
        </section>

        <BucketSection
          title={t(($) => $.compare.section_improved, {
            count: comparison.improved.length,
          })}
          items={comparison.improved}
        />
        <BucketSection
          title={t(($) => $.compare.section_regressed, {
            count: comparison.regressed.length,
          })}
          items={comparison.regressed}
        />
        <BucketSection
          title={t(($) => $.compare.section_newly_resolved, {
            count: comparison.newly_resolved.length,
          })}
          items={comparison.newly_resolved}
        />
        <BucketSection
          title={t(($) => $.compare.section_lost_resolved, {
            count: comparison.lost_resolved.length,
          })}
          items={comparison.lost_resolved}
        />

        <section className="rounded-lg border bg-background p-4">
          <h2 className="mb-3 text-sm font-medium">
            {t(($) => $.compare.categories_title)}
          </h2>
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            <div>
              <h3 className="mb-2 text-xs uppercase tracking-wide text-muted-foreground">
                {t(($) => $.compare.categories_added)} (
                {comparison.categories.added.length})
              </h3>
              <CategoryList items={comparison.categories.added} />
            </div>
            <div>
              <h3 className="mb-2 text-xs uppercase tracking-wide text-muted-foreground">
                {t(($) => $.compare.categories_cleared)} (
                {comparison.categories.cleared.length})
              </h3>
              <CategoryList items={comparison.categories.cleared} />
            </div>
          </div>
        </section>
      </div>
    </div>
  );
}

function HeaderBar({
  title,
  onBack,
  backLabel,
}: {
  title: string;
  onBack: () => void;
  backLabel: string;
}) {
  return (
    <PageHeader className="justify-between gap-2 px-5">
      <div className="flex items-center gap-2">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={backLabel}
          onClick={onBack}
        >
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <h1 className="text-sm font-medium">{title}</h1>
      </div>
    </PageHeader>
  );
}

function BucketSection({
  title,
  items,
}: {
  title: string;
  items: ComparisonInstance[];
}) {
  return (
    <section className="rounded-lg border bg-background p-4">
      <h2 className="mb-2 text-sm font-medium">{title}</h2>
      <InstanceList items={items} />
    </section>
  );
}

function InstanceList({ items }: { items: ComparisonInstance[] }) {
  if (items.length === 0) {
    return <p className="text-sm text-muted-foreground">—</p>;
  }
  return (
    <ul className="text-sm">
      {items.map((i) => (
        <li
          key={i.instance_id}
          className="flex items-center justify-between gap-4 border-b py-1.5 last:border-b-0"
        >
          <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
            {i.instance_id}
          </code>
          <span className="font-mono tabular-nums text-xs text-muted-foreground">
            {i.base_pass_rate.toFixed(3)}{" "}
            <span aria-hidden="true">→</span>{" "}
            {i.cand_pass_rate.toFixed(3)}
          </span>
        </li>
      ))}
    </ul>
  );
}

function CategoryList({ items }: { items: string[] }) {
  if (items.length === 0) {
    return <p className="text-sm text-muted-foreground">—</p>;
  }
  return (
    <ul className="flex flex-wrap gap-1.5">
      {items.map((c) => (
        <li key={c}>
          <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
            {c}
          </code>
        </li>
      ))}
    </ul>
  );
}

/**
 * Format a delta as `+N` / `-N` / `±0`. Colour-codes by direction:
 * green for "good", red for "bad", muted for zero. `invertGood` flips
 * the polarity for metrics where lower is better (e.g. errored count).
 */
function DeltaRow({
  label,
  delta,
  format,
  invertGood,
}: {
  label: string;
  delta: number;
  format?: "rate";
  invertGood?: boolean;
}) {
  const fmt = (n: number) =>
    format === "rate" ? n.toFixed(3) : String(n);
  const sign = delta > 0 ? "+" : delta < 0 ? "−" : "±";
  const magnitude = fmt(Math.abs(delta));
  const good = invertGood ? delta < 0 : delta > 0;
  const cls =
    delta === 0
      ? "text-muted-foreground"
      : good
        ? "text-green-600 dark:text-green-400"
        : "text-red-600 dark:text-red-400";
  return (
    <tr className="border-b last:border-b-0">
      <td className="py-1.5">{label}</td>
      <td
        className={`py-1.5 text-right font-mono tabular-nums ${cls}`}
      >
        {sign}
        {magnitude}
      </td>
    </tr>
  );
}
