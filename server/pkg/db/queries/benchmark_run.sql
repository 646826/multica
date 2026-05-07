-- name: CreateBenchmarkRun :one
INSERT INTO benchmark_run (
    workspace_id, suite_id, suite_instance_ids, profile_id, base_run_id,
    display_name, status, evaluator_mode, adapter_version, submission_timeout_seconds, created_by
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetBenchmarkRun :one
SELECT * FROM benchmark_run WHERE id = $1 AND workspace_id = $2;

-- name: ListBenchmarkRuns :many
SELECT * FROM benchmark_run WHERE workspace_id = $1
ORDER BY created_at DESC LIMIT $2;
