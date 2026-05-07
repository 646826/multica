/* eslint-disable i18next/no-literal-string -- T22 will wrap user-facing strings in t(...) */
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

/**
 * Map a benchmark error code to a user-facing message for the profile detail
 * view. Covers the full union so new codes are caught at compile time.
 */
function messageForCode(code: BenchmarkErrorCode): string {
  switch (code) {
    case "profile_not_found":
      return "Profile not found.";
    case "unauthenticated":
      return "Please sign in.";
    case "workspace_required":
    case "bad_workspace_id":
    case "bad_user_id":
      return "Workspace context is missing — try reloading the page.";
    case "bad_id":
      return "Invalid profile identifier.";
    case "internal_error":
      return "The server hit an internal error. Try again in a moment.";
    case "suite_not_found":
      return "Suite not found.";
    case "agent_not_found":
      return "Agent not found.";
    case "slug_taken":
      return "That slug is already used by another profile.";
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
  return "Failed to load profile.";
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
        aria-label="Back to profiles"
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
      <HeaderBar title="Profile" onBack={onBack} />
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
    const message = error ? errorMessage(error) : "Profile not found.";
    return (
      <div className="flex flex-1 min-h-0 flex-col">
        <HeaderBar title="Profile" onBack={goBack} />
        <div className="flex flex-1 min-h-0 flex-col gap-4 p-6">
          <Alert variant="destructive">
            <AlertCircle />
            <AlertTitle>Couldn&apos;t load profile</AlertTitle>
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
              Back to profiles
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
            <AlertTitle>Duplicate of an existing profile</AlertTitle>
            <AlertDescription>
              An identical profile already exists at id{" "}
              <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">
                {profile.duplicate_of}
              </code>
              .
            </AlertDescription>
          </Alert>
        )}

        <section className="flex flex-col gap-2">
          <div className="flex items-baseline gap-2">
            <h2 className="text-sm font-medium">Attached skills</h2>
            <span className="font-mono text-xs tabular-nums text-muted-foreground/70">
              {profile.attached_skills.length}
            </span>
          </div>
          {profile.attached_skills.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              No attached skills.
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
          <h2 className="text-sm font-medium">Prompt source</h2>
          <details className="rounded-lg border bg-background">
            <summary className="cursor-pointer select-none px-3 py-2 text-sm text-muted-foreground hover:text-foreground">
              Show prompt source
            </summary>
            <pre className="overflow-auto whitespace-pre-wrap break-words rounded-b-lg bg-muted px-3 py-2 font-mono text-xs">
              {profile.prompt_source}
            </pre>
          </details>
        </section>

        <p className="text-xs text-muted-foreground">
          Profiles are immutable snapshots — the prompt and attached skills
          are frozen at capture time.
        </p>
      </div>
    </div>
  );
}
