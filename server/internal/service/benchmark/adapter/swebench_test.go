package adapter

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSWEBenchCatalog_Kind(t *testing.T) {
	require.Equal(t, "swe_bench", NewSWEBenchCatalog().Kind())
}

func TestSWEBenchComposer_Kind(t *testing.T) {
	require.Equal(t, "swe_bench", NewSWEBenchComposer().Kind())
}

func TestSWEBenchParser_Kind(t *testing.T) {
	require.Equal(t, "swe_bench", NewSWEBenchParser().Kind())
}

func TestSWEBenchComposer_Compose(t *testing.T) {
	c := NewSWEBenchComposer()
	out, err := c.Compose(context.Background(), ComposeInput{
		Run:      RunRef{DisplayName: "smoke-1"},
		Task:     TaskRef{InstanceID: "django__django-1234"},
		Instance: Instance{ID: "django__django-1234", Language: "python", Difficulty: "medium"},
	})
	require.NoError(t, err)
	require.Equal(t, "solution.patch", out.SubmissionFilename)
	require.Equal(t, "SWEBenchSolver", out.AssigneeAgentName)
	require.Contains(t, out.Description, "django__django-1234")
	require.Contains(t, out.Description, "solution.patch")
	require.Contains(t, strings.ToLower(out.Description), "git apply")
	require.Contains(t, strings.ToLower(out.Title), "smoke-1")
	require.Contains(t, out.Title, "django__django-1234")
}

func TestSWEBenchParser_Validate(t *testing.T) {
	p := NewSWEBenchParser()

	require.NoError(t, p.Validate(context.Background(), Attachment{
		Filename: "solution.patch", MimeType: "text/x-patch", SizeBytes: 1024,
	}))

	err := p.Validate(context.Background(), Attachment{
		Filename: "solution.diff", MimeType: "text/x-patch", SizeBytes: 1024,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "solution.patch")

	err = p.Validate(context.Background(), Attachment{
		Filename: "solution.patch", MimeType: "text/x-patch", SizeBytes: 6 * 1024 * 1024,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "too large")
}

func TestSWEBenchCatalog_List_NotImplemented(t *testing.T) {
	c := NewSWEBenchCatalog()
	_, err := c.List(context.Background(), ListFilter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not implemented")
}

func TestSWEBenchEvaluator_Kind(t *testing.T) {
	require.Equal(t, "swe_bench", NewSWEBenchEvaluator().Kind())
}

func TestParseSWEBenchReport_Resolved(t *testing.T) {
	raw := []byte(`{"resolved":true,"passed":["t1","t2"],"failed":[],"errors":[]}`)
	out, err := parseSWEBenchReport(raw, "x")
	require.NoError(t, err)
	require.True(t, out.Resolved)
	require.Equal(t, 2, out.PassedTests)
	require.Equal(t, 2, out.TotalTests)
	require.Empty(t, out.FailedCategories)
	require.NotEmpty(t, out.RawEvalJSON)
}

func TestParseSWEBenchReport_FailedCategories(t *testing.T) {
	raw := []byte(`{"resolved":false,"passed":["t1"],"failed":["t2"],"errors":["t3"]}`)
	out, err := parseSWEBenchReport(raw, "x")
	require.NoError(t, err)
	require.False(t, out.Resolved)
	require.Equal(t, 1, out.PassedTests)
	require.Equal(t, 3, out.TotalTests)
	require.ElementsMatch(t, []string{"tests_failed", "harness_error"}, out.FailedCategories)
}

func TestParseSWEBenchReport_Malformed(t *testing.T) {
	_, err := parseSWEBenchReport([]byte(`not json`), "x")
	require.Error(t, err)
}

func TestSWEBenchEvaluator_Evaluate_RequiredFields(t *testing.T) {
	e := NewSWEBenchEvaluator()
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
