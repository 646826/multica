-- Journal listing and retention pruning both scan per Connection by recency.
-- Separate single-statement migration: CREATE INDEX CONCURRENTLY cannot run
-- inside a transaction or share a migration file.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_jira_journal_connection_created
    ON jira_journal (connection_id, created_at DESC);
