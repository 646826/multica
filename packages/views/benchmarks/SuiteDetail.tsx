/* eslint-disable i18next/no-literal-string -- T22 will wrap user-facing strings in t(...) */
"use client";

import { AlertCircle, ArrowLeft } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import {
  benchmarkSuiteDetailOptions,
  extractBenchmarkErrorCode,
} from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { BenchmarkErrorCode } from "@multica/core/types";
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

/**
 * Map a benchmark error code to a user-facing message for the detail view.
 * Covers the full union so new codes are caught at compile time.
 */
function messageForCode(code: BenchmarkErrorCode): string {
  switch (code) {
    case "suite_not_found":
      return "Suite not found.";
    case "unauthenticated":
      return "Please sign in.";
    case "workspace_required":
    case "bad_workspace_id":
    case "bad_user_id":
      return "Workspace context is missing — try reloading the page.";
    case "bad_id":
      return "Invalid suite identifier.";
    case "internal_error":
      return "The server hit an internal error. Try again in a moment.";
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
  }
}

function errorMessage(err: unknown): string {
  const code = extractBenchmarkErrorCode(err);
  if (code) return messageForCode(code);
  if (err instanceof Error && err.message) return err.message;
  return "Failed to load suite.";
}

function HeaderBar({
  title,
  onBack,
}: {
  title: string;
  onBack: () => void;
}) {
  return (
    <PageHeader className="gap-2 px-5">
      <Button
        type="button"
        variant="ghost"
        size="icon"
        aria-label="Back to suites"
        onClick={onBack}
      >
        <ArrowLeft className="h-4 w-4" />
      </Button>
      <h1 className="text-sm font-medium">{title}</h1>
    </PageHeader>
  );
}

function LoadingState({ onBack }: { onBack: () => void }) {
  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <HeaderBar title="Suite" onBack={onBack} />
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

  const suitesBase = paths.benchmarkSuites();
  const goBack = () => navigation.push(suitesBase);

  const {
    data: suite,
    isLoading,
    error,
  } = useQuery(benchmarkSuiteDetailOptions(wsId, suiteId));

  if (isLoading) {
    return <LoadingState onBack={goBack} />;
  }

  if (error || !suite) {
    const message = error ? errorMessage(error) : "Suite not found.";
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <HeaderBar title="Suite" onBack={goBack} />
        <div className="flex flex-1 min-h-0 flex-col gap-4 p-6">
          <Alert variant="destructive">
            <AlertCircle />
            <AlertTitle>Couldn&apos;t load suite</AlertTitle>
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
              Back to suites
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
              Created {timeAgo(suite.created_at)}
            </span>
          </div>
          {description && (
            <p className="max-w-2xl text-sm text-muted-foreground">
              {description}
            </p>
          )}
        </div>

        <section className="flex flex-col gap-2">
          <div className="flex items-baseline gap-2">
            <h2 className="text-sm font-medium">Instances</h2>
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
          Suites become immutable once a run uses them. Edit freely until then.
        </p>
      </div>
    </div>
  );
}
