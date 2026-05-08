"use client";

import { useMemo, useState, type FormEvent } from "react";
import { AlertCircle, ArrowLeft } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import {
  benchmarkProfileListOptions,
  benchmarkRunListOptions,
  benchmarkSuiteListOptions,
  extractBenchmarkErrorCode,
  useStartBenchmarkRun,
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
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  NativeSelect,
  NativeSelectOption,
} from "@multica/ui/components/ui/native-select";
import {
  RadioGroup,
  RadioGroupItem,
} from "@multica/ui/components/ui/radio-group";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useNavigation } from "../navigation";
import { PageHeader } from "../layout/page-header";
import { useT } from "../i18n";

type EvaluatorMode = "managed" | "imported";

type Translator = ReturnType<typeof useT<"benchmarks">>["t"];

/**
 * Map a benchmark error code to a user-facing message for the start run form.
 * The full union is covered exhaustively so a new code in the union becomes a
 * type error rather than a silent fall-through.
 */
function messageForCode(t: Translator, code: BenchmarkErrorCode): string {
  switch (code) {
    case "unauthenticated":
      return t(($) => $.errors.unauthenticated);
    case "workspace_required":
    case "bad_workspace_id":
    case "bad_user_id":
      return t(($) => $.errors.workspace_context_missing);
    case "bad_id":
      return t(($) => $.errors.bad_id);
    case "bad_body":
      return t(($) => $.errors.bad_body);
    case "internal_error":
      return t(($) => $.errors.internal_error);
    case "suite_not_found":
      return t(($) => $.errors.suite_not_found);
    case "profile_not_found":
      return t(($) => $.errors.profile_not_found);
    case "agent_not_found":
      return t(($) => $.errors.agent_not_found);
    case "slug_taken":
      return t(($) => $.errors.slug_taken_suite);
    case "instance_list_empty":
      return t(($) => $.errors.instance_list_empty);
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
  return t(($) => $.errors.internal_error);
}

export default function StartRunWizard() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const { t } = useT("benchmarks");
  const startRun = useStartBenchmarkRun();

  const runsBase = paths.benchmarkRuns();
  const goBack = () => navigation.push(runsBase);

  const { data: suites = [], isLoading: suitesLoading } = useQuery(
    benchmarkSuiteListOptions(wsId),
  );
  const { data: profiles = [], isLoading: profilesLoading } = useQuery(
    benchmarkProfileListOptions(wsId),
  );
  const { data: runs = [] } = useQuery(benchmarkRunListOptions(wsId));

  const [suiteId, setSuiteId] = useState("");
  const [profileId, setProfileId] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [notes, setNotes] = useState("");
  const [evaluatorMode, setEvaluatorMode] =
    useState<EvaluatorMode>("imported");
  const [baseRunId, setBaseRunId] = useState("");
  const [validationError, setValidationError] = useState<string | null>(null);

  const baselineCandidates = useMemo(() => {
    if (!suiteId) return [];
    return runs.filter(
      (r) => r.status === "complete" && r.suite_id === suiteId,
    );
  }, [runs, suiteId]);

  const onSuiteChange = (next: string) => {
    setSuiteId(next);
    // Selected baseline must belong to the chosen suite — clear it on switch.
    setBaseRunId("");
  };

  const onSubmit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setValidationError(null);

    const trimmedName = displayName.trim();

    if (!suiteId) {
      setValidationError(t(($) => $.start_run.validation_suite_required));
      return;
    }
    if (!profileId) {
      setValidationError(t(($) => $.start_run.validation_profile_required));
      return;
    }
    if (!trimmedName) {
      setValidationError(
        t(($) => $.start_run.validation_display_name_required),
      );
      return;
    }

    const trimmedNotes = notes.trim();

    try {
      const result = await startRun.mutateAsync({
        suite_id: suiteId,
        profile_id: profileId,
        display_name: trimmedName,
        notes: trimmedNotes || undefined,
        evaluator_mode: evaluatorMode,
        base_run_id: baseRunId || undefined,
      });
      navigation.push(paths.benchmarkRunDetail(result.id));
    } catch {
      // Error is rendered from `startRun.error` below.
    }
  };

  const submitError = startRun.error ? errorMessage(t, startRun.error) : null;
  const inlineError = validationError ?? submitError;

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeader className="gap-2 px-5">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={t(($) => $.run_detail.back)}
          onClick={goBack}
        >
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <h1 className="text-sm font-medium">
          {t(($) => $.start_run.page_title)}
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
              <AlertTitle>{t(($) => $.start_run.page_title)}</AlertTitle>
              <AlertDescription>{inlineError}</AlertDescription>
            </Alert>
          )}

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="run-suite">
              {t(($) => $.start_run.field_suite)}
            </Label>
            <NativeSelect
              id="run-suite"
              value={suiteId}
              onChange={(e) => onSuiteChange(e.target.value)}
              disabled={suitesLoading || suites.length === 0}
              required
            >
              <NativeSelectOption value="">
                {t(($) => $.start_run.field_suite_placeholder)}
              </NativeSelectOption>
              {suites.map((s) => (
                <NativeSelectOption key={s.id} value={s.id}>
                  {s.display_name}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="run-profile">
              {t(($) => $.start_run.field_profile)}
            </Label>
            <NativeSelect
              id="run-profile"
              value={profileId}
              onChange={(e) => setProfileId(e.target.value)}
              disabled={profilesLoading || profiles.length === 0}
              required
            >
              <NativeSelectOption value="">
                {t(($) => $.start_run.field_profile_placeholder)}
              </NativeSelectOption>
              {profiles.map((p) => (
                <NativeSelectOption key={p.id} value={p.id}>
                  {p.display_name}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="run-name">
              {t(($) => $.start_run.field_display_name)}
            </Label>
            <Input
              id="run-name"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              required
            />
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="run-notes">
              {t(($) => $.start_run.field_notes)}
            </Label>
            <Textarea
              id="run-notes"
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              rows={3}
            />
          </div>

          <div className="flex flex-col gap-2">
            <Label>{t(($) => $.start_run.field_mode)}</Label>
            <RadioGroup
              value={evaluatorMode}
              onValueChange={(v) => setEvaluatorMode(v as EvaluatorMode)}
              className="gap-3"
            >
              <label className="flex items-start gap-2 text-sm">
                <RadioGroupItem value="imported" className="mt-0.5" />
                <span className="flex flex-col">
                  <span>{t(($) => $.run_detail.mode_imported)}</span>
                  <span className="text-xs text-muted-foreground">
                    {t(($) => $.start_run.field_mode_imported)}
                  </span>
                </span>
              </label>
              <label className="flex items-start gap-2 text-sm">
                <RadioGroupItem value="managed" className="mt-0.5" />
                <span className="flex flex-col">
                  <span>{t(($) => $.run_detail.mode_managed)}</span>
                  <span className="text-xs text-muted-foreground">
                    {t(($) => $.start_run.field_mode_managed)}
                  </span>
                </span>
              </label>
            </RadioGroup>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="run-baseline">
              {t(($) => $.start_run.field_baseline)}
            </Label>
            <NativeSelect
              id="run-baseline"
              value={baseRunId}
              onChange={(e) => setBaseRunId(e.target.value)}
              disabled={!suiteId || baselineCandidates.length === 0}
            >
              <NativeSelectOption value="">
                {t(($) => $.start_run.field_baseline_placeholder)}
              </NativeSelectOption>
              {baselineCandidates.map((r) => (
                <NativeSelectOption key={r.id} value={r.id}>
                  {r.display_name}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <p className="text-xs text-muted-foreground">
              {t(($) => $.start_run.field_baseline_help)}
            </p>
          </div>

          <div className="flex items-center gap-2 pt-2">
            <Button type="submit" size="sm" disabled={startRun.isPending}>
              {startRun.isPending
                ? t(($) => $.start_run.submit_pending)
                : t(($) => $.start_run.submit_create)}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={goBack}
              disabled={startRun.isPending}
            >
              {t(($) => $.suite_create.cancel)}
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}
