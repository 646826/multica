import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** Query key namespace for the workspace's Jira Connection. The settings
 * section invalidates `connection(wsId)` after every management mutation
 * (connect / patch / delete) — sync state itself refreshes on read. */
export const jiraKeys = {
  all: (wsId: string) => ["jira", wsId] as const,
  connection: (wsId: string) => [...jiraKeys.all(wsId), "connection"] as const,
};

export const jiraConnectionOptions = (wsId: string) =>
  queryOptions({
    queryKey: jiraKeys.connection(wsId),
    queryFn: () => api.getJiraConnection(wsId),
    enabled: !!wsId,
  });
