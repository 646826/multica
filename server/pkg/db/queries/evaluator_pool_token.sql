-- name: CreateEvaluatorPoolToken :one
INSERT INTO evaluator_pool_token (
    workspace_id, token_prefix, token_hash, display_name, created_by
) VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListEvaluatorPoolTokens :many
SELECT id, workspace_id, token_prefix, display_name, created_at, created_by, last_used_at, revoked_at
FROM evaluator_pool_token
WHERE workspace_id = $1
ORDER BY created_at DESC;

-- name: GetEvaluatorPoolTokenByHash :one
-- Returns the token regardless of revoked_at; the service layer is
-- responsible for distinguishing "not found" from "revoked".
SELECT * FROM evaluator_pool_token
WHERE token_hash = $1;

-- name: TouchEvaluatorPoolToken :exec
UPDATE evaluator_pool_token SET last_used_at = now() WHERE id = $1;

-- name: RevokeEvaluatorPoolToken :exec
UPDATE evaluator_pool_token SET revoked_at = now()
WHERE id = $1 AND workspace_id = $2 AND revoked_at IS NULL;
