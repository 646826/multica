-- Native Jira sync: the Link — 1:1 association between a Multica issue and a
-- Jira issue (identity = immutable Jira issue id; the key is display data).
--
-- items JSONB carries the per-item two-sided last-synced state (pinned v1
-- shape, ARCHITECTURE-SPINE AD-5): scalar text items store sha256 of each
-- side's raw/canonical value plus a breadcrumb-dedup marker; status stores the
-- raw pair; labels store materialized sets (last_synced + propagated). All
-- sync correctness state lives HERE, never in the journal (AD-3/AD-14).
-- Dirty flags implement the transient-failure retry ladder (AD-2): failed
-- items re-enter the observe set without pinning the Cursor.

CREATE TABLE jira_link (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    issue_id UUID NOT NULL,
    jira_issue_id TEXT NOT NULL,
    jira_key TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'ok' CHECK (state IN ('ok', 'pending', 'dormant', 'orphaned')),
    items JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(items) = 'object'),
    dirty BOOLEAN NOT NULL DEFAULT false,
    retry_at TIMESTAMPTZ,
    retry_count INT NOT NULL DEFAULT 0,
    last_seen_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
