package benchmark

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// defaultTimeoutWatchdogInterval is the polling cadence used when a caller
// does not override it. 30s is slow enough to keep load on the database
// minimal — the row count is small, and a tardy 'errored' transition by a
// few seconds carries no functional cost — but quick enough that the run
// finalizer sees timed-out tasks within a tick or two.
const defaultTimeoutWatchdogInterval = 30 * time.Second

// TimeoutWatchdog flips 'issued' benchmark_task rows to 'errored' once they
// pass their run's submission_timeout_seconds. It is a stateless polling
// worker: every tick it lists overdue tasks via a single SQL query and
// updates each one. The list query is the source of truth for "overdue" —
// the watchdog itself does no clock arithmetic, which avoids drift between
// dispatcher replicas with skewed clocks.
//
// Issue-closing on timeout is deferred (same as TaskDispatcher's deferred
// path on submission). The task row state is the canonical signal; the
// existing benchmark issue stays open until a follow-up ticket wires
// origin-aware closure.
type TimeoutWatchdog struct {
	q        *db.Queries
	bus      Publisher
	interval time.Duration
}

// NewTimeoutWatchdog constructs a watchdog with the default 30s poll
// interval. bus may be nil — when nil the watchdog still updates rows
// but does not publish lifecycle events; useful for fixtures that just
// want the row transitions.
func NewTimeoutWatchdog(q *db.Queries, bus Publisher) *TimeoutWatchdog {
	return &TimeoutWatchdog{
		q:        q,
		bus:      bus,
		interval: defaultTimeoutWatchdogInterval,
	}
}

// Start runs Tick on a time.Ticker until ctx is canceled. Per-tick errors
// are logged and discarded so a transient database hiccup cannot kill the
// goroutine — the next tick will retry.
func (w *TimeoutWatchdog) Start(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.Tick(ctx); err != nil {
				slog.Warn("benchmark.timeout_watchdog.tick_failed", "err", err)
			}
		}
	}
}

// Tick lists every 'issued' task whose age exceeds its run's
// submission_timeout_seconds and flips each to 'errored' with reason
// 'submission_timeout'. Per-row update failures are logged and skipped —
// one bad row must not block the rest of the timed-out batch.
func (w *TimeoutWatchdog) Tick(ctx context.Context) error {
	rows, err := w.q.ListIssuedTasksPastTimeout(ctx)
	if err != nil {
		return fmt.Errorf("list issued tasks past timeout: %w", err)
	}
	for _, t := range rows {
		if _, uerr := w.q.UpdateBenchmarkTaskStatus(ctx, db.UpdateBenchmarkTaskStatusParams{
			ID:           t.ID,
			WorkspaceID:  t.WorkspaceID,
			Status:       "errored",
			StatusReason: "submission_timeout",
		}); uerr != nil {
			slog.Warn("benchmark.timeout_watchdog.update_failed",
				"task_id", util.UUIDToString(t.ID),
				"err", uerr,
			)
			continue
		}
		if w.bus != nil {
			w.bus.Publish(events.Event{
				Type:        protocol.EventBenchmarkTaskStatus,
				WorkspaceID: util.UUIDToString(t.WorkspaceID),
				TaskID:      util.UUIDToString(t.ID),
				Payload: map[string]any{
					"run_id":        util.UUIDToString(t.RunID),
					"task_id":       util.UUIDToString(t.ID),
					"instance_id":   t.InstanceID,
					"status":        "errored",
					"status_reason": "submission_timeout",
				},
			})
		}
		slog.Info("benchmark.timeout_watchdog.errored",
			"run_id", util.UUIDToString(t.RunID),
			"task_id", util.UUIDToString(t.ID),
			"instance_id", t.InstanceID,
		)
	}
	return nil
}
