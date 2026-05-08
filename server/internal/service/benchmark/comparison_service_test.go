package benchmark_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service/benchmark"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// completeRunSpec is the per-run input newComparisonFixture turns into a
// fully-stitched (run, tasks, eval_results, run_summary) tuple in the DB —
// the minimum the comparison + leaderboard service paths read.
type completeRunSpec struct {
	DisplayName       string
	Profile           benchmark.Profile
	Status            string // 'complete' (default) or 'submitting'/'failed' for negative cases
	Resolved          int32
	Total             int32
	Errored           int32
	AveragePassRate   float64
	AggregatePassRate float64
	FailureCategories []failureCategory
	// Evals seeded as benchmark_task + benchmark_eval_result pairs.
	Evals []evalSpec
}

type evalSpec struct {
	InstanceID string
	Resolved   bool
	PassRate   float64
}

type failureCategory struct {
	Name  string `json:"Name"`
	Count int    `json:"Count"`
}

// comparisonFixture holds the workspace + suite + run service so individual
// tests can call CompareRuns / LeaderboardForSuite against pre-seeded runs.
type comparisonFixture struct {
	WS      fixtureWorkspace
	Suite   benchmark.Suite
	Service *benchmark.RunService
}

func newComparisonFixture(t *testing.T) comparisonFixture {
	t.Helper()
	ctx := context.Background()
	ws := newFixtureWorkspace(t)

	suiteSvc := benchmark.NewSuiteService(testQueries)
	suite, err := suiteSvc.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: ws.WorkspaceID,
		Slug:        "cmp-suite",
		DisplayName: "Comparison Suite",
		AdapterKind: "programbench",
		InstanceIDs: []string{"alpha__one.aaa", "beta__two.bbb"},
		CreatedBy:   ws.UserID,
	})
	require.NoError(t, err)

	bus := events.New()
	svc := benchmark.NewRunService(testQueries, testPool, bus)

	return comparisonFixture{WS: ws, Suite: suite, Service: svc}
}

// newComparisonProfile inserts an agent + profile pair scoped to the fixture
// workspace, so tests can attribute runs to multiple profiles for leaderboard
// scenarios.
func newComparisonProfile(t *testing.T, ws fixtureWorkspace, slug, displayName string) benchmark.Profile {
	t.Helper()
	ctx := context.Background()
	agentID := newFixtureAgent(t, ws, agentSpec{
		Name:         "CmpAgent_" + slug,
		Model:        "claude-opus-4-7",
		PromptSource: "# system\ncomparison test\n",
	})
	profSvc := benchmark.NewProfileService(testQueries)
	prof, err := profSvc.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: ws.WorkspaceID,
		AgentID:     agentID,
		Slug:        slug,
		DisplayName: displayName,
		CapturedBy:  ws.UserID,
	})
	require.NoError(t, err)
	return prof
}

