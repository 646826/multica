import { redirect } from "next/navigation";

export default async function BenchmarksIndex({
  params,
}: {
  params: Promise<{ workspaceSlug: string }>;
}) {
  const { workspaceSlug } = await params;
  redirect(`/${workspaceSlug}/benchmarks/suites`);
}
