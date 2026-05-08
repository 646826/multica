-- name: CreateBenchmarkEvalJob :one
INSERT INTO benchmark_eval_job (
    task_id, workspace_id, adapter_kind, state
) VALUES ($1, $2, $3, 'pending')
RETURNING *;

-- name: ClaimBenchmarkEvalJobs :many
WITH claimed AS (
    SELECT id FROM benchmark_eval_job
    WHERE state = 'pending'
      AND benchmark_eval_job.workspace_id = $1
      AND adapter_kind = ANY($2::text[])
    ORDER BY enqueued_at ASC
    LIMIT $3
    FOR UPDATE SKIP LOCKED
)
UPDATE benchmark_eval_job
SET state = 'claimed', claimed_by = $4, claimed_at = now()
WHERE id IN (SELECT id FROM claimed)
RETURNING *;

-- name: CompleteBenchmarkEvalJob :exec
UPDATE benchmark_eval_job
SET state = 'done', finished_at = now()
WHERE id = $1;

-- name: FailBenchmarkEvalJob :one
UPDATE benchmark_eval_job
SET state = CASE WHEN attempt + 1 >= $3 THEN 'failed' ELSE 'pending' END,
    attempt = attempt + 1,
    last_error = $2,
    claimed_by = NULL,
    claimed_at = NULL,
    finished_at = CASE WHEN attempt + 1 >= $3 THEN now() ELSE finished_at END
WHERE id = $1
RETURNING *;

-- name: ReclaimStuckEvalJobs :many
UPDATE benchmark_eval_job
SET state = 'pending', claimed_by = NULL, claimed_at = NULL
WHERE state = 'claimed' AND claimed_at < now() - make_interval(secs => $1)
RETURNING *;

-- name: GetBenchmarkEvalJob :one
SELECT * FROM benchmark_eval_job WHERE id = $1;

-- name: DeleteBenchmarkEvalJobsForRun :exec
DELETE FROM benchmark_eval_job
WHERE task_id IN (SELECT id FROM benchmark_task WHERE run_id = $1);
