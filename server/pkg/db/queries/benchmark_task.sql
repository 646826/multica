-- name: CreateBenchmarkTask :one
INSERT INTO benchmark_task (
    run_id, workspace_id, instance_id, instance_meta, status, status_reason
) VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetBenchmarkTask :one
SELECT * FROM benchmark_task WHERE id = $1 AND workspace_id = $2;

-- name: GetBenchmarkTaskByInstance :one
SELECT * FROM benchmark_task WHERE run_id = $1 AND instance_id = $2;

-- name: ListBenchmarkTasksByRun :many
SELECT * FROM benchmark_task WHERE run_id = $1 ORDER BY instance_id ASC;

-- name: ListBenchmarkTasksByRunStatus :many
SELECT * FROM benchmark_task WHERE run_id = $1 AND status = $2 ORDER BY instance_id ASC;

-- name: ListIncompleteTasksForRun :many
SELECT * FROM benchmark_task
WHERE run_id = $1
  AND status IN ('queued', 'issued', 'submitted', 'evaluating')
ORDER BY instance_id ASC;

-- name: UpdateBenchmarkTaskStatus :one
UPDATE benchmark_task
SET status = $3, status_reason = $4,
    submitted_at = COALESCE(submitted_at, CASE WHEN $3 IN ('submitted', 'evaluating') THEN now() ELSE submitted_at END),
    scored_at = COALESCE(scored_at, CASE WHEN $3 IN ('scored', 'errored', 'skipped') THEN now() ELSE scored_at END)
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: AttachIssueToTask :exec
UPDATE benchmark_task SET issue_id = $3 WHERE id = $1 AND workspace_id = $2;

-- name: AttachAttachmentToTask :exec
UPDATE benchmark_task SET attachment_id = $3 WHERE id = $1 AND workspace_id = $2;

-- name: GetBenchmarkTaskByIssue :one
SELECT * FROM benchmark_task WHERE issue_id = $1;

-- name: ListIssuedTasksPastTimeout :many
SELECT t.* FROM benchmark_task t
JOIN benchmark_run r ON r.id = t.run_id
WHERE t.status = 'issued'
  AND t.created_at < now() - make_interval(secs => r.submission_timeout_seconds);
