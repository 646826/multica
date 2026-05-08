"use client";

// English literal strings live here intentionally for Phase 1c-T05; the
// dedicated i18n pass for runs UI is tracked as P1c-T06 and will replace
// every hard-coded label below with `useT("benchmarks").t(...)` lookups.
/* eslint-disable i18next/no-literal-string */

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

type EvaluatorMode = "managed" | "imported";

/**
 * Map a benchmark error code to a user-facing English message for the start
 * run form. The full union is covered so a new code becomes a compile error.
 *
 * Two codes called out by the spec — `invalid_evaluator_mode` and
 * `suite_or_profile_not_found` — are NOT yet members of the
 * `BenchmarkErrorCode` union. Until the backend adds them, we surface the
 * closest existing codes (`bad_body` for the former, `suite_not_found` /
 * `profile_not_found` for the latter) with friendly wording. T06 will swap
 * this for the shared `useT` translator pattern.
 */
function messageForCode(code: BenchmarkErrorCode): string {
  switch (code) {
    case "unauthenticated":
      return "You must be signed in to start a run.";
    case "workspace_required":
    case "bad_workspace_id":
    case "bad_user_id":
      return "Workspace context is missing or invalid.";
    case "bad_id":
      return "Invalid id.";
    case "bad_body":
      return "The form contains invalid values. Please check evaluator mode and other fields.";
    case "internal_error":
      return "Something went wrong on the server.";
    case "suite_not_found":
      return "Selected suite was not found. It may have been deleted.";
    case "profile_not_found":
      return "Selected profile was not found. It may have been deleted.";
    case "agent_not_found":
      return "Agent not found.";
    case "slug_taken":
      return "That slug is already taken.";
    case "instance_list_empty":
      return "Suite has no instances.";
  }
}

function errorMessage(err: unknown): string {
  const code = extractBenchmarkErrorCode(err);
  if (code) return messageForCode(code);
  if (err instanceof Error && err.message) return err.message;
  return "Failed to start run.";
}

export default function StartRunWizard() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
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
      setValidationError("Please select a suite.");
      return;
    }
    if (!profileId) {
      setValidationError("Please select a profile.");
      return;
    }
    if (!trimmedName) {
      setValidationError("Display name is required.");
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

  const submitError = startRun.error ? errorMessage(startRun.error) : null;
  const inlineError = validationError ?? submitError;

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeader className="gap-2 px-5">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label="Back to runs"
          onClick={goBack}
        >
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <h1 className="text-sm font-medium">Start run</h1>
      </PageHeader>

      <div className="flex flex-1 min-h-0 flex-col overflow-auto p-6">
        <form
          onSubmit={onSubmit}
          className="flex w-full max-w-2xl flex-col gap-5"
        >
          {inlineError && (
            <Alert variant="destructive">
              <AlertCircle />
              <AlertTitle>Could not start run</AlertTitle>
              <AlertDescription>{inlineError}</AlertDescription>
            </Alert>
          )}

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="run-suite">Suite</Label>
            <NativeSelect
              id="run-suite"
              value={suiteId}
              onChange={(e) => onSuiteChange(e.target.value)}
              disabled={suitesLoading || suites.length === 0}
              required
            >
              <NativeSelectOption value="">
                {suitesLoading
                  ? "Loading suites…"
                  : suites.length === 0
                    ? "No suites available"
                    : "Select a suite…"}
              </NativeSelectOption>
              {suites.map((s) => (
                <NativeSelectOption key={s.id} value={s.id}>
                  {s.display_name}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="run-profile">Profile</Label>
            <NativeSelect
              id="run-profile"
              value={profileId}
              onChange={(e) => setProfileId(e.target.value)}
              disabled={profilesLoading || profiles.length === 0}
              required
            >
              <NativeSelectOption value="">
                {profilesLoading
                  ? "Loading profiles…"
                  : profiles.length === 0
                    ? "No profiles available"
                    : "Select a profile…"}
              </NativeSelectOption>
              {profiles.map((p) => (
                <NativeSelectOption key={p.id} value={p.id}>
                  {p.display_name}
                </NativeSelectOption>
              ))}
            </NativeSelect>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="run-name">Display name</Label>
            <Input
              id="run-name"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="e.g. Nightly run — 2026-05-07"
              required
            />
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="run-notes">Notes</Label>
            <Textarea
              id="run-notes"
              value={notes}
              onChange={(e) => setNotes(e.target.value)}
              placeholder="Optional context for this run"
              rows={3}
            />
          </div>

          <div className="flex flex-col gap-2">
            <Label>Evaluator mode</Label>
            <RadioGroup
              value={evaluatorMode}
              onValueChange={(v) => setEvaluatorMode(v as EvaluatorMode)}
              className="gap-3"
            >
              <label className="flex items-start gap-2 text-sm">
                <RadioGroupItem value="imported" className="mt-0.5" />
                <span className="flex flex-col">
                  <span>Imported</span>
                  <span className="text-xs text-muted-foreground">
                    Results are uploaded externally. Works without an
                    evaluator pod.
                  </span>
                </span>
              </label>
              <label className="flex items-start gap-2 text-sm">
                <RadioGroupItem value="managed" className="mt-0.5" />
                <span className="flex flex-col">
                  <span>Managed</span>
                  <span className="text-xs text-muted-foreground">
                    Server-driven evaluation. Requires a running evaluator
                    pod.
                  </span>
                </span>
              </label>
            </RadioGroup>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="run-baseline">Compare with (optional)</Label>
            <NativeSelect
              id="run-baseline"
              value={baseRunId}
              onChange={(e) => setBaseRunId(e.target.value)}
              disabled={!suiteId || baselineCandidates.length === 0}
            >
              <NativeSelectOption value="">
                {!suiteId
                  ? "Select a suite first"
                  : baselineCandidates.length === 0
                    ? "No completed runs for this suite"
                    : "None"}
              </NativeSelectOption>
              {baselineCandidates.map((r) => (
                <NativeSelectOption key={r.id} value={r.id}>
                  {r.display_name}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <p className="text-xs text-muted-foreground">
              Pick a previous complete run of the same suite to baseline
              against.
            </p>
          </div>

          <div className="flex items-center gap-2 pt-2">
            <Button type="submit" size="sm" disabled={startRun.isPending}>
              {startRun.isPending ? "Starting…" : "Start run"}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={goBack}
              disabled={startRun.isPending}
            >
              Cancel
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}
