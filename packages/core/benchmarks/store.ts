"use client";

import { create } from "zustand";

/**
 * UI-only state for the benchmarks views — free-text filter strings shared
 * across the suite-list and profile-list pages. Server data lives in the
 * React Query cache (see {@link ./queries}); this store holds nothing
 * fetchable.
 */
interface BenchmarksUIState {
  suiteFilter: string;
  profileFilter: string;
  setSuiteFilter: (s: string) => void;
  setProfileFilter: (s: string) => void;
}

export const useBenchmarksUI = create<BenchmarksUIState>((set) => ({
  suiteFilter: "",
  profileFilter: "",
  setSuiteFilter: (s) => set({ suiteFilter: s }),
  setProfileFilter: (s) => set({ profileFilter: s }),
}));
