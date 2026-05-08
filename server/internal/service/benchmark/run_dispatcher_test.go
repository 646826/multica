package benchmark_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/service/benchmark"
	"github.com/multica-ai/multica/server/internal/service/benchmark/adapter"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// fakeCatalog is a test double for adapter.Catalog. Resolve returns the
// configured Instance for a known id and an error otherwise — this lets
// the dispatcher exercise both the "queued" and "skipped" task branches
// without touching network/disk.
type fakeCatalog struct {
	kind      string
	responses map[string]adapter.Instance
}

func (f *fakeCatalog) Kind() string { return f.kind }

func (f *fakeCatalog) Resolve(_ context.Context, id string) (adapter.Instance, error) {
	if r, ok := f.responses[id]; ok {
		return r, nil
	}
	return adapter.Instance{}, errors.New("unknown instance")
}

func (f *fakeCatalog) List(_ context.Context, _ adapter.ListFilter) ([]adapter.Instance, error) {
	return nil, nil
}

func TestRunDispatcher_Tick_ResolvesAndCreatesTasks(t *testing.T) {
	ctx := context.Background()
	ws := newFixtureWorkspace(t)

	// Catalog resolves "good__id.cafe" with non-empty meta and errors on the
	// "bad__id.beef" id we will also include in the suite below.
	fakeCat := &fakeCatalog{
		kind: "programbench",
		responses: map[string]adapter.Instance{
			"good__id.cafe": {
				ID:         "good__id.cafe",
				Language:   "c",
				Difficulty: "easy",
				Meta:       json.RawMessage(`{"x":1}`),
			},
		},
	}
	reg := adapter.NewRegistry()
	reg.RegisterCatalog(fakeCat)

	suiteSvc := benchmark.NewSuiteService(testQueries)
	suite, err := suiteSvc.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: ws.WorkspaceID,
		Slug:        "rd-suite",
		DisplayName: "RD Suite",
		AdapterKind: "programbench",
		InstanceIDs: []string{"good__id.cafe", "bad__id.beef"},
		CreatedBy:   ws.UserID,
	})
	require.NoError(t, err)

	agentID := newFixtureAgent(t, ws, agentSpec{
		Name:         "RDAgent",
		Model:        "claude-opus-4-7",
		PromptSource: "# system\nrd test\n",
	})
	profSvc := benchmark.NewProfileService(testQueries)
	profile, err := profSvc.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: ws.WorkspaceID,
		AgentID:     agentID,
		Slug:        "rd-profile",
		DisplayName: "RD Profile",
		CapturedBy:  ws.UserID,
	})
	require.NoError(t, err)

	startPub := &recordingPublisher{}
	runSvc := benchmark.NewRunService(testQueries, testPool, startPub)
	run, err := runSvc.StartRun(ctx, benchmark.StartRunInput{
		WorkspaceID:   ws.WorkspaceID,
		SuiteID:       suite.ID,
		ProfileID:     profile.ID,
		DisplayName:   "RD Run",
		EvaluatorMode: "imported",
		CreatedBy:     ws.UserID,
	})
	require.NoError(t, err)

	pub := &recordingPublisher{}
	d := benchmark.NewRunDispatcher(testQueries, testPool, reg, pub)
	require.NoError(t, d.Tick(ctx))

	after, err := runSvc.GetRun(ctx, run.ID, ws.WorkspaceID)
	require.NoError(t, err)
	require.Equal(t, "submitting", after.Status)

	tasks, err := testQueries.ListBenchmarkTasksByRun(ctx, run.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 2)

	var sawGood, sawBad bool
	for _, task := range tasks {
		switch task.InstanceID {
		case "good__id.cafe":
			sawGood = true
			require.Equal(t, "queued", task.Status)
			require.JSONEq(t, `{"x":1}`, string(task.InstanceMeta))
		case "bad__id.beef":
			sawBad = true
			require.Equal(t, "skipped", task.Status)
			require.Equal(t, "unknown_instance", task.StatusReason)
			require.JSONEq(t, `{}`, string(task.InstanceMeta))
		default:
			t.Fatalf("unexpected instance: %s", task.InstanceID)
		}
	}
	require.True(t, sawGood, "expected a task for good__id.cafe")
	require.True(t, sawBad, "expected a task for bad__id.beef")

	// One submitting event published from the dispatcher.
	evs := pub.snapshot()
	require.Len(t, evs, 1)
	require.Equal(t, protocol.EventBenchmarkRunStatus, evs[0].Type)
	payload, ok := evs[0].Payload.(map[string]any)
	require.True(t, ok, "payload should be map[string]any")
	require.Equal(t, "submitting", payload["status"])
}

func TestRunDispatcher_Tick_SkipsRunsWithUnknownAdapter(t *testing.T) {
	ctx := context.Background()
	ws := newFixtureWorkspace(t)

	suiteSvc := benchmark.NewSuiteService(testQueries)
	suite, err := suiteSvc.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: ws.WorkspaceID,
		Slug:        "rd-suite-noadapter",
		DisplayName: "No Adapter",
		AdapterKind: "programbench",
		InstanceIDs: []string{"x__y.zz"},
		CreatedBy:   ws.UserID,
	})
	require.NoError(t, err)

	agentID := newFixtureAgent(t, ws, agentSpec{
		Name:         "RDAgentNoAdapter",
		Model:        "claude-opus-4-7",
		PromptSource: "# system\nno adapter\n",
	})
	profSvc := benchmark.NewProfileService(testQueries)
	profile, err := profSvc.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: ws.WorkspaceID,
		AgentID:     agentID,
		Slug:        "rd-profile-noadapter",
		DisplayName: "No Adapter Profile",
		CapturedBy:  ws.UserID,
	})
	require.NoError(t, err)

	runSvc := benchmark.NewRunService(testQueries, testPool, &recordingPublisher{})
	run, err := runSvc.StartRun(ctx, benchmark.StartRunInput{
		WorkspaceID:   ws.WorkspaceID,
		SuiteID:       suite.ID,
		ProfileID:     profile.ID,
		DisplayName:   "No Adapter Run",
		EvaluatorMode: "imported",
		CreatedBy:     ws.UserID,
	})
	require.NoError(t, err)

	// Empty registry — adapter "programbench" not registered.
	emptyReg := adapter.NewRegistry()
	pub := &recordingPublisher{}
	d := benchmark.NewRunDispatcher(testQueries, testPool, emptyReg, pub)

	// Tick itself returns nil (per-run errors are logged, not propagated)
	// so a single misconfigured run cannot stall the whole dispatcher.
	require.NoError(t, d.Tick(ctx))

	// Run stays queued — no tasks, no submitting event.
	after, err := runSvc.GetRun(ctx, run.ID, ws.WorkspaceID)
	require.NoError(t, err)
	require.Equal(t, "queued", after.Status)

	tasks, err := testQueries.ListBenchmarkTasksByRun(ctx, run.ID)
	require.NoError(t, err)
	require.Empty(t, tasks)
	require.Empty(t, pub.snapshot())
}
