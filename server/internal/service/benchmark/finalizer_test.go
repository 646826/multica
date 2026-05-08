package benchmark_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service/benchmark"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// finalizerFixture builds a workspace + suite + profile + run + N pre-seeded
// benchmark_task rows in a single status. Tests then mutate per-task state
// (status / eval_result) and call MaybeFinalize.
type finalizerFixture struct {
	WS       fixtureWorkspace
	Run      benchmark.Run
	Bus      *events.Bus
	Recorded *recordingPublisher
	Tasks    []db.BenchmarkTask
}

func newFinalizerFixture(t *testing.T, instanceIDs []string) finalizerFixture {
	t.Helper()
	ctx := context.Background()
	ws := newFixtureWorkspace(t)

	// Use the SuiteService + ProfileService directly so we don't depend on
	// adapter resolution. The tasks are inserted manually below in whatever
	// status the test wants — we never need RunDispatcher / TaskDispatcher
	// to advance them for the finalizer's input.
	suiteSvc := benchmark.NewSuiteService(testQueries)
	suite, err := suiteSvc.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: ws.WorkspaceID,
		Slug:        "fin-suite",
		DisplayName: "Finalizer Suite",
		AdapterKind: "programbench",
		InstanceIDs: instanceIDs,
		CreatedBy:   ws.UserID,
	})
	require.NoError(t, err)

	agentID := newFixtureAgent(t, ws, agentSpec{
		Name:         "FinAgent",
		Model:        "claude-opus-4-7",
		PromptSource: "# system\nfin test\n",
	})
	profSvc := benchmark.NewProfileService(testQueries)
	profile, err := profSvc.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: ws.WorkspaceID,
		AgentID:     agentID,
		Slug:        "fin-profile",
		DisplayName: "Finalizer Profile",
		CapturedBy:  ws.UserID,
	})
	require.NoError(t, err)

	bus := events.New()
	rec := &recordingPublisher{}
	// Bus is the real one (RunFinalizer subscribes to it), but we also keep
	// a recording publisher attached as a global handler so tests can see
	// every event the finalizer emits.
	bus.SubscribeAll(rec.Publish)

	runSvc := benchmark.NewRunService(testQueries, testPool, bus)
	run, err := runSvc.StartRun(ctx, benchmark.StartRunInput{
		WorkspaceID:   ws.WorkspaceID,
		SuiteID:       suite.ID,
		ProfileID:     profile.ID,
		DisplayName:   "Finalizer Run",
		EvaluatorMode: "imported",
		CreatedBy:     ws.UserID,
	})
	require.NoError(t, err)

	// Move run to 'submitting' so the per-test status mutations (failed /
	// complete) are valid lifecycle transitions and so that the partial-
	// incomplete test has a non-terminal status to assert is preserved.
	_, err = testQueries.UpdateBenchmarkRunStatus(ctx, db.UpdateBenchmarkRunStatusParams{
		ID:          run.ID,
		WorkspaceID: ws.WorkspaceID,
		Status:      "submitting",
	})
	require.NoError(t, err)

	tasks := make([]db.BenchmarkTask, 0, len(instanceIDs))
	for _, id := range instanceIDs {
		tk, err := testQueries.CreateBenchmarkTask(ctx, db.CreateBenchmarkTaskParams{
			RunID:        run.ID,
			WorkspaceID:  ws.WorkspaceID,
			InstanceID:   id,
			InstanceMeta: []byte(`{}`),
			Status:       "issued",
		})
		require.NoError(t, err)
		tasks = append(tasks, tk)
	}

	return finalizerFixture{
		WS:       ws,
		Run:      benchmark.Run{ID: run.ID, WorkspaceID: ws.WorkspaceID, Status: "submitting"},
		Bus:      bus,
		Recorded: rec,
		Tasks:    tasks,
	}
}

func setTaskStatus(t *testing.T, ws pgtype.UUID, taskID pgtype.UUID, status string) {
	t.Helper()
	_, err := testQueries.UpdateBenchmarkTaskStatus(context.Background(), db.UpdateBenchmarkTaskStatusParams{
		ID:          taskID,
		WorkspaceID: ws,
		Status:      status,
	})
	require.NoError(t, err)
}

