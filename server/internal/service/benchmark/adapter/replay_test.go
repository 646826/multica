package adapter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReplayParser_AcceptsValidSubmission(t *testing.T) {
	p := NewReplayParser()
	require.NoError(t, p.Validate(context.Background(), Attachment{
		Filename: "solution.patch", SizeBytes: 1024,
	}))
	require.NoError(t, p.Validate(context.Background(), Attachment{
		Filename: "solution.diff", SizeBytes: 1024,
	}))
}

func TestReplayParser_RejectsBadFilename(t *testing.T) {
	p := NewReplayParser()
	require.Error(t, p.Validate(context.Background(), Attachment{
		Filename: "solution.tar.gz", SizeBytes: 1024,
	}))
}

func TestReplayParser_RejectsOversize(t *testing.T) {
	p := NewReplayParser()
	require.Error(t, p.Validate(context.Background(), Attachment{
		Filename: "solution.patch", SizeBytes: 11 * 1024 * 1024,
	}))
}

func TestJaccardLines_Identical(t *testing.T) {
	s := "line one\nline two\nline three\n"
	require.InDelta(t, 1.0, jaccardLines(s, s), 1e-9)
}

func TestJaccardLines_Disjoint(t *testing.T) {
	a := "alpha\nbeta\n"
	b := "gamma\ndelta\n"
	require.InDelta(t, 0.0, jaccardLines(a, b), 1e-9)
}

func TestJaccardLines_IgnoresDiffMetadata(t *testing.T) {
	a := "diff --git a/x b/x\n@@ -1,3 +1,3 @@\n+new\n-old\n context\n"
	b := "diff --git a/y b/y\n@@ -10,3 +10,3 @@\n+new\n-old\n context\n"
	// Different file headers and hunk lines, but content is identical.
	require.InDelta(t, 1.0, jaccardLines(a, b), 1e-9)
}

func TestJaccardLines_PartialOverlap(t *testing.T) {
	a := "a\nb\nc\nd\n"
	b := "b\nc\ne\nf\n"
	// intersection {b,c}=2, union {a,b,c,d,e,f}=6, jaccard=2/6=0.333...
	require.InDelta(t, 1.0/3.0, jaccardLines(a, b), 1e-6)
}

func TestReplayEvaluator_ResolvedAtThreshold(t *testing.T) {
	ev := NewReplayEvaluator()
	work := t.TempDir()
	sub := filepath.Join(work, "submission.patch")
	require.NoError(t, os.WriteFile(sub, []byte("line a\nline b\nline c\n"), 0o644))

	metaJSON, _ := json.Marshal(ReplayInstanceMeta{
		SourceIssueID:     "x",
		ReferenceSolution: "line a\nline b\nline c\n", // identical → 1.0
	})
	out, err := ev.Evaluate(context.Background(), EvaluateInput{
		SubmissionPath: sub,
		WorkDir:        work,
		Instance:       Instance{Meta: metaJSON},
	})
	require.NoError(t, err)
	require.True(t, out.Resolved)
	require.Equal(t, 1000, out.PassedTests)
	require.Equal(t, 1000, out.TotalTests)
}

func TestReplayEvaluator_BelowThresholdNotResolved(t *testing.T) {
	ev := NewReplayEvaluator()
	work := t.TempDir()
	sub := filepath.Join(work, "submission.patch")
	require.NoError(t, os.WriteFile(sub, []byte("totally different\n"), 0o644))

	metaJSON, _ := json.Marshal(ReplayInstanceMeta{
		ReferenceSolution: "line a\nline b\n",
	})
	out, err := ev.Evaluate(context.Background(), EvaluateInput{
		SubmissionPath: sub,
		Instance:       Instance{Meta: metaJSON},
	})
	require.NoError(t, err)
	require.False(t, out.Resolved)
	require.NotEmpty(t, out.FailedCategories)
}

func TestReplayEvaluator_RejectsMissingReference(t *testing.T) {
	ev := NewReplayEvaluator()
	work := t.TempDir()
	sub := filepath.Join(work, "submission.patch")
	require.NoError(t, os.WriteFile(sub, []byte("anything"), 0o644))

	metaJSON, _ := json.Marshal(ReplayInstanceMeta{}) // empty ref
	_, err := ev.Evaluate(context.Background(), EvaluateInput{
		SubmissionPath: sub, Instance: Instance{Meta: metaJSON},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "reference_solution missing")
}

func TestReplayComposer_BuildsTaskDescription(t *testing.T) {
	metaJSON, _ := json.Marshal(ReplayInstanceMeta{
		SourceIssueNumber:      42,
		SourceIssueTitle:       "Fix the foo bar",
		SourceIssueDescription: "When user clicks foo, the bar widget should bar.",
	})
	c := NewReplayComposer()
	out, err := c.Compose(context.Background(), ComposeInput{
		Run:      RunRef{DisplayName: "smoke-replay"},
		Task:     TaskRef{InstanceID: "multica-issue:abc"},
		Instance: Instance{Meta: metaJSON},
	})
	require.NoError(t, err)
	require.Contains(t, out.Title, "Fix the foo bar")
	require.Contains(t, out.Description, "issue #42")
	require.Contains(t, out.Description, "When user clicks foo")
	require.Contains(t, out.Description, "solution.patch")
	require.Equal(t, "solution.patch", out.SubmissionFilename)
}

func TestParseReplayInstanceID(t *testing.T) {
	valid := "multica-issue:550e8400-e29b-41d4-a716-446655440000"
	_, err := parseReplayInstanceID(valid)
	require.NoError(t, err)

	_, err = parseReplayInstanceID("not-a-replay-id")
	require.Error(t, err)

	_, err = parseReplayInstanceID("multica-issue:not-a-uuid")
	require.Error(t, err)
}

func TestCategorize(t *testing.T) {
	require.Equal(t, "diff_unrelated", categorize(0.05))
	require.Equal(t, "diff_partial", categorize(0.30))
	require.Equal(t, "diff_close_but_below_threshold", categorize(0.80))
	require.Equal(t, "diff_match", categorize(0.95))
}
