-- One Link per Multica issue (1:1 pairs; also the badge lookup path).
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_jira_link_issue
    ON jira_link (issue_id);