func upsertEval(t *testing.T, ws pgtype.UUID, taskID pgtype.UUID, resolved bool, passed, total int32, passRate float64, failedCats []string) {
	t.Helper()
	cats, err := json.Marshal(failedCats)
	require.NoError(t, err)
	var pr pgtype.Numeric
	require.NoError(t, pr.Scan(fmt.Sprintf("%.5f", passRate)))
	_, err = testQueries.UpsertBenchmarkEvalResult(context.Background(), db.UpsertBenchmarkEvalResultParams{
		TaskID:           taskID,
		WorkspaceID:      ws,
		Resolved:         resolved,
		PassedTests:      passed,
		TotalTests:       total,
		PassRate:         pr,
		RawEvalJson:      []byte(`{}`),
		FailedCategories: cats,
	})
	require.NoError(t, err)
}

func TestRunFinalizer_AllScored_AdvancesToComplete(t *testing.T) {
	ctx := context.Background()
	f := newFinalizerFixture(t, []string{"alpha__one.aaa", "beta__two.bbb"})

	// Both tasks scored, both have eval_result rows.
	setTaskStatus(t, f.WS.WorkspaceID, f.Tasks[0].ID, "scored")
	setTaskStatus(t, f.WS.WorkspaceID, f.Tasks[1].ID, "scored")
	upsertEval(t, f.WS.WorkspaceID, f.Tasks[0].ID, true, 8, 10, 0.8, []string{"flake"})
	upsertEval(t, f.WS.WorkspaceID, f.Tasks[1].ID, false, 2, 10, 0.2, []string{"flake", "compile"})

	fin := benchmark.NewRunFinalizer(testQueries, testPool, f.Bus)
	require.NoError(t, fin.MaybeFinalize(ctx, f.Run.ID, f.WS.WorkspaceID))

	got, err := testQueries.GetBenchmarkRun(ctx, db.GetBenchmarkRunParams{
		ID:          f.Run.ID,
		WorkspaceID: f.WS.WorkspaceID,
	})
	require.NoError(t, err)
	require.Equal(t, "complete", got.Status)

	sum, err := testQueries.GetBenchmarkRunSummary(ctx, f.Run.ID)
	require.NoError(t, err)
	require.Equal(t, int32(1), sum.ResolvedCount)
	require.Equal(t, int32(2), sum.TotalCount)
	require.Equal(t, int32(0), sum.ErroredCount)

	// aggregate = (8+2) / (10+10) = 0.5; average = (0.8+0.2)/2 = 0.5.
	aggStr, _ := sum.AggregatePassRate.Value()
	avgStr, _ := sum.AveragePassRate.Value()
	require.Equal(t, "0.50000", aggStr)
	require.Equal(t, "0.50000", avgStr)

	// failure_categories: flake=2, compile=1.
	var cats []map[string]any
	require.NoError(t, json.Unmarshal(sum.FailureCategories, &cats))
	require.Len(t, cats, 2)
	require.Equal(t, "flake", cats[0]["Name"])
	require.EqualValues(t, 2, cats[0]["Count"])
	require.Equal(t, "compile", cats[1]["Name"])
	require.EqualValues(t, 1, cats[1]["Count"])

	// Exactly one EventBenchmarkRunCompleted should have fired.
	var completed int
	for _, e := range f.Recorded.snapshot() {
		if e.Type == protocol.EventBenchmarkRunCompleted {
			completed++
		}
	}
	require.Equal(t, 1, completed)

	// Idempotency: a second call must not re-publish the completion event,
	// and the run status / summary stay valid.
	require.NoError(t, fin.MaybeFinalize(ctx, f.Run.ID, f.WS.WorkspaceID))
	completed = 0
	for _, e := range f.Recorded.snapshot() {
		if e.Type == protocol.EventBenchmarkRunCompleted {
			completed++
		}
	}
	require.Equal(t, 1, completed, "second MaybeFinalize must not re-publish completion")
}

