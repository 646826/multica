-- =====================
-- Jira Connection
-- =====================

-- name: GetJiraConnectionByWorkspace :one
SELECT * FROM jira_connection
WHERE workspace_id = $1;

-- name: GetJiraConnectionByID :one
SELECT * FROM jira_connection
WHERE id = $1;

-- name: GetJiraConnectionBySiteProject :one
-- Deployment-wide double-writer lookup (FR-1); the unique index is the
-- authority, this query powers the friendly pre-check error.
SELECT * FROM jira_connection
WHERE site_host = $1 AND project_key = $2;

-- name: ListEnabledJiraConnections :many
-- Worker tick input: every enabled Connection, processed independently.
SELECT * FROM jira_connection
WHERE enabled = true
ORDER BY created_at ASC, id ASC;

-- name: CreateJiraConnection :one
INSERT INTO jira_connection (
    workspace_id, site_url, site_host, project_key, project_id, email,
    token_encrypted, connected_by_id, mode, leading_system,
    comments_enabled, labels_enabled, custom_fields_enabled,
    create_from_jira, create_to_jira, jql_filter, label_prefix,
    mention_bridge_enabled, outbound_issue_type, status_map,
    cycle_interval_seconds
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10,
    $11, $12, $13,
    $14, $15, $16, $17,
    $18, $19, $20,
    $21
)
RETURNING *;

-- name: UpdateJiraConnectionConfig :one
-- Full-set update of mutable config; the handler validates and merges partial
-- PATCH payloads before calling (strict server-side validation, AD-8).
UPDATE jira_connection
SET enabled = $2,
    mode = $3,
    leading_system = $4,
    comments_enabled = $5,
    labels_enabled = $6,
    custom_fields_enabled = $7,
    create_from_jira = $8,
    create_to_jira = $9,
    jql_filter = $10,
    label_prefix = $11,
    mention_bridge_enabled = $12,
    outbound_issue_type = $13,
    status_map = $14,
    field_map = $15,
    tag_rules = $16,
    cycle_interval_seconds = $17,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateJiraConnectionToken :exec
-- Token rotation path; write-only (the token is never read back via API).
UPDATE jira_connection
SET email = $2, token_encrypted = $3, updated_at = now()
WHERE id = $1;

-- name: UpdateJiraConnectionCursors :exec
UPDATE jira_connection
SET jira_cursor = $2, local_cursor = $3, updated_at = now()
WHERE id = $1;

-- name: UpdateJiraConnectionHealth :exec
UPDATE jira_connection
SET health = $2, updated_at = now()
WHERE id = $1;

-- name: DeleteJiraConnection :exec
DELETE FROM jira_connection
WHERE id = $1;

-- =====================
-- Jira Journal
-- =====================

-- name: InsertJiraJournal :one
INSERT INTO jira_journal (
    connection_id, workspace_id, cycle_id, kind, issue_id, jira_key, detail
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
RETURNING *;

-- name: ListJiraJournalRecent :many
SELECT * FROM jira_journal
WHERE connection_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2;

-- name: CountJiraJournalByKindSince :many
-- Health skip-class counters (NFR-6): per-kind counts over a recent window.
SELECT kind, COUNT(*) AS count FROM jira_journal
WHERE connection_id = $1 AND created_at >= $2
GROUP BY kind;

-- name: PruneJiraJournalByAge :execrows
DELETE FROM jira_journal
WHERE connection_id = $1 AND created_at < $2;

-- name: PruneJiraJournalByCount :execrows
-- Keep the newest $2 rows per Connection (retention: 30d or 100k, AD-14).
DELETE FROM jira_journal
WHERE id IN (
    SELECT j2.id FROM jira_journal j2
    WHERE j2.connection_id = $1
    ORDER BY j2.created_at DESC, j2.id DESC
    OFFSET $2
);
