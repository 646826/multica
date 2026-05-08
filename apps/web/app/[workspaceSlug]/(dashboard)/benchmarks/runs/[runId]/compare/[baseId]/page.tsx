"use client";
import { use } from "react";
import { RunCompare } from "@multica/views/benchmarks";

export default function Page({
  params,
}: {
  params: Promise<{ workspaceSlug: string; runId: string; baseId: string }>;
}) {
  const { runId, baseId } = use(params);
  return <RunCompare candID={runId} baseID={baseId} />;
}
