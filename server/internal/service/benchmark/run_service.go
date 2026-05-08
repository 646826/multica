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

// Service-layer error sentinels for RunService.
var (
	ErrRunNotFound             = errors.New("benchmark: run not found")
	ErrSuiteResolution         = errors.New("benchmark: suite or profile not found in workspace")
	ErrInvalidEvaluator        = errors.New("benchmark: evaluator_mode must be 'managed' or 'imported'")
	ErrTaskNotFoundForInstance = errors.New("benchmark: task not found for run+instance")
	ErrRunNotComplete          = errors.New("benchmark: run is not complete; cannot compare")
)

// Default per-task submission timeout used when StartRunInput leaves it
// unset. Mirrors the database column default (7200s = 2h) so the row
// shape is identical regardless of which side picks the value.
const defaultSubmissionTimeoutSeconds int32 = 7200

// Publisher is the minimal subset of *events.Bus that RunService needs
// to publish lifecycle events. Defined in this package so tests can
// inject a recording double instead of a live bus.
type Publisher interface {
	Publish(events.Event)
}

// Run is the service-layer representation of benchmark_run. Mirrors the
// shape of db.BenchmarkRun but stays in the benchmark package so handlers
// and downstream services depend on this layer rather than sqlc types.
type Run struct {
	ID                       pgtype.UUID
	WorkspaceID              pgtype.UUID
	SuiteID                  pgtype.UUID
	SuiteInstanceIDs         []string
	ProfileID                pgtype.UUID
	BaseRunID                pgtype.UUID
	DisplayName              string
	Status                   string
	StatusReason             string
	Notes                    string
	EvaluatorMode            string
	AdapterVersion           string
	SubmissionTimeoutSeconds int32
	CreatedBy                pgtype.UUID
}

// StartRunInput is the validated input to RunService.StartRun.
type StartRunInput struct {
	WorkspaceID    pgtype.UUID
	SuiteID        pgtype.UUID
	ProfileID      pgtype.UUID
	BaseRunID      pgtype.UUID
	DisplayName    string
	Notes          string
	EvaluatorMode  string // 'managed' | 'imported'
	AdapterVersion string
	CreatedBy      pgtype.UUID
}

// ImportEvalResultInput is the validated input to RunService.ImportEvalResult.
// Used by the imported-evaluator flow (external CI / Modal evaluator that
// posts results back into Multica) to attach scoring data and advance a task.
type ImportEvalResultInput struct {
	WorkspaceID      pgtype.UUID
	RunID            pgtype.UUID
	InstanceID       string
	Resolved         bool
	PassedTests      int
	TotalTests       int
	PassRate         float64
	RawEvalJSON      json.RawMessage
	FailedCategories []string
}

// RunService is the lifecycle service for benchmark_run. It owns the
// initial creation (StartRun), workspace-scoped reads (GetRun / ListRuns),
// user cancellation (CancelRun), and the imported-evaluator import path
// (ImportEvalResult). Dispatching tasks to agents and computing run summaries
// are handled by separate services (T09–T12).
type RunService struct {
	q    *db.Queries
	pool *pgxpool.Pool
	bus  Publisher
}

// NewRunService constructs a RunService bound to the given query set,
// connection pool (for transactional ImportEvalResult), and event bus.
func NewRunService(q *db.Queries, pool *pgxpool.Pool, bus Publisher) *RunService {
	return &RunService{q: q, pool: pool, bus: bus}
}

