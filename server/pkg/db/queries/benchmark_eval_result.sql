-- name: UpsertBenchmarkEvalResult :one
INSERT INTO benchmark_eval_result (
    task_id, workspace_id, resolved, passed_tests, total_tests, pass_rate,
    raw_eval_json, failed_categories
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (task_id) DO UPDATE
SET resolved = EXCLUDED.resolved,
    passed_tests = EXCLUDED.passed_tests,
    total_tests = EXCLUDED.total_tests,
    pass_rate = EXCLUDED.pass_rate,
    raw_eval_json = EXCLUDED.raw_eval_json,
    failed_categories = EXCLUDED.failed_categories,
    evaluated_at = now()
RETURNING *;

-- name: GetBenchmarkEvalResult :one
SELECT * FROM benchmark_eval_result WHERE task_id = $1;

-- name: ListBenchmarkEvalResultsForRun :many
SELECT er.* FROM benchmark_eval_result er
JOIN benchmark_task t ON t.id = er.task_id
WHERE t.run_id = $1
ORDER BY t.instance_id ASC;
