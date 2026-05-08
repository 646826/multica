"use client";

import { useMemo, useState, type FormEvent } from "react";
import { AlertCircle, ArrowLeft } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import {
  benchmarkReplayEligibleIssuesOptions,
  extractBenchmarkErrorCode,
  useCreateBenchmarkSuite,
  useCreateReplaySuite,
} from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { BenchmarkErrorCode, EligibleIssue } from "@multica/core/types";
import {
  Alert,
  AlertDescription,
  AlertTitle,
} from "@multica/ui/components/ui/alert";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  NativeSelect,
  NativeSelectOption,
} from "@multica/ui/components/ui/native-select";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { useNavigation } from "../navigation";
import { PageHeader } from "../layout/page-header";
import { useT } from "../i18n";

const ADAPTER_KIND = "programbench" as const;

type Translator = ReturnType<typeof useT<"benchmarks">>["t"];
type SuiteMode = "programbench" | "replay";

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

/** Per-issue reference patch + optional PR url, indexed by source_issue_id. */
type ReferenceEntry = { patch: string; prUrl: string };

export default function SuiteCreate() {
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const wsId = useWorkspaceId();
  const createSuite = useCreateBenchmarkSuite();
  const createReplay = useCreateReplaySuite();
  const { t } = useT("benchmarks");

  const suitesBase = paths.benchmarkSuites();

  const [mode, setMode] = useState<SuiteMode>("programbench");
  const [slug, setSlug] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [instanceIdsText, setInstanceIdsText] = useState("");
  const [description, setDescription] = useState("");
  const [validationError, setValidationError] = useState<string | null>(null);

  // Replay-specific state.
  const [pickedIds, setPickedIds] = useState<Set<string>>(() => new Set());
  const [refs, setRefs] = useState<Record<string, ReferenceEntry>>({});

  const eligibleQuery = useQuery({
    ...benchmarkReplayEligibleIssuesOptions(wsId),
    enabled: mode === "replay",
  });

  const goBack = () => navigation.push(suitesBase);

  const togglePicked = (id: string, next: boolean) => {
    setPickedIds((prev) => {
      const copy = new Set(prev);
      if (next) copy.add(id);
      else copy.delete(id);
      return copy;
    });
    if (next && !refs[id]) {
      setRefs((prev) => ({ ...prev, [id]: { patch: "", prUrl: "" } }));
    }
  };

  const updateRef = (id: string, patch: Partial<ReferenceEntry>) => {
    setRefs((prev) => ({
      ...prev,
      [id]: { patch: "", prUrl: "", ...prev[id], ...patch },
    }));
  };

  const onSubmitProgramBench = async () => {
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
      // Error rendered from createSuite.error below.
    }
  };

  const onSubmitReplay = async () => {
    const trimmedSlug = slug.trim();
    const trimmedName = displayName.trim();

    if (!trimmedSlug) {
      setValidationError(t(($) => $.suite_create.slug_required));
      return;
    }
    if (!trimmedName) {
      setValidationError(t(($) => $.suite_create.name_required));
      return;
    }
    if (pickedIds.size === 0) {
      setValidationError(t(($) => $.suite_create.replay_no_picks));
      return;
    }

    const instances: Array<{
      source_issue_id: string;
      reference_solution: string;
      reference_pr_url?: string;
    }> = [];
    for (const id of pickedIds) {
      const entry = refs[id];
      const patch = entry?.patch.trim() ?? "";
      if (!patch) {
        setValidationError(t(($) => $.suite_create.replay_no_picks));
        return;
      }
      const prUrl = entry?.prUrl.trim() ?? "";
      instances.push({
        source_issue_id: id,
        reference_solution: patch,
        ...(prUrl ? { reference_pr_url: prUrl } : {}),
      });
    }

    const trimmedDescription = description.trim();

    try {
      const result = await createReplay.mutateAsync({
        slug: trimmedSlug,
        display_name: trimmedName,
        instances,
        ...(trimmedDescription ? { description: trimmedDescription } : {}),
      });
      navigation.push(`${suitesBase}/${result.id}`);
    } catch {
      // Error rendered from createReplay.error below.
    }
  };

  const onSubmit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setValidationError(null);
    if (mode === "programbench") {
      await onSubmitProgramBench();
    } else {
      await onSubmitReplay();
    }
  };

  const activeMutation = mode === "programbench" ? createSuite : createReplay;
  const submitError = activeMutation.error
    ? errorMessage(t, activeMutation.error)
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
          <div className="flex gap-2">
            <Button
              type="button"
              size="sm"
              variant={mode === "programbench" ? "default" : "outline"}
              onClick={() => {
                setMode("programbench");
                setValidationError(null);
              }}
            >
              {t(($) => $.suite_create.mode_programbench)}
            </Button>
            <Button
              type="button"
              size="sm"
              variant={mode === "replay" ? "default" : "outline"}
              onClick={() => {
                setMode("replay");
                setValidationError(null);
              }}
            >
              {t(($) => $.suite_create.mode_replay)}
            </Button>
          </div>

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

          {mode === "programbench" && (
            <>
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
            </>
          )}

          {mode === "replay" && (
            <ReplayPicker
              t={t}
              loading={eligibleQuery.isLoading}
              issues={eligibleQuery.data ?? []}
              pickedIds={pickedIds}
              onToggle={togglePicked}
              refs={refs}
              onUpdateRef={updateRef}
            />
          )}

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
              disabled={activeMutation.isPending}
            >
              {activeMutation.isPending
                ? t(($) => $.suite_create.submit_pending)
                : t(($) => $.suite_create.submit)}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={goBack}
              disabled={activeMutation.isPending}
            >
              {t(($) => $.suite_create.cancel)}
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}

