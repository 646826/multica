"use client";
import { use } from "react";
import { RunDetail } from "@multica/views/benchmarks";

export default function Page({
  params,
}: {
  params: Promise<{ workspaceSlug: string; runId: string }>;
}) {
  const { runId } = use(params);
  return <RunDetail runId={runId} />;
}
