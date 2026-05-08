"use client";

import { useMemo, useState, type FormEvent } from "react";
import { AlertCircle, ArrowLeft } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import {
  benchmarkProfileListOptions,
  benchmarkRunListOptions,
  benchmarkSuiteListOptions,
  useStartBenchmarkRun,
} from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
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
import { useBenchmarkErrorFallback } from "./error-message";

type EvaluatorMode = "managed" | "imported";

export default function StartRunWizard() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const { t } = useT("benchmarks");
  const errorMessage = useBenchmarkErrorFallback({
    slug_taken: (t) => t(($) => $.errors.slug_taken_suite),
  });
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

  const submitError = startRun.error
    ? errorMessage(startRun.error, t(($) => $.errors.internal_error))
    : null;
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
