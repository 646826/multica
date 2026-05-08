package benchmark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// failureCategoriesTopK caps the size of the run summary's failure_categories
// JSON array. The dashboard renders the top buckets, so persisting more than
// this is wasted bytes — and a stable cap keeps the column small regardless
// of how badly a run goes.
const failureCategoriesTopK = 5

// RunFinalizer reacts to terminal task lifecycle events and, once every
// task in a run is in a terminal state ('scored', 'errored', 'skipped'),
// computes the run summary and advances the run to 'complete' (≥1 scored)
// or 'failed' (all errored, no scored). It is idempotent: a second call on
// an already-finished run upserts the same summary and observes the
// terminal status, suppressing the EventBenchmarkRunCompleted re-publish.
type RunFinalizer struct {
	q    *db.Queries
	pool *pgxpool.Pool
	bus  *events.Bus
}

// NewRunFinalizer wires the finalizer to the DB and bus. The bus must be
// the concrete *events.Bus (not the Publisher interface used elsewhere)
// because the finalizer also subscribes — Subscribe is not part of the
// Publisher contract.
func NewRunFinalizer(q *db.Queries, pool *pgxpool.Pool, bus *events.Bus) *RunFinalizer {
	return &RunFinalizer{q: q, pool: pool, bus: bus}
}

// Start subscribes to task-lifecycle events and blocks until ctx is canceled.
// The handler is called synchronously by the bus, so per-event errors are
// logged and discarded — a single bad event must not poison subsequent
// deliveries.
func (f *RunFinalizer) Start(ctx context.Context) {
	f.bus.Subscribe(protocol.EventBenchmarkTaskStatus, f.handleTaskEvent(ctx))
	f.bus.Subscribe(protocol.EventBenchmarkTaskScored, f.handleTaskEvent(ctx))
	<-ctx.Done()
}

// handleTaskEvent returns a bus handler that extracts (run_id, workspace_id)
// from the event payload and triggers MaybeFinalize. We capture ctx in the
// closure so the handler stops doing real work after Start returns — any
// in-flight DB call is canceled.
func (f *RunFinalizer) handleTaskEvent(ctx context.Context) events.Handler {
	return func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		runIDStr, _ := payload["run_id"].(string)
		if runIDStr == "" {
			return
		}
		runID, err := util.ParseUUID(runIDStr)
		if err != nil {
			slog.Warn("benchmark.run_finalizer.bad_run_id", "run_id", runIDStr, "err", err)
			return
		}
		workspaceID, err := util.ParseUUID(e.WorkspaceID)
		if err != nil {
			slog.Warn("benchmark.run_finalizer.bad_workspace_id", "workspace_id", e.WorkspaceID, "err", err)
			return
		}
		if err := f.MaybeFinalize(ctx, runID, workspaceID); err != nil {
			slog.Warn("benchmark.run_finalizer.finalize_failed",
				"run_id", runIDStr,
				"err", err,
			)
		}
	}
}