// StartRun verifies the suite + profile exist in the workspace, inserts a
// benchmark_run with status='queued', and publishes EventBenchmarkRunCreated.
// The suite_instance_ids column is populated from the suite at start time
// so a later edit/delete to the suite does not change which instances the
// run was meant to cover (snapshot semantics).
func (s *RunService) StartRun(ctx context.Context, in StartRunInput) (Run, error) {
	if in.EvaluatorMode != "managed" && in.EvaluatorMode != "imported" {
		return Run{}, ErrInvalidEvaluator
	}

	// Workspace-scoped lookups so we never accept a suite/profile from a
	// different workspace even if the caller has the id.
	suite, err := s.q.GetBenchmarkSuite(ctx, db.GetBenchmarkSuiteParams{
		ID:          in.SuiteID,
		WorkspaceID: in.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrSuiteResolution
	}
	if err != nil {
		return Run{}, fmt.Errorf("get suite: %w", err)
	}

	if _, err := s.q.GetBenchmarkProfile(ctx, db.GetBenchmarkProfileParams{
		ID:          in.ProfileID,
		WorkspaceID: in.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, ErrSuiteResolution
		}
		return Run{}, fmt.Errorf("get profile: %w", err)
	}

	row, err := s.q.CreateBenchmarkRun(ctx, db.CreateBenchmarkRunParams{
		WorkspaceID:              in.WorkspaceID,
		SuiteID:                  in.SuiteID,
		SuiteInstanceIds:         suite.InstanceIds,
		ProfileID:                in.ProfileID,
		BaseRunID:                in.BaseRunID,
		DisplayName:              in.DisplayName,
		Status:                   "queued",
		EvaluatorMode:            in.EvaluatorMode,
		AdapterVersion:           in.AdapterVersion,
		SubmissionTimeoutSeconds: defaultSubmissionTimeoutSeconds,
		CreatedBy:                in.CreatedBy,
	})
	if err != nil {
		return Run{}, fmt.Errorf("create benchmark_run: %w", err)
	}

	s.bus.Publish(events.Event{
		Type:        protocol.EventBenchmarkRunCreated,
		WorkspaceID: util.UUIDToString(row.WorkspaceID),
		Payload: map[string]any{
			"run_id":         util.UUIDToString(row.ID),
			"suite_id":       util.UUIDToString(row.SuiteID),
			"profile_id":     util.UUIDToString(row.ProfileID),
			"status":         row.Status,
			"evaluator_mode": row.EvaluatorMode,
		},
	})

	return rowToRun(row), nil
}

// GetRun fetches a single run by id, scoped to the workspace.
// Returns ErrRunNotFound when the row does not exist.
func (s *RunService) GetRun(ctx context.Context, id, workspaceID pgtype.UUID) (Run, error) {
	row, err := s.q.GetBenchmarkRun(ctx, db.GetBenchmarkRunParams{
		ID:          id,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrRunNotFound
	}
	if err != nil {
		return Run{}, err
	}
	return rowToRun(row), nil
}

// ListRuns returns the most recent runs in a workspace, newest first,
// up to the given limit.
func (s *RunService) ListRuns(ctx context.Context, workspaceID pgtype.UUID, limit int32) ([]Run, error) {
	rows, err := s.q.ListBenchmarkRuns(ctx, db.ListBenchmarkRunsParams{
		WorkspaceID: workspaceID,
		Limit:       limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToRun(r))
	}
	return out, nil
}

// CancelRun marks the run as 'canceled' with reason 'user_canceled'.
// Downstream services (TaskDispatcher / RunFinalizer) detect the new
// status and skip pending dispatches; this method itself only flips the
// row. Returns ErrRunNotFound if no row matches the (id, workspace) pair.
func (s *RunService) CancelRun(ctx context.Context, id, workspaceID pgtype.UUID) error {
	_, err := s.q.UpdateBenchmarkRunStatus(ctx, db.UpdateBenchmarkRunStatusParams{
		ID:           id,
		WorkspaceID:  workspaceID,
		Status:       "canceled",
		StatusReason: "user_canceled",
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRunNotFound
	}
	if err != nil {
		return fmt.Errorf("update benchmark_run status: %w", err)
	}
	return nil
}

// ImportEvalResult is the imported-evaluator path: an external evaluator
// has produced scoring data for a (run, instance_id) and we need to
// persist it and advance the task to 'scored'. The upsert + status update
// run inside a single transaction so a partial failure can never leave a
// task scored with no eval_result, or vice versa. Publishes
// EventBenchmarkTaskScored on success.
func (s *RunService) ImportEvalResult(ctx context.Context, in ImportEvalResultInput) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.q.WithTx(tx)

	task, err := qtx.GetBenchmarkTaskByInstance(ctx, db.GetBenchmarkTaskByInstanceParams{
		RunID:      in.RunID,
		InstanceID: in.InstanceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTaskNotFoundForInstance
	}
	if err != nil {
		return fmt.Errorf("lookup task by instance: %w", err)
	}
	// Defensive: a task is only addressable via its workspace, so reject
	// cross-workspace imports even if the run+instance pair happened to match.
	if task.WorkspaceID.Bytes != in.WorkspaceID.Bytes {
		return ErrTaskNotFoundForInstance
	}

	failedCats, err := json.Marshal(normalizeFailedCategories(in.FailedCategories))
	if err != nil {
		// json.Marshal of a []string never fails in practice, but the
		// generated query expects []byte so propagate any error rather
		// than swallow it.
		return fmt.Errorf("encode failed_categories: %w", err)
	}

	if _, err := qtx.UpsertBenchmarkEvalResult(ctx, db.UpsertBenchmarkEvalResultParams{
		TaskID:           task.ID,
		WorkspaceID:      in.WorkspaceID,
		Resolved:         in.Resolved,
		PassedTests:      int32(in.PassedTests),
		TotalTests:       int32(in.TotalTests),
		PassRate:         pgNumeric(in.PassRate),
		RawEvalJson:      []byte(in.RawEvalJSON),
		FailedCategories: failedCats,
	}); err != nil {
		return fmt.Errorf("upsert eval_result: %w", err)
	}

	if _, err := qtx.UpdateBenchmarkTaskStatus(ctx, db.UpdateBenchmarkTaskStatusParams{
		ID:           task.ID,
		WorkspaceID:  in.WorkspaceID,
		Status:       "scored",
		StatusReason: "imported",
	}); err != nil {
		return fmt.Errorf("advance task to scored: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	s.bus.Publish(events.Event{
		Type:        protocol.EventBenchmarkTaskScored,
		WorkspaceID: util.UUIDToString(in.WorkspaceID),
		TaskID:      util.UUIDToString(task.ID),
		Payload: map[string]any{
			"run_id":      util.UUIDToString(in.RunID),
			"task_id":     util.UUIDToString(task.ID),
			"instance_id": in.InstanceID,
			"resolved":    in.Resolved,
			"pass_rate":   in.PassRate,
		},
	})
	return nil
}

// normalizeFailedCategories ensures we always serialize a JSON array
// (never `null`), matching the column's NOT NULL DEFAULT '[]'::jsonb shape.
func normalizeFailedCategories(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// pgNumeric converts a float64 to a pgtype.Numeric suitable for the
// numeric(6,5) pass_rate column. Formatting via %.5f ensures we never
// emit more precision than the column accepts.
func pgNumeric(f float64) pgtype.Numeric {
	var n pgtype.Numeric
	_ = n.Scan(fmt.Sprintf("%.5f", f))
	return n
}

// CompareRuns compares two complete runs in the same workspace and returns
// a service-layer ComparisonResult. Both runs must exist (workspace-scoped)
// and both must be in status 'complete' — otherwise ErrRunNotFound or
// ErrRunNotComplete is returned. The DB-shaped per-instance eval rows are
// turned into the EvalResultView map the pure Compare function expects.
func (s *RunService) CompareRuns(ctx context.Context, baseID, candID, workspaceID pgtype.UUID) (ComparisonResult, error) {
	base, err := s.q.GetBenchmarkRun(ctx, db.GetBenchmarkRunParams{ID: baseID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ComparisonResult{}, ErrRunNotFound
	}
	if err != nil {
		return ComparisonResult{}, fmt.Errorf("get base run: %w", err)
	}
	cand, err := s.q.GetBenchmarkRun(ctx, db.GetBenchmarkRunParams{ID: candID, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ComparisonResult{}, ErrRunNotFound
	}
	if err != nil {
		return ComparisonResult{}, fmt.Errorf("get cand run: %w", err)
	}

	if base.Status != "complete" || cand.Status != "complete" {
		return ComparisonResult{}, ErrRunNotComplete
	}

	baseSummary, err := s.q.GetBenchmarkRunSummary(ctx, baseID)
	if err != nil {
		return ComparisonResult{}, fmt.Errorf("get base summary: %w", err)
	}
	candSummary, err := s.q.GetBenchmarkRunSummary(ctx, candID)
	if err != nil {
		return ComparisonResult{}, fmt.Errorf("get cand summary: %w", err)
	}

	baseEvals, err := s.q.ListBenchmarkEvalResultsForRun(ctx, baseID)
	if err != nil {
		return ComparisonResult{}, fmt.Errorf("list base eval_results: %w", err)
	}
	candEvals, err := s.q.ListBenchmarkEvalResultsForRun(ctx, candID)
	if err != nil {
		return ComparisonResult{}, fmt.Errorf("list cand eval_results: %w", err)
	}

	baseEvalMap, err := evalRowsToMap(ctx, s.q, baseEvals)
	if err != nil {
		return ComparisonResult{}, fmt.Errorf("map base eval_results: %w", err)
	}
	candEvalMap, err := evalRowsToMap(ctx, s.q, candEvals)
	if err != nil {
		return ComparisonResult{}, fmt.Errorf("map cand eval_results: %w", err)
	}

	return Compare(
		runSummaryToView(baseSummary, util.UUIDToString(baseID)),
		runSummaryToView(candSummary, util.UUIDToString(candID)),
		baseEvalMap,
		candEvalMap,
	), nil
}

// LeaderboardForSuite returns a dense-ranked leaderboard across complete runs
// of the given suite within the workspace. The ranking honors best-run-per-
// profile; profiles with no completed runs do not appear. Returns
// ErrSuiteResolution when the suite slug is unknown in the workspace.
func (s *RunService) LeaderboardForSuite(ctx context.Context, workspaceID pgtype.UUID, suiteSlug string) ([]LeaderboardRow, error) {
	if _, err := s.q.GetBenchmarkSuiteByWorkspaceAndSlug(ctx, db.GetBenchmarkSuiteByWorkspaceAndSlugParams{
		WorkspaceID: workspaceID,
		Slug:        suiteSlug,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSuiteResolution
		}
		return nil, fmt.Errorf("get suite by slug: %w", err)
	}

	runs, err := s.q.ListCompleteRunsBySuiteSlug(ctx, db.ListCompleteRunsBySuiteSlugParams{
		WorkspaceID: workspaceID,
		Slug:        suiteSlug,
		Limit:       leaderboardRunLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("list complete runs: %w", err)
	}
	if len(runs) == 0 {
		return []LeaderboardRow{}, nil
	}

	runIDs := make([]pgtype.UUID, len(runs))
	for i, r := range runs {
		runIDs[i] = r.ID
	}

	summaries, err := s.q.ListBenchmarkRunSummariesByRunIDs(ctx, runIDs)
	if err != nil {
		return nil, fmt.Errorf("list summaries: %w", err)
	}
	summByID := make(map[string]db.BenchmarkRunSummary, len(summaries))
	for _, sm := range summaries {
		summByID[util.UUIDToString(sm.RunID)] = sm
	}

	// Cache profile lookups so 50 runs from the same profile do not become
	// 50 round-trips. The (workspace, profile_id) tuple is stable per row.
	profCache := map[[16]byte]db.BenchmarkAgentProfile{}

	rows := make([]LeaderboardRunRow, 0, len(runs))
	for _, r := range runs {
		sm, ok := summByID[util.UUIDToString(r.ID)]
		if !ok {
			// Run reached 'complete' but its summary row is missing — surface
			// nothing rather than ranking on zeros.
			continue
		}
		prof, ok := profCache[r.ProfileID.Bytes]
		if !ok {
			fetched, err := s.q.GetBenchmarkProfile(ctx, db.GetBenchmarkProfileParams{
				ID:          r.ProfileID,
				WorkspaceID: workspaceID,
			})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return nil, fmt.Errorf("get profile: %w", err)
			}
			prof = fetched
			profCache[r.ProfileID.Bytes] = fetched
		}
		rows = append(rows, LeaderboardRunRow{
			RunID:              util.UUIDToString(r.ID),
			RunDisplayName:     r.DisplayName,
			ProfileID:          util.UUIDToString(r.ProfileID),
			ProfileSlug:        prof.Slug,
			ProfileDisplayName: prof.DisplayName,
			ResolvedCount:      int(sm.ResolvedCount),
			TotalCount:         int(sm.TotalCount),
			AveragePassRate:    pgNumericToFloat(sm.AveragePassRate),
			AggregatePassRate:  pgNumericToFloat(sm.AggregatePassRate),
			ErroredCount:       int(sm.ErroredCount),
			CompletedAt:        util.TimestampToString(r.CompletedAt),
		})
	}
	return Leaderboard(rows), nil
}

// leaderboardRunLimit caps how many recent complete runs the leaderboard
// considers. 200 is enough headroom for "best per profile" across realistic
// per-suite catalogs while keeping the query bounded.
const leaderboardRunLimit = 200

// runSummaryToView adapts the sqlc-shaped summary row into the pure-function
// view consumed by Compare. The DB stores failure_categories as a JSONB
// array of {Name, Count} objects (see finalizer.catCount); the view only
// cares about the names, so we project to []string.
func runSummaryToView(s db.BenchmarkRunSummary, runID string) RunSummaryView {
	return RunSummaryView{
		RunID:             runID,
		ResolvedCount:     int(s.ResolvedCount),
		TotalCount:        int(s.TotalCount),
		ErroredCount:      int(s.ErroredCount),
		AggregatePassRate: pgNumericToFloat(s.AggregatePassRate),
		AveragePassRate:   pgNumericToFloat(s.AveragePassRate),
		FailureCategories: failureCategoryNames(s.FailureCategories),
	}
}

// failureCategoryNames decodes the JSONB failure_categories column written
// by the finalizer (a list of {Name, Count}) and returns the names in their
// stored order. Malformed/empty input yields an empty slice — the comparison
// view should never see nil.
func failureCategoryNames(raw []byte) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var entries []struct {
		Name  string `json:"Name"`
		Count int    `json:"Count"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Name == "" {
			continue
		}
		out = append(out, e.Name)
	}
	return out
}

// evalRowsToMap turns the sqlc-shaped eval_result rows into the per-instance
// map Compare expects. The eval_result row only carries task_id, so we
// resolve instance_id via benchmark_task. Any task lookup failure for an
// individual row is surfaced rather than silently dropping the result.
func evalRowsToMap(ctx context.Context, q *db.Queries, rows []db.BenchmarkEvalResult) (map[string]EvalResultView, error) {
	out := make(map[string]EvalResultView, len(rows))
	for _, r := range rows {
		task, err := q.GetBenchmarkTask(ctx, db.GetBenchmarkTaskParams{
			ID:          r.TaskID,
			WorkspaceID: r.WorkspaceID,
		})
		if err != nil {
			return nil, fmt.Errorf("get task %s: %w", util.UUIDToString(r.TaskID), err)
		}
		out[task.InstanceID] = EvalResultView{
			InstanceID: task.InstanceID,
			Resolved:   r.Resolved,
			PassRate:   pgNumericToFloat(r.PassRate),
		}
	}
	return out, nil
}

// pgNumericToFloat is the read-side counterpart of pgNumeric. Float64Value is
// the canonical pgx conversion for numeric(p,s); an invalid Numeric (NULL /
// NaN) safely yields 0 because Float64Value returns Valid=false in that case.
func pgNumericToFloat(n pgtype.Numeric) float64 {
	v, err := n.Float64Value()
	if err != nil || !v.Valid {
		return 0
	}
	return v.Float64
}

func rowToRun(r db.BenchmarkRun) Run {
	return Run{
		ID:                       r.ID,
		WorkspaceID:              r.WorkspaceID,
		SuiteID:                  r.SuiteID,
		SuiteInstanceIDs:         r.SuiteInstanceIds,
		ProfileID:                r.ProfileID,
		BaseRunID:                r.BaseRunID,
		DisplayName:              r.DisplayName,
		Status:                   r.Status,
		StatusReason:             r.StatusReason,
		Notes:                    r.Notes,
		EvaluatorMode:            r.EvaluatorMode,
		AdapterVersion:           r.AdapterVersion,
		SubmissionTimeoutSeconds: r.SubmissionTimeoutSeconds,
		CreatedBy:                r.CreatedBy,
	}
}
