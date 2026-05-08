"use client";

import { useState, type FormEvent } from "react";
import { AlertCircle, ArrowLeft } from "lucide-react";
import {
  extractBenchmarkErrorCode,
  useCreateBenchmarkSuite,
} from "@multica/core/benchmarks";
import { useWorkspacePaths } from "@multica/core/paths";
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
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useNavigation } from "../navigation";
import { PageHeader } from "../layout/page-header";
import { useT } from "../i18n";

const ADAPTER_KIND = "programbench" as const;

type Translator = ReturnType<typeof useT<"benchmarks">>["t"];

/**
 * Map a benchmark error code to a user-facing message for the create form.
 * Covers the full union so new codes are caught at compile time.
 */
function messageForCode(t: Translator, code: BenchmarkErrorCode): string {
  switch (code) {
    case "slug_taken":
      return t(($) => $.errors.slug_taken_pick_different);
    case "instance_list_empty":
      return t(($) => $.errors.add_one_instance);
    case "bad_body":
      return t(($) => $.errors.bad_form_body);
    case "bad_id":
      return t(($) => $.errors.bad_id);
    case "bad_user_id":
    case "bad_workspace_id":
    case "workspace_required":
      return t(($) => $.errors.workspace_context_missing);
    case "unauthenticated":
      return t(($) => $.errors.unauthenticated);
    case "internal_error":
      return t(($) => $.errors.internal_error);
    case "suite_not_found":
      return t(($) => $.errors.suite_not_found);
    case "profile_not_found":
      return t(($) => $.errors.profile_not_found);
    case "agent_not_found":
      return t(($) => $.errors.agent_not_found);
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
  }
}

function errorMessage(t: Translator, err: unknown): string {
  const code = extractBenchmarkErrorCode(err);
  if (code) return messageForCode(t, code);
  if (err instanceof Error && err.message) return err.message;
  return t(($) => $.errors.create_suite_failed);
}

export default function SuiteCreate() {
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const createSuite = useCreateBenchmarkSuite();
  const { t } = useT("benchmarks");

  const suitesBase = paths.benchmarkSuites();

  const [slug, setSlug] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [instanceIdsText, setInstanceIdsText] = useState("");
  const [description, setDescription] = useState("");
  const [validationError, setValidationError] = useState<string | null>(null);

  const goBack = () => navigation.push(suitesBase);

  const onSubmit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setValidationError(null);

    const trimmedSlug = slug.trim();
    const trimmedName = displayName.trim();
    const instanceIds = instanceIdsText
      .split("\n")
      .map((line) => line.trim())
      .filter((line) => line.length > 0);

    if (!trimmedSlug) {
      setValidationError(t(($) => $.suite_create.slug_required));
      return;
    }
    if (!trimmedName) {
      setValidationError(t(($) => $.suite_create.name_required));
      return;
    }
    if (instanceIds.length === 0) {
      setValidationError(t(($) => $.errors.add_one_instance));
      return;
    }

    const trimmedDescription = description.trim();

    try {
      const result = await createSuite.mutateAsync({
        slug: trimmedSlug,
        display_name: trimmedName,
        adapter_kind: ADAPTER_KIND,
        instance_ids: instanceIds,
        ...(trimmedDescription ? { description: trimmedDescription } : {}),
      });
      navigation.push(`${suitesBase}/${result.id}`);
    } catch {
      // Error is rendered from `createSuite.error` below.
    }
  };

  const submitError = createSuite.error
    ? errorMessage(t, createSuite.error)
    : null;
  const inlineError = validationError ?? submitError;

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeader className="gap-2 px-5">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={t(($) => $.suite_create.back_aria)}
          onClick={goBack}
        >
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <h1 className="text-sm font-medium">
          {t(($) => $.suite_create.page_title)}
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
              <AlertTitle>{t(($) => $.suite_create.error_title)}</AlertTitle>
              <AlertDescription>{inlineError}</AlertDescription>
            </Alert>
          )}

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="suite-slug">
              {t(($) => $.suite_create.slug_label)}
            </Label>
            <Input
              id="suite-slug"
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              placeholder={t(($) => $.suite_create.slug_placeholder)}
              required
              autoFocus
            />
            <p className="text-xs text-muted-foreground">
              {t(($) => $.suite_create.slug_help)}
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="suite-name">
              {t(($) => $.suite_create.name_label)}
            </Label>
            <Input
              id="suite-name"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder={t(($) => $.suite_create.name_placeholder)}
              required
            />
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="suite-adapter">
              {t(($) => $.suite_create.adapter_label)}
            </Label>
            <NativeSelect
              id="suite-adapter"
              value={ADAPTER_KIND}
              onChange={() => {
                /* single option for v1; no-op */
              }}
              className="w-64"
            >
              <NativeSelectOption value={ADAPTER_KIND}>
                {ADAPTER_KIND}
              </NativeSelectOption>
            </NativeSelect>
            <p className="text-xs text-muted-foreground">
              {t(($) => $.suite_create.adapter_help)}
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="suite-instances">
              {t(($) => $.suite_create.instances_label)}
            </Label>
            <Textarea
              id="suite-instances"
              value={instanceIdsText}
              onChange={(e) => setInstanceIdsText(e.target.value)}
              placeholder={t(($) => $.suite_create.instances_placeholder)}
              rows={6}
              required
              className="font-mono text-xs"
            />
            <p className="text-xs text-muted-foreground">
              {t(($) => $.suite_create.instances_help)}
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="suite-description">
              {t(($) => $.suite_create.description_label)}
            </Label>
            <Textarea
              id="suite-description"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder={t(($) => $.suite_create.description_placeholder)}
              rows={3}
            />
          </div>

          <div className="flex items-center gap-2 pt-2">
            <Button
              type="submit"
              size="sm"
              disabled={createSuite.isPending}
            >
              {createSuite.isPending
                ? t(($) => $.suite_create.submit_pending)
                : t(($) => $.suite_create.submit)}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={goBack}
              disabled={createSuite.isPending}
            >
              {t(($) => $.suite_create.cancel)}
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}
