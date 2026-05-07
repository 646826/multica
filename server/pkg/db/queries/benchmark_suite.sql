-- name: CreateBenchmarkSuite :one
INSERT INTO benchmark_suite (
    workspace_id, slug, display_name, adapter_kind, instance_ids, description, created_by
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetBenchmarkSuite :one
SELECT * FROM benchmark_suite WHERE id = $1 AND workspace_id = $2;

-- name: GetBenchmarkSuiteBySlug :one
SELECT * FROM benchmark_suite WHERE workspace_id = $1 AND slug = $2;

-- name: ListBenchmarkSuites :many
SELECT * FROM benchmark_suite
WHERE workspace_id = $1
ORDER BY created_at DESC;

-- name: UpdateBenchmarkSuite :one
UPDATE benchmark_suite
SET display_name = $3, instance_ids = $4, description = $5
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteBenchmarkSuite :exec
DELETE FROM benchmark_suite WHERE id = $1 AND workspace_id = $2;

-- name: CountBenchmarkRunsForSuite :one
SELECT COUNT(*) FROM benchmark_run WHERE suite_id = $1;
