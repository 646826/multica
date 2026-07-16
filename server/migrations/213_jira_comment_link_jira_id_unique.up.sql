-- One link per Jira comment (partial: outbound intents have no id yet).
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_jira_comment_link_jira_id
    ON jira_comment_link (connection_id, jira_comment_id) WHERE jira_comment_id <> '';
