package benchmark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service/benchmark/adapter"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// defaultTaskDispatcherInterval is the polling cadence for the create-issues
// loop. Matches RunDispatcher / TimeoutWatchdog so the whole benchmark
// lifecycle ticks on the same heartbeat.
const defaultTaskDispatcherInterval = 5 * time.Second

// SubmissionEvent is the bus-agnostic input to OnSubmissionEvent. The bus
// listener registered in Start parses comment:created payloads into this
// shape; tests can construct it directly without mounting the full bus.
type SubmissionEvent struct {
	WorkspaceID  pgtype.UUID
	IssueID      pgtype.UUID
	AttachmentID pgtype.UUID
	Filename     string
	MimeType     string
	SizeBytes    int64
}

// TaskEnqueuer is the minimal subset of *service.TaskService that the
// dispatcher needs to schedule an agent task once the benchmark issue is
// created. Defined here so tests can pass nil (no agent claim wiring) or
// a recording double instead of the production TaskService.
type TaskEnqueuer interface {
	EnqueueTaskForIssue(ctx context.Context, issue db.Issue, triggerCommentID ...pgtype.UUID) (db.AgentTaskQueue, error)
}

// TaskDispatcher turns 'queued' benchmark_task rows into 'issued' rows by
// composing a per-instance issue (via the adapter Composer), assigning it
// to the profile's agent, and stamping origin_type='benchmark_run' so
// existing autopilot/inbox listeners ignore it. It also subscribes to
// comment:created events so an agent's submission attachment advances the
// task to 'submitted' and (in managed mode) enqueues an eval_job.
type TaskDispatcher struct {
	q        *db.Queries
	pool     *pgxpool.Pool
	registry *adapter.Registry
	bus      *events.Bus
	tasks    TaskEnqueuer
	interval time.Duration
}

// NewTaskDispatcher constructs a TaskDispatcher with the default poll
// interval. tasks may be nil — when nil the dispatcher creates the issue
// and stops short of enqueueing an agent task; useful for tests that only
// care about the issue/task transitions.
func NewTaskDispatcher(q *db.Queries, pool *pgxpool.Pool, reg *adapter.Registry, bus *events.Bus, tasks TaskEnqueuer) *TaskDispatcher {
	return &TaskDispatcher{
		q:        q,
		pool:     pool,
		registry: reg,
		bus:      bus,
		tasks:    tasks,
		interval: defaultTaskDispatcherInterval,
	}
}

// Start subscribes to comment:created and runs Tick on a ticker until
// ctx is canceled. Per-tick errors are logged and discarded so a transient
// database error cannot kill the goroutine.
func (d *TaskDispatcher) Start(ctx context.Context) {
	if d.bus != nil {
		d.bus.Subscribe(protocol.EventCommentCreated, func(e events.Event) {
			subs := extractSubmissionEvents(e)
			for _, s := range subs {
				if err := d.OnSubmissionEvent(ctx, s); err != nil {
					slog.Warn("benchmark.task_dispatcher.submission_failed",
						"issue_id", util.UUIDToString(s.IssueID),
						"err", err,
					)
				}
			}
		})
	}

	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.Tick(ctx); err != nil {
				slog.Warn("benchmark.task_dispatcher.tick_failed", "err", err)
			}
		}
	}
}

// Tick lists active runs in 'submitting', and for each run flips queued
// tasks to 'issued' by creating an issue per task. Per-run / per-task
// errors are logged but not returned: one bad task must not block the
// rest of the queue.
func (d *TaskDispatcher) Tick(ctx context.Context) error {
	runs, err := d.q.ListActiveBenchmarkRuns(ctx)
	if err != nil {
		return fmt.Errorf("list active runs: %w", err)
	}
	for _, r := range runs {
		if r.Status != "submitting" {
			continue
		}
		if err := d.dispatchRunTasks(ctx, r); err != nil {
			slog.Warn("benchmark.task_dispatcher.run_failed",
				"run_id", util.UUIDToString(r.ID),
				"err", err,
			)
		}
	}
	return nil
}

