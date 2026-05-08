package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"

	"github.com/multica-ai/multica/server/internal/service/benchmark/adapter"
)

type Runner struct {
	client   *Client
	registry *adapter.Registry
	workRoot string
}

func NewRunner(client *Client, registry *adapter.Registry, workRoot string) *Runner {
	return &Runner{client: client, registry: registry, workRoot: workRoot}
}

// Run executes one job. On any error during eval, posts /fail; on success, /complete.
func (r *Runner) Run(ctx context.Context, job ClaimedJob) {
	workDir := filepath.Join(r.workRoot, job.JobID)
	defer os.RemoveAll(workDir)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		r.fail(ctx, job.JobID, fmt.Sprintf("mkdir work dir: %v", err))
		return
	}

	if job.SubmissionDownloadURL == "" {
		r.fail(ctx, job.JobID, "submission download url not set on job")
		return
	}
	submissionPath := filepath.Join(workDir, "submission.tar.gz")
	if err := r.client.DownloadSubmission(ctx, job.SubmissionDownloadURL, submissionPath); err != nil {
		r.fail(ctx, job.JobID, fmt.Sprintf("download submission: %v", err))
		return
	}

	ev, ok := r.registry.Evaluator(job.AdapterKind)
	if !ok {
		r.fail(ctx, job.JobID, fmt.Sprintf("evaluator not registered for kind %q", job.AdapterKind))
		return
	}

	var meta json.RawMessage = job.InstanceMeta
	out, err := ev.Evaluate(ctx, adapter.EvaluateInput{
		Task:           adapter.TaskRef{InstanceID: job.InstanceID},
		Instance:       adapter.Instance{ID: job.InstanceID, Meta: meta},
		SubmissionPath: submissionPath,
		WorkDir:        workDir,
	})
	if err != nil {
		r.fail(ctx, job.JobID, err.Error())
		return
	}

	passRate := 0.0
	if out.TotalTests > 0 {
		passRate = float64(out.PassedTests) / float64(out.TotalTests)
		if math.IsNaN(passRate) || math.IsInf(passRate, 0) {
			passRate = 0
		}
	}

	if err := r.client.Complete(ctx, job.JobID, CompleteRequest{
		Resolved:         out.Resolved,
		PassedTests:      out.PassedTests,
		TotalTests:       out.TotalTests,
		PassRate:         passRate,
		RawEvalJSON:      out.RawEvalJSON,
		FailedCategories: out.FailedCategories,
	}); err != nil {
		slog.Error("evaluator.complete_failed", "job_id", job.JobID, "err", err)
	}
}

func (r *Runner) fail(ctx context.Context, jobID, lastErr string) {
	if err := r.client.Fail(ctx, jobID, lastErr); err != nil {
		slog.Error("evaluator.fail_call_failed", "job_id", jobID, "err", err, "last_error", lastErr)
	}
}
