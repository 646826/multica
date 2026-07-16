-- Inbound intent-first creation (AD-15): a 'pending' Link claims the Jira
-- issue id BEFORE the Multica issue exists, so re-observation can never
-- double-import. issue_id becomes nullable for that pending window; the
-- UNIQUE(issue_id) index treats NULLs as distinct.
ALTER TABLE jira_link ALTER COLUMN issue_id DROP NOT NULL;
