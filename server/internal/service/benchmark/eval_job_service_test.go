package benchmark_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/service/benchmark"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// evalJobFixture wraps the bits every EvalJobService test needs: a
// workspace + run + service + a recording publisher. Individual tests
// then create the specific tasks/jobs they want via the helpers below.
type evalJobFixture struct {
	WS      fixtureWorkspace
	Run     benchmark.Run
	Pub     *recordingPublisher
	Service *benchmark.EvalJobService
}

func newEvalJobFixture(t *testing.T) evalJobFixture {
	t.Helper()
	rf := newRunFixture(t)

	run, err := rf.Service.StartRun(context.Background(), benchmark.StartRunInput{
		WorkspaceID:    rf.WS.WorkspaceID,
		SuiteID:        rf.Suite.ID,
		ProfileID:      rf.Profile.ID,
		DisplayName:    "EvalJob Run",
		EvaluatorMode:  "managed",
		AdapterVersion: "programbench@v1",
		CreatedBy:      rf.WS.UserID,
	})
	require.NoError(t, err)

	pub := &recordingPublisher{}
	svc := benchmark.NewEvalJobService(testQueries, testPool, pub)

	return evalJobFixture{
		WS:      rf.WS,
		Run:     run,
		Pub:     pub,
		Service: svc,
	}
}

// makeTaskAndJob inserts a benchmark_task in 'submitted' status (the
// state from which the evaluator pool is meant to take over) and an
// eval_job in 'pending' for the given adapter kind. Returns the task
// and job rows so the caller can assert on their ids.
func makeTaskAndJob(t *testing.T, f evalJobFixture, instanceID, adapterKind string) (db.BenchmarkTask, db.BenchmarkEvalJob) {
	t.Helper()
	ctx := context.Background()
	task, err := testQueries.CreateBenchmarkTask(ctx, db.CreateBenchmarkTaskParams{
		RunID:        f.Run.ID,
		WorkspaceID:  f.WS.WorkspaceID,
		InstanceID:   instanceID,
		InstanceMeta: []byte(`{"k":"v"}`),
		Status:       "submitted",
	})
	require.NoError(t, err)

	job, err := testQueries.CreateBenchmarkEvalJob(ctx, db.CreateBenchmarkEvalJobParams{
		TaskID:      task.ID,
		WorkspaceID: f.WS.WorkspaceID,
		AdapterKind: adapterKind,
	})
	require.NoError(t, err)
	return task, job
}

func TestEvalJobService_Claim_PicksPendingJobs(t *testing.T) {
	ctx := context.Background()
	f := newEvalJobFixture(t)

	_, j1 := makeTaskAndJob(t, f, "alpha__one.aaa", "programbench")
	_, j2 := makeTaskAndJob(t, f, "beta__two.bbb", "programbench")

	got, err := f.Service.Claim(ctx, "evaluator-A", []string{"programbench"}, 5)
	require.NoError(t, err)
	require.Len(t, got, 2)

	// Each returned job carries instance + adapter context.
	gotIDs := map[[16]byte]bool{
		got[0].JobID.Bytes: true,
		got[1].JobID.Bytes: true,
	}
	require.True(t, gotIDs[j1.ID.Bytes])
	require.True(t, gotIDs[j2.ID.Bytes])
	for _, c := range got {
		require.Equal(t, "programbench", c.AdapterKind)
		require.True(t, c.TaskID.Valid)
		require.NotEmpty(t, c.InstanceID)
	}

	// Both rows are now state='claimed' with claimed_by set.
	for _, jid := range []pgtype.UUID{j1.ID, j2.ID} {
		row, err := testQueries.GetBenchmarkEvalJob(ctx, jid)
		require.NoError(t, err)
		require.Equal(t, "claimed", row.State)
		require.Equal(t, "evaluator-A", row.ClaimedBy.String)
	}
}

func TestEvalJobService_Claim_RespectsLimit(t *testing.T) {
	ctx := context.Background()
	f := newEvalJobFixture(t)

	_, j1 := makeTaskAndJob(t, f, "alpha__one.aaa", "programbench")
	_, j2 := makeTaskAndJob(t, f, "beta__two.bbb", "programbench")
	_, j3 := makeTaskAndJob(t, f, "gamma__three.ccc", "programbench")

	got, err := f.Service.Claim(ctx, "evaluator-A", []string{"programbench"}, 1)
	require.NoError(t, err)
	require.Len(t, got, 1)

	// Exactly one of the three is claimed; the other two remain pending.
	claimed := 0
	pending := 0
	for _, jid := range []pgtype.UUID{j1.ID, j2.ID, j3.ID} {
		row, err := testQueries.GetBenchmarkEvalJob(ctx, jid)
		require.NoError(t, err)
		switch row.State {
		case "claimed":
			claimed++
		case "pending":
			pending++
		default:
			t.Fatalf("unexpected state for job %s: %s", jid.Bytes, row.State)
		}
	}
	require.Equal(t, 1, claimed)
	require.Equal(t, 2, pending)
}

func TestEvalJobService_Claim_FiltersByAdapterKind(t *testing.T) {
	ctx := context.Background()
	f := newEvalJobFixture(t)

	_, _ = makeTaskAndJob(t, f, "alpha__one.aaa", "programbench")
	_, otherJob := makeTaskAndJob(t, f, "beta__two.bbb", "swebench")

	got, err := f.Service.Claim(ctx, "evaluator-A", []string{"programbench"}, 5)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "programbench", got[0].AdapterKind)

	// The swebench job is untouched.
	row, err := testQueries.GetBenchmarkEvalJob(ctx, otherJob.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", row.State)
}