// seedCompleteRun materializes a benchmark_run row in the requested status,
// inserts a benchmark_task per evalSpec with a benchmark_eval_result attached,
// and writes the benchmark_run_summary the production code path would have
// written had the finalizer run. Returns the run id so the test can hand it
// to CompareRuns / read it back.
func seedCompleteRun(t *testing.T, ws fixtureWorkspace, suite benchmark.Suite, spec completeRunSpec) pgtype.UUID {
	t.Helper()
	ctx := context.Background()

	status := spec.Status
	if status == "" {
		status = "complete"
	}

	bus := events.New()
	svc := benchmark.NewRunService(testQueries, testPool, bus)
	run, err := svc.StartRun(ctx, benchmark.StartRunInput{
		WorkspaceID:    ws.WorkspaceID,
		SuiteID:        suite.ID,
		ProfileID:      spec.Profile.ID,
		DisplayName:    spec.DisplayName,
		EvaluatorMode:  "imported",
		AdapterVersion: "programbench@v1",
		CreatedBy:      ws.UserID,
	})
	require.NoError(t, err)

	for _, ev := range spec.Evals {
		task, err := testQueries.CreateBenchmarkTask(ctx, db.CreateBenchmarkTaskParams{
			RunID:        run.ID,
			WorkspaceID:  ws.WorkspaceID,
			InstanceID:   ev.InstanceID,
			InstanceMeta: []byte(`{}`),
			Status:       "scored",
		})
		require.NoError(t, err)

		var pr pgtype.Numeric
		require.NoError(t, pr.Scan(fmt.Sprintf("%.5f", ev.PassRate)))
		_, err = testQueries.UpsertBenchmarkEvalResult(ctx, db.UpsertBenchmarkEvalResultParams{
			TaskID:           task.ID,
			WorkspaceID:      ws.WorkspaceID,
			Resolved:         ev.Resolved,
			PassedTests:      0,
			TotalTests:       1,
			PassRate:         pr,
			RawEvalJson:      []byte(`{}`),
			FailedCategories: []byte(`[]`),
		})
		require.NoError(t, err)
	}

	if status != "queued" {
		// Drive the run through a valid lifecycle so completed_at is set on
		// the way to a terminal state. UpdateBenchmarkRunStatus only stamps
		// completed_at for ('complete','failed','canceled').
		_, err := testQueries.UpdateBenchmarkRunStatus(ctx, db.UpdateBenchmarkRunStatusParams{
			ID:          run.ID,
			WorkspaceID: ws.WorkspaceID,
			Status:      "submitting",
		})
		require.NoError(t, err)
		if status != "submitting" {
			_, err = testQueries.UpdateBenchmarkRunStatus(ctx, db.UpdateBenchmarkRunStatusParams{
				ID:          run.ID,
				WorkspaceID: ws.WorkspaceID,
				Status:      status,
			})
			require.NoError(t, err)
		}
	}

	// The summary row is keyed on run_id; write it only for runs the test
	// declared as 'complete' so non-complete runs realistically lack one.
	if status == "complete" {
		cats := spec.FailureCategories
		if cats == nil {
			cats = []failureCategory{}
		}
		catJSON, err := json.Marshal(cats)
		require.NoError(t, err)

		var agg, avg pgtype.Numeric
		require.NoError(t, agg.Scan(fmt.Sprintf("%.5f", spec.AggregatePassRate)))
		require.NoError(t, avg.Scan(fmt.Sprintf("%.5f", spec.AveragePassRate)))
		_, err = testQueries.UpsertBenchmarkRunSummary(ctx, db.UpsertBenchmarkRunSummaryParams{
			RunID:             run.ID,
			WorkspaceID:       ws.WorkspaceID,
			ResolvedCount:     spec.Resolved,
			TotalCount:        spec.Total,
			AggregatePassRate: agg,
			AveragePassRate:   avg,
			ErroredCount:      spec.Errored,
			FailureCategories: catJSON,
		})
		require.NoError(t, err)
	}
	return run.ID
}

