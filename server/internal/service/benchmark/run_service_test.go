package benchmark_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service/benchmark"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// recordingPublisher implements benchmark.Publisher and captures events for assertions.
type recordingPublisher struct {
	mu     sync.Mutex
	events []events.Event
}

func (r *recordingPublisher) Publish(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recordingPublisher) snapshot() []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]events.Event, len(r.events))
	copy(out, r.events)
	return out
}

// runFixture creates a workspace + suite + profile so tests can call StartRun.
type runFixture struct {
	WS      fixtureWorkspace
	Suite   benchmark.Suite
	Profile benchmark.Profile
	Pub     *recordingPublisher
	Service *benchmark.RunService
}

func newRunFixture(t *testing.T) runFixture {
	t.Helper()
	ctx := context.Background()
	ws := newFixtureWorkspace(t)
	agentID := newFixtureAgent(t, ws, agentSpec{
		Name:         "RunSvcAgent",
		Model:        "claude-opus-4-7",
		PromptSource: "# system\nrun service test\n",
	})

	suiteSvc := benchmark.NewSuiteService(testQueries)
	suite, err := suiteSvc.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: ws.WorkspaceID,
		Slug:        "rs-suite",
		DisplayName: "Run Svc Suite",
		AdapterKind: "programbench",
		InstanceIDs: []string{"alpha__one.aaa", "beta__two.bbb"},
		CreatedBy:   ws.UserID,
	})
	require.NoError(t, err)

	profSvc := benchmark.NewProfileService(testQueries)
	profile, err := profSvc.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: ws.WorkspaceID,
		AgentID:     agentID,
		Slug:        "rs-profile",
		DisplayName: "Run Svc Profile",
		CapturedBy:  ws.UserID,
	})
	require.NoError(t, err)

	pub := &recordingPublisher{}
	svc := benchmark.NewRunService(testQueries, testPool, pub)

	return runFixture{
		WS:      ws,
		Suite:   suite,
		Profile: profile,
		Pub:     pub,
		Service: svc,
	}
}

func TestRunService_StartRun_CreatesQueuedRun(t *testing.T) {
	ctx := context.Background()
	rf := newRunFixture(t)

	got, err := rf.Service.StartRun(ctx, benchmark.StartRunInput{
		WorkspaceID:    rf.WS.WorkspaceID,
		SuiteID:        rf.Suite.ID,
		ProfileID:      rf.Profile.ID,
		DisplayName:    "Run 1",
		EvaluatorMode:  "managed",
		AdapterVersion: "programbench@v1",
		CreatedBy:      rf.WS.UserID,
	})
	require.NoError(t, err)
	require.True(t, got.ID.Valid)
	require.Equal(t, "queued", got.Status)
	require.Equal(t, []string{"alpha__one.aaa", "beta__two.bbb"}, got.SuiteInstanceIDs)
	require.Equal(t, "managed", got.EvaluatorMode)
	require.Equal(t, rf.Suite.ID.Bytes, got.SuiteID.Bytes)
	require.Equal(t, rf.Profile.ID.Bytes, got.ProfileID.Bytes)

	evs := rf.Pub.snapshot()
	require.Len(t, evs, 1)
	require.Equal(t, protocol.EventBenchmarkRunCreated, evs[0].Type)
}

func TestRunService_StartRun_RejectsUnknownSuite(t *testing.T) {
	ctx := context.Background()
	rf := newRunFixture(t)

	bogusSuite := newRandomUUID(t)
	_, err := rf.Service.StartRun(ctx, benchmark.StartRunInput{
		WorkspaceID:   rf.WS.WorkspaceID,
		SuiteID:       bogusSuite,
		ProfileID:     rf.Profile.ID,
		DisplayName:   "Bad Suite",
		EvaluatorMode: "managed",
		CreatedBy:     rf.WS.UserID,
	})
	require.ErrorIs(t, err, benchmark.ErrSuiteResolution)
	require.Empty(t, rf.Pub.snapshot())
}

func TestRunService_StartRun_RejectsBadEvaluatorMode(t *testing.T) {
	ctx := context.Background()
	rf := newRunFixture(t)

	_, err := rf.Service.StartRun(ctx, benchmark.StartRunInput{
		WorkspaceID:   rf.WS.WorkspaceID,
		SuiteID:       rf.Suite.ID,
		ProfileID:     rf.Profile.ID,
		DisplayName:   "Bad Mode",
		EvaluatorMode: "weird",
		CreatedBy:     rf.WS.UserID,
	})
	require.ErrorIs(t, err, benchmark.ErrInvalidEvaluator)
	require.Empty(t, rf.Pub.snapshot())
}

func TestRunService_GetRun_404(t *testing.T) {
	ctx := context.Background()
	rf := newRunFixture(t)

	bogus := newRandomUUID(t)
	_, err := rf.Service.GetRun(ctx, bogus, rf.WS.WorkspaceID)
	require.ErrorIs(t, err, benchmark.ErrRunNotFound)
}

func TestRunService_CancelRun_FlipsStatus(t *testing.T) {
	ctx := context.Background()
	rf := newRunFixture(t)

	run, err := rf.Service.StartRun(ctx, benchmark.StartRunInput{
		WorkspaceID:   rf.WS.WorkspaceID,
		SuiteID:       rf.Suite.ID,
		ProfileID:     rf.Profile.ID,
		DisplayName:   "To Cancel",
		EvaluatorMode: "managed",
		CreatedBy:     rf.WS.UserID,
	})
	require.NoError(t, err)

	require.NoError(t, rf.Service.CancelRun(ctx, run.ID, rf.WS.WorkspaceID))

	got, err := rf.Service.GetRun(ctx, run.ID, rf.WS.WorkspaceID)
	require.NoError(t, err)
	require.Equal(t, "canceled", got.Status)
	require.Equal(t, "user_canceled", got.StatusReason)

	// CancelRun on a non-existent id => ErrRunNotFound
	bogus := newRandomUUID(t)
	require.ErrorIs(t, rf.Service.CancelRun(ctx, bogus, rf.WS.WorkspaceID), benchmark.ErrRunNotFound)
}

