/* eslint-disable i18next/no-literal-string -- T22 will wrap user-facing strings in t(...) */
"use client";

import { useState, type FormEvent } from "react";
import { AlertCircle, ArrowLeft } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import {
  extractBenchmarkErrorCode,
  useCaptureBenchmarkProfile,
} from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { agentListOptions } from "@multica/core/workspace/queries";
import type { BenchmarkErrorCode } from "@multica/core/types";
import {
  Alert,
  AlertDescription,
  AlertTitle,
} from "@multica/ui/components/ui/alert";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  NativeSelect,
  NativeSelectOption,
} from "@multica/ui/components/ui/native-select";
import { useNavigation } from "../navigation";
import { PageHeader } from "../layout/page-header";

/**
 * Map a benchmark error code to a user-facing message for the capture form.
 * Covers the full union so new codes are caught at compile time.
 */
function messageForCode(code: BenchmarkErrorCode): string {
  switch (code) {
    case "agent_not_found":
      return "Agent not found in this workspace.";
    case "slug_taken":
      return "Slug already in use — pick a different one.";
    case "instance_list_empty":
      return "Add at least one instance id.";
    case "bad_body":
      return "The form data was rejected by the server.";
    case "bad_id":
      return "Invalid identifier.";
    case "bad_user_id":
    case "bad_workspace_id":
    case "workspace_required":
      return "Workspace context is missing — try reloading the page.";
    case "unauthenticated":
      return "Please sign in.";
    case "internal_error":
      return "The server hit an internal error. Try again in a moment.";
    case "suite_not_found":
      return "Suite not found.";
    case "profile_not_found":
      return "Profile not found.";
  }
}

function errorMessage(err: unknown): string {
  const code = extractBenchmarkErrorCode(err);
  if (code) return messageForCode(code);
  if (err instanceof Error && err.message) return err.message;
  return "Failed to capture profile.";
}

export default function ProfileCapture() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const captureProfile = useCaptureBenchmarkProfile();

  const profilesBase = paths.benchmarkProfiles();

  const { data: agents = [], isLoading: agentsLoading } = useQuery(
    agentListOptions(wsId),
  );

  const [agentId, setAgentId] = useState("");
  const [slug, setSlug] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [validationError, setValidationError] = useState<string | null>(null);

  const goBack = () => navigation.push(profilesBase);

  const onSubmit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setValidationError(null);

    const trimmedSlug = slug.trim();
    const trimmedName = displayName.trim();

    if (!agentId) {
      setValidationError("Pick an agent to snapshot.");
      return;
    }
    if (!trimmedSlug) {
      setValidationError("Slug is required.");
      return;
    }
    if (!trimmedName) {
      setValidationError("Display name is required.");
      return;
    }

    try {
      const result = await captureProfile.mutateAsync({
        agent_id: agentId,
        slug: trimmedSlug,
        display_name: trimmedName,
      });
      navigation.push(`${profilesBase}/${result.id}`);
    } catch {
      // Error is rendered from `captureProfile.error` below.
    }
  };

  const submitError = captureProfile.error
    ? errorMessage(captureProfile.error)
    : null;
  const inlineError = validationError ?? submitError;

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeader className="gap-2 px-5">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label="Back to profiles"
          onClick={goBack}
        >
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <h1 className="text-sm font-medium">Capture profile</h1>
      </PageHeader>

      <div className="flex flex-1 min-h-0 flex-col overflow-auto p-6">
        <form
          onSubmit={onSubmit}
          className="flex w-full max-w-2xl flex-col gap-5"
        >
          {inlineError && (
            <Alert variant="destructive">
              <AlertCircle />
              <AlertTitle>Couldn&apos;t capture profile</AlertTitle>
              <AlertDescription>{inlineError}</AlertDescription>
            </Alert>
          )}

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="profile-agent">Agent</Label>
            <NativeSelect
              id="profile-agent"
              value={agentId}
              onChange={(e) => setAgentId(e.target.value)}
              required
              disabled={agentsLoading}
              className="w-full max-w-md"
            >
              <NativeSelectOption value="">
                {agentsLoading
                  ? "Loading agents…"
                  : agents.length === 0
                    ? "No agents in this workspace"
                    : "Select an agent…"}
              </NativeSelectOption>
              {agents.map((agent) => (
                <NativeSelectOption key={agent.id} value={agent.id}>
                  {agent.name}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <p className="text-xs text-muted-foreground">
              The selected agent&apos;s prompt, model, and skills are
              snapshotted at capture time.
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="profile-slug">Slug</Label>
            <Input
              id="profile-slug"
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              placeholder="claude-opus-baseline"
              required
            />
            <p className="text-xs text-muted-foreground">
              Stable identifier, unique within this workspace.
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="profile-name">Display name</Label>
            <Input
              id="profile-name"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="Claude Opus baseline"
              required
            />
          </div>

          <div className="flex items-center gap-2 pt-2">
            <Button
              type="submit"
              size="sm"
              disabled={captureProfile.isPending}
            >
              {captureProfile.isPending ? "Capturing…" : "Capture profile"}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={goBack}
              disabled={captureProfile.isPending}
            >
              Cancel
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}
