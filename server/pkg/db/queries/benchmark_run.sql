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

-- name: UpdateBenchmarkRunStatus :one
UPDATE benchmark_run
SET status = $3, status_reason = $4,
    started_at = COALESCE(started_at, CASE WHEN $3 = 'submitting' THEN now() ELSE started_at END),
    completed_at = COALESCE(completed_at, CASE WHEN $3 IN ('complete', 'failed', 'canceled') THEN now() ELSE completed_at END)
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: ListActiveBenchmarkRuns :many
SELECT * FROM benchmark_run
WHERE status IN ('queued', 'submitting', 'evaluating')
ORDER BY created_at ASC;

-- name: ListBenchmarkRunsBySuite :many
SELECT * FROM benchmark_run
WHERE workspace_id = $1 AND suite_id = $2 AND status = 'complete'
ORDER BY completed_at DESC LIMIT $3;

-- name: CountActiveTasksForRun :one
SELECT
    COUNT(*) FILTER (WHERE status IN ('queued', 'issued', 'submitted', 'evaluating')) AS active,
    COUNT(*) FILTER (WHERE status = 'scored') AS scored,
    COUNT(*) FILTER (WHERE status = 'errored') AS errored,
    COUNT(*) FILTER (WHERE status = 'skipped') AS skipped,
    COUNT(*) AS total
FROM benchmark_task
WHERE run_id = $1;
