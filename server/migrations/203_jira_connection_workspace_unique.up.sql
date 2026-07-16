-- One Connection per workspace (PRD: 1 workspace ↔ 1 Jira project in v1).
-- Separate single-statement migration: CREATE UNIQUE INDEX CONCURRENTLY cannot
-- run inside a transaction or share a migration file.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_jira_connection_workspace
    ON jira_connection (workspace_id);
