import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { benchmarkKeys } from "./queries";
import type {
  BenchmarkRun,
  CaptureProfileRequest,
  CreateReplaySuiteRequest,
  CreateSuiteRequest,
  ListBenchmarkProfilesResponse,
  ListBenchmarkSuitesResponse,
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
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: benchmarkKeys.suites(wsId) });
      const prev = qc.getQueryData<ListBenchmarkSuitesResponse>(
        benchmarkKeys.suites(wsId),
      );
      qc.setQueryData<ListBenchmarkSuitesResponse>(
        benchmarkKeys.suites(wsId),
        (old) =>
          old ? { ...old, items: old.items.filter((s) => s.id !== id) } : old,
      );
      return { prev };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev !== undefined) {
        qc.setQueryData(benchmarkKeys.suites(wsId), ctx.prev);
      }
    },
    onSettled: (_data, _err, id) => {
      void qc.invalidateQueries({ queryKey: benchmarkKeys.suites(wsId) });
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
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: benchmarkKeys.profiles(wsId) });
      const prev = qc.getQueryData<ListBenchmarkProfilesResponse>(
        benchmarkKeys.profiles(wsId),
      );
      qc.setQueryData<ListBenchmarkProfilesResponse>(
        benchmarkKeys.profiles(wsId),
        (old) =>
          old ? { ...old, items: old.items.filter((p) => p.id !== id) } : old,
      );
      return { prev };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.prev !== undefined) {
        qc.setQueryData(benchmarkKeys.profiles(wsId), ctx.prev);
      }
    },
    onSettled: (_data, _err, id) => {
      void qc.invalidateQueries({ queryKey: benchmarkKeys.profiles(wsId) });
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

// Resolves a PR / patch URL into a unified diff. Stateless — no cache
// invalidation needed; the caller drops the result into form state.
export function useFetchReplayReference() {
  return useMutation({
    mutationFn: (url: string) => api.fetchReplayReference(url),
  });
}

export function useCancelBenchmarkRun() {
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.cancelBenchmarkRun(id),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: benchmarkKeys.runs(wsId) });
      await qc.cancelQueries({ queryKey: benchmarkKeys.run(wsId, id) });
      const prevList = qc.getQueryData<BenchmarkRun[]>(
        benchmarkKeys.runs(wsId),
      );
      const prevDetail = qc.getQueryData<BenchmarkRun>(
        benchmarkKeys.run(wsId, id),
      );
      qc.setQueryData<BenchmarkRun[]>(benchmarkKeys.runs(wsId), (old) =>
        old?.map((r) =>
          r.id === id ? { ...r, status: "canceled" as const } : r,
        ),
      );
      if (prevDetail) {
        qc.setQueryData<BenchmarkRun>(benchmarkKeys.run(wsId, id), {
          ...prevDetail,
          status: "canceled",
        });
      }
      return { prevList, prevDetail };
    },
    onError: (_err, id, ctx) => {
      if (ctx?.prevList !== undefined) {
        qc.setQueryData(benchmarkKeys.runs(wsId), ctx.prevList);
      }
      if (ctx?.prevDetail !== undefined) {
        qc.setQueryData(benchmarkKeys.run(wsId, id), ctx.prevDetail);
      }
    },
    onSettled: (_data, _err, id) => {
      void qc.invalidateQueries({ queryKey: benchmarkKeys.runs(wsId) });
      void qc.invalidateQueries({ queryKey: benchmarkKeys.run(wsId, id) });
    },
  });
}
