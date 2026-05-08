package benchmark_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service/benchmark"
	"github.com/multica-ai/multica/server/internal/service/benchmark/adapter"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// taskDispatcherFixture builds the full chain (workspace → suite → profile →
// run → queued tasks) so each test can call TaskDispatcher.Tick or
// OnSubmissionEvent without re-rolling the boilerplate.
type taskDispatcherFixture struct {
	WS       fixtureWorkspace
	Run      benchmark.Run
	Profile  benchmark.Profile
	Registry *adapter.Registry
	Bus      *events.Bus
}

func newTaskDispatcherFixture(t *testing.T, evalMode string, instanceIDs []string) taskDispatcherFixture {
	t.Helper()
	ctx := context.Background()
	ws := newFixtureWorkspace(t)

	// Resolve every requested id to a non-empty instance so RunDispatcher
	// flips them to 'queued' (not 'skipped'). The real ProgramBenchCatalog
	// shells out to uvx, which is not available in unit tests.
	fakeCat := &fakeCatalog{
		kind:      "programbench",
		responses: map[string]adapter.Instance{},
	}
	for _, id := range instanceIDs {
		fakeCat.responses[id] = adapter.Instance{
			ID:   id,
			Meta: []byte(`{}`),
		}
	}
	reg := adapter.NewRegistry()
	reg.RegisterCatalog(fakeCat)
	reg.RegisterComposer(adapter.NewProgramBenchComposer())
	reg.RegisterParser(adapter.NewProgramBenchParser())

	suiteSvc := benchmark.NewSuiteService(testQueries)
	suite, err := suiteSvc.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: ws.WorkspaceID,
		Slug:        "td-suite",
		DisplayName: "TD Suite",
		AdapterKind: "programbench",
		InstanceIDs: instanceIDs,
		CreatedBy:   ws.UserID,
	})
	require.NoError(t, err)

	agentID := newFixtureAgent(t, ws, agentSpec{
		Name:         "TDAgent",
		Model:        "claude-opus-4-7",
		PromptSource: "# system\ntd test\n",
	})
	profSvc := benchmark.NewProfileService(testQueries)
	profile, err := profSvc.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: ws.WorkspaceID,
		AgentID:     agentID,
		Slug:        "td-profile",
		DisplayName: "TD Profile",
		CapturedBy:  ws.UserID,
	})
	require.NoError(t, err)

	bus := events.New()
	runSvc := benchmark.NewRunService(testQueries, testPool, bus)
	run, err := runSvc.StartRun(ctx, benchmark.StartRunInput{
		WorkspaceID:   ws.WorkspaceID,
		SuiteID:       suite.ID,
		ProfileID:     profile.ID,
		DisplayName:   "TD Run",
		EvaluatorMode: evalMode,
		CreatedBy:     ws.UserID,
	})
	require.NoError(t, err)

	// Run the RunDispatcher once so the run advances to 'submitting' and
	// queued tasks exist. Catalog Resolve errors on unknown ids; we use
	// ProgramBench's own catalog which accepts any owner__repo.sha-style id.
	rd := benchmark.NewRunDispatcher(testQueries, testPool, reg, &recordingPublisher{})
	require.NoError(t, rd.Tick(ctx))

	return taskDispatcherFixture{
		WS:       ws,
		Run:      run,
		Profile:  profile,
		Registry: reg,
		Bus:      bus,
	}
}

