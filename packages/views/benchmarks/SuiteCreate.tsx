/* eslint-disable i18next/no-literal-string -- T22 will wrap user-facing strings in t(...) */
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

const ADAPTER_KIND = "programbench" as const;

/**
 * Map a benchmark error code to a user-facing message for the create form.
 * Covers the full union so new codes are caught at compile time.
 */
function messageForCode(code: BenchmarkErrorCode): string {
  switch (code) {
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
    case "agent_not_found":
      return "Agent not found.";
  }
}

function errorMessage(err: unknown): string {
  const code = extractBenchmarkErrorCode(err);
  if (code) return messageForCode(code);
  if (err instanceof Error && err.message) return err.message;
  return "Failed to create suite.";
}

export default function SuiteCreate() {
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const createSuite = useCreateBenchmarkSuite();

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
      setValidationError("Slug is required.");
      return;
    }
    if (!trimmedName) {
      setValidationError("Display name is required.");
      return;
    }
    if (instanceIds.length === 0) {
      setValidationError("Add at least one instance id.");
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
    ? errorMessage(createSuite.error)
    : null;
  const inlineError = validationError ?? submitError;

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeader className="gap-2 px-5">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label="Back to suites"
          onClick={goBack}
        >
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <h1 className="text-sm font-medium">Create suite</h1>
      </PageHeader>

      <div className="flex flex-1 min-h-0 flex-col overflow-auto p-6">
        <form
          onSubmit={onSubmit}
          className="flex w-full max-w-2xl flex-col gap-5"
        >
          {inlineError && (
            <Alert variant="destructive">
              <AlertCircle />
              <AlertTitle>Couldn&apos;t create suite</AlertTitle>
              <AlertDescription>{inlineError}</AlertDescription>
            </Alert>
          )}

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="suite-slug">Slug</Label>
            <Input
              id="suite-slug"
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              placeholder="programbench-mini"
              required
              autoFocus
            />
            <p className="text-xs text-muted-foreground">
              Stable identifier, unique within this workspace.
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="suite-name">Display name</Label>
            <Input
              id="suite-name"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="ProgramBench Mini"
              required
            />
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="suite-adapter">Adapter</Label>
            <NativeSelect
              id="suite-adapter"
              value={ADAPTER_KIND}
              onChange={() => {
                /* single option for v1; no-op */
              }}
              className="w-64"
            >
              <NativeSelectOption value={ADAPTER_KIND}>
                programbench
              </NativeSelectOption>
            </NativeSelect>
            <p className="text-xs text-muted-foreground">
              Only ProgramBench is supported in v1.
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="suite-instances">Instance ids</Label>
            <Textarea
              id="suite-instances"
              value={instanceIdsText}
              onChange={(e) => setInstanceIdsText(e.target.value)}
              placeholder={"abishekvashok__cmatrix.5c082c6\nfoo__bar.deadbee"}
              rows={6}
              required
              className="font-mono text-xs"
            />
            <p className="text-xs text-muted-foreground">
              One instance id per line, e.g. abishekvashok__cmatrix.5c082c6.
            </p>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="suite-description">Description</Label>
            <Textarea
              id="suite-description"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Optional notes about this suite."
              rows={3}
            />
          </div>

          <div className="flex items-center gap-2 pt-2">
            <Button
              type="submit"
              size="sm"
              disabled={createSuite.isPending}
            >
              {createSuite.isPending ? "Creating…" : "Create suite"}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={goBack}
              disabled={createSuite.isPending}
            >
              Cancel
            </Button>
          </div>
        </form>
      </div>
    </div>
  );
}
