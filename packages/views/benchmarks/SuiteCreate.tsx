"use client";

import { useMemo, useState, type DragEvent, type FormEvent } from "react";
import { AlertCircle, ArrowLeft } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import {
  benchmarkReplayEligibleIssuesOptions,
  extractBenchmarkErrorCode,
  useCreateBenchmarkSuite,
  useCreateReplaySuite,
  useFetchReplayReference,
} from "@multica/core/benchmarks";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { EligibleIssue } from "@multica/core/types";
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
import {
  useBenchmarkErrorFallback,
  useBenchmarkErrorMessage,
} from "./error-message";

const ADAPTER_KIND = "programbench" as const;

type Translator = ReturnType<typeof useT<"benchmarks">>["t"];
type SuiteMode = "programbench" | "replay";

/**
 * Per-form override map: pasted patches and form-body errors get the
 * "form data was rejected" copy; missing-instance and slug-taken get
 * the "add at least one instance id" / "pick a different slug" copy
 * the create form has used historically.
 */
const SUITE_CREATE_OVERRIDES = {
  slug_taken: (t: Translator) => t(($) => $.errors.slug_taken_pick_different),
  instance_list_empty: (t: Translator) => t(($) => $.errors.add_one_instance),
  bad_body: (t: Translator) => t(($) => $.errors.bad_form_body),
};

/** Per-issue reference patch + optional PR url, indexed by source_issue_id. */
type ReferenceEntry = { patch: string; prUrl: string };

export default function SuiteCreate() {
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const wsId = useWorkspaceId();
  const createSuite = useCreateBenchmarkSuite();
  const createReplay = useCreateReplaySuite();
  const { t } = useT("benchmarks");
  const errorMessage = useBenchmarkErrorFallback(SUITE_CREATE_OVERRIDES);

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
    ? errorMessage(activeMutation.error, t(($) => $.errors.create_suite_failed))
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

/**
 * Cap drag-dropped patch files at 10 MiB. Real-world unified diffs almost
 * never exceed a few hundred KiB; anything larger is overwhelmingly likely
 * to be a misclick (binary, log, etc.) and we don't want to load it into
 * the textarea at all.
 */
const MAX_PATCH_FILE_BYTES = 10 * 1024 * 1024;

function ReferencePatchEditor({
  t,
  issueId,
  value,
  onChange,
}: ReferencePatchEditorProps) {
  const patchId = `replay-patch-${issueId}`;
  const urlId = `replay-pr-url-${issueId}`;
  const fetchUrlId = `replay-fetch-url-${issueId}`;
  const fetchRef = useFetchReplayReference();
  const messageFn = useBenchmarkErrorMessage(SUITE_CREATE_OVERRIDES);
  const [fetchUrl, setFetchUrl] = useState("");
  const [fetchError, setFetchError] = useState<string | null>(null);
  const [dragging, setDragging] = useState(false);
  const [dropError, setDropError] = useState<string | null>(null);

  const canFetch = fetchUrl.trim().length > 0 && !fetchRef.isPending;

  async function handleFetch() {
    const trimmed = fetchUrl.trim();
    if (!trimmed) return;
    setFetchError(null);
    try {
      const res = await fetchRef.mutateAsync(trimmed);
      onChange({ patch: res.patch, prUrl: res.source_url });
    } catch (err) {
      const code = extractBenchmarkErrorCode(err);
      setFetchError(
        code
          ? messageFn(code)
          : err instanceof Error && err.message
            ? err.message
            : t(($) => $.errors.reference_fetch_failed),
      );
    }
  }

  function handleDragOver(e: DragEvent<HTMLDivElement>) {
    // Only react to file drags so non-file drags (text selections, etc.)
    // don't make the whole textarea pulse blue.
    if (!e.dataTransfer.types.includes("Files")) return;
    e.preventDefault();
    setDragging(true);
  }

  function handleDragLeave(e: DragEvent<HTMLDivElement>) {
    e.preventDefault();
    setDragging(false);
  }

  async function handleDrop(e: DragEvent<HTMLDivElement>) {
    e.preventDefault();
    setDragging(false);
    setDropError(null);
    const file = e.dataTransfer.files[0];
    if (!file) return;
    if (!/\.(patch|diff)$/i.test(file.name)) {
      setDropError(t(($) => $.suite_create.replay_dragdrop_wrong_ext));
      return;
    }
    if (file.size > MAX_PATCH_FILE_BYTES) {
      setDropError(t(($) => $.suite_create.replay_dragdrop_too_large));
      return;
    }
    const text = await file.text();
    onChange({ patch: text });
  }

  return (
    <div className="flex flex-col gap-2 pl-6">
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={fetchUrlId}>
          {t(($) => $.suite_create.replay_fetch_url_label)}
        </Label>
        <div className="flex gap-2">
          <Input
            id={fetchUrlId}
            value={fetchUrl}
            onChange={(e) => setFetchUrl(e.target.value)}
            placeholder="https://github.com/owner/repo/pull/123"
          />
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={handleFetch}
            disabled={!canFetch}
          >
            {fetchRef.isPending
              ? t(($) => $.suite_create.replay_fetch_pending)
              : t(($) => $.suite_create.replay_fetch_button)}
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          {t(($) => $.suite_create.replay_fetch_help)}
        </p>
        {fetchError && (
          <Alert variant="destructive">
            <AlertCircle />
            <AlertDescription>{fetchError}</AlertDescription>
          </Alert>
        )}
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor={patchId}>
          {t(($) => $.suite_create.replay_reference_label)}
        </Label>
        <div
          onDragOver={handleDragOver}
          onDragLeave={handleDragLeave}
          onDrop={handleDrop}
          className={cn(
            "rounded-md transition-shadow",
            dragging && "ring-2 ring-blue-500 ring-offset-1",
          )}
        >
          <Textarea
            id={patchId}
            value={value.patch}
            onChange={(e) => onChange({ patch: e.target.value })}
            rows={6}
            required
            className="font-mono text-xs"
          />
        </div>
        <p className="text-xs text-muted-foreground">
          {t(($) => $.suite_create.replay_reference_help)}
        </p>
        <p className="text-xs text-muted-foreground">
          {t(($) => $.suite_create.replay_dragdrop_help)}
        </p>
        {dropError && (
          <Alert variant="destructive">
            <AlertCircle />
            <AlertDescription>{dropError}</AlertDescription>
          </Alert>
        )}
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