func TestTaskDispatcher_Tick_CreatesIssuesAndAdvancesTasks(t *testing.T) {
	ctx := context.Background()
	f := newTaskDispatcherFixture(t, "imported", []string{
		"alpha__one.aaaaaa1",
		"beta__two.bbbbbbb2",
	})

	d := benchmark.NewTaskDispatcher(testQueries, testPool, f.Registry, f.Bus, nil)
	require.NoError(t, d.Tick(ctx))

	tasks, err := testQueries.ListBenchmarkTasksByRun(ctx, f.Run.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	for _, task := range tasks {
		require.Equal(t, "issued", task.Status, "instance %s", task.InstanceID)
		require.True(t, task.IssueID.Valid, "issue_id should be set for %s", task.InstanceID)
	}

	// The issues created should carry origin_type='benchmark_run' + origin_id=run.ID.
	for _, task := range tasks {
		issue, err := testQueries.GetIssue(ctx, task.IssueID)
		require.NoError(t, err)
		require.True(t, issue.OriginType.Valid)
		require.Equal(t, "benchmark_run", issue.OriginType.String)
		require.Equal(t, f.Run.ID.Bytes, issue.OriginID.Bytes)
		require.Equal(t, "todo", issue.Status)
		require.True(t, issue.AssigneeType.Valid)
		require.Equal(t, "agent", issue.AssigneeType.String)
		require.Equal(t, f.Profile.AgentID.Bytes, issue.AssigneeID.Bytes)
	}
}

func TestTaskDispatcher_OnSubmissionEvent_AdvancesTaskInManagedMode(t *testing.T) {
	ctx := context.Background()
	f := newTaskDispatcherFixture(t, "managed", []string{"foo__bar.cafebab"})

	d := benchmark.NewTaskDispatcher(testQueries, testPool, f.Registry, f.Bus, nil)
	require.NoError(t, d.Tick(ctx))

	tasks, err := testQueries.ListBenchmarkTasksByRun(ctx, f.Run.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	task := tasks[0]
	require.True(t, task.IssueID.Valid)

	// Insert a real attachment row so AttachAttachmentToTask has a valid FK.
	attachmentID := insertFixtureAttachment(t, f.WS, task.IssueID, "submission.tar.gz", 1024)

	require.NoError(t, d.OnSubmissionEvent(ctx, benchmark.SubmissionEvent{
		WorkspaceID:  f.WS.WorkspaceID,
		IssueID:      task.IssueID,
		AttachmentID: attachmentID,
		Filename:     "submission.tar.gz",
		MimeType:     "application/gzip",
		SizeBytes:    1024,
	}))

	after, err := testQueries.GetBenchmarkTask(ctx, db.GetBenchmarkTaskParams{
		ID:          task.ID,
		WorkspaceID: f.WS.WorkspaceID,
	})
	require.NoError(t, err)
	require.Equal(t, "submitted", after.Status)
	require.True(t, after.AttachmentID.Valid)
	require.Equal(t, attachmentID.Bytes, after.AttachmentID.Bytes)

	// Managed mode → an eval_job should exist for this task.
	jobs := listEvalJobsForTask(t, task.ID)
	require.Len(t, jobs, 1, "managed mode should enqueue exactly one eval_job")
	require.Equal(t, "pending", jobs[0].State)
	require.Equal(t, "programbench", jobs[0].AdapterKind)

	// The benchmark issue should be auto-closed so it stops appearing on
	// the workspace board after the agent submits.
	issue, err := testQueries.GetIssue(ctx, task.IssueID)
	require.NoError(t, err)
	require.Equal(t, "done", issue.Status)
}

func TestTaskDispatcher_OnSubmissionEvent_NoEvalJobInImportedMode(t *testing.T) {
	ctx := context.Background()
	f := newTaskDispatcherFixture(t, "imported", []string{"baz__qux.deadbe1"})

	d := benchmark.NewTaskDispatcher(testQueries, testPool, f.Registry, f.Bus, nil)
	require.NoError(t, d.Tick(ctx))
	tasks, err := testQueries.ListBenchmarkTasksByRun(ctx, f.Run.ID)
	require.NoError(t, err)
	task := tasks[0]

	attachmentID := insertFixtureAttachment(t, f.WS, task.IssueID, "submission.tar.gz", 1024)

	require.NoError(t, d.OnSubmissionEvent(ctx, benchmark.SubmissionEvent{
		WorkspaceID:  f.WS.WorkspaceID,
		IssueID:      task.IssueID,
		AttachmentID: attachmentID,
		Filename:     "submission.tar.gz",
		SizeBytes:    1024,
	}))

	after, err := testQueries.GetBenchmarkTask(ctx, db.GetBenchmarkTaskParams{
		ID:          task.ID,
		WorkspaceID: f.WS.WorkspaceID,
	})
	require.NoError(t, err)
	require.Equal(t, "submitted", after.Status)
	require.Empty(t, listEvalJobsForTask(t, task.ID), "imported mode must not enqueue eval_job")

	// Issue auto-close is mode-independent.
	issue, err := testQueries.GetIssue(ctx, task.IssueID)
	require.NoError(t, err)
	require.Equal(t, "done", issue.Status)
}

func TestTaskDispatcher_OnSubmissionEvent_IgnoresWrongFilename(t *testing.T) {
	ctx := context.Background()
	f := newTaskDispatcherFixture(t, "managed", []string{"zip__zap.0badf001"})

	d := benchmark.NewTaskDispatcher(testQueries, testPool, f.Registry, f.Bus, nil)
	require.NoError(t, d.Tick(ctx))
	tasks, err := testQueries.ListBenchmarkTasksByRun(ctx, f.Run.ID)
	require.NoError(t, err)
	task := tasks[0]

	// Don't even need an attachment row — Validate fails before we touch DB.
	require.NoError(t, d.OnSubmissionEvent(ctx, benchmark.SubmissionEvent{
		WorkspaceID: f.WS.WorkspaceID,
		IssueID:     task.IssueID,
		Filename:    "wrong-name.zip",
		SizeBytes:   1024,
	}))

	after, err := testQueries.GetBenchmarkTask(ctx, db.GetBenchmarkTaskParams{
		ID:          task.ID,
		WorkspaceID: f.WS.WorkspaceID,
	})
	require.NoError(t, err)
	require.Equal(t, "issued", after.Status, "wrong filename must not advance task")
	require.Empty(t, listEvalJobsForTask(t, task.ID))
}

// TestTaskDispatcher_WorkspaceConcurrencyCap verifies the per-workspace
// in-flight cap honored in dispatchRunTasks: with 5 queued tasks and a cap
// of 2, exactly 2 should advance to 'issued' on the first Tick and the rest
// should stay 'queued' until the next Tick (which we don't run here — the
// point is that one Tick must NOT issue past the cap).
func TestTaskDispatcher_WorkspaceConcurrencyCap(t *testing.T) {
	ctx := context.Background()
	f := newTaskDispatcherFixture(t, "imported", []string{
		"cap__a.aaaaaa1",
		"cap__b.bbbbbbb2",
		"cap__c.ccccccc3",
		"cap__d.ddddddd4",
		"cap__e.eeeeeee5",
	})

	d := benchmark.NewTaskDispatcher(testQueries, testPool, f.Registry, f.Bus, nil)
	d.SetWorkspaceMaxParallel(2)
	require.NoError(t, d.Tick(ctx))

	tasks, err := testQueries.ListBenchmarkTasksByRun(ctx, f.Run.ID)
	require.NoError(t, err)
	require.Len(t, tasks, 5)

	var issued, queued int
	for _, task := range tasks {
		switch task.Status {
		case "issued":
			issued++
			require.True(t, task.IssueID.Valid, "issued task should have issue_id set")
		case "queued":
			queued++
			require.False(t, task.IssueID.Valid, "queued task should not have issue_id")
		default:
			t.Fatalf("unexpected status %q for instance %s", task.Status, task.InstanceID)
		}
	}
	require.Equal(t, 2, issued, "exactly cap=2 tasks should be issued")
	require.Equal(t, 3, queued, "the remaining 3 tasks should stay queued for the next tick")

	// Sanity: the count query the dispatcher consults agrees with what we see.
	active, err := testQueries.CountActiveBenchmarkTasksByWorkspace(ctx, f.WS.WorkspaceID)
	require.NoError(t, err)
	require.Equal(t, int64(2), active)

	// A second Tick with the same cap and no completed tasks must not issue
	// any more work — the workspace is still at-cap.
	require.NoError(t, d.Tick(ctx))
	tasksAfter, err := testQueries.ListBenchmarkTasksByRun(ctx, f.Run.ID)
	require.NoError(t, err)
	var issuedAfter int
	for _, task := range tasksAfter {
		if task.Status == "issued" {
			issuedAfter++
		}
	}
	require.Equal(t, 2, issuedAfter, "cap must be honored on every tick, not just the first")
}

func TestTaskDispatcher_OnSubmissionEvent_IgnoresUnknownIssue(t *testing.T) {
	ctx := context.Background()
	f := newTaskDispatcherFixture(t, "managed", []string{"who__cares.99999ff"})
	d := benchmark.NewTaskDispatcher(testQueries, testPool, f.Registry, f.Bus, nil)

	// An issue id that has no corresponding benchmark_task — must be a no-op.
	bogus := mustParseUUID(t, "00000000-0000-0000-0000-000000000001")
	require.NoError(t, d.OnSubmissionEvent(ctx, benchmark.SubmissionEvent{
		WorkspaceID: f.WS.WorkspaceID,
		IssueID:     bogus,
		Filename:    "submission.tar.gz",
		SizeBytes:   1024,
	}))
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func insertFixtureAttachment(t *testing.T, ws fixtureWorkspace, issueID pgtype.UUID, filename string, size int64) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	var idStr string
	err := testPool.QueryRow(ctx, `
		INSERT INTO attachment (
			id, workspace_id, issue_id, uploader_type, uploader_id,
			filename, url, content_type, size_bytes
		)
		VALUES (gen_random_uuid(), $1, $2, 'agent', $3, $4, $5, $6, $7)
		RETURNING id
	`,
		ws.WorkspaceID, issueID, ws.UserID, filename,
		"https://example.test/"+filename,
		"application/gzip", size,
	).Scan(&idStr)
	if err != nil {
		t.Fatalf("insert attachment: %v", err)
	}
	return mustParseUUID(t, idStr)
}

func listEvalJobsForTask(t *testing.T, taskID pgtype.UUID) []db.BenchmarkEvalJob {
	t.Helper()
	ctx := context.Background()
	rows, err := testPool.Query(ctx, `
		SELECT id, task_id, workspace_id, adapter_kind, state, attempt,
		       claimed_by, claimed_at, enqueued_at, finished_at, last_error
		FROM benchmark_eval_job
		WHERE task_id = $1
		ORDER BY enqueued_at ASC
	`, taskID)
	if err != nil {
		t.Fatalf("query eval_job: %v", err)
	}
	defer rows.Close()
	var out []db.BenchmarkEvalJob
	for rows.Next() {
		var j db.BenchmarkEvalJob
		if err := rows.Scan(
			&j.ID, &j.TaskID, &j.WorkspaceID, &j.AdapterKind, &j.State,
			&j.Attempt, &j.ClaimedBy, &j.ClaimedAt, &j.EnqueuedAt,
			&j.FinishedAt, &j.LastError,
		); err != nil {
			t.Fatalf("scan eval_job: %v", err)
		}
		out = append(out, j)
	}
	return out
}