interface ReplayPickerProps {
  t: Translator;
  loading: boolean;
  issues: EligibleIssue[];
  pickedIds: Set<string>;
  onToggle: (id: string, next: boolean) => void;
  refs: Record<string, ReferenceEntry>;
  onUpdateRef: (id: string, patch: Partial<ReferenceEntry>) => void;
}

function ReplayPicker({
  t,
  loading,
  issues,
  pickedIds,
  onToggle,
  refs,
  onUpdateRef,
}: ReplayPickerProps) {
  // Stable ordering: most recently updated first; the server typically
  // returns rows already sorted, but we re-sort defensively so the UI is
  // not at the mercy of the backend's `ORDER BY`.
  const sorted = useMemo(
    () =>
      [...issues].sort((a, b) =>
        a.updated_at < b.updated_at ? 1 : a.updated_at > b.updated_at ? -1 : 0,
      ),
    [issues],
  );

  return (
    <div className="flex flex-col gap-2">
      <Label>{t(($) => $.suite_create.replay_picker_label)}</Label>
      {loading ? (
        <p className="text-xs text-muted-foreground">
          {t(($) => $.suite_create.replay_picker_loading)}
        </p>
      ) : sorted.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          {t(($) => $.suite_create.replay_picker_empty)}
        </p>
      ) : (
        <ul className="flex flex-col gap-3 rounded-md border p-3">
          {sorted.map((issue) => {
            const checked = pickedIds.has(issue.id);
            const entry = refs[issue.id];
            return (
              <li
                key={issue.id}
                className={cn(
                  "flex flex-col gap-2 rounded-sm",
                  checked && "bg-muted/30 p-2",
                )}
              >
                <label className="flex items-start gap-2 text-sm">
                  <Checkbox
                    checked={checked}
                    onCheckedChange={(next) =>
                      onToggle(issue.id, next === true)
                    }
                    className="mt-0.5"
                  />
                  <span className="flex flex-col">
                    <span className="font-medium">
                      #{issue.number} {issue.title}
                    </span>
                    <span className="text-xs text-muted-foreground">
                      {issue.status}
                    </span>
                  </span>
                </label>
                {checked && (
                  <ReferencePatchEditor
                    t={t}
                    issueId={issue.id}
                    value={entry ?? { patch: "", prUrl: "" }}
                    onChange={(p) => onUpdateRef(issue.id, p)}
                  />
                )}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

interface ReferencePatchEditorProps {
  t: Translator;
  issueId: string;
  value: ReferenceEntry;
  onChange: (patch: Partial<ReferenceEntry>) => void;
}

function ReferencePatchEditor({
  t,
  issueId,
  value,
  onChange,
}: ReferencePatchEditorProps) {
  const patchId = `replay-patch-${issueId}`;
  const urlId = `replay-pr-url-${issueId}`;
  return (
    <div className="flex flex-col gap-2 pl-6">
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={patchId}>
          {t(($) => $.suite_create.replay_reference_label)}
        </Label>
        <Textarea
          id={patchId}
          value={value.patch}
          onChange={(e) => onChange({ patch: e.target.value })}
          rows={6}
          required
          className="font-mono text-xs"
        />
        <p className="text-xs text-muted-foreground">
          {t(($) => $.suite_create.replay_reference_help)}
        </p>
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={urlId}>
          {t(($) => $.suite_create.replay_pr_url_label)}
        </Label>
        <Input
          id={urlId}
          value={value.prUrl}
          onChange={(e) => onChange({ prUrl: e.target.value })}
          placeholder="https://github.com/owner/repo/pull/123"
        />
      </div>
    </div>
  );
}
