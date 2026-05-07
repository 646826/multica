"use client";

import Link from "next/link";
import { useParams, usePathname } from "next/navigation";
import { paths } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";

/**
 * Benchmarks dashboard sub-nav.
 *
 * Phase 0 ships with two enabled tabs (Suites, Profiles) and two
 * placeholders (Runs, Leaderboard) that arrive in Phase 1. Disabled
 * tabs render as non-interactive list items rather than links so
 * keyboard navigation and screen readers correctly skip them.
 */
type Tab = {
  key: "runs" | "suites" | "profiles" | "leaderboard";
  label: string;
  href: string | null;
  enabled: boolean;
};

export default function BenchmarksLayout({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const params = useParams<{ workspaceSlug: string }>();
  const slug = params.workspaceSlug;
  const wsPaths = paths.workspace(slug);

  const tabs: Tab[] = [
    { key: "runs", label: "Runs", href: null, enabled: false },
    { key: "suites", label: "Suites", href: wsPaths.benchmarkSuites(), enabled: true },
    { key: "profiles", label: "Profiles", href: wsPaths.benchmarkProfiles(), enabled: true },
    { key: "leaderboard", label: "Leaderboard", href: null, enabled: false },
  ];

  return (
    <div className="flex h-full flex-col">
      <nav
        aria-label="Benchmarks sections"
        className="flex shrink-0 items-center gap-1 border-b border-border px-4"
      >
        {tabs.map((tab) => {
          const isActive =
            tab.href !== null && (pathname === tab.href || pathname.startsWith(tab.href + "/"));
          const baseClass =
            "relative -mb-px inline-flex items-center px-3 py-2.5 text-sm font-medium transition-colors";
          const activeClass = "border-b-2 border-foreground text-foreground";
          const inactiveClass = "border-b-2 border-transparent text-muted-foreground hover:text-foreground";

          if (!tab.enabled || tab.href === null) {
            return (
              <span
                key={tab.key}
                aria-disabled="true"
                tabIndex={-1}
                title="Available after Phase 1"
                className={cn(
                  baseClass,
                  "border-b-2 border-transparent text-muted-foreground opacity-50 cursor-not-allowed",
                )}
              >
                {tab.label}
              </span>
            );
          }

          return (
            <Link
              key={tab.key}
              href={tab.href}
              aria-current={isActive ? "page" : undefined}
              className={cn(baseClass, isActive ? activeClass : inactiveClass)}
            >
              {tab.label}
            </Link>
          );
        })}
      </nav>
      <div className="min-h-0 flex-1 overflow-auto">{children}</div>
    </div>
  );
}
