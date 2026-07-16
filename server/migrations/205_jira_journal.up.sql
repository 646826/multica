-- Native Jira sync: per-Connection journal (AD-14).
--
-- Append-only observability record behind Health counters ("why didn't X
-- sync"): policy skips, guards, conflicts, degradations, rule applications,
-- failures and their paired recovery events. Kinds come from a closed Go
-- registry (integrations/jira/journal.go); adding a kind is a code change.
-- Bounded retention (age/count pruning) — no correctness logic may read this
-- table; durable sync state lives on jira_link rows (AD-3/AD-5).

CREATE TABLE jira_journal (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connection_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    cycle_id UUID,
    kind TEXT NOT NULL,
    issue_id UUID,
    jira_key TEXT NOT NULL DEFAULT '',
    detail JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(detail) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
