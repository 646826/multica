-- Deployment-wide double-writer guard (FR-1): the same Jira project may be
-- connected by at most one workspace across the deployment. The application
-- check is advisory; this index is the authority.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_jira_connection_site_project
    ON jira_connection (site_host, project_key);
