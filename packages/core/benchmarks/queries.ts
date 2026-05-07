import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

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
