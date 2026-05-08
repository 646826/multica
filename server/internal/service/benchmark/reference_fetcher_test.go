package benchmark

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReferenceFetcher_GitHubPR(t *testing.T) {
	received := make(chan *http.Request, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r
		_, _ = io.WriteString(w, "diff --git a/x b/x\n@@ -1 +1 @@\n-old\n+new\n")
	}))
	defer srv.Close()

	f := NewReferenceFetcher("", "ghp_test")
	f.httpClient = srv.Client()

	// Override URL detection: we'd need a real github.com URL for isGitHubPR
	// to fire. Instead, exercise the do() path via fetchPlain on the test server.
	out, err := f.fetchPlain(context.Background(), srv.URL+"/x.diff")
	require.NoError(t, err)
	require.Contains(t, out.Patch, "+new")

	select {
	case r := <-received:
		// For plain fetch we don't set GitHub token; that's expected.
		_ = r
	default:
		t.Fatal("server did not receive request")
	}
}

func TestReferenceFetcher_PlainURL_Suffix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "patch contents\n")
	}))
	defer srv.Close()

	f := NewReferenceFetcher("", "")
	f.httpClient = srv.Client()
	out, err := f.FetchPatch(context.Background(), srv.URL+"/some.patch")
	require.NoError(t, err)
	require.Equal(t, "patch contents\n", out.Patch)
}

func TestReferenceFetcher_UnsupportedURL(t *testing.T) {
	f := NewReferenceFetcher("", "")
	_, err := f.FetchPatch(context.Background(), "https://example.com/random/page")
	require.ErrorIs(t, err, ErrUnsupportedReferenceURL)
}

func TestReferenceFetcher_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	f := NewReferenceFetcher("", "")
	f.httpClient = srv.Client()
	_, err := f.FetchPatch(context.Background(), srv.URL+"/missing.diff")
	require.ErrorIs(t, err, ErrReferenceFetchFailed)
}

func TestIsGitHubPR(t *testing.T) {
	cases := []struct {
		url   string
		match bool
	}{
		{"https://github.com/foo/bar/pull/1", true},
		{"https://github.com/foo/bar/pull/123/", true},
		{"https://github.com/foo/bar/issues/1", false},
		{"https://example.com/foo/bar/pull/1", false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.url)
		require.Equal(t, c.match, isGitHubPR(u), c.url)
	}
}

func TestIsAzureDevOpsPR(t *testing.T) {
	cases := []struct {
		url   string
		match bool
	}{
		{"https://dev.azure.com/org/proj/_git/repo/pullrequest/42", true},
		{"https://contoso.visualstudio.com/proj/_git/repo/pullrequest/42", true},
		{"https://dev.azure.com/org/proj/_git/repo/branches", false},
		{"https://github.com/foo/bar/pull/1", false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.url)
		require.Equal(t, c.match, isAzureDevOpsPR(u), c.url)
	}
}

func TestADOBasicAuth(t *testing.T) {
	require.Equal(t, "OnNlY3JldA==", adoBasicAuth("secret"))
}
