-- Native Jira sync: per-workspace Connection (PRD prd-multica-jira-sync-2026-07-16).
--
-- One row binds a workspace to one Jira Cloud project. All connector
-- configuration lives here as validated columns/JSONB (handler-enforced);
-- the API token is secretbox-encrypted (never stored or returned in plaintext).
-- Sync bookkeeping rows (jira_link / jira_journal) carry this row's id.
-- No foreign keys per repo DB rules — relationships and cleanup are resolved
-- in application code (connection delete removes dependents in one tx).

CREATE TABLE jira_connection (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    -- Jira Cloud site, e.g. https://acme.atlassian.net; site_host is the
    -- normalized host used for the deployment-wide double-writer guard.
    site_url TEXT NOT NULL,
    site_host TEXT NOT NULL,
    project_key TEXT NOT NULL,
    project_id TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL,
    token_encrypted BYTEA NOT NULL,
    connected_by_id UUID NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT false,
    -- Sync mode preset + explicit conflict winner (PRD §4.2).
    mode TEXT NOT NULL DEFAULT 'jira_leads'
        CHECK (mode IN ('mirror', 'jira_leads', 'multica_leads', 'two_way')),
    leading_system TEXT NOT NULL DEFAULT 'jira'
        CHECK (leading_system IN ('jira', 'multica')),
    -- Facet toggles (fields+status are always in scope and have no toggle).
    comments_enabled BOOLEAN NOT NULL DEFAULT true,
    labels_enabled BOOLEAN NOT NULL DEFAULT true,
    custom_fields_enabled BOOLEAN NOT NULL DEFAULT true,
    -- Creation flows (validated against mode: mirror forces create_to_jira off).
    create_from_jira BOOLEAN NOT NULL DEFAULT true,
    create_to_jira BOOLEAN NOT NULL DEFAULT false,
    jql_filter TEXT NOT NULL DEFAULT '',
    label_prefix TEXT NOT NULL DEFAULT '',
    mention_bridge_enabled BOOLEAN NOT NULL DEFAULT true,
    outbound_issue_type TEXT NOT NULL DEFAULT 'Task',
    -- status_map: {"jira_status_id": "multica_status", "_out": {"multica_status": "jira_status_id"}}
    -- shape validated in the handler; stored as data (AD-8).
    status_map JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(status_map) = 'object'),
    -- field_map: [{"external_field", "property_id", "direction"}]
    field_map JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(field_map) = 'array'),
    -- tag_rules: [{"match_type": "label"|"assignee", "match_value", "agent_id"}]
    tag_rules JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(tag_rules) = 'array'),
    cycle_interval_seconds INT NOT NULL DEFAULT 45
        CHECK (cycle_interval_seconds >= 30 AND cycle_interval_seconds <= 300),
    -- Reconcile cursors (AD-2): observation watermarks per side.
    jira_cursor TIMESTAMPTZ,
    local_cursor TIMESTAMPTZ,
    -- Last-cycle health snapshot rendered by the API; counters derive from jira_journal.
    health JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(health) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
