-- Link identity: one Link per Jira issue (immutable id). Separate
-- single-statement migration: CREATE UNIQUE INDEX CONCURRENTLY cannot run
-- inside a transaction or share a migration file.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_jira_link_jira_issue
    ON jira_link (connection_id, jira_issue_id);
