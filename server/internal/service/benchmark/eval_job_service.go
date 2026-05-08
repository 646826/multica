package benchmark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// evalJobMaxAttempts is the upper bound on FailBenchmarkEvalJob attempts
// before the job flips from 'pending' to 'failed' (and the corresponding
// task to 'errored'). Three matches the convention in TaskDispatcher and
// is enough to ride out transient evaluator/network errors without
// looping forever on a poisonous payload.
const evalJobMaxAttempts = 3

// ErrEvalJobNotFound is returned by EvalJobService methods when the
// targeted benchmark_eval_job row does not exist. Handlers map this to
// a 404 response.
var ErrEvalJobNotFound = errors.New("benchmark: eval job not found")

// ClaimedJob is the service-layer view of a successfully claimed
// eval_job. It bundles enough context for an external evaluator to
// download the submission archive and run scoring without making
// further round-trips: instance identity + adapter kind + a download
// URL pointing at the submission attachment (empty if no attachment
// was recorded yet — defensive; in practice the dispatcher always
// records one before enqueuing the eval job).
type ClaimedJob struct {
	JobID                 pgtype.UUID
	TaskID                pgtype.UUID
	InstanceID            string
	InstanceMeta          json.RawMessage
	AdapterKind           string
	AttachmentID          pgtype.UUID
	SubmissionDownloadURL string
}

// CompleteEvalJobInput is the validated input to EvalJobService.Complete.
// Mirrors RunService.ImportEvalResultInput but is keyed by job_id rather
// than (run_id, instance_id) — the evaluator already holds the job_id
// it claimed, so there is no need for the indirection.
type CompleteEvalJobInput struct {
	JobID            pgtype.UUID
	Resolved         bool
	PassedTests      int
	TotalTests       int
	PassRate         float64
	RawEvalJSON      json.RawMessage
	FailedCategories []string
}

// EvalJobService is the server-side helper for the evaluator pool.
// It owns the three lifecycle transitions an external evaluator
// performs over its working life: Claim (pending → claimed), Complete
// (claimed → done + task scored), and Fail (claimed → pending|failed,
// with task → errored on permanent failure).
//
// Stuck-job recovery (claimed-too-long → pending) lives in a separate
// watchdog (T08+) that calls ReclaimStuckEvalJobs directly.
type EvalJobService struct {
	q    *db.Queries
	pool *pgxpool.Pool
	bus  Publisher
}

// NewEvalJobService constructs an EvalJobService bound to the given
// query set, connection pool (for transactional Complete), and event bus.
func NewEvalJobService(q *db.Queries, pool *pgxpool.Pool, bus Publisher) *EvalJobService {
	return &EvalJobService{q: q, pool: pool, bus: bus}
}

