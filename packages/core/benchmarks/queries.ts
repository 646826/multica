import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import { ApiError } from "../api/client";

/**
 * Benchmark cache keys. Workspace-scoped lists / details for suites and
 * profiles. Mutations under {@link ./mutations} invalidate using `all(wsId)`
 * for broad cache busting and the narrower list keys for targeted refetch.
 *
 * Note: the backend list endpoints derive the workspace from the auth
 * context, so `wsId` participates in the key (cache identity) only — it is
 * NOT passed to the API call.
 */
export const benchmarkKeys = {
  all: (wsId: string) => ["benchmarks", wsId] as const,
  suites: (wsId: string) => [...benchmarkKeys.all(wsId), "suites"] as const,
  suite: (wsId: string, id: string) =>
    [...benchmarkKeys.all(wsId), "suite", id] as const,
  profiles: (wsId: string) => [...benchmarkKeys.all(wsId), "profiles"] as const,
  profile: (wsId: string, id: string) =>
    [...benchmarkKeys.all(wsId), "profile", id] as const,
  runs: (wsId: string) => [...benchmarkKeys.all(wsId), "runs"] as const,
  run: (wsId: string, id: string) =>
    [...benchmarkKeys.all(wsId), "run", id] as const,
  compare: (wsId: string, candID: string, baseID: string) =>
    ["benchmarks", wsId, "compare", candID, baseID] as const,
  leaderboard: (wsId: string, suiteSlug: string) =>
    ["benchmarks", wsId, "leaderboard", suiteSlug] as const,
  tasks: (wsId: string, runID: string) =>
    ["benchmarks", wsId, "tasks", runID] as const,
  summary: (wsId: string, runID: string) =>
    ["benchmarks", wsId, "summary", runID] as const,
};

export function benchmarkSuiteListOptions(wsId: string) {
  return queryOptions({
    queryKey: benchmarkKeys.suites(wsId),
    queryFn: () => api.listBenchmarkSuites(),
    select: (data) => data.items,
  });
}

export function benchmarkSuiteDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: benchmarkKeys.suite(wsId, id),
    queryFn: () => api.getBenchmarkSuite(id),
    enabled: Boolean(id),
  });
}

export function benchmarkProfileListOptions(wsId: string) {
  return queryOptions({
    queryKey: benchmarkKeys.profiles(wsId),
    queryFn: () => api.listBenchmarkProfiles(),
    select: (data) => data.items,
  });
}

export function benchmarkProfileDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: benchmarkKeys.profile(wsId, id),
    queryFn: () => api.getBenchmarkProfile(id),
    enabled: Boolean(id),
  });
}

export function benchmarkRunListOptions(wsId: string) {
  return queryOptions({
    queryKey: benchmarkKeys.runs(wsId),
    queryFn: () => api.listBenchmarkRuns().then((r) => r.items),
  });
}

export function benchmarkRunDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: benchmarkKeys.run(wsId, id),
    queryFn: () => api.getBenchmarkRun(id),
    enabled: Boolean(id),
  });
}

export function benchmarkCompareOptions(wsId: string, candID: string, baseID: string) {
  return queryOptions({
    queryKey: benchmarkKeys.compare(wsId, candID, baseID),
    queryFn: () => api.compareBenchmarkRun(candID, baseID),
    enabled: Boolean(candID && baseID),
  });
}

export function benchmarkLeaderboardOptions(wsId: string, suiteSlug: string) {
  return queryOptions({
    queryKey: benchmarkKeys.leaderboard(wsId, suiteSlug),
    queryFn: () => api.getBenchmarkLeaderboard(suiteSlug).then((r) => r.items),
    enabled: Boolean(suiteSlug),
  });
}

export function benchmarkRunTasksOptions(wsId: string, runID: string) {
  return queryOptions({
    queryKey: benchmarkKeys.tasks(wsId, runID),
    queryFn: () => api.listBenchmarkRunTasks(runID).then((r) => r.items),
    enabled: Boolean(runID),
  });
}

/**
 * Summary query for a run. The server returns 404 with error code
 * `summary_not_available` while the run is still in progress and
 * 404 `run_not_found` for an unknown id — both surface as ApiError
 * with `status === 404`. Retrying either is wasteful (the summary
 * row appears on a finalizer event, not by polling), so we short-
 * circuit retries on any 404. Callers that want to refetch when a
 * run completes should invalidate this key on the run-status event.
 */
export function benchmarkRunSummaryOptions(wsId: string, runID: string) {
  return queryOptions({
    queryKey: benchmarkKeys.summary(wsId, runID),
    queryFn: () => api.getBenchmarkRunSummary(runID),
    enabled: Boolean(runID),
    retry: (_failureCount, err) => {
      if (err instanceof ApiError && err.status === 404) return false;
      return true;
    },
  });
}