func TestEvalJobService_Claim_NoWorkInputs(t *testing.T) {
	ctx := context.Background()
	f := newEvalJobFixture(t)

	got, err := f.Service.Claim(ctx, "evaluator-A", nil, 5)
	require.NoError(t, err)
	require.Nil(t, got)

	got, err = f.Service.Claim(ctx, "evaluator-A", []string{"programbench"}, 0)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestEvalJobService_Complete_AdvancesTaskAndCreatesResult(t *testing.T) {
	ctx := context.Background()
	f := newEvalJobFixture(t)

	task, _ := makeTaskAndJob(t, f, "alpha__one.aaa", "programbench")

	claimed, err := f.Service.Claim(ctx, "evaluator-A", []string{"programbench"}, 5)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	// Drop the claim event noise — Claim itself doesn't publish, but
	// other tests in the package may share the publisher in the future.
	require.Empty(t, f.Pub.snapshot())

	rawEval := json.RawMessage(`{"resolved":true,"passed":4,"total":5}`)
	err = f.Service.Complete(ctx, benchmark.CompleteEvalJobInput{
		JobID:            claimed[0].JobID,
		Resolved:         true,
		PassedTests:      4,
		TotalTests:       5,
		PassRate:         0.8,
		RawEvalJSON:      rawEval,
		FailedCategories: []string{"flake"},
	})
	require.NoError(t, err)

	updatedTask, err := testQueries.GetBenchmarkTask(ctx, db.GetBenchmarkTaskParams{
		ID:          task.ID,
		WorkspaceID: f.WS.WorkspaceID,
	})
	require.NoError(t, err)
	require.Equal(t, "scored", updatedTask.Status)
	require.Equal(t, "evaluator_complete", updatedTask.StatusReason)

	er, err := testQueries.GetBenchmarkEvalResult(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, er.Resolved)
	require.Equal(t, int32(4), er.PassedTests)
	require.Equal(t, int32(5), er.TotalTests)

	job, err := testQueries.GetBenchmarkEvalJob(ctx, claimed[0].JobID)
	require.NoError(t, err)
	require.Equal(t, "done", job.State)
	require.True(t, job.FinishedAt.Valid)

	evs := f.Pub.snapshot()
	require.Len(t, evs, 1)
	require.Equal(t, protocol.EventBenchmarkTaskScored, evs[0].Type)
}

func TestEvalJobService_Complete_NotFound(t *testing.T) {
	ctx := context.Background()
	f := newEvalJobFixture(t)

	bogus := newRandomUUID(t)
	err := f.Service.Complete(ctx, benchmark.CompleteEvalJobInput{
		JobID:            bogus,
		Resolved:         true,
		PassedTests:      1,
		TotalTests:       1,
		PassRate:         1.0,
		RawEvalJSON:      json.RawMessage(`{}`),
		FailedCategories: []string{},
	})
	require.ErrorIs(t, err, benchmark.ErrEvalJobNotFound)
	require.Empty(t, f.Pub.snapshot())
}

func TestEvalJobService_Fail_RetriesUntilMaxAttempts(t *testing.T) {
	ctx := context.Background()
	f := newEvalJobFixture(t)

	task, job := makeTaskAndJob(t, f, "alpha__one.aaa", "programbench")

	// Three Fail() calls — the FailBenchmarkEvalJob query bumps attempt
	// each time and flips state to 'failed' on the third (attempt+1 >= 3).
	for i := 0; i < 3; i++ {
		// Re-claim each round so the job is in 'claimed' state when we fail it.
		// (Not strictly required by the SQL, but mirrors the real evaluator
		// loop where Fail follows a successful Claim.)
		_, err := f.Service.Claim(ctx, "evaluator-A", []string{"programbench"}, 5)
		require.NoError(t, err)

		require.NoError(t, f.Service.Fail(ctx, job.ID, "boom"))
	}

	row, err := testQueries.GetBenchmarkEvalJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", row.State)
	require.Equal(t, int32(3), row.Attempt)
	require.Equal(t, "boom", row.LastError)

	updatedTask, err := testQueries.GetBenchmarkTask(ctx, db.GetBenchmarkTaskParams{
		ID:          task.ID,
		WorkspaceID: f.WS.WorkspaceID,
	})
	require.NoError(t, err)
	require.Equal(t, "errored", updatedTask.Status)
	require.Equal(t, "eval_failed", updatedTask.StatusReason)

	// Only the final permanent failure publishes a status event;
	// the two transient retries do not.
	evs := f.Pub.snapshot()
	require.Len(t, evs, 1)
	require.Equal(t, protocol.EventBenchmarkTaskStatus, evs[0].Type)
}

func TestEvalJobService_Fail_TransientReturnsToPending(t *testing.T) {
	ctx := context.Background()
	f := newEvalJobFixture(t)

	_, job := makeTaskAndJob(t, f, "alpha__one.aaa", "programbench")

	_, err := f.Service.Claim(ctx, "evaluator-A", []string{"programbench"}, 5)
	require.NoError(t, err)

	require.NoError(t, f.Service.Fail(ctx, job.ID, "transient"))

	row, err := testQueries.GetBenchmarkEvalJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", row.State)
	require.Equal(t, int32(1), row.Attempt)
	require.False(t, row.ClaimedBy.Valid)

	require.Empty(t, f.Pub.snapshot())
}

func TestEvalJobService_Fail_NotFound(t *testing.T) {
	ctx := context.Background()
	f := newEvalJobFixture(t)

	bogus := newRandomUUID(t)
	err := f.Service.Fail(ctx, bogus, "boom")
	require.ErrorIs(t, err, benchmark.ErrEvalJobNotFound)
}