func (d *TaskDispatcher) dispatchRunTasks(ctx context.Context, run db.BenchmarkRun) error {
	suite, err := d.q.GetBenchmarkSuite(ctx, db.GetBenchmarkSuiteParams{
		ID:          run.SuiteID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("get suite: %w", err)
	}
	composer, ok := d.registry.Composer(suite.AdapterKind)
	if !ok {
		return fmt.Errorf("composer not registered: %s", suite.AdapterKind)
	}
	profile, err := d.q.GetBenchmarkProfile(ctx, db.GetBenchmarkProfileParams{
		ID:          run.ProfileID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("get profile: %w", err)
	}

	queued, err := d.q.ListBenchmarkTasksByRunStatus(ctx, db.ListBenchmarkTasksByRunStatusParams{
		RunID:  run.ID,
		Status: "queued",
	})
	if err != nil {
		return fmt.Errorf("list queued tasks: %w", err)
	}

	for _, task := range queued {
		if err := d.issueOneTask(ctx, run, profile, composer, task); err != nil {
			slog.Warn("benchmark.task_dispatcher.task_failed",
				"run_id", util.UUIDToString(run.ID),
				"task_id", util.UUIDToString(task.ID),
				"instance_id", task.InstanceID,
				"err", err,
			)
		}
	}
	return nil
}

// issueOneTask composes an issue for a single benchmark_task and runs the
// status flip + issue insert in a single transaction so a partial failure
// can never leave a task 'issued' without an issue (or vice versa).
func (d *TaskDispatcher) issueOneTask(
	ctx context.Context,
	run db.BenchmarkRun,
	profile db.BenchmarkAgentProfile,
	composer adapter.IssueComposer,
	task db.BenchmarkTask,
) error {
	out, err := composer.Compose(ctx, adapter.ComposeInput{
		Run: adapter.RunRef{
			ID:          run.ID,
			SuiteID:     run.SuiteID,
			ProfileID:   run.ProfileID,
			DisplayName: run.DisplayName,
			WorkspaceID: run.WorkspaceID,
		},
		Task: adapter.TaskRef{
			ID:         task.ID,
			InstanceID: task.InstanceID,
		},
		Instance: adapter.Instance{
			ID:   task.InstanceID,
			Meta: json.RawMessage(task.InstanceMeta),
		},
	})
	if err != nil {
		return fmt.Errorf("compose: %w", err)
	}

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := d.q.WithTx(tx)

	issueNumber, err := qtx.IncrementIssueCounter(ctx, run.WorkspaceID)
	if err != nil {
		return fmt.Errorf("increment issue counter: %w", err)
	}

	issue, err := qtx.CreateIssueWithOrigin(ctx, db.CreateIssueWithOriginParams{
		WorkspaceID:  run.WorkspaceID,
		Title:        out.Title,
		Description:  pgtype.Text{String: out.Description, Valid: out.Description != ""},
		Status:       "todo",
		Priority:     "none",
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
		AssigneeID:   profile.AgentID,
		// The issue.creator_type CHECK only allows 'member' or 'agent',
		// so we attribute benchmark issues to the run creator (the user
		// who clicked Start Run) instead of inventing a 'system' creator.
		// Origin_type='benchmark_run' is what marks the issue as
		// programmatically generated for downstream filtering.
		CreatorType:   "member",
		CreatorID:     run.CreatedBy,
		ParentIssueID: pgtype.UUID{},
		Position:      0,
		DueDate:       pgtype.Timestamptz{},
		Number:        issueNumber,
		ProjectID:     pgtype.UUID{},
		OriginType:    pgtype.Text{String: "benchmark_run", Valid: true},
		OriginID:      run.ID,
	})
	if err != nil {
		return fmt.Errorf("create issue: %w", err)
	}

	if err := qtx.AttachIssueToTask(ctx, db.AttachIssueToTaskParams{
		ID:          task.ID,
		WorkspaceID: run.WorkspaceID,
		IssueID:     issue.ID,
	}); err != nil {
		return fmt.Errorf("attach issue to task: %w", err)
	}

	if _, err := qtx.UpdateBenchmarkTaskStatus(ctx, db.UpdateBenchmarkTaskStatusParams{
		ID:           task.ID,
		WorkspaceID:  run.WorkspaceID,
		Status:       "issued",
		StatusReason: "",
	}); err != nil {
		return fmt.Errorf("advance task to issued: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	if d.bus != nil {
		prefix := d.workspacePrefix(ctx, run.WorkspaceID)
		// Publish issue:created so the existing chain (subscribers, activity,
		// notifications) fires identically to a human-created issue.
		d.bus.Publish(events.Event{
			Type:        protocol.EventIssueCreated,
			WorkspaceID: util.UUIDToString(run.WorkspaceID),
			ActorType:   "member",
			ActorID:     util.UUIDToString(run.CreatedBy),
			Payload: map[string]any{
				"issue": issueToBenchmarkMap(issue, prefix),
			},
		})
		d.bus.Publish(events.Event{
			Type:        protocol.EventBenchmarkTaskStatus,
			WorkspaceID: util.UUIDToString(run.WorkspaceID),
			TaskID:      util.UUIDToString(task.ID),
			Payload: map[string]any{
				"run_id":      util.UUIDToString(run.ID),
				"task_id":     util.UUIDToString(task.ID),
				"instance_id": task.InstanceID,
				"status":      "issued",
				"issue_id":    util.UUIDToString(issue.ID),
			},
		})
	}

	// Enqueue the agent task once the issue is committed. Mirrors the
	// autopilot create_issue path — the daemon claims by (agent, runtime)
	// and reads the issue body when it picks up the task.
	if d.tasks != nil {
		if _, err := d.tasks.EnqueueTaskForIssue(ctx, issue); err != nil {
			// Issue is already created and task is already 'issued'; a
			// failure here means the agent will not auto-pick the work
			// but the row state is still consistent. Log and continue.
			slog.Warn("benchmark.task_dispatcher.enqueue_failed",
				"issue_id", util.UUIDToString(issue.ID),
				"task_id", util.UUIDToString(task.ID),
				"err", err,
			)
		}
	}

	slog.Info("benchmark.task_dispatcher.issued",
		"run_id", util.UUIDToString(run.ID),
		"task_id", util.UUIDToString(task.ID),
		"instance_id", task.InstanceID,
		"issue_id", util.UUIDToString(issue.ID),
	)
	return nil
}

// OnSubmissionEvent is called for every comment:created event the listener
// extracts an attachment from. It looks up the benchmark_task by issue id
// (no-op for non-benchmark issues), validates the attachment via the
// adapter Parser, and on success advances the task to 'submitted',
// attaches the attachment, and (for managed mode) enqueues an eval_job.
// All inside a single transaction for the same partial-failure reasons
// as issueOneTask.
func (d *TaskDispatcher) OnSubmissionEvent(ctx context.Context, ev SubmissionEvent) error {
	if !ev.IssueID.Valid {
		return nil
	}
	task, err := d.q.GetBenchmarkTaskByIssue(ctx, ev.IssueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // not a benchmark issue
	}
	if err != nil {
		return fmt.Errorf("lookup task by issue: %w", err)
	}
	// Defense-in-depth: the issue-by-attachment lookup might cross workspaces
	// if a stale event slipped through; reject anything that does not match.
	if ev.WorkspaceID.Valid && task.WorkspaceID.Bytes != ev.WorkspaceID.Bytes {
		return nil
	}
	if task.Status != "issued" {
		// Already submitted (race with a duplicate event) or the run was
		// canceled. Either way silent no-op.
		return nil
	}

	run, err := d.q.GetBenchmarkRun(ctx, db.GetBenchmarkRunParams{
		ID:          task.RunID,
		WorkspaceID: task.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}
	suite, err := d.q.GetBenchmarkSuite(ctx, db.GetBenchmarkSuiteParams{
		ID:          run.SuiteID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return fmt.Errorf("get suite: %w", err)
	}
	parser, ok := d.registry.Parser(suite.AdapterKind)
	if !ok {
		return fmt.Errorf("parser not registered: %s", suite.AdapterKind)
	}
	if err := parser.Validate(ctx, adapter.Attachment{
		Filename:  ev.Filename,
		MimeType:  ev.MimeType,
		SizeBytes: ev.SizeBytes,
	}); err != nil {
		// Not a valid submission — agent may try again with the right
		// filename. We do NOT advance the task.
		slog.Debug("benchmark.task_dispatcher.attachment_rejected",
			"task_id", util.UUIDToString(task.ID),
			"filename", ev.Filename,
			"reason", err.Error(),
		)
		return nil
	}

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := d.q.WithTx(tx)

	if ev.AttachmentID.Valid {
		if err := qtx.AttachAttachmentToTask(ctx, db.AttachAttachmentToTaskParams{
			ID:           task.ID,
			WorkspaceID:  task.WorkspaceID,
			AttachmentID: ev.AttachmentID,
		}); err != nil {
			return fmt.Errorf("attach attachment to task: %w", err)
		}
	}

	if _, err := qtx.UpdateBenchmarkTaskStatus(ctx, db.UpdateBenchmarkTaskStatusParams{
		ID:           task.ID,
		WorkspaceID:  task.WorkspaceID,
		Status:       "submitted",
		StatusReason: "",
	}); err != nil {
		return fmt.Errorf("advance task to submitted: %w", err)
	}

	if run.EvaluatorMode == "managed" {
		if _, err := qtx.CreateBenchmarkEvalJob(ctx, db.CreateBenchmarkEvalJobParams{
			TaskID:      task.ID,
			WorkspaceID: task.WorkspaceID,
			AdapterKind: suite.AdapterKind,
		}); err != nil {
			return fmt.Errorf("create eval job: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	if d.bus != nil {
		d.bus.Publish(events.Event{
			Type:        protocol.EventBenchmarkTaskStatus,
			WorkspaceID: util.UUIDToString(task.WorkspaceID),
			TaskID:      util.UUIDToString(task.ID),
			Payload: map[string]any{
				"run_id":      util.UUIDToString(task.RunID),
				"task_id":     util.UUIDToString(task.ID),
				"instance_id": task.InstanceID,
				"status":      "submitted",
			},
		})
	}

	slog.Info("benchmark.task_dispatcher.submitted",
		"run_id", util.UUIDToString(task.RunID),
		"task_id", util.UUIDToString(task.ID),
		"instance_id", task.InstanceID,
	)
	return nil
}

// workspacePrefix is a best-effort lookup for issue identifier formatting.
// Failure returns an empty prefix; the event still publishes — the prefix
// is only cosmetic for downstream UI.
func (d *TaskDispatcher) workspacePrefix(ctx context.Context, workspaceID pgtype.UUID) string {
	ws, err := d.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return ""
	}
	return ws.IssuePrefix
}

// extractSubmissionEvents pulls (issue_id, attachment) tuples out of a
// comment:created bus event. The handler.publish path stores the comment
// as a CommentResponse map under "comment"; attachments live on
// comment.attachments as []AttachmentResponse maps. We fish out the JSON
// shape rather than importing the handler package (cyclic) — the keys
// match the JSON tags on those types.
func extractSubmissionEvents(e events.Event) []SubmissionEvent {
	payload, ok := e.Payload.(map[string]any)
	if !ok {
		return nil
	}
	comment, ok := payload["comment"].(map[string]any)
	if !ok {
		return nil
	}
	issueIDStr, _ := comment["issue_id"].(string)
	if issueIDStr == "" {
		return nil
	}
	issueID, err := util.ParseUUID(issueIDStr)
	if err != nil {
		return nil
	}
	attsRaw, _ := comment["attachments"].([]any)
	if len(attsRaw) == 0 {
		// Some payloads carry typed slices; fall back to a loose check.
		return nil
	}
	wsID, _ := util.ParseUUID(e.WorkspaceID)
	out := make([]SubmissionEvent, 0, len(attsRaw))
	for _, a := range attsRaw {
		attMap, ok := a.(map[string]any)
		if !ok {
			continue
		}
		attID, _ := util.ParseUUID(stringField(attMap, "id"))
		size := int64Field(attMap, "size_bytes")
		out = append(out, SubmissionEvent{
			WorkspaceID:  wsID,
			IssueID:      issueID,
			AttachmentID: attID,
			Filename:     stringField(attMap, "filename"),
			MimeType:     stringField(attMap, "content_type"),
			SizeBytes:    size,
		})
	}
	return out
}

func stringField(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func int64Field(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

// issueToBenchmarkMap is a stripped-down sibling of service.issueToMap.
// We avoid importing the service package to keep the benchmark service
// tree free of upstream service deps; the fields downstream UI cares
// about are id/title/status/assignee, which we cover here.
func issueToBenchmarkMap(issue db.Issue, prefix string) map[string]any {
	identifier := ""
	if prefix != "" {
		identifier = fmt.Sprintf("%s-%d", prefix, issue.Number)
	}
	out := map[string]any{
		"id":            util.UUIDToString(issue.ID),
		"workspace_id":  util.UUIDToString(issue.WorkspaceID),
		"number":        issue.Number,
		"identifier":    identifier,
		"title":         issue.Title,
		"description":   util.TextToPtr(issue.Description),
		"status":        issue.Status,
		"priority":      issue.Priority,
		"assignee_type": util.TextToPtr(issue.AssigneeType),
		"assignee_id":   util.UUIDToPtr(issue.AssigneeID),
		"creator_type":  issue.CreatorType,
		"creator_id":    util.UUIDToString(issue.CreatorID),
		"created_at":    util.TimestampToString(issue.CreatedAt),
		"updated_at":    util.TimestampToString(issue.UpdatedAt),
		"origin_type":   util.TextToPtr(issue.OriginType),
		"origin_id":     util.UUIDToPtr(issue.OriginID),
	}
	return out
}
