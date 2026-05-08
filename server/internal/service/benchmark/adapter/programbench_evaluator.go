package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const programBenchEvalTimeout = 30 * time.Minute

// ProgramBenchEvaluator runs `uvx programbench eval <runRoot>` against a
// submission archive and parses the resulting `<instance_id>.eval.json`.
//
// runArgs is overridable for tests. In production it shells out to uvx with
// a 30-minute hard timeout.
type ProgramBenchEvaluator struct {
	runArgs func(ctx context.Context, dir string, args ...string) ([]byte, error)
}

func NewProgramBenchEvaluator() *ProgramBenchEvaluator {
	return &ProgramBenchEvaluator{
		runArgs: func(ctx context.Context, dir string, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, programBenchEvalTimeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, args[0], args[1:]...)
			cmd.Dir = dir
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			if err := cmd.Run(); err != nil {
				return out.Bytes(), fmt.Errorf("uvx programbench eval: %w (output=%s)", err, strings.TrimSpace(out.String()))
			}
			return out.Bytes(), nil
		},
	}
}

func (e *ProgramBenchEvaluator) Kind() string { return programBenchKind }

func (e *ProgramBenchEvaluator) Evaluate(ctx context.Context, in EvaluateInput) (EvaluateOutput, error) {
	if in.SubmissionPath == "" {
		return EvaluateOutput{}, errors.New("submission path required")
	}
	if in.WorkDir == "" {
		return EvaluateOutput{}, errors.New("work dir required")
	}
	if in.Task.InstanceID == "" {
		return EvaluateOutput{}, errors.New("instance id required")
	}

	runRoot := filepath.Join(in.WorkDir, "run")
	instDir := filepath.Join(runRoot, in.Task.InstanceID)
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		return EvaluateOutput{}, fmt.Errorf("create run dir: %w", err)
	}
	targetSubmission := filepath.Join(instDir, "submission.tar.gz")
	if err := copyFile(in.SubmissionPath, targetSubmission); err != nil {
		return EvaluateOutput{}, fmt.Errorf("copy submission: %w", err)
	}

	if _, err := e.runArgs(ctx, in.WorkDir, "uvx", "programbench", "eval", runRoot); err != nil {
		return EvaluateOutput{}, err
	}

	evalPath := filepath.Join(instDir, in.Task.InstanceID+".eval.json")
	raw, err := os.ReadFile(evalPath)
	if err != nil {
		return EvaluateOutput{}, fmt.Errorf("read eval json: %w", err)
	}
	return parseProgramBenchEval(raw, in.Task.InstanceID)
}

// parseProgramBenchEval is a v1 minimal parser.
// ProgramBench eval JSON shape (best-effort):
//
//	{
//	  "instance_id": "...",
//	  "tests": [{"name": "category/whatever", "passed": true, "category": "compile_error"} ...],
//	  "resolved": true|false,
//	  "passed_tests": int,  // optional aggregate
//	  "total_tests": int,   // optional aggregate
//	  "ignored_tests": [...] // optional
//	}
//
// Strategy: prefer top-level aggregates if present; otherwise count tests[].
// For failed_categories, collect category strings of non-passing tests; if
// the JSON's tests don't carry category, fall back to the prefix before "/"
// in name.
func parseProgramBenchEval(raw []byte, instanceID string) (EvaluateOutput, error) {
	var blob struct {
		Resolved    *bool `json:"resolved"`
		PassedTests *int  `json:"passed_tests"`
		TotalTests  *int  `json:"total_tests"`
		Tests       []struct {
			Name     string `json:"name"`
			Passed   bool   `json:"passed"`
			Category string `json:"category"`
		} `json:"tests"`
	}
	if err := json.Unmarshal(raw, &blob); err != nil {
		return EvaluateOutput{}, fmt.Errorf("decode eval json: %w", err)
	}
	out := EvaluateOutput{RawEvalJSON: append(json.RawMessage(nil), raw...)}

	if blob.PassedTests != nil {
		out.PassedTests = *blob.PassedTests
	}
	if blob.TotalTests != nil {
		out.TotalTests = *blob.TotalTests
	}
	if blob.PassedTests == nil || blob.TotalTests == nil {
		// Compute from tests[] if aggregates missing.
		for _, t := range blob.Tests {
			if blob.TotalTests == nil {
				out.TotalTests++
			}
			if t.Passed && blob.PassedTests == nil {
				out.PassedTests++
			}
		}
	}
	if blob.Resolved != nil {
		out.Resolved = *blob.Resolved
	} else {
		out.Resolved = out.TotalTests > 0 && out.PassedTests == out.TotalTests
	}

	// Failure categories: dedupe and collect from non-passing tests.
	seen := map[string]struct{}{}
	cats := []string{}
	for _, t := range blob.Tests {
		if t.Passed {
			continue
		}
		cat := t.Category
		if cat == "" {
			cat = strings.SplitN(t.Name, "/", 2)[0] // fallback: prefix before /
		}
		if cat == "" {
			continue
		}
		if _, dup := seen[cat]; dup {
			continue
		}
		seen[cat] = struct{}{}
		cats = append(cats, cat)
	}
	out.FailedCategories = cats
	return out, nil
}

func copyFile(src, dst string) error {
	sf, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sf.Close()
	df, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer df.Close()
	_, err = io.Copy(df, sf)
	return err
}
