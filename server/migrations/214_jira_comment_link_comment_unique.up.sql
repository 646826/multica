-- One link per Multica comment (NULLs distinct: inbound rows before create).
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_jira_comment_link_comment
    ON jira_comment_link (comment_id);
