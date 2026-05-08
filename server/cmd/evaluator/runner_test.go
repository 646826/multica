package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/service/benchmark/adapter"
)

// TestRunner_Run_HappyPath stubs the multica server with a small httptest handler
// and exercises Run() against a stubbed Evaluator.
func TestRunner_Run_HappyPath(t *testing.T) {
	var completed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/attachments/abc/download":
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write([]byte("fake-tarball"))
		case r.URL.Path == "/api/internal/eval-jobs/job-1/complete":
			require.Equal(t, http.MethodPost, r.Method)
			w.WriteHeader(http.StatusNoContent)
			completed = true
		case r.URL.Path == "/api/internal/eval-jobs/job-1/fail":
			t.Fatalf("unexpected fail call")
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	work := t.TempDir()
	client := NewClient(srv.URL, "evp_test")

	fakeEval := &fakeEvaluator{}
	reg := adapter.NewRegistry()
	reg.RegisterEvaluator(fakeEval)

	runner := NewRunner(client, reg, work)
	runner.Run(context.Background(), ClaimedJob{
		JobID:                 "job-1",
		TaskID:                "task-1",
		InstanceID:            "abc__def.cafe",
		AdapterKind:           "programbench",
		SubmissionDownloadURL: "/api/attachments/abc/download",
		InstanceMeta:          json.RawMessage(`{}`),
	})

	require.True(t, completed)
	// submission file existed during eval; cleaned up after run
	_, err := os.Stat(filepath.Join(work, "job-1"))
	require.True(t, os.IsNotExist(err))
}

type fakeEvaluator struct{}

func (f *fakeEvaluator) Kind() string { return "programbench" }
func (f *fakeEvaluator) Evaluate(ctx context.Context, in adapter.EvaluateInput) (adapter.EvaluateOutput, error) {
	return adapter.EvaluateOutput{
		RawEvalJSON:      json.RawMessage(`{"resolved":true}`),
		Resolved:         true,
		PassedTests:      5,
		TotalTests:       5,
		FailedCategories: []string{},
	}, nil
}