// Claim atomically picks up to max pending eval_jobs whose adapter_kind
// matches one of adapterKinds, marks them 'claimed' by evaluatorID,
// and returns the data the evaluator needs to start scoring. The
// underlying SQL uses FOR UPDATE SKIP LOCKED so concurrent claimers
// never see the same row. A zero or negative max — or an empty
// adapterKinds list — is treated as "no work" and returns nil without
// touching the database.
//
// If the eval_job's task row has gone missing (defensive — referential
// integrity should prevent this), the job is marked failed and skipped
// rather than returned to the caller with garbage fields.
func (s *EvalJobService) Claim(ctx context.Context, evaluatorID string, adapterKinds []string, max int32) ([]ClaimedJob, error) {
	if max <= 0 || len(adapterKinds) == 0 {
		return nil, nil
	}
	rows, err := s.q.ClaimBenchmarkEvalJobs(ctx, db.ClaimBenchmarkEvalJobsParams{
		Column1:   adapterKinds,
		Limit:     max,
		ClaimedBy: pgtype.Text{String: evaluatorID, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("claim jobs: %w", err)
	}
	out := make([]ClaimedJob, 0, len(rows))
	for _, r := range rows {
		task, err := s.q.GetBenchmarkTask(ctx, db.GetBenchmarkTaskParams{
			ID:          r.TaskID,
			WorkspaceID: r.WorkspaceID,
		})
		if err != nil {
			// Task vanished between enqueue and claim — fail the job
			// permanently so it doesn't keep cycling. We deliberately
			// pass attempt=1 so the single increment inside the SQL
			// pushes it straight to 'failed'.
			_, _ = s.q.FailBenchmarkEvalJob(ctx, db.FailBenchmarkEvalJobParams{
				ID:        r.ID,
				LastError: "task lookup failed during claim",
				Attempt:   1,
			})
			continue
		}
		cj := ClaimedJob{
			JobID:        r.ID,
			TaskID:       r.TaskID,
			InstanceID:   task.InstanceID,
			InstanceMeta: task.InstanceMeta,
			AdapterKind:  r.AdapterKind,
			AttachmentID: task.AttachmentID,
		}
		if task.AttachmentID.Valid {
			cj.SubmissionDownloadURL = "/api/attachments/" + util.UUIDToString(task.AttachmentID) + "/download"
		}
		out = append(out, cj)
	}
	return out, nil
}

// Complete records the evaluator's verdict for a claimed job: it
// upserts the eval_result row, advances the corresponding task to
// 'scored', and marks the job 'done' — all inside a single transaction
// so a partial failure can never leave the system with a scored task
// missing its result, or vice versa. Mirrors the transactional shape
// of RunService.ImportEvalResult.
//
// On success EventBenchmarkTaskScored is published. Returns
// ErrEvalJobNotFound if the job does not exist; otherwise propagates
// the underlying error.
func (s *EvalJobService) Complete(ctx context.Context, in CompleteEvalJobInput) error {
	job, err := s.q.GetBenchmarkEvalJob(ctx, in.JobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrEvalJobNotFound
	}
	if err != nil {
		return fmt.Errorf("get eval job: %w", err)
	}

	failedCats, err := json.Marshal(normalizeFailedCategories(in.FailedCategories))
	if err != nil {
		return fmt.Errorf("encode failed_categories: %w", err)
	}
	rawEval := in.RawEvalJSON
	if rawEval == nil {
		rawEval = json.RawMessage(`null`)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	task, err := qtx.GetBenchmarkTask(ctx, db.GetBenchmarkTaskParams{
		ID:          job.TaskID,
		WorkspaceID: job.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("get task: %w", err)
	}

	if _, err := qtx.UpsertBenchmarkEvalResult(ctx, db.UpsertBenchmarkEvalResultParams{
		TaskID:           task.ID,
		WorkspaceID:      task.WorkspaceID,
		Resolved:         in.Resolved,
		PassedTests:      int32(in.PassedTests),
		TotalTests:       int32(in.TotalTests),
		PassRate:         pgNumeric(in.PassRate),
		RawEvalJson:      []byte(rawEval),
		FailedCategories: failedCats,
	}); err != nil {
		return fmt.Errorf("upsert eval_result: %w", err)
	}

	if _, err := qtx.UpdateBenchmarkTaskStatus(ctx, db.UpdateBenchmarkTaskStatusParams{
		ID:           task.ID,
		WorkspaceID:  task.WorkspaceID,
		Status:       "scored",
		StatusReason: "evaluator_complete",
	}); err != nil {
		return fmt.Errorf("advance task to scored: %w", err)
	}

	if err := qtx.CompleteBenchmarkEvalJob(ctx, in.JobID); err != nil {
		return fmt.Errorf("complete eval job: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	if s.bus != nil {
		s.bus.Publish(events.Event{
			Type:        protocol.EventBenchmarkTaskScored,
			WorkspaceID: util.UUIDToString(task.WorkspaceID),
			TaskID:      util.UUIDToString(task.ID),
			Payload: map[string]any{
				"run_id":      util.UUIDToString(task.RunID),
				"task_id":     util.UUIDToString(task.ID),
				"instance_id": task.InstanceID,
				"resolved":    in.Resolved,
				"pass_rate":   in.PassRate,
			},
		})
	}
	return nil
}

// Fail records a failed evaluator attempt. The underlying SQL bumps
// attempt and atomically picks the next state: under the cap the job
// returns to 'pending' for re-claiming, at the cap it flips to
// 'failed' and the task is advanced to 'errored' so the run can move
// on. Publishes EventBenchmarkTaskStatus only on permanent failure
// (transient retries are evaluator-internal and don't deserve a UI
// status flicker).
func (s *EvalJobService) Fail(ctx context.Context, jobID pgtype.UUID, lastError string) error {
	row, err := s.q.FailBenchmarkEvalJob(ctx, db.FailBenchmarkEvalJobParams{
		ID:        jobID,
		LastError: lastError,
		Attempt:   int32(evalJobMaxAttempts),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrEvalJobNotFound
	}
	if err != nil {
		return fmt.Errorf("fail eval job: %w", err)
	}
	if row.State != "failed" {
		// Transient — returned to pending; will be re-claimed.
		return nil
	}

	// Permanent failure: advance task to 'errored' so the run finalizer
	// can count it and move the run forward.
	task, err := s.q.UpdateBenchmarkTaskStatus(ctx, db.UpdateBenchmarkTaskStatusParams{
		ID:           row.TaskID,
		WorkspaceID:  row.WorkspaceID,
		Status:       "errored",
		StatusReason: "eval_failed",
	})
	if err != nil {
		return fmt.Errorf("advance task to errored: %w", err)
	}

	if s.bus != nil {
		s.bus.Publish(events.Event{
			Type:        protocol.EventBenchmarkTaskStatus,
			WorkspaceID: util.UUIDToString(task.WorkspaceID),
			TaskID:      util.UUIDToString(task.ID),
			Payload: map[string]any{
				"run_id":        util.UUIDToString(task.RunID),
				"task_id":       util.UUIDToString(task.ID),
				"instance_id":   task.InstanceID,
				"status":        "errored",
				"status_reason": "eval_failed",
				"last_error":    lastError,
			},
		})
	}
	return nil
}