func TestRunService_CompareRuns_HappyPath(t *testing.T) {
	ctx := context.Background()
	f := newComparisonFixture(t)
	profA := newComparisonProfile(t, f.WS, "cmp-prof-a", "Profile A")
	profB := newComparisonProfile(t, f.WS, "cmp-prof-b", "Profile B")

	baseID := seedCompleteRun(t, f.WS, f.Suite, completeRunSpec{
		DisplayName:       "Base Run",
		Profile:           profA,
		Resolved:          0,
		Total:             2,
		AveragePassRate:   0.5,
		AggregatePassRate: 0.5,
		FailureCategories: []failureCategory{
			{Name: "compile_error", Count: 1},
			{Name: "wrong_output", Count: 1},
		},
		Evals: []evalSpec{
			{InstanceID: "alpha__one.aaa", PassRate: 0.5, Resolved: false},
			{InstanceID: "beta__two.bbb", PassRate: 0.5, Resolved: false},
		},
	})
	candID := seedCompleteRun(t, f.WS, f.Suite, completeRunSpec{
		DisplayName:       "Candidate Run",
		Profile:           profB,
		Resolved:          1,
		Total:             2,
		AveragePassRate:   0.75,
		AggregatePassRate: 0.75,
		FailureCategories: []failureCategory{
			{Name: "wrong_output", Count: 1},
			{Name: "timeout", Count: 1},
		},
		Evals: []evalSpec{
			{InstanceID: "alpha__one.aaa", PassRate: 1.0, Resolved: true},
			{InstanceID: "beta__two.bbb", PassRate: 0.5, Resolved: false},
		},
	})

	res, err := f.Service.CompareRuns(ctx, baseID, candID, f.WS.WorkspaceID)
	require.NoError(t, err)

	require.Equal(t, util.UUIDToString(baseID), res.BaseRunID)
	require.Equal(t, util.UUIDToString(candID), res.CandRunID)
	require.False(t, res.Partial, "fully overlapping instance sets must not be partial")

	require.Equal(t, 1, res.Delta.Resolved)
	require.InDelta(t, 0.25, res.Delta.AvgPassRate, 1e-9)
	require.InDelta(t, 0.25, res.Delta.AggPassRate, 1e-9)

	require.Len(t, res.Improved, 1)
	require.Equal(t, "alpha__one.aaa", res.Improved[0].InstanceID)
	require.Empty(t, res.Regressed)
	require.Len(t, res.NewlyResolved, 1)
	require.Equal(t, "alpha__one.aaa", res.NewlyResolved[0].InstanceID)
	require.Empty(t, res.LostResolved)

	require.Equal(t, []string{"timeout"}, res.Categories.Added)
	require.Equal(t, []string{"compile_error"}, res.Categories.Cleared)
}

func TestRunService_CompareRuns_RejectsNonComplete(t *testing.T) {
	ctx := context.Background()
	f := newComparisonFixture(t)
	prof := newComparisonProfile(t, f.WS, "cmp-prof-only", "Only Profile")

	completeID := seedCompleteRun(t, f.WS, f.Suite, completeRunSpec{
		DisplayName:     "Complete",
		Profile:         prof,
		Resolved:        1,
		Total:           1,
		AveragePassRate: 1.0,
		Evals: []evalSpec{
			{InstanceID: "alpha__one.aaa", PassRate: 1.0, Resolved: true},
		},
	})
	pendingID := seedCompleteRun(t, f.WS, f.Suite, completeRunSpec{
		DisplayName: "Still Submitting",
		Profile:     prof,
		Status:      "submitting",
	})

	_, err := f.Service.CompareRuns(ctx, completeID, pendingID, f.WS.WorkspaceID)
	require.ErrorIs(t, err, benchmark.ErrRunNotComplete)

	// Symmetric: also fails when the *base* run is the non-complete one.
	_, err = f.Service.CompareRuns(ctx, pendingID, completeID, f.WS.WorkspaceID)
	require.ErrorIs(t, err, benchmark.ErrRunNotComplete)
}

func TestRunService_CompareRuns_404_OnUnknownRun(t *testing.T) {
	ctx := context.Background()
	f := newComparisonFixture(t)
	prof := newComparisonProfile(t, f.WS, "cmp-prof-404", "404 Profile")

	completeID := seedCompleteRun(t, f.WS, f.Suite, completeRunSpec{
		DisplayName:     "Complete",
		Profile:         prof,
		Resolved:        1,
		Total:           1,
		AveragePassRate: 1.0,
		Evals: []evalSpec{
			{InstanceID: "alpha__one.aaa", PassRate: 1.0, Resolved: true},
		},
	})
	bogus := newRandomUUID(t)

	_, err := f.Service.CompareRuns(ctx, completeID, bogus, f.WS.WorkspaceID)
	require.ErrorIs(t, err, benchmark.ErrRunNotFound)
}

