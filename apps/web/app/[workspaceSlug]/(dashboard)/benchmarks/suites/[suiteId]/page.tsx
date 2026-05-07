"use client";
import { use } from "react";
import { SuiteDetail } from "@multica/views/benchmarks";

export default function Page({
  params,
}: {
  params: Promise<{ workspaceSlug: string; suiteId: string }>;
}) {
  const { suiteId } = use(params);
  return <SuiteDetail suiteId={suiteId} />;
}
