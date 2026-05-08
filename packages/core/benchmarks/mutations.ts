import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { benchmarkKeys } from "./queries";
import type {
  CaptureProfileRequest,
  CreateReplaySuiteRequest,
  CreateSuiteRequest,
  StartRunRequest,
} from "../types";

export function useCreateBenchmarkSuite() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: CreateSuiteRequest) => api.createBenchmarkSuite(data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: benchmarkKeys.suites(wsId) });
    },
  });
}

export function useDeleteBenchmarkSuite() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.deleteBenchmarkSuite(id),
    onSuccess: (_data, id) => {
      qc.invalidateQueries({ queryKey: benchmarkKeys.suites(wsId) });
      // Drop the per-detail cache so a follow-up navigation doesn't briefly
      // render a stale suite before the 404 lands.
      qc.removeQueries({ queryKey: benchmarkKeys.suite(wsId, id) });
    },
  });
}

// useSyncBenchmarkSuite re-resolves the suite's instance_ids against the
// registered Catalog. v1 informational only — the suite is not mutated, so
// no list cache needs invalidating; we only invalidate the per-detail key in
// case the resolved/unresolved info gets surfaced there in a future revision.
export function useSyncBenchmarkSuite() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.syncBenchmarkSuite(id),
    onSuccess: (_, id) => {
      void qc.invalidateQueries({ queryKey: benchmarkKeys.suite(wsId, id) });
    },
  });
}

export function useCaptureBenchmarkProfile() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: CaptureProfileRequest) =>
      api.captureBenchmarkProfile(data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: benchmarkKeys.profiles(wsId) });
    },
  });
}

export function useDeleteBenchmarkProfile() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.deleteBenchmarkProfile(id),
    onSuccess: (_data, id) => {
      qc.invalidateQueries({ queryKey: benchmarkKeys.profiles(wsId) });
      qc.removeQueries({ queryKey: benchmarkKeys.profile(wsId, id) });
    },
  });
}

export function useStartBenchmarkRun() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: StartRunRequest) => api.startBenchmarkRun(input),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: benchmarkKeys.runs(wsId) });
    },
  });
}

// Creates a Replay-mode benchmark suite from a list of completed issues
// plus their reference solution patches. Invalidates the suites list so the
// new suite appears immediately on the suites page.
export function useCreateReplaySuite() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateReplaySuiteRequest) =>
      api.createReplaySuite(input),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: benchmarkKeys.suites(wsId) });
    },
  });
}

export function useCancelBenchmarkRun() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.cancelBenchmarkRun(id),
    onSuccess: (_, id) => {
      void qc.invalidateQueries({ queryKey: benchmarkKeys.runs(wsId) });
      void qc.invalidateQueries({ queryKey: benchmarkKeys.run(wsId, id) });
    },
  });
}
