// Native Jira sync — wire types (snake_case mirrors the Go DTOs).
// New backend fields must stay optional so older desktop clients keep
// parsing (see types/slack.ts forward-compat note).

export type JiraSyncMode = "mirror" | "jira_leads" | "multica_leads" | "two_way";
export type JiraLeadingSystem = "jira" | "multica";

export interface JiraHealth {
  state?: string; // ok | degraded | auth_expired | forbidden (server-driven; keep open)
  last_cycle_at?: string;
  last_error?: string;
  requests_last_cycle?: number;
}

export interface JiraConnection {
  id: string;
  site_url: string;
  project_key: string;
  project_id?: string;
  email: string;
  enabled: boolean;
  mode: JiraSyncMode | (string & {});
  leading_system: JiraLeadingSystem | (string & {});
  comments_enabled: boolean;
  labels_enabled: boolean;
  custom_fields_enabled: boolean;
  create_from_jira: boolean;
  create_to_jira: boolean;
  jql_filter: string;
  label_prefix: string;
  mention_bridge_enabled: boolean;
  outbound_issue_type: string;
  status_map?: unknown;
  field_map?: unknown;
  tag_rules?: unknown;
  cycle_interval_seconds: number;
  health?: JiraHealth;
  jira_cursor?: string;
  created_at?: string;
  updated_at?: string;
}

export interface JiraConnectionEnvelope {
  configured: boolean;
  can_manage: boolean;
  connection: JiraConnection | null;
}

export interface ConnectJiraPayload {
  site_url: string;
  email: string;
  token: string;
  project_key: string;
  project_id?: string;
  mode?: JiraSyncMode;
}

export interface UpdateJiraConnectionPayload {
  enabled?: boolean;
  mode?: JiraSyncMode;
  leading_system?: JiraLeadingSystem;
  comments_enabled?: boolean;
  labels_enabled?: boolean;
  custom_fields_enabled?: boolean;
  create_from_jira?: boolean;
  create_to_jira?: boolean;
  jql_filter?: string;
  label_prefix?: string;
  mention_bridge_enabled?: boolean;
  outbound_issue_type?: string;
  status_map?: unknown;
  field_map?: unknown;
  tag_rules?: unknown;
  cycle_interval_seconds?: number;
  email?: string;
  token?: string;
}

export interface JiraProject {
  id: string;
  key: string;
  name: string;
}

export interface ListJiraProjectsResponse {
  projects: JiraProject[];
}
