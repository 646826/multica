"use client";

import { AlertCircle, ArrowLeft, Info } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { benchmarkProfileDetailOptions } from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
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
import { useBenchmarkErrorFallback } from "./error-message";

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
  const errorMessage = useBenchmarkErrorFallback({
    bad_id: (t) => t(($) => $.errors.bad_id_profile),
    slug_taken: (t) => t(($) => $.errors.slug_taken_profile),
  });

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
      ? errorMessage(error, t(($) => $.errors.load_profile_failed))
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
