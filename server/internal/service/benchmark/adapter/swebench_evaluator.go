package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const sweBenchEvalTimeout = 30 * time.Minute

// SWEBenchEvaluator runs SWE-bench's harness against a solution.patch and
// parses the resulting report.json. Mirrors the structure of
// ProgramBenchEvaluator: runArgs is overridable for tests.
type SWEBenchEvaluator struct {
	runArgs func(ctx context.Context, dir string, args ...string) ([]byte, error)
}

func NewSWEBenchEvaluator() *SWEBenchEvaluator {
	return &SWEBenchEvaluator{
		runArgs: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, sweBenchEvalTimeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, args[0], args[1:]...)
			cmd.Dir = dir
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			if err := cmd.Run(); err != nil {
				return out.Bytes(), fmt.Errorf("uvx swe-bench eval: %w (output=%s)", err, strings.TrimSpace(out.String()))
			}
			return out.Bytes(), nil
		},
	}
}

func (e *SWEBenchEvaluator) Kind() string { return sweBenchKind }

func (e *SWEBenchEvaluator) Evaluate(ctx context.Context, in EvaluateInput) (EvaluateOutput, error) {
	if in.SubmissionPath == "" {
		return EvaluateOutput{}, errors.New("submission path required")
	}
	if in.WorkDir == "" {
		return EvaluateOutput{}, errors.New("work dir required")
	}
	if in.Task.InstanceID == "" {
		return EvaluateOutput{}, errors.New("instance id required")
	}

	reportPath := filepath.Join(in.WorkDir, "report.json")
	if _, err := e.runArgs(ctx, in.WorkDir,
		"uvx", "--from", "swebench", "python", "-m", "swebench.harness",
		"--instance", in.Task.InstanceID,
		"--patch", in.SubmissionPath,
		"--report", reportPath,
	); err != nil {
		return EvaluateOutput{}, err
	}

	raw, err := os.ReadFile(reportPath)
	if err != nil {
		return EvaluateOutput{}, fmt.Errorf("read report: %w", err)
	}
	return parseSWEBenchReport(raw, in.Task.InstanceID)
}

// parseSWEBenchReport reads SWE-bench harness reports of shape:
//
//	{"resolved": bool, "passed": [...], "failed": [...], "errors": [...]}
//
// and translates them to EvaluateOutput. FailedCategories is a deduped, ordered
// set of {"tests_failed", "harness_error"} based on which buckets are non-empty.
func parseSWEBenchReport(raw []byte, instanceID string) (EvaluateOutput, error) {
	var blob struct {
		Resolved bool     `json:"resolved"`
		Passed   []string `json:"passed"`
		Failed   []string `json:"failed"`
		Errors   []string `json:"errors"`
	}
	if err := json.Unmarshal(raw, &blob); err != nil {
		return EvaluateOutput{}, fmt.Errorf("decode swebench report: %w", err)
	}
	out := EvaluateOutput{
		RawEvalJSON: append(json.RawMessage(nil), raw...),
		Resolved:    blob.Resolved,
		PassedTests: len(blob.Passed),
		TotalTests:  len(blob.Passed) + len(blob.Failed) + len(blob.Errors),
	}
	cats := []string{}
	if len(blob.Failed) > 0 {
		cats = append(cats, "tests_failed")
	}
	if len(blob.Errors) > 0 {
		cats = append(cats, "harness_error")
	}
	out.FailedCategories = cats
	return out, nil
}
