"use client";

import Link from "next/link";
import { useParams, usePathname } from "next/navigation";
import { paths } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "@multica/views/i18n";

/**
 * Benchmarks dashboard sub-nav.
 *
 * Phase 0 ships with two enabled tabs (Suites, Profiles) and two
 * placeholders (Runs, Leaderboard) that arrive in Phase 1. Disabled
 * tabs render as non-interactive list items rather than links so
 * keyboard navigation and screen readers correctly skip them.
 */
type TabKey = "runs" | "suites" | "profiles" | "leaderboard";

type Tab = {
  key: TabKey;
  href: string | null;
  enabled: boolean;
};

export default function BenchmarksLayout({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const params = useParams<{ workspaceSlug: string }>();
  const slug = params.workspaceSlug;
  const wsPaths = paths.workspace(slug);
  const { t } = useT("benchmarks");

  const tabs: Tab[] = [
    { key: "runs", href: wsPaths.benchmarkRuns(), enabled: true },
    { key: "suites", href: wsPaths.benchmarkSuites(), enabled: true },
    { key: "profiles", href: wsPaths.benchmarkProfiles(), enabled: true },
    { key: "leaderboard", href: null, enabled: false },
  ];

  return (
    <div className="flex h-full flex-col">
      <nav
        aria-label={t(($) => $.tabs.aria_label)}
        className="flex shrink-0 items-center gap-1 border-b border-border px-4"
      >
        {tabs.map((tab) => {
          const isActive =
            tab.href !== null && (pathname === tab.href || pathname.startsWith(tab.href + "/"));
          const baseClass =
            "relative -mb-px inline-flex items-center px-3 py-2.5 text-sm font-medium transition-colors";
          const activeClass = "border-b-2 border-foreground text-foreground";
          const inactiveClass = "border-b-2 border-transparent text-muted-foreground hover:text-foreground";

          const label = t(($) => $.tabs[tab.key]);

          if (!tab.enabled || tab.href === null) {
            return (
              <span
                key={tab.key}
                aria-disabled="true"
                tabIndex={-1}
                title={t(($) => $.tabs.phase1_tooltip)}
                className={cn(
                  baseClass,
                  "border-b-2 border-transparent text-muted-foreground opacity-50 cursor-not-allowed",
                )}
              >
                {label}
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
              {label}
            </Link>
          );
        })}
      </nav>
      <div className="min-h-0 flex-1 overflow-auto">{children}</div>
    </div>
  );
}
