"use client";

import { useMemo } from "react";
import { AlertCircle, Camera, Plus, Search, Trash2 } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import {
  benchmarkProfileListOptions,
  extractBenchmarkErrorCode,
  useBenchmarksUI,
  useDeleteBenchmarkProfile,
} from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { BenchmarkErrorCode, BenchmarkProfile } from "@multica/core/types";
import { Alert, AlertDescription, AlertTitle } from "@multica/ui/components/ui/alert";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useNavigation } from "../navigation";
import { PageHeader } from "../layout/page-header";
import { useT } from "../i18n";

type Translator = ReturnType<typeof useT<"benchmarks">>["t"];

/**
 * Map a benchmark error code to a user-facing message. The list endpoint can
 * realistically only fail with auth / workspace-context errors; everything
 * else (slug_taken, bad_body, …) is a write-side concern. We still cover the
 * full union so the type checker keeps us honest if new codes appear.
 */
function messageForCode(t: Translator, code: BenchmarkErrorCode): string {
  switch (code) {
    case "unauthenticated":
      return t(($) => $.errors.unauthenticated);
    case "workspace_required":
    case "bad_workspace_id":
    case "bad_user_id":
      return t(($) => $.errors.workspace_context_missing);
    case "internal_error":
      return t(($) => $.errors.internal_error);
    case "suite_not_found":
      return t(($) => $.errors.suite_not_found);
    case "profile_not_found":
      return t(($) => $.errors.profile_not_found);
    case "agent_not_found":
      return t(($) => $.errors.agent_not_found);
    case "slug_taken":
      return t(($) => $.errors.slug_taken_profile);
    case "instance_list_empty":
      return t(($) => $.errors.instance_list_empty);
    case "bad_body":
      return t(($) => $.errors.bad_body);
    case "bad_id":
      return t(($) => $.errors.bad_id);
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
  const { t } = useT("benchmarks");
  return (
    <PageHeader className="justify-between px-5">
      <div className="flex items-center gap-2">
        <Camera className="h-4 w-4 text-muted-foreground" />
        <h1 className="text-sm font-medium">{t(($) => $.profiles_list.page_title)}</h1>
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
        {t(($) => $.profiles_list.create_cta)}
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
        <Camera className="h-6 w-6 text-muted-foreground" />
      </div>
      <h2 className="mt-4 text-base font-semibold">
        {t(($) => $.profiles_list.empty_title)}
      </h2>
      <p className="mt-1 max-w-md text-sm text-muted-foreground">
        {t(($) => $.profiles_list.empty_description)}
      </p>
      <Button
        type="button"
        size="sm"
        className="mt-5"
        onClick={() => navigation.push(createHref)}
      >
        <Plus className="h-3 w-3" />
        {t(($) => $.profiles_list.create_cta)}
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
  const code = extractBenchmarkErrorCode(error);
  const message = code
    ? messageForCode(t, code)
    : error instanceof Error
      ? error.message
      : t(($) => $.errors.load_profiles_failed);
  return (
    <Alert variant="destructive">
      <AlertCircle />
      <AlertTitle>{t(($) => $.profiles_list.error_title)}</AlertTitle>
      <AlertDescription>{message}</AlertDescription>
    </Alert>
  );
}

interface ProfileRowProps {
  profile: BenchmarkProfile;
  onOpen: () => void;
  onDelete: () => void;
  deleting: boolean;
}

function ProfileRow({ profile, onOpen, onDelete, deleting }: ProfileRowProps) {
  const { t } = useT("benchmarks");
  return (
    <tr
      className="cursor-pointer border-t border-border/60 transition-colors hover:bg-muted/40"
      onClick={onOpen}
    >
      <td className="px-4 py-3 font-mono text-xs">{profile.slug}</td>
      <td className="px-4 py-3 text-sm">{profile.display_name}</td>
      <td className="px-4 py-3 text-sm text-muted-foreground">
        {profile.agent_name}
      </td>
      <td className="px-4 py-3 text-sm text-muted-foreground">
        {profile.model}
      </td>
      <td className="px-4 py-3 font-mono text-xs text-muted-foreground">
        {profile.prompt_hash.slice(0, 8)}
      </td>
      <td className="px-2 py-2 text-right">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={t(($) => $.profiles_list.delete_aria, {
            slug: profile.slug,
          })}
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

export default function ProfilesList() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const { t } = useT("benchmarks");
  const profileFilter = useBenchmarksUI((s) => s.profileFilter);
  const setProfileFilter = useBenchmarksUI((s) => s.setProfileFilter);

  const profilesBase = paths.benchmarkProfiles();
  const createHref = `${profilesBase}/new`;

  const {
    data: profiles = [],
    isLoading,
    error,
  } = useQuery(benchmarkProfileListOptions(wsId));

  const deleteProfile = useDeleteBenchmarkProfile();

  const filtered = useMemo(() => {
    const q = profileFilter.trim().toLowerCase();
    if (!q) return profiles;
    return profiles.filter(
      (p) =>
        p.slug.toLowerCase().includes(q) ||
        p.display_name.toLowerCase().includes(q) ||
        p.agent_name.toLowerCase().includes(q),
    );
  }, [profiles, profileFilter]);

  if (isLoading) {
    return <LoadingState createHref={createHref} />;
  }

  const totalCount = profiles.length;
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
                  value={profileFilter}
                  onChange={(e) => setProfileFilter(e.target.value)}
                  placeholder={t(($) => $.profiles_list.filter_placeholder)}
                  className="h-8 w-64 pl-8 text-sm"
                />
              </div>
            </div>
            {filtered.length === 0 ? (
              <div className="flex flex-1 flex-col items-center justify-center gap-2 px-4 py-16 text-center text-muted-foreground">
                <Search className="h-8 w-8 text-muted-foreground/40" />
                <p className="text-sm">
                  {t(($) => $.profiles_list.filter_no_match)}
                </p>
              </div>
            ) : (
              <div className="flex-1 overflow-auto">
                <table className="w-full text-left">
                  <thead className="sticky top-0 z-10 bg-muted/40 text-xs uppercase tracking-wide text-muted-foreground">
                    <tr>
                      <th className="px-4 py-2 font-medium">
                        {t(($) => $.profiles_list.col_slug)}
                      </th>
                      <th className="px-4 py-2 font-medium">
                        {t(($) => $.profiles_list.col_display_name)}
                      </th>
                      <th className="px-4 py-2 font-medium">
                        {t(($) => $.profiles_list.col_agent)}
                      </th>
                      <th className="px-4 py-2 font-medium">
                        {t(($) => $.profiles_list.col_model)}
                      </th>
                      <th className="px-4 py-2 font-medium">
                        {t(($) => $.profiles_list.col_hash)}
                      </th>
                      <th
                        className="px-4 py-2 font-medium"
                        aria-label={t(($) => $.profiles_list.actions_aria)}
                      />
                    </tr>
                  </thead>
                  <tbody>
                    {filtered.map((profile) => (
                      <ProfileRow
                        key={profile.id}
                        profile={profile}
                        onOpen={() =>
                          navigation.push(`${profilesBase}/${profile.id}`)
                        }
                        onDelete={() => deleteProfile.mutate(profile.id)}
                        deleting={
                          deleteProfile.isPending &&
                          deleteProfile.variables === profile.id
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
