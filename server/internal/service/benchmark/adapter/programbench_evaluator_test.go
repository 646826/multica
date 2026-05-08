package adapter

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseProgramBenchEval_AggregatesPresent(t *testing.T) {
	raw := []byte(`{"resolved":true,"passed_tests":5,"total_tests":5,"tests":[]}`)
	out, err := parseProgramBenchEval(raw, "x")
	require.NoError(t, err)
	require.True(t, out.Resolved)
	require.Equal(t, 5, out.PassedTests)
	require.Equal(t, 5, out.TotalTests)
	require.Empty(t, out.FailedCategories)
}

func TestParseProgramBenchEval_AggregatesMissing_ComputesFromTests(t *testing.T) {
	raw := []byte(`{"tests":[
        {"name":"compile/test1","passed":true},
        {"name":"runtime/crash_test","passed":false,"category":"runtime_crash"},
        {"name":"output/diff","passed":false,"category":"wrong_output"}
    ]}`)
	out, err := parseProgramBenchEval(raw, "x")
	require.NoError(t, err)
	require.False(t, out.Resolved)
	require.Equal(t, 1, out.PassedTests)
	require.Equal(t, 3, out.TotalTests)
	require.ElementsMatch(t, []string{"runtime_crash", "wrong_output"}, out.FailedCategories)
}

func TestParseProgramBenchEval_FallbackCategoryFromNamePrefix(t *testing.T) {
	raw := []byte(`{"tests":[{"name":"compile/foo","passed":false}]}`)
	out, err := parseProgramBenchEval(raw, "x")
	require.NoError(t, err)
	require.Equal(t, []string{"compile"}, out.FailedCategories)
}

func TestProgramBenchEvaluator_Kind(t *testing.T) {
	require.Equal(t, "programbench", NewProgramBenchEvaluator().Kind())
}

func TestProgramBenchEvaluator_Evaluate_StubbedRun(t *testing.T) {
	work := t.TempDir()
	sub := filepath.Join(work, "input.tar.gz")
	require.NoError(t, os.WriteFile(sub, []byte("fake"), 0o644))

	e := NewProgramBenchEvaluator()
	e.runArgs = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		// Verify args order: uvx programbench eval <runRoot>
		require.Equal(t, "uvx", args[0])
		require.Equal(t, "programbench", args[1])
		require.Equal(t, "eval", args[2])
		runRoot := args[3]

		// Verify the submission was laid out where ProgramBench expects it.
		instDir := filepath.Join(runRoot, "abc__def.cafe")
		copied, err := os.ReadFile(filepath.Join(instDir, "submission.tar.gz"))
		require.NoError(t, err)
		require.Equal(t, []byte("fake"), copied)

		// Write a fake eval json the parser will consume.
		require.NoError(t, os.WriteFile(filepath.Join(instDir, "abc__def.cafe.eval.json"),
			[]byte(`{"resolved":true,"passed_tests":3,"total_tests":3,"tests":[]}`), 0o644))
		return []byte("ok"), nil
	}

	out, err := e.Evaluate(context.Background(), EvaluateInput{
		Task:           TaskRef{InstanceID: "abc__def.cafe"},
		SubmissionPath: sub,
		WorkDir:        work,
	})
	require.NoError(t, err)
	require.True(t, out.Resolved)
	require.Equal(t, 3, out.TotalTests)
	require.Equal(t, 3, out.PassedTests)
	require.NotEmpty(t, out.RawEvalJSON)
}

func TestProgramBenchEvaluator_Evaluate_RequiredFields(t *testing.T) {
	e := NewProgramBenchEvaluator()
	_, err := e.Evaluate(context.Background(), EvaluateInput{
		Task: TaskRef{InstanceID: "x"}, WorkDir: "/tmp",
	})
	require.Error(t, err)

	_, err = e.Evaluate(context.Background(), EvaluateInput{
		Task: TaskRef{InstanceID: "x"}, SubmissionPath: "/tmp/s",
	})
	require.Error(t, err)

	_, err = e.Evaluate(context.Background(), EvaluateInput{
		SubmissionPath: "/tmp/s", WorkDir: "/tmp",
	})
	require.Error(t, err)
}

func TestProgramBenchEvaluator_Evaluate_RunFailurePropagates(t *testing.T) {
	work := t.TempDir()
	sub := filepath.Join(work, "input.tar.gz")
	require.NoError(t, os.WriteFile(sub, []byte("x"), 0o644))

	e := NewProgramBenchEvaluator()
	e.runArgs = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		return nil, context.Canceled
	}
	_, err := e.Evaluate(context.Background(), EvaluateInput{
		Task: TaskRef{InstanceID: "iid"}, SubmissionPath: sub, WorkDir: work,
	})
	require.Error(t, err)
}
