-- name: UpsertBenchmarkRunSummary :one
INSERT INTO benchmark_run_summary (
    run_id, workspace_id, resolved_count, total_count,
    aggregate_pass_rate, average_pass_rate, errored_count, failure_categories
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (run_id) DO UPDATE
SET resolved_count = EXCLUDED.resolved_count,
    total_count = EXCLUDED.total_count,
    aggregate_pass_rate = EXCLUDED.aggregate_pass_rate,
    average_pass_rate = EXCLUDED.average_pass_rate,
    errored_count = EXCLUDED.errored_count,
    failure_categories = EXCLUDED.failure_categories,
    computed_at = now()
RETURNING *;

-- name: GetBenchmarkRunSummary :one
SELECT * FROM benchmark_run_summary WHERE run_id = $1;

-- name: ListBenchmarkRunSummariesByRunIDs :many
SELECT * FROM benchmark_run_summary WHERE run_id = ANY($1::uuid[]);
