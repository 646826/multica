-- Dirty-ladder scan: due retries per connection without full-table walks.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_jira_link_dirty
    ON jira_link (connection_id, retry_at) WHERE dirty;
