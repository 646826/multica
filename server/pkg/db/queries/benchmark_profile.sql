-- name: CreateBenchmarkProfile :one
INSERT INTO benchmark_agent_profile (
    workspace_id, slug, display_name, agent_id, agent_name, model,
    prompt_source, prompt_hash, attached_skills, captured_by
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetBenchmarkProfile :one
SELECT * FROM benchmark_agent_profile WHERE id = $1 AND workspace_id = $2;

-- name: GetBenchmarkProfileBySlug :one
SELECT * FROM benchmark_agent_profile WHERE workspace_id = $1 AND slug = $2;

-- name: ListBenchmarkProfiles :many
SELECT * FROM benchmark_agent_profile
WHERE workspace_id = $1
ORDER BY captured_at DESC;

-- name: ListBenchmarkProfilesForAgent :many
SELECT * FROM benchmark_agent_profile
WHERE workspace_id = $1 AND agent_id = $2
ORDER BY captured_at DESC;

-- name: FindProfileByHash :one
SELECT * FROM benchmark_agent_profile
WHERE workspace_id = $1 AND prompt_hash = $2
LIMIT 1;

-- name: DeleteBenchmarkProfile :execrows
DELETE FROM benchmark_agent_profile WHERE id = $1 AND workspace_id = $2;
