"use client";

import Link from "next/link";
import { useParams, usePathname } from "next/navigation";
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
  segment: string;
  enabled: boolean;
};

const TABS: Tab[] = [
  { key: "runs", label: "Runs", segment: "runs", enabled: false },
  { key: "suites", label: "Suites", segment: "suites", enabled: true },
  { key: "profiles", label: "Profiles", segment: "profiles", enabled: true },
  { key: "leaderboard", label: "Leaderboard", segment: "leaderboard", enabled: false },
];

export default function BenchmarksLayout({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const params = useParams<{ workspaceSlug: string }>();
  const slug = params.workspaceSlug;

  return (
    <div className="flex h-full flex-col">
      <nav
        aria-label="Benchmarks sections"
        className="flex shrink-0 items-center gap-1 border-b border-border px-4"
      >
        {TABS.map((tab) => {
          const href = `/${slug}/benchmarks/${tab.segment}`;
          const isActive = pathname === href || pathname.startsWith(href + "/");
          const baseClass =
            "relative -mb-px inline-flex items-center px-3 py-2.5 text-sm font-medium transition-colors";
          const activeClass = "border-b-2 border-foreground text-foreground";
          const inactiveClass = "border-b-2 border-transparent text-muted-foreground hover:text-foreground";

          if (!tab.enabled) {
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
              href={href}
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
