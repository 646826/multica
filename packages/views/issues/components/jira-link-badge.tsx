"use client";

import { useQuery } from "@tanstack/react-query";
import { ExternalLink } from "lucide-react";
import { api } from "@multica/core/api";
import { openExternal } from "../../platform";
import { useT } from "../../i18n";

// JiraLinkBadge shows the linked Jira issue on the issue page (FR-13):
// key → deep link, plus the sync-state chip. Renders nothing for unlinked
// issues, so mounting it unconditionally is free.
export function JiraLinkBadge({ issueId }: { issueId: string }) {
  const { t } = useT("issues");
  const { data } = useQuery({
    queryKey: ["jira", "issue-link", issueId],
    queryFn: () => api.getIssueJiraLink(issueId),
    enabled: !!issueId,
  });

  if (!data?.linked || !data.jira_key) return null;

  return (
    <button
      type="button"
      data-testid="jira-link-badge"
      onClick={() => data.url && openExternal(data.url)}
      title={t(($) => $.jira_link.open_in_jira)}
      className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent"
    >
      <ExternalLink className="size-3.5 shrink-0 text-muted-foreground" />
      <span className="truncate font-medium">{data.jira_key}</span>
      {data.state && data.state !== "ok" ? (
        <span className="ml-auto shrink-0 rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
          {data.state}
        </span>
      ) : null}
    </button>
  );
}
