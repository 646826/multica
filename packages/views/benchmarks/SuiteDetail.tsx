"use client";

import { AlertCircle, ArrowLeft, RefreshCw } from "lucide-react";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  benchmarkSuiteDetailOptions,
  extractBenchmarkErrorCode,
  useSyncBenchmarkSuite,
} from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { BenchmarkErrorCode, SuiteSyncResult } from "@multica/core/types";
import { timeAgo } from "@multica/core/utils";
import {
  Alert,
  AlertDescription,
  AlertTitle,
} from "@multica/ui/components/ui/alert";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useNavigation } from "../navigation";
import { PageHeader } from "../layout/page-header";
import { useT } from "../i18n";

type Translator = ReturnType<typeof useT<"benchmarks">>["t"];

/**
 * Map a benchmark error code to a user-facing message for the detail view.
 * Covers the full union so new codes are caught at compile time.
 */
function messageForCode(t: Translator, code: BenchmarkErrorCode): string {
  switch (code) {
    case "suite_not_found":
      return t(($) => $.errors.suite_not_found);
    case "unauthenticated":
      return t(($) => $.errors.unauthenticated);
    case "workspace_required":
    case "bad_workspace_id":
    case "bad_user_id":
      return t(($) => $.errors.workspace_context_missing);
    case "bad_id":
      return t(($) => $.errors.bad_id_suite);
    case "internal_error":
      return t(($) => $.errors.internal_error);
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
}

function errorMessage(t: Translator, err: unknown): string {
  const code = extractBenchmarkErrorCode(err);
  if (code) return messageForCode(t, code);
  if (err instanceof Error && err.message) return err.message;
  return t(($) => $.errors.load_suite_failed);
}

function HeaderBar({
  title,
  onBack,
}: {
  title: string;
  onBack: () => void;
}) {
  const { t } = useT("benchmarks");
  return (
    <PageHeader className="gap-2 px-5">
      <Button
        type="button"
        variant="ghost"
        size="icon"
        aria-label={t(($) => $.suite_detail.back_aria)}
        onClick={onBack}
      >
        <ArrowLeft className="h-4 w-4" />
      </Button>
      <h1 className="text-sm font-medium">{title}</h1>
    </PageHeader>
  );
}

function LoadingState({ onBack }: { onBack: () => void }) {
  const { t } = useT("benchmarks");
  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <HeaderBar title={t(($) => $.suite_detail.fallback_title)} onBack={onBack} />
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

export default function SuiteDetail({ suiteId }: { suiteId: string }) {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const { t } = useT("benchmarks");

  const suitesBase = paths.benchmarkSuites();
  const goBack = () => navigation.push(suitesBase);

  const {
    data: suite,
    isLoading,
    error,
  } = useQuery(benchmarkSuiteDetailOptions(wsId, suiteId));

  // Sync-from-catalog state. Result and error are mirrored into local state
  // so re-clicking the button reuses the rendered area without flashing
  // empty between the new pending and the new result.
  const sync = useSyncBenchmarkSuite();
  const [syncResult, setSyncResult] = useState<SuiteSyncResult | null>(null);
  const [syncError, setSyncError] = useState<unknown>(null);
  const onSyncClick = () => {
    setSyncError(null);
    sync.mutate(suiteId, {
      onSuccess: (data) => {
        setSyncResult(data);
      },
      onError: (err) => {
        setSyncResult(null);
        setSyncError(err);
      },
    });
  };

  if (isLoading) {
    return <LoadingState onBack={goBack} />;
  }

  if (error || !suite) {
    const message = error
      ? errorMessage(t, error)
      : t(($) => $.errors.suite_not_found);
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <HeaderBar title={t(($) => $.suite_detail.fallback_title)} onBack={goBack} />
        <div className="flex flex-1 min-h-0 flex-col gap-4 p-6">
          <Alert variant="destructive">
            <AlertCircle />
            <AlertTitle>{t(($) => $.suite_detail.error_title)}</AlertTitle>
            <AlertDescription>{message}</AlertDescription>
          </Alert>
          <div>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={goBack}
            >
              <ArrowLeft className="h-3 w-3" />
              {t(($) => $.suite_detail.back_link)}
            </Button>
          </div>
        </div>
      </div>
    );
  }

  const description = suite.description.trim();

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <HeaderBar title={suite.display_name} onBack={goBack} />

      <div className="flex flex-1 min-h-0 flex-col gap-6 overflow-auto p-6">
        <div className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
              {suite.slug}
            </code>
            <Badge variant="outline">{suite.adapter_kind}</Badge>
            <span className="text-xs text-muted-foreground">
              {t(($) => $.suite_detail.created_prefix, {
                when: timeAgo(suite.created_at),
              })}
            </span>
            <div className="ml-auto flex items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={onSyncClick}
                disabled={sync.isPending}
              >
                <RefreshCw className="h-3 w-3" />
                {sync.isPending
                  ? t(($) => $.suite_detail.sync_button_pending)
                  : t(($) => $.suite_detail.sync_button)}
              </Button>
            </div>
          </div>
          {description && (
            <p className="max-w-2xl text-sm text-muted-foreground">
              {description}
            </p>
          )}
        </div>

        {syncError != null && (
          <Alert variant="destructive">
            <AlertCircle />
            <AlertTitle>{t(($) => $.suite_detail.sync_error_title)}</AlertTitle>
            <AlertDescription>{errorMessage(t, syncError)}</AlertDescription>
          </Alert>
        )}

        {syncResult && (
          <section
            aria-label={t(($) => $.suite_detail.sync_result_aria)}
            className="flex flex-col gap-2 rounded-lg border bg-background p-3"
          >
            <div className="flex flex-wrap items-baseline gap-3 text-xs">
              <span className="font-medium">
                {t(($) => $.suite_detail.sync_result_resolved, {
                  count: syncResult.resolved.length,
                })}
              </span>
              <span
                className={
                  syncResult.unresolved.length > 0
                    ? "font-medium text-destructive"
                    : "font-medium"
                }
              >
                {t(($) => $.suite_detail.sync_result_unresolved, {
                  count: syncResult.unresolved.length,
                })}
              </span>
            </div>
            {syncResult.unresolved.length > 0 && (
              <ul className="flex flex-col gap-1">
                {syncResult.unresolved.map((iid) => (
                  <li key={iid}>
                    <code className="font-mono text-xs text-destructive">
                      {iid}
                    </code>
                  </li>
                ))}
              </ul>
            )}
          </section>
        )}

        <section className="flex flex-col gap-2">
          <div className="flex items-baseline gap-2">
            <h2 className="text-sm font-medium">
              {t(($) => $.suite_detail.instances_heading)}
            </h2>
            <span className="font-mono text-xs tabular-nums text-muted-foreground/70">
              {suite.instance_ids.length}
            </span>
          </div>
          <ul className="flex flex-col gap-1 rounded-lg border bg-background p-3">
            {suite.instance_ids.map((iid) => (
              <li key={iid}>
                <code className="font-mono text-xs">{iid}</code>
              </li>
            ))}
          </ul>
        </section>

        <p className="text-xs text-muted-foreground">
          {t(($) => $.suite_detail.immutable_note)}
        </p>
      </div>
    </div>
  );
}
