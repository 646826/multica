package benchmark

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type ReferenceFetcher struct {
	httpClient  *http.Client
	adoToken    string
	githubToken string
	adoBaseURL  string // override for tests; "" → use https://dev.azure.com
}

func NewReferenceFetcher(adoToken, githubToken string) *ReferenceFetcher {
	return &ReferenceFetcher{
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		adoToken:    adoToken,
		githubToken: githubToken,
	}
}

// SetHTTPClient swaps the underlying *http.Client. Intended for tests in
// other packages (e.g. handler) that need to point the fetcher at an
// httptest.Server without exposing the field directly.
func (f *ReferenceFetcher) SetHTTPClient(c *http.Client) {
	f.httpClient = c
}

// SetADOBaseURL overrides the ADO base URL (default https://dev.azure.com).
// Intended for tests pointing the fetcher at an httptest.Server.
func (f *ReferenceFetcher) SetADOBaseURL(baseURL string) {
	f.adoBaseURL = baseURL
}

// FetchPatch detects the URL type and returns the unified-diff text.
//
// Supported:
//   - https://github.com/<owner>/<repo>/pull/<n>           -> fetches "<url>.diff"
//   - https://dev.azure.com/<org>/<project>/_git/<repo>/pullrequest/<n>
//   - https://<org>.visualstudio.com/<project>/_git/<repo>/pullrequest/<n>
//   - any URL ending in .patch or .diff (raw GET)
func (f *ReferenceFetcher) FetchPatch(ctx context.Context, prURL string) (FetchedPatch, error) {
	u, err := url.Parse(prURL)
	if err != nil {
		return FetchedPatch{}, fmt.Errorf("invalid url: %w", err)
	}

	if isGitHubPR(u) {
		return f.fetchGitHubPR(ctx, prURL)
	}
	if isAzureDevOpsPR(u) {
		return f.fetchAzureDevOpsPR(ctx, u)
	}
	if isPlainPatchURL(u) {
		return f.fetchPlain(ctx, prURL)
	}
	return FetchedPatch{}, ErrUnsupportedReferenceURL
}

type FetchedPatch struct {
	Patch     string `json:"patch"`
	SourceURL string `json:"source_url"`
}

var (
	ErrUnsupportedReferenceURL = errors.New("benchmark: unsupported reference url")
	ErrReferenceFetchFailed    = errors.New("benchmark: reference fetch failed")
)

func isGitHubPR(u *url.URL) bool {
	if !strings.HasSuffix(u.Host, "github.com") {
		return false
	}
	return regexp.MustCompile(`^/[^/]+/[^/]+/pull/\d+/?$`).MatchString(u.Path)
}

func isAzureDevOpsPR(u *url.URL) bool {
	host := strings.ToLower(u.Host)
	isADO := host == "dev.azure.com" || strings.HasSuffix(host, "visualstudio.com")
	if !isADO {
		return false
	}
	return regexp.MustCompile(`/_git/[^/]+/pullrequest/\d+/?$`).MatchString(u.Path)
}

func isPlainPatchURL(u *url.URL) bool {
	p := strings.ToLower(u.Path)
	return strings.HasSuffix(p, ".patch") || strings.HasSuffix(p, ".diff")
}

func (f *ReferenceFetcher) fetchGitHubPR(ctx context.Context, prURL string) (FetchedPatch, error) {
	diffURL := strings.TrimSuffix(prURL, "/") + ".diff"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, diffURL, nil)
	if err != nil {
		return FetchedPatch{}, err
	}
	if f.githubToken != "" {
		req.Header.Set("Authorization", "Bearer "+f.githubToken)
	}
	req.Header.Set("Accept", "application/vnd.github.v3.diff")
	return f.do(req, diffURL)
}

func (f *ReferenceFetcher) fetchAzureDevOpsPR(ctx context.Context, u *url.URL) (FetchedPatch, error) {
	if f.adoToken == "" {
		return FetchedPatch{}, fmt.Errorf("%w: ADO token not configured (set MULTICA_ADO_PAT)", ErrReferenceFetchFailed)
	}
	parts := parseADOURL(u)
	if parts == nil {
		return FetchedPatch{}, fmt.Errorf("%w: cannot parse ADO PR URL", ErrUnsupportedReferenceURL)
	}
	target, source, err := f.adoGetPR(ctx, parts)
	if err != nil {
		return FetchedPatch{}, err
	}
	diffs, err := f.adoGetCommitDiffs(ctx, parts, target, source)
	if err != nil {
		return FetchedPatch{}, err
	}
	return FetchedPatch{
		Patch:     buildSummaryPatch(diffs, target, source),
		SourceURL: u.String(),
	}, nil
}

type adoURLParts struct {
	org, project, repo string
	prID               string
}

// parseADOURL extracts org/project/repo/prID from an ADO PR URL.
// Supports:
//   - https://dev.azure.com/<org>/<project>/_git/<repo>/pullrequest/<id>
//   - https://<org>.visualstudio.com/<project>/_git/<repo>/pullrequest/<id>
func parseADOURL(u *url.URL) *adoURLParts {
	p := strings.Trim(u.Path, "/")
	segs := strings.Split(p, "/")
	host := strings.ToLower(u.Host)
	var org string
	var rest []string
	if host == "dev.azure.com" {
		// segs: [org, project, _git, repo, pullrequest, id, ...]
		if len(segs) < 6 || segs[2] != "_git" || segs[4] != "pullrequest" {
			return nil
		}
		org = segs[0]
		rest = segs[1:]
	} else if strings.HasSuffix(host, ".visualstudio.com") {
		// segs: [project, _git, repo, pullrequest, id, ...]
		if len(segs) < 5 || segs[1] != "_git" || segs[3] != "pullrequest" {
			return nil
		}
		org = strings.TrimSuffix(host, ".visualstudio.com")
		rest = segs
	} else {
		return nil
	}
	// rest is now [project, _git, repo, pullrequest, id, ...]
	return &adoURLParts{
		org:     org,
		project: rest[0],
		repo:    rest[2],
		prID:    rest[4],
	}
}