func TestRunService_LeaderboardForSuite_RanksByBestRun(t *testing.T) {
	ctx := context.Background()
	f := newComparisonFixture(t)
	profA := newComparisonProfile(t, f.WS, "lb-prof-a", "Profile A")
	profB := newComparisonProfile(t, f.WS, "lb-prof-b", "Profile B")

	// Profile A has two runs — the leaderboard must surface only the better
	// one (resolved=2 beats resolved=1) so we can verify best-per-profile.
	_ = seedCompleteRun(t, f.WS, f.Suite, completeRunSpec{
		DisplayName:       "A weak",
		Profile:           profA,
		Resolved:          1,
		Total:             2,
		AveragePassRate:   0.5,
		AggregatePassRate: 0.5,
		Evals: []evalSpec{
			{InstanceID: "alpha__one.aaa", PassRate: 1.0, Resolved: true},
			{InstanceID: "beta__two.bbb", PassRate: 0.0, Resolved: false},
		},
	})
	// Sleep 1.1s between best-run inserts to keep completed_at distinguishable
	// from the weak-run timestamp; without it, all three runs in a tight loop
	// can land within the same Postgres millisecond and the tiebreaker becomes
	// flaky on fast hardware.
	time.Sleep(1100 * time.Millisecond)
	bestAID := seedCompleteRun(t, f.WS, f.Suite, completeRunSpec{
		DisplayName:       "A best",
		Profile:           profA,
		Resolved:          2,
		Total:             2,
		AveragePassRate:   1.0,
		AggregatePassRate: 1.0,
		Evals: []evalSpec{
			{InstanceID: "alpha__one.aaa", PassRate: 1.0, Resolved: true},
			{InstanceID: "beta__two.bbb", PassRate: 1.0, Resolved: true},
		},
	})
	time.Sleep(1100 * time.Millisecond)
	bestBID := seedCompleteRun(t, f.WS, f.Suite, completeRunSpec{
		DisplayName:       "B only",
		Profile:           profB,
		Resolved:          1,
		Total:             2,
		AveragePassRate:   0.5,
		AggregatePassRate: 0.5,
		Evals: []evalSpec{
			{InstanceID: "alpha__one.aaa", PassRate: 1.0, Resolved: true},
			{InstanceID: "beta__two.bbb", PassRate: 0.0, Resolved: false},
		},
	})

	rows, err := f.Service.LeaderboardForSuite(ctx, f.WS.WorkspaceID, f.Suite.Slug)
	require.NoError(t, err)
	require.Len(t, rows, 2, "two profiles → two rows; A's weak run must be folded into best-per-profile")

	// Rank 1 = profile A's best run (resolved=2 beats B's resolved=1).
	require.Equal(t, 1, rows[0].Rank)
	require.Equal(t, "lb-prof-a", rows[0].ProfileSlug)
	require.Equal(t, util.UUIDToString(bestAID), rows[0].BestRunID)
	require.Equal(t, 2, rows[0].ResolvedCount)
	require.InDelta(t, 1.0, rows[0].AveragePassRate, 1e-9)

	// Rank 2 = profile B.
	require.Equal(t, 2, rows[1].Rank)
	require.Equal(t, "lb-prof-b", rows[1].ProfileSlug)
	require.Equal(t, util.UUIDToString(bestBID), rows[1].BestRunID)
	require.Equal(t, 1, rows[1].ResolvedCount)
}

func TestRunService_LeaderboardForSuite_404_OnUnknownSlug(t *testing.T) {
	ctx := context.Background()
	f := newComparisonFixture(t)

	_, err := f.Service.LeaderboardForSuite(ctx, f.WS.WorkspaceID, "no-such-suite")
	require.ErrorIs(t, err, benchmark.ErrSuiteResolution)
}

func TestRunService_LeaderboardForSuite_EmptyWhenNoCompleteRuns(t *testing.T) {
	ctx := context.Background()
	f := newComparisonFixture(t)
	prof := newComparisonProfile(t, f.WS, "lb-prof-empty", "Empty Profile")

	// Run exists but is still 'submitting', so it must not appear.
	_ = seedCompleteRun(t, f.WS, f.Suite, completeRunSpec{
		DisplayName: "Pending",
		Profile:     prof,
		Status:      "submitting",
	})

	rows, err := f.Service.LeaderboardForSuite(ctx, f.WS.WorkspaceID, f.Suite.Slug)
	require.NoError(t, err)
	require.Empty(t, rows)
}
