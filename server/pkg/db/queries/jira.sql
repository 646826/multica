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

-- name: DeleteJiraJournalByConnection :execrows
-- Connection-delete cleanup (application-code cascade, AD-3).
DELETE FROM jira_journal
WHERE connection_id = $1;

-- =====================
-- Jira Link
-- =====================

-- name: CreateJiraLink :one
INSERT INTO jira_link (
    connection_id, workspace_id, issue_id, jira_issue_id, jira_key, state, items
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetJiraLinkByJiraIssueID :one
SELECT * FROM jira_link
WHERE connection_id = $1 AND jira_issue_id = $2;

-- name: GetJiraLinkByIssueID :one
SELECT * FROM jira_link
WHERE issue_id = $1;

-- name: UpdateJiraLinkItems :exec
-- Snapshot refresh after an apply (same-tx signature-forwarding, AD-5).
UPDATE jira_link
SET items = $2, jira_key = $3, last_seen_at = now(), updated_at = now()
WHERE id = $1;

-- name: SetJiraLinkState :exec
UPDATE jira_link
SET state = $2, updated_at = now()
WHERE id = $1;


-- name: MarkJiraLinkDirty :exec
UPDATE jira_link
SET dirty = true, retry_count = retry_count + 1, retry_at = $2, updated_at = now()
WHERE id = $1;

-- name: ClearJiraLinkDirty :exec
UPDATE jira_link
SET dirty = false, retry_count = 0, retry_at = NULL, updated_at = now()
WHERE id = $1;

-- name: ListDueDirtyJiraLinks :many
SELECT * FROM jira_link
WHERE connection_id = $1 AND dirty = true AND (retry_at IS NULL OR retry_at <= now())
ORDER BY updated_at ASC
LIMIT $2;

-- name: ListJiraLinksUnseenSince :many
-- Orphan/move sweep input (FR-14): links not observed for a prolonged window.
SELECT * FROM jira_link
WHERE connection_id = $1 AND state = 'ok' AND (last_seen_at IS NULL OR last_seen_at < $2)
ORDER BY last_seen_at ASC NULLS FIRST
LIMIT $3;

-- name: TouchJiraLinksSeen :exec
UPDATE jira_link
SET last_seen_at = now()
WHERE connection_id = $1 AND jira_issue_id = ANY($2::text[]);

-- name: DeleteJiraLinksByConnection :execrows
-- Connection-delete cleanup (application-code cascade, AD-3).
DELETE FROM jira_link
WHERE connection_id = $1;

-- name: UpsertPendingJiraLink :one
-- Idempotent inbound claim (AD-15): first caller creates the pending row;
-- re-observation returns the existing row unchanged (no-op update makes
-- ON CONFLICT return it).
INSERT INTO jira_link (connection_id, workspace_id, issue_id, jira_issue_id, jira_key, state, items)
VALUES ($1, $2, NULL, $3, $4, 'pending', '{}'::jsonb)
ON CONFLICT (connection_id, jira_issue_id)
DO UPDATE SET updated_at = jira_link.updated_at
RETURNING *;

-- name: FinalizeJiraLinkInbound :exec
UPDATE jira_link
SET state = 'ok', issue_id = $2, jira_key = $3, items = $4, last_seen_at = now(), updated_at = now()
WHERE id = $1;

-- name: FindIssueIDByJiraMarker :one
-- Crash-window resolver (AD-15): adopt an already-created mirror by its
-- metadata marker instead of re-creating. Read-only touch of the core table.
SELECT id FROM issue
WHERE workspace_id = $1 AND metadata @> $2::jsonb
LIMIT 1;

-- =====================
-- Jira Comment Link
-- =====================

-- name: GetJiraCommentLinkByJiraID :one
SELECT * FROM jira_comment_link
WHERE connection_id = $1 AND jira_comment_id = $2;

-- name: CreateJiraCommentLinkInbound :one
INSERT INTO jira_comment_link (
    connection_id, workspace_id, issue_id, comment_id, jira_comment_id, origin, state
) VALUES ($1, $2, $3, $4, $5, 'inbound', 'ok')
RETURNING *;


-- name: UpdateJiraConnectionServiceAccount :exec
UPDATE jira_connection
SET service_account_id = $2, updated_at = now()
WHERE id = $1;

-- name: DeleteJiraCommentLinksByConnection :execrows
DELETE FROM jira_comment_link
WHERE connection_id = $1;

-- name: ListUnsyncedCommentsForConnection :many
-- Outbound comment detection (Story 3.2): human/agent comments on healthy
-- Linked pairs that have no identity row yet. Read-only join on core tables.
SELECT c.id, c.issue_id, c.author_type, c.author_id, c.content, c.created_at,
       jl.id AS link_id, jl.jira_issue_id, jl.jira_key
FROM comment c
JOIN jira_link jl ON jl.issue_id = c.issue_id
WHERE jl.connection_id = $1
  AND jl.state = 'ok'
  AND c.author_type IN ('member', 'agent')
  AND c.type = 'comment'
  AND NOT EXISTS (SELECT 1 FROM jira_comment_link jcl WHERE jcl.comment_id = c.id)
ORDER BY c.created_at ASC
LIMIT $2;

-- name: ClaimJiraCommentLinkOutbound :one
-- Intent-first outbound claim (AD-15): the first caller creates the pending
-- row with its marker; a concurrent/replayed caller gets the existing row.
INSERT INTO jira_comment_link (connection_id, workspace_id, issue_id, comment_id, jira_comment_id, origin, marker, state)
VALUES ($1, $2, $3, $4, '', 'outbound', $5, 'pending')
ON CONFLICT (comment_id)
DO UPDATE SET updated_at = jira_comment_link.updated_at
RETURNING *;

-- name: GetPendingOutboundCommentLinkByMarker :one
SELECT * FROM jira_comment_link
WHERE connection_id = $1 AND marker = $2 AND origin = 'outbound' AND state = 'pending';

-- name: FinalizeJiraCommentLinkOutbound :exec
UPDATE jira_comment_link
SET jira_comment_id = $2, state = 'ok', updated_at = now()
WHERE id = $1;

-- name: ListLocallyChangedLinkedIssues :many
-- Local observation (AD-2, Multica side): healthy Linked pairs whose issue
-- row changed since the local Cursor. Read-only join on the core table.
SELECT jl.*, i.updated_at AS issue_updated_at
FROM jira_link jl
JOIN issue i ON i.id = jl.issue_id
WHERE jl.connection_id = $1
  AND jl.state = 'ok'
  AND i.updated_at > $2
ORDER BY i.updated_at ASC
LIMIT $3;

-- name: ListUnlinkedLocalIssues :many
-- Outbound issue creation (Story 4.4): workspace issues with no link row yet,
-- eligible for Multica→Jira creation. origin gate excludes nothing here; the
-- caller enforces create_to_jira + a marker-based dedup.
SELECT i.* FROM issue i
WHERE i.workspace_id = $1
  AND NOT EXISTS (SELECT 1 FROM jira_link jl WHERE jl.issue_id = i.id)
  AND i.status <> 'cancelled'
  AND i.created_at > $2
ORDER BY i.created_at ASC
LIMIT $3;

-- name: FinalizeJiraLinkCreate :exec
-- Outbound create finalize (AD-15): bind the real Jira id AND the Multica
-- issue id, mark seen so the orphan sweep never flags a just-created pair.
UPDATE jira_link
SET state = 'ok', jira_issue_id = $2, issue_id = $3, jira_key = $4, items = $5,
    last_seen_at = now(), updated_at = now()
WHERE id = $1;
