"use client";
import { use } from "react";
import { ProfileDetail } from "@multica/views/benchmarks";

export default function Page({
  params,
}: {
  params: Promise<{ workspaceSlug: string; profileId: string }>;
}) {
  const { profileId } = use(params);
  return <ProfileDetail profileId={profileId} />;
}