func TestRunFinalizer_AllErrored_AdvancesToFailed(t *testing.T) {
	ctx := context.Background()
	f := newFinalizerFixture(t, []string{"x__one.111", "y__two.222"})

	setTaskStatus(t, f.WS.WorkspaceID, f.Tasks[0].ID, "errored")
	setTaskStatus(t, f.WS.WorkspaceID, f.Tasks[1].ID, "errored")
	// No eval_result rows for errored tasks.

	fin := benchmark.NewRunFinalizer(testQueries, testPool, f.Bus)
	require.NoError(t, fin.MaybeFinalize(ctx, f.Run.ID, f.WS.WorkspaceID))

	got, err := testQueries.GetBenchmarkRun(ctx, db.GetBenchmarkRunParams{
		ID:          f.Run.ID,
		WorkspaceID: f.WS.WorkspaceID,
	})
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status)

	sum, err := testQueries.GetBenchmarkRunSummary(ctx, f.Run.ID)
	require.NoError(t, err)
	require.Equal(t, int32(0), sum.ResolvedCount)
	require.Equal(t, int32(2), sum.TotalCount)
	require.Equal(t, int32(2), sum.ErroredCount)
}

func TestRunFinalizer_PartialIncomplete_StaysSubmitting(t *testing.T) {
	ctx := context.Background()
	f := newFinalizerFixture(t, []string{"p__one.aaa", "p__two.bbb"})

	// One scored, one still issued.
	setTaskStatus(t, f.WS.WorkspaceID, f.Tasks[0].ID, "scored")
	upsertEval(t, f.WS.WorkspaceID, f.Tasks[0].ID, true, 5, 5, 1.0, nil)
	// Tasks[1] stays 'issued' (the seeded status).

	fin := benchmark.NewRunFinalizer(testQueries, testPool, f.Bus)
	require.NoError(t, fin.MaybeFinalize(ctx, f.Run.ID, f.WS.WorkspaceID))

	got, err := testQueries.GetBenchmarkRun(ctx, db.GetBenchmarkRunParams{
		ID:          f.Run.ID,
		WorkspaceID: f.WS.WorkspaceID,
	})
	require.NoError(t, err)
	require.Equal(t, "submitting", got.Status, "run must not advance when work is pending")

	// No summary row should exist yet.
	_, err = testQueries.GetBenchmarkRunSummary(ctx, f.Run.ID)
	require.Error(t, err, "summary must not be written for an unfinished run")
}

func TestRunFinalizer_FailureCategoryAggregation(t *testing.T) {
	ctx := context.Background()
	// Six categories spread across three tasks so the top-5 cap kicks in.
	f := newFinalizerFixture(t, []string{"a__1.111", "b__2.222", "c__3.333"})
	for _, tk := range f.Tasks {
		setTaskStatus(t, f.WS.WorkspaceID, tk.ID, "scored")
	}
	// Counts: alpha=3, beta=2, gamma=2, delta=1, epsilon=1, zeta=1.
	upsertEval(t, f.WS.WorkspaceID, f.Tasks[0].ID, false, 0, 1, 0.0,
		[]string{"alpha", "beta", "gamma", "delta"})
	upsertEval(t, f.WS.WorkspaceID, f.Tasks[1].ID, false, 0, 1, 0.0,
		[]string{"alpha", "beta", "epsilon"})
	upsertEval(t, f.WS.WorkspaceID, f.Tasks[2].ID, false, 0, 1, 0.0,
		[]string{"alpha", "gamma", "zeta"})

	fin := benchmark.NewRunFinalizer(testQueries, testPool, f.Bus)
	require.NoError(t, fin.MaybeFinalize(ctx, f.Run.ID, f.WS.WorkspaceID))

	sum, err := testQueries.GetBenchmarkRunSummary(ctx, f.Run.ID)
	require.NoError(t, err)

	var cats []map[string]any
	require.NoError(t, json.Unmarshal(sum.FailureCategories, &cats))
	require.Len(t, cats, 5, "failure_categories must be capped at top-5")

	// First entry: alpha (3).
	require.Equal(t, "alpha", cats[0]["Name"])
	require.EqualValues(t, 3, cats[0]["Count"])

	// Then beta + gamma (both 2) sorted ASC by name → beta, gamma.
	require.Equal(t, "beta", cats[1]["Name"])
	require.EqualValues(t, 2, cats[1]["Count"])
	require.Equal(t, "gamma", cats[2]["Name"])
	require.EqualValues(t, 2, cats[2]["Count"])

	// Tail (count 1): delta, epsilon (zeta drops out at the top-5 cap).
	require.Equal(t, "delta", cats[3]["Name"])
	require.EqualValues(t, 1, cats[3]["Count"])
	require.Equal(t, "epsilon", cats[4]["Name"])
	require.EqualValues(t, 1, cats[4]["Count"])
}
