package benchmark_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/service/benchmark"
	"github.com/multica-ai/multica/server/pkg/protocol"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestTimeoutWatchdog_Tick_MarksStaleTasksErrored(t *testing.T) {
	ctx := context.Background()
	// Reuse the TaskDispatcher fixture — it builds workspace + suite + profile
	// + queued run, runs the RunDispatcher to flip the run to 'submitting',
	// and we then run the TaskDispatcher to flip the task to 'issued'. From
	// there we backdate created_at past the run's submission_timeout_seconds
	// so the watchdog sees the task as stale.
	f := newTaskDispatcherFixture(t, "imported", []string{"foo__bar.deadbe1"})

	td := benchmark.NewTaskDispatcher(testQueries, testPool, f.Registry, f.Bus, nil)
	require.NoError(t, td.Tick(ctx))

	tasks, err := testQueries.ListBenchmarkTasksByRun(ctx, f.Run.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	task := tasks[0]
	require.Equal(t, "issued", task.Status)

	// Backdate the task far enough that it lives past the run's
	// submission_timeout_seconds (default 7200s). 8000s is comfortably
	// past that without depending on the exact default.
	_, err = testPool.Exec(ctx,
		"UPDATE benchmark_task SET created_at = now() - interval '8000 seconds' WHERE id = $1",
		task.ID,
	)
	require.NoError(t, err)

	pub := &recordingPublisher{}
	w := benchmark.NewTimeoutWatchdog(testQueries, pub)
	require.NoError(t, w.Tick(ctx))

	after, err := testQueries.GetBenchmarkTask(ctx, db.GetBenchmarkTaskParams{
		ID:          task.ID,
		WorkspaceID: f.WS.WorkspaceID,
	})
	require.NoError(t, err)
	require.Equal(t, "errored", after.Status)
	require.Equal(t, "submission_timeout", after.StatusReason)

	evs := pub.snapshot()
	require.Len(t, evs, 1, "expected exactly one bus event for the timed-out task")
	require.Equal(t, protocol.EventBenchmarkTaskStatus, evs[0].Type)
	payload, ok := evs[0].Payload.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "errored", payload["status"])
	require.Equal(t, "submission_timeout", payload["status_reason"])
}

func TestTimeoutWatchdog_Tick_LeavesFreshIssuedTasksAlone(t *testing.T) {
	ctx := context.Background()
	f := newTaskDispatcherFixture(t, "imported", []string{"alive__one.cafe001"})

	td := benchmark.NewTaskDispatcher(testQueries, testPool, f.Registry, f.Bus, nil)
	require.NoError(t, td.Tick(ctx))

	pub := &recordingPublisher{}
	w := benchmark.NewTimeoutWatchdog(testQueries, pub)
	require.NoError(t, w.Tick(ctx))

	tasks, err := testQueries.ListBenchmarkTasksByRun(ctx, f.Run.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "issued", tasks[0].Status, "fresh task must not be marked errored")
	require.Empty(t, pub.snapshot(), "no events expected when nothing timed out")
}
