"use client";

import { AlertCircle, ArrowLeft, Info } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import {
  benchmarkProfileDetailOptions,
  extractBenchmarkErrorCode,
} from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { BenchmarkErrorCode } from "@multica/core/types";
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

type Translator = ReturnType<typeof useT<"benchmarks">>["t"];

/**
 * Map a benchmark error code to a user-facing message for the profile detail
 * view. Covers the full union so new codes are caught at compile time.
 */
function messageForCode(t: Translator, code: BenchmarkErrorCode): string {
  switch (code) {
    case "profile_not_found":
      return t(($) => $.errors.profile_not_found);
    case "unauthenticated":
      return t(($) => $.errors.unauthenticated);
    case "workspace_required":
    case "bad_workspace_id":
    case "bad_user_id":
      return t(($) => $.errors.workspace_context_missing);
    case "bad_id":
      return t(($) => $.errors.bad_id_profile);
    case "internal_error":
      return t(($) => $.errors.internal_error);
    case "suite_not_found":
      return t(($) => $.errors.suite_not_found);
    case "agent_not_found":
      return t(($) => $.errors.agent_not_found);
    case "slug_taken":
      return t(($) => $.errors.slug_taken_profile);
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
  return t(($) => $.errors.load_profile_failed);
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
        aria-label={t(($) => $.profile_detail.back_aria)}
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
      <HeaderBar title={t(($) => $.profile_detail.fallback_title)} onBack={onBack} />
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

export default function ProfileDetail({ profileId }: { profileId: string }) {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const { t } = useT("benchmarks");

  const profilesBase = paths.benchmarkProfiles();
  const goBack = () => navigation.push(profilesBase);

  const {
    data: profile,
    isLoading,
    error,
  } = useQuery(benchmarkProfileDetailOptions(wsId, profileId));

  if (isLoading) {
    return <LoadingState onBack={goBack} />;
  }

  if (error || !profile) {
    const message = error
      ? errorMessage(t, error)
      : t(($) => $.errors.profile_not_found);
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <HeaderBar
          title={t(($) => $.profile_detail.fallback_title)}
          onBack={goBack}
        />
        <div className="flex flex-1 min-h-0 flex-col gap-4 p-6">
          <Alert variant="destructive">
            <AlertCircle />
            <AlertTitle>{t(($) => $.profile_detail.error_title)}</AlertTitle>
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
              {t(($) => $.profile_detail.back_link)}
            </Button>
          </div>
        </div>
      </div>
    );
  }

  const truncatedHash =
    profile.prompt_hash.length > 12
      ? `${profile.prompt_hash.slice(0, 12)}…`
      : profile.prompt_hash;

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <HeaderBar title={profile.display_name} onBack={goBack} />

      <div className="flex flex-1 min-h-0 flex-col gap-6 overflow-auto p-6">
        <div className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
              {profile.slug}
            </code>
            <span>{profile.agent_name}</span>
            <span aria-hidden="true">·</span>
            <span>{profile.model}</span>
            <span aria-hidden="true">·</span>
            <code
              className="font-mono text-xs"
              title={profile.prompt_hash}
            >
              {truncatedHash}
            </code>
          </div>
        </div>

        {profile.duplicate_of && (
          <Alert variant="default">
            <Info />
            <AlertTitle>{t(($) => $.profile_detail.duplicate_title)}</AlertTitle>
            <AlertDescription>
              {t(($) => $.profile_detail.duplicate_description_prefix)}{" "}
              <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">
                {profile.duplicate_of}
              </code>
              {t(($) => $.profile_detail.duplicate_description_suffix)}
            </AlertDescription>
          </Alert>
        )}

        <section className="flex flex-col gap-2">
          <div className="flex items-baseline gap-2">
            <h2 className="text-sm font-medium">
              {t(($) => $.profile_detail.skills_heading)}
            </h2>
            <span className="font-mono text-xs tabular-nums text-muted-foreground/70">
              {profile.attached_skills.length}
            </span>
          </div>
          {profile.attached_skills.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {t(($) => $.profile_detail.skills_empty)}
            </p>
          ) : (
            <ul className="flex flex-col gap-1 rounded-lg border bg-background p-3">
              {profile.attached_skills.map((skill) => (
                <li key={`${skill.slug}@${skill.version}`}>
                  <code className="font-mono text-xs">
                    {skill.slug}@{skill.version}
                  </code>
                </li>
              ))}
            </ul>
          )}
        </section>

        <section className="flex flex-col gap-2">
          <h2 className="text-sm font-medium">
            {t(($) => $.profile_detail.prompt_heading)}
          </h2>
          <details className="rounded-lg border bg-background">
            <summary className="cursor-pointer select-none px-3 py-2 text-sm text-muted-foreground hover:text-foreground">
              {t(($) => $.profile_detail.prompt_show)}
            </summary>
            <pre className="overflow-auto whitespace-pre-wrap break-words rounded-b-lg bg-muted px-3 py-2 font-mono text-xs">
              {profile.prompt_source}
            </pre>
          </details>
        </section>

        <p className="text-xs text-muted-foreground">
          {t(($) => $.profile_detail.immutable_note)}
        </p>
      </div>
    </div>
  );
}
