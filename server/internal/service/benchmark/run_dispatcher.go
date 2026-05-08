package benchmark

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service/benchmark/adapter"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// defaultRunDispatcherInterval is the polling cadence used when a caller
// does not override it. 5s matches the rest of the benchmark lifecycle
// pollers (TimeoutWatchdog / RunFinalizer in Phase 1a) — fast enough that
// queued runs do not feel stuck, slow enough that an idle workspace does
// not flood the database.
const defaultRunDispatcherInterval = 5 * time.Second

// RunDispatcher turns 'queued' benchmark_run rows into 'submitting' rows
// with a benchmark_task per instance id. It is a stateless polling worker:
// every tick it lists active runs, picks the queued ones, resolves each
// suite's adapter Catalog, and inserts tasks inside a single transaction
// guarded by a Postgres transaction-scoped advisory lock. The lock keys
// on the run id, so multiple dispatcher replicas (or a manual Tick called
// alongside the goroutine) can never double-submit the same run.
type RunDispatcher struct {
	q        *db.Queries
	pool     *pgxpool.Pool
	registry *adapter.Registry
	bus      Publisher
	interval time.Duration
}

// NewRunDispatcher constructs a RunDispatcher with the default 5s poll
// interval. Callers wire in the same *db.Queries / *pgxpool.Pool used by
// the rest of the benchmark services and a Publisher for lifecycle events.
func NewRunDispatcher(q *db.Queries, pool *pgxpool.Pool, reg *adapter.Registry, bus Publisher) *RunDispatcher {
	return &RunDispatcher{
		q:        q,
		pool:     pool,
		registry: reg,
		bus:      bus,
		interval: defaultRunDispatcherInterval,
	}
}

// Start runs Tick on a time.Ticker until ctx is canceled. Per-tick errors
// are logged and discarded so a transient database hiccup cannot kill the
// goroutine — the next tick will retry.
func (d *RunDispatcher) Start(ctx context.Context) {
	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.Tick(ctx); err != nil {
				slog.Warn("benchmark.run_dispatcher.tick_failed", "err", err)
			}
		}
	}
}

// Tick lists all active runs and dispatches the ones still in 'queued'.
// Per-run errors are logged but not returned — one bad run (e.g. a suite
// referencing an unregistered adapter) must not block dispatch of the
// other queued runs in the same tick.
func (d *RunDispatcher) Tick(ctx context.Context) error {
	runs, err := d.q.ListActiveBenchmarkRuns(ctx)
	if err != nil {
		return fmt.Errorf("list active runs: %w", err)
	}
	for _, r := range runs {
		if r.Status != "queued" {
			continue
		}
		if err := d.dispatchOne(ctx, r); err != nil {
			slog.Warn("benchmark.run_dispatcher.dispatch_failed",
				"run_id", util.UUIDToString(r.ID),
				"err", err,
			)
		}
	}
	return nil
}

// dispatchOne resolves the suite's adapter, opens a transaction, takes a
// transaction-scoped advisory lock keyed on the run id, inserts a task per
// instance id, and flips the run to 'submitting'. The advisory lock is
// released automatically at COMMIT or ROLLBACK; if another worker holds
// it we return nil so the caller treats this as "someone else has it" and
// moves on instead of erroring.
func (d *RunDispatcher) dispatchOne(ctx context.Context, run db.BenchmarkRun) error {
	suite, err := d.q.GetBenchmarkSuite(ctx, db.GetBenchmarkSuiteParams{
		ID:          run.SuiteID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("get suite: %w", err)
	}
	cat, ok := d.registry.Catalog(suite.AdapterKind)
	if !ok {
		return fmt.Errorf("adapter not registered: %s", suite.AdapterKind)
	}

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// hashtext folds the lock key into the int4 space pg_try_advisory_xact_lock
	// expects. Collisions across distinct run ids are theoretically possible
	// but harmless: the worst case is one run waits a tick longer because
	// another run with a colliding hash held the lock first.
	lockKey := "benchmark_run:" + util.UUIDToString(run.ID)
	var locked bool
	if err := tx.QueryRow(ctx,
		"SELECT pg_try_advisory_xact_lock(hashtext($1))",
		lockKey,
	).Scan(&locked); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}
	if !locked {
		// Another worker is dispatching this run right now — back off
		// silently and let the next tick (after the other worker commits
		// and the run is no longer 'queued') skip it via the status check.
		return nil
	}

	// Replay path: pre-baked per-instance meta on the suite wins over
	// Catalog.Resolve. The dispatcher decodes it once per run; an absent
	// or empty column leaves the map nil and we fall back to live catalog
	// resolution as before.
	var overridesByID map[string]json.RawMessage
	if len(suite.InstanceMetaOverrides) > 0 {
		if err := json.Unmarshal(suite.InstanceMetaOverrides, &overridesByID); err != nil {
			return fmt.Errorf("decode suite overrides: %w", err)
		}
	}

	qtx := d.q.WithTx(tx)
	for _, instanceID := range run.SuiteInstanceIds {
		meta := json.RawMessage(`{}`)
		status := "queued"
		reason := ""

		if override, ok := overridesByID[instanceID]; ok && len(override) > 0 {
			// Replay-style suite: the suite already carries the frozen meta
			// for this instance; skip Catalog.Resolve entirely.
			meta = override
		} else {
			inst, rerr := cat.Resolve(ctx, instanceID)
			if rerr != nil {
				// Unknown instance id is not a tick-level failure: we record
				// it as a 'skipped' task with a stable status_reason so the
				// run summary can still account for every requested instance.
				status = "skipped"
				reason = "unknown_instance"
			} else if len(inst.Meta) > 0 {
				meta = inst.Meta
			}
		}

		if _, terr := qtx.CreateBenchmarkTask(ctx, db.CreateBenchmarkTaskParams{
			RunID:        run.ID,
			WorkspaceID:  run.WorkspaceID,
			InstanceID:   instanceID,
			InstanceMeta: meta,
			Status:       status,
			StatusReason: reason,
		}); terr != nil {
			return fmt.Errorf("create task for %s: %w", instanceID, terr)
		}
	}

	if _, err := qtx.UpdateBenchmarkRunStatus(ctx, db.UpdateBenchmarkRunStatusParams{
		ID:           run.ID,
		WorkspaceID:  run.WorkspaceID,
		Status:       "submitting",
		StatusReason: "",
	}); err != nil {
		return fmt.Errorf("advance run to submitting: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	if d.bus != nil {
		d.bus.Publish(events.Event{
			Type:        protocol.EventBenchmarkRunStatus,
			WorkspaceID: util.UUIDToString(run.WorkspaceID),
			Payload: map[string]any{
				"run_id": util.UUIDToString(run.ID),
				"status": "submitting",
			},
		})
	}
	return nil
}
