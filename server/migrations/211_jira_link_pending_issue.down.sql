DELETE FROM jira_link WHERE issue_id IS NULL;
ALTER TABLE jira_link ALTER COLUMN issue_id SET NOT NULL;
