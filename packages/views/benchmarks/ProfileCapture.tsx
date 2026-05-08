"use client";

import { useState, type FormEvent } from "react";
import { AlertCircle, ArrowLeft } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useCaptureBenchmarkProfile } from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { agentListOptions } from "@multica/core/workspace/queries";
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
import { useT } from "../i18n";
import { useBenchmarkErrorFallback } from "./error-message";

export default function ProfileCapture() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const captureProfile = useCaptureBenchmarkProfile();
  const { t } = useT("benchmarks");
  const errorMessage = useBenchmarkErrorFallback({
    agent_not_found: (t) => t(($) => $.errors.agent_not_found_in_workspace),
    slug_taken: (t) => t(($) => $.errors.slug_taken_pick_different),
    instance_list_empty: (t) => t(($) => $.errors.add_one_instance),
    bad_body: (t) => t(($) => $.errors.bad_form_body),
  });

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
      setValidationError(t(($) => $.profile_capture.agent_required));
      return;
    }
    if (!trimmedSlug) {
      setValidationError(t(($) => $.profile_capture.slug_required));
      return;
    }
    if (!trimmedName) {
      setValidationError(t(($) => $.profile_capture.name_required));
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
    ? errorMessage(
        captureProfile.error,
        t(($) => $.errors.capture_profile_failed),
      )
    : null;
  const inlineError = validationError ?? submitError;

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeader className="gap-2 px-5">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={t(($) => $.profile_capture.back_aria)}
          onClick={goBack}
        >
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <h1 className="text-sm font-medium">
          {t(($) => $.profile_capture.page_title)}
        </h1>
      </PageHeader>

      <div className="flex flex-1 min-h-0 flex-col overflow-auto p-6">
        <form
          onSubmit={onSubmit}
          className="flex w-full max-w-2xl flex-col gap-5"
        >
          {inlineError && (
            <Alert variant="destructive">
              <AlertCircle />
              <AlertTitle>{t(($) => $.profile_capture.error_title)}</AlertTitle>
              <AlertDescription>{inlineError}</AlertDescription>
            </Alert>
          )}

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="profile-agent">
              {t(($) => $.profile_capture.agent_label)}
            </Label>
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
                  ? t(($) => $.profile_capture.agent_loading)
                  : agents.length === 0
                    ? t(($) => $.profile_capture.agent_empty)
                    : t(($) => $.profile_capture.agent_placeholder)}
              </NativeSelectOption>
              {agents.map((agent) => (
                <NativeSelectOption key={agent.id} value={agent.id}>
                  {agent.name}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <p className="text-xs text-muted-foreground">
              {t(($) => $.profile_capture.agent_help)}
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="profile-slug">
              {t(($) => $.profile_capture.slug_label)}
            </Label>
            <Input
              id="profile-slug"
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              placeholder={t(($) => $.profile_capture.slug_placeholder)}
              required
            />
            <p className="text-xs text-muted-foreground">
              {t(($) => $.profile_capture.slug_help)}
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="profile-name">
              {t(($) => $.profile_capture.name_label)}
            </Label>
            <Input
              id="profile-name"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder={t(($) => $.profile_capture.name_placeholder)}
              required
            />
          </div>

          <div className="flex items-center gap-2 pt-2">
            <Button
              type="submit"
              size="sm"
              disabled={captureProfile.isPending}
            >
              {captureProfile.isPending
                ? t(($) => $.profile_capture.submit_pending)
                : t(($) => $.profile_capture.submit)}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={goBack}
              disabled={captureProfile.isPending}
            >
              {t(($) => $.profile_capture.cancel)}
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}