// MaybeFinalize is the testable entry point: looks at one run, returns
// without side effects if any task is still active, otherwise computes
// the summary and advances the run. Safe to call multiple times — the
// second call is a no-op for the run-status update and skips the
// EventBenchmarkRunCompleted publish.
func (f *RunFinalizer) MaybeFinalize(ctx context.Context, runID, workspaceID pgtype.UUID) error {
	counts, err := f.q.CountActiveTasksForRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("count active tasks: %w", err)
	}
	if counts.Active > 0 {
		return nil
	}
	if counts.Total == 0 {
		// A run with zero tasks is a degenerate state (RunDispatcher should
		// always create at least one task or mark the run failed). Bail out
		// without writing a summary — attempting to score it makes no sense.
		return nil
	}

	run, err := f.q.GetBenchmarkRun(ctx, db.GetBenchmarkRunParams{
		ID:          runID,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}
	// If the run is already in a terminal state, the summary upsert is a
	// safe no-op but we must not re-emit EventBenchmarkRunCompleted. A
	// canceled run (user_canceled) also short-circuits — re-finalizing
	// would clobber the user's intent.
	alreadyTerminal := run.Status == "complete" || run.Status == "failed" || run.Status == "canceled"

	evalResults, err := f.q.ListBenchmarkEvalResultsForRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("list eval results: %w", err)
	}

	summary := computeSummary(counts, evalResults)
	catJSON, err := json.Marshal(summary.failureCategories)
	if err != nil {
		// json.Marshal of a []catCount never fails in practice; surface
		// any pathological encoder error rather than silently truncating.
		return fmt.Errorf("encode failure categories: %w", err)
	}

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := f.q.WithTx(tx)

	persisted, err := qtx.UpsertBenchmarkRunSummary(ctx, db.UpsertBenchmarkRunSummaryParams{
		RunID:             runID,
		WorkspaceID:       workspaceID,
		ResolvedCount:     int32(summary.resolved),
		TotalCount:        int32(counts.Total),
		AggregatePassRate: pgNumeric(summary.aggregatePassRate),
		AveragePassRate:   pgNumeric(summary.averagePassRate),
		ErroredCount:      int32(counts.Errored),
		FailureCategories: catJSON,
	})
	if err != nil {
		return fmt.Errorf("upsert run summary: %w", err)
	}

	// Pick the new run status: complete if any task scored, failed otherwise.
	// 'skipped' tasks alone (no scored, no errored) are also treated as
	// 'failed' — the run produced no usable signal, so 'failed' is the
	// right shelf for it.
	nextStatus := "complete"
	nextReason := ""
	if counts.Scored == 0 {
		nextStatus = "failed"
		nextReason = "all_errored"
	}

	if !alreadyTerminal {
		if _, err := qtx.UpdateBenchmarkRunStatus(ctx, db.UpdateBenchmarkRunStatusParams{
			ID:           runID,
			WorkspaceID:  workspaceID,
			Status:       nextStatus,
			StatusReason: nextReason,
		}); err != nil {
			return fmt.Errorf("update run status: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	if alreadyTerminal {
		return nil
	}

	f.bus.Publish(events.Event{
		Type:        protocol.EventBenchmarkRunCompleted,
		WorkspaceID: util.UUIDToString(workspaceID),
		Payload: map[string]any{
			"run_id": util.UUIDToString(runID),
			"status": nextStatus,
			"summary": map[string]any{
				"resolved_count":      persisted.ResolvedCount,
				"total_count":         persisted.TotalCount,
				"errored_count":       persisted.ErroredCount,
				"aggregate_pass_rate": summary.aggregatePassRate,
				"average_pass_rate":   summary.averagePassRate,
				"failure_categories":  summary.failureCategories,
			},
		},
	})
	slog.Info("benchmark.run_finalizer.completed",
		"run_id", util.UUIDToString(runID),
		"status", nextStatus,
		"resolved", persisted.ResolvedCount,
		"total", persisted.TotalCount,
		"errored", persisted.ErroredCount,
	)
	return nil
}

// catCount is the per-failure-category aggregate stored in the JSONB
// failure_categories column. The Name/Count field names match what the
// dashboard expects when it deserializes the column.
type catCount struct {
	Name  string `json:"Name"`
	Count int    `json:"Count"`
}

// runSummary is the in-memory shape the finalizer computes before
// persisting to benchmark_run_summary. Splitting computation from the
// upsert keeps the SQL call site free of business arithmetic.
type runSummary struct {
	resolved          int
	aggregatePassRate float64
	averagePassRate   float64
	failureCategories []catCount
}

// computeSummary turns the raw counts row + eval_result list into the
// numeric/JSON values the run_summary row needs.
func computeSummary(counts db.CountActiveTasksForRunRow, evalResults []db.BenchmarkEvalResult) runSummary {
	var (
		resolved          int
		sumPassed         int64
		sumTotal          int64
		sumPassRate       float64
		passRateDenom     int
		categoryFrequency = map[string]int{}
	)
	for _, er := range evalResults {
		if er.Resolved {
			resolved++
		}
		sumPassed += int64(er.PassedTests)
		sumTotal += int64(er.TotalTests)

		// pass_rate is numeric(6,5); pgtype.Numeric.Float64Value is the
		// canonical lossy conversion. An invalid Numeric (NULL/NaN) returns
		// !Valid — skip it instead of polluting the average with zeros.
		if v, err := er.PassRate.Float64Value(); err == nil && v.Valid {
			sumPassRate += v.Float64
			passRateDenom++
		}

		var cats []string
		// failed_categories is JSONB '[]' on insert; ignore decode errors —
		// a single malformed row should not block the whole summary.
		if len(er.FailedCategories) > 0 {
			_ = json.Unmarshal(er.FailedCategories, &cats)
		}
		for _, c := range cats {
			if c == "" {
				continue
			}
			categoryFrequency[c]++
		}
	}

	var aggregate float64
	if sumTotal > 0 {
		aggregate = float64(sumPassed) / float64(sumTotal)
	}
	var average float64
	if passRateDenom > 0 {
		average = sumPassRate / float64(passRateDenom)
	}

	items := make([]catCount, 0, len(categoryFrequency))
	for name, c := range categoryFrequency {
		items = append(items, catCount{Name: name, Count: c})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Name < items[j].Name
	})
	if len(items) > failureCategoriesTopK {
		items = items[:failureCategoriesTopK]
	}

	return runSummary{
		resolved:          resolved,
		aggregatePassRate: aggregate,
		averagePassRate:   average,
		failureCategories: items,
	}
}