type adoPRResponse struct {
	LastMergeSourceCommit struct {
		CommitID string `json:"commitId"`
	} `json:"lastMergeSourceCommit"`
	LastMergeTargetCommit struct {
		CommitID string `json:"commitId"`
	} `json:"lastMergeTargetCommit"`
}

type adoCommitDiffsResponse struct {
	Changes []struct {
		Item struct {
			Path string `json:"path"`
		} `json:"item"`
		ChangeType string `json:"changeType"`
	} `json:"changes"`
}

func (f *ReferenceFetcher) adoBase() string {
	if f.adoBaseURL != "" {
		return strings.TrimRight(f.adoBaseURL, "/")
	}
	return "https://dev.azure.com"
}

// adoGetPR returns (targetCommit, sourceCommit, error).
func (f *ReferenceFetcher) adoGetPR(ctx context.Context, parts *adoURLParts) (string, string, error) {
	u := fmt.Sprintf("%s/%s/%s/_apis/git/repositories/%s/pullrequests/%s?api-version=7.0",
		f.adoBase(), parts.org, parts.project, parts.repo, parts.prID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrReferenceFetchFailed, err)
	}
	req.Header.Set("Authorization", "Basic "+adoBasicAuth(f.adoToken))
	req.Header.Set("Accept", "application/json")
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrReferenceFetchFailed, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("%w: ADO PR fetch %s (body=%s)", ErrReferenceFetchFailed, resp.Status, string(body))
	}
	var pr adoPRResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return "", "", fmt.Errorf("%w: decode ADO PR: %v", ErrReferenceFetchFailed, err)
	}
	if pr.LastMergeSourceCommit.CommitID == "" || pr.LastMergeTargetCommit.CommitID == "" {
		return "", "", fmt.Errorf("%w: PR has no merge commits (not yet merged?)", ErrReferenceFetchFailed)
	}
	return pr.LastMergeTargetCommit.CommitID, pr.LastMergeSourceCommit.CommitID, nil
}

func (f *ReferenceFetcher) adoGetCommitDiffs(ctx context.Context, parts *adoURLParts, target, source string) (*adoCommitDiffsResponse, error) {
	u := fmt.Sprintf("%s/%s/%s/_apis/git/repositories/%s/diffs/commits"+
		"?baseVersion=%s&baseVersionType=commit&targetVersion=%s&targetVersionType=commit&api-version=7.0",
		f.adoBase(), parts.org, parts.project, parts.repo, target, source)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrReferenceFetchFailed, err)
	}
	req.Header.Set("Authorization", "Basic "+adoBasicAuth(f.adoToken))
	req.Header.Set("Accept", "application/json")
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrReferenceFetchFailed, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%w: ADO diffs %s (body=%s)", ErrReferenceFetchFailed, resp.Status, string(body))
	}
	var d adoCommitDiffsResponse
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("%w: decode ADO diffs: %v", ErrReferenceFetchFailed, err)
	}
	return &d, nil
}

// buildSummaryPatch produces a unified-diff-shaped summary listing changed
// files. NOT byte-equivalent to a real diff; for similarity scoring the line
// set still includes file paths, which gives operators a useful signal.
func buildSummaryPatch(d *adoCommitDiffsResponse, target, source string) string {
	var b strings.Builder
	b.WriteString("# ADO PR auto-generated file-summary patch (NOT a true unified diff)\n")
	b.WriteString(fmt.Sprintf("# base=%s target=%s files=%d\n", target, source, len(d.Changes)))
	for _, c := range d.Changes {
		path := strings.TrimPrefix(c.Item.Path, "/")
		b.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", path, path))
		switch strings.ToLower(c.ChangeType) {
		case "add":
			b.WriteString(fmt.Sprintf("--- /dev/null\n+++ b/%s\n", path))
		case "delete":
			b.WriteString(fmt.Sprintf("--- a/%s\n+++ /dev/null\n", path))
		default:
			b.WriteString(fmt.Sprintf("--- a/%s\n+++ b/%s\n", path, path))
		}
		b.WriteString(fmt.Sprintf("@@ ado:%s @@\n", c.ChangeType))
	}
	return b.String()
}

func (f *ReferenceFetcher) fetchPlain(ctx context.Context, u string) (FetchedPatch, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return FetchedPatch{}, err
	}
	return f.do(req, u)
}

func (f *ReferenceFetcher) do(req *http.Request, sourceURL string) (FetchedPatch, error) {
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return FetchedPatch{}, fmt.Errorf("%w: %v", ErrReferenceFetchFailed, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10 MiB cap
	if resp.StatusCode >= 400 {
		return FetchedPatch{}, fmt.Errorf("%w: %s (body=%s)", ErrReferenceFetchFailed, resp.Status, strings.TrimSpace(string(body)))
	}
	return FetchedPatch{Patch: string(body), SourceURL: sourceURL}, nil
}

// adoBasicAuth returns the base64 encoded ":<pat>" for ADO Basic-auth.
func adoBasicAuth(pat string) string {
	return base64.StdEncoding.EncodeToString([]byte(":" + pat))
}