func TestRunService_ImportEvalResult_AdvancesTask(t *testing.T) {
	ctx := context.Background()
	rf := newRunFixture(t)

	run, err := rf.Service.StartRun(ctx, benchmark.StartRunInput{
		WorkspaceID:    rf.WS.WorkspaceID,
		SuiteID:        rf.Suite.ID,
		ProfileID:      rf.Profile.ID,
		DisplayName:    "Imported Run",
		EvaluatorMode:  "imported",
		AdapterVersion: "programbench@v1",
		CreatedBy:      rf.WS.UserID,
	})
	require.NoError(t, err)

	// Manually create a benchmark_task in 'submitted' state — TaskDispatcher
	// (T10) doesn't exist yet, and ImportEvalResult requires a row to advance.
	task, err := testQueries.CreateBenchmarkTask(ctx, db.CreateBenchmarkTaskParams{
		RunID:        run.ID,
		WorkspaceID:  rf.WS.WorkspaceID,
		InstanceID:   "alpha__one.aaa",
		InstanceMeta: []byte("{}"),
		Status:       "submitted",
		StatusReason: "",
	})
	require.NoError(t, err)

	rawEval := json.RawMessage(`{"resolved":true,"passed":3,"total":3}`)
	err = rf.Service.ImportEvalResult(ctx, benchmark.ImportEvalResultInput{
		WorkspaceID:      rf.WS.WorkspaceID,
		RunID:            run.ID,
		InstanceID:       "alpha__one.aaa",
		Resolved:         true,
		PassedTests:      3,
		TotalTests:       3,
		PassRate:         1.0,
		RawEvalJSON:      rawEval,
		FailedCategories: []string{},
	})
	require.NoError(t, err)

	// Task should be advanced to 'scored'.
	updatedTask, err := testQueries.GetBenchmarkTask(ctx, db.GetBenchmarkTaskParams{
		ID:          task.ID,
		WorkspaceID: rf.WS.WorkspaceID,
	})
	require.NoError(t, err)
	require.Equal(t, "scored", updatedTask.Status)

	// eval_result row should exist with matching values.
	er, err := testQueries.GetBenchmarkEvalResult(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, er.Resolved)
	require.Equal(t, int32(3), er.PassedTests)
	require.Equal(t, int32(3), er.TotalTests)

	// One run:created event from StartRun + one task:scored from ImportEvalResult.
	evs := rf.Pub.snapshot()
	require.Len(t, evs, 2)
	require.Equal(t, protocol.EventBenchmarkRunCreated, evs[0].Type)
	require.Equal(t, protocol.EventBenchmarkTaskScored, evs[1].Type)
}

func TestRunService_ImportEvalResult_TaskNotFound(t *testing.T) {
	ctx := context.Background()
	rf := newRunFixture(t)

	run, err := rf.Service.StartRun(ctx, benchmark.StartRunInput{
		WorkspaceID:   rf.WS.WorkspaceID,
		SuiteID:       rf.Suite.ID,
		ProfileID:     rf.Profile.ID,
		DisplayName:   "Empty Run",
		EvaluatorMode: "imported",
		CreatedBy:     rf.WS.UserID,
	})
	require.NoError(t, err)

	err = rf.Service.ImportEvalResult(ctx, benchmark.ImportEvalResultInput{
		WorkspaceID:      rf.WS.WorkspaceID,
		RunID:            run.ID,
		InstanceID:       "missing__instance.000",
		Resolved:         false,
		PassedTests:      0,
		TotalTests:       1,
		PassRate:         0.0,
		RawEvalJSON:      json.RawMessage(`{}`),
		FailedCategories: []string{"missing"},
	})
	require.ErrorIs(t, err, benchmark.ErrTaskNotFoundForInstance)
}

func TestRunService_ListRuns_NewestFirst(t *testing.T) {
	ctx := context.Background()
	rf := newRunFixture(t)

	r1, err := rf.Service.StartRun(ctx, benchmark.StartRunInput{
		WorkspaceID: rf.WS.WorkspaceID, SuiteID: rf.Suite.ID, ProfileID: rf.Profile.ID,
		DisplayName: "First", EvaluatorMode: "managed", CreatedBy: rf.WS.UserID,
	})
	require.NoError(t, err)
	r2, err := rf.Service.StartRun(ctx, benchmark.StartRunInput{
		WorkspaceID: rf.WS.WorkspaceID, SuiteID: rf.Suite.ID, ProfileID: rf.Profile.ID,
		DisplayName: "Second", EvaluatorMode: "managed", CreatedBy: rf.WS.UserID,
	})
	require.NoError(t, err)

	got, err := rf.Service.ListRuns(ctx, rf.WS.WorkspaceID, 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	// Newest first.
	require.Equal(t, r2.ID.Bytes, got[0].ID.Bytes)
	require.Equal(t, r1.ID.Bytes, got[1].ID.Bytes)
}

// Compile-time assertion that *events.Bus satisfies benchmark.Publisher.
var _ benchmark.Publisher = (*events.Bus)(nil)

// Silence unused imports if any test branch is removed in the future.
var _ pgtype.UUID
