package benchmark

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

func TestParseADOURL(t *testing.T) {
	cases := []struct {
		url     string
		want    *adoURLParts
		wantNil bool
	}{
		{
			url:  "https://dev.azure.com/myorg/myproj/_git/myrepo/pullrequest/42",
			want: &adoURLParts{org: "myorg", project: "myproj", repo: "myrepo", prID: "42"},
		},
		{
			url:  "https://contoso.visualstudio.com/proj/_git/repo/pullrequest/7",
			want: &adoURLParts{org: "contoso", project: "proj", repo: "repo", prID: "7"},
		},
		{url: "https://github.com/foo/bar/pull/1", wantNil: true},
		{url: "https://dev.azure.com/org/proj/_git/repo/branches", wantNil: true},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.url)
		got := parseADOURL(u)
		if c.wantNil {
			require.Nil(t, got, c.url)
			continue
		}
		require.NotNil(t, got, c.url)
		require.Equal(t, *c.want, *got, c.url)
	}
}

func TestReferenceFetcher_AzureDevOpsPR_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/pullrequests/42"):
			require.Equal(t, "Basic OmZha2VfcGF0", r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
                "lastMergeSourceCommit": {"commitId": "src111"},
                "lastMergeTargetCommit": {"commitId": "tgt000"}
            }`))
		case strings.Contains(r.URL.Path, "/diffs/commits"):
			require.Equal(t, "Basic OmZha2VfcGF0", r.Header.Get("Authorization"))
			require.Equal(t, "tgt000", r.URL.Query().Get("baseVersion"))
			require.Equal(t, "src111", r.URL.Query().Get("targetVersion"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
                "changes": [
                    {"item":{"path":"/foo.go"},"changeType":"edit"},
                    {"item":{"path":"/new.go"},"changeType":"add"},
                    {"item":{"path":"/old.go"},"changeType":"delete"}
                ]
            }`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	f := NewReferenceFetcher("fake_pat", "")
	f.SetADOBaseURL(srv.URL)
	f.httpClient = srv.Client()

	out, err := f.FetchPatch(context.Background(),
		"https://dev.azure.com/myorg/myproj/_git/myrepo/pullrequest/42")
	require.NoError(t, err)
	require.Contains(t, out.Patch, "foo.go")
	require.Contains(t, out.Patch, "new.go")
	require.Contains(t, out.Patch, "old.go")
	require.Contains(t, out.Patch, "/dev/null") // for add and delete
	require.Contains(t, out.Patch, "base=tgt000 target=src111 files=3")
	require.Equal(t,
		"https://dev.azure.com/myorg/myproj/_git/myrepo/pullrequest/42",
		out.SourceURL)
}

func TestReferenceFetcher_ADO_NoToken(t *testing.T) {
	f := NewReferenceFetcher("", "")
	_, err := f.FetchPatch(context.Background(),
		"https://dev.azure.com/o/p/_git/r/pullrequest/1")
	require.ErrorIs(t, err, ErrReferenceFetchFailed)
	require.Contains(t, err.Error(), "ADO token")
}

func TestReferenceFetcher_ADO_PRFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	f := NewReferenceFetcher("fake_pat", "")
	f.SetADOBaseURL(srv.URL)
	f.httpClient = srv.Client()

	_, err := f.FetchPatch(context.Background(),
		"https://dev.azure.com/o/p/_git/r/pullrequest/1")
	require.ErrorIs(t, err, ErrReferenceFetchFailed)
}

func TestReferenceFetcher_ADO_PRNotMerged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`)) // missing commit IDs
	}))
	defer srv.Close()

	f := NewReferenceFetcher("fake_pat", "")
	f.SetADOBaseURL(srv.URL)
	f.httpClient = srv.Client()

	_, err := f.FetchPatch(context.Background(),
		"https://dev.azure.com/o/p/_git/r/pullrequest/1")
	require.ErrorIs(t, err, ErrReferenceFetchFailed)
	require.Contains(t, err.Error(), "merge commits")
}
