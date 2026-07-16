-- The service account's Jira accountId: the comment actor filter (AD-5) —
-- comments it authored are never mirrored as content, only scanned for
-- outbound intent markers. Captured at connect and on token rotation.
ALTER TABLE jira_connection ADD COLUMN service_account_id TEXT NOT NULL DEFAULT '';
