import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import { benchmarkKeys } from "./queries";
import type { CaptureProfileRequest, CreateSuiteRequest } from "../types";

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
