package benchmark

import (
	"context"
	"encoding/base64"
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
}

func NewReferenceFetcher(adoToken, githubToken string) *ReferenceFetcher {
	return &ReferenceFetcher{
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		adoToken:    adoToken,
		githubToken: githubToken,
	}
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
	// ADO doesn't expose a single ".diff" endpoint; we use:
	// GET <base>/<org>/<project>/_apis/git/repositories/<repo>/pullrequests/<id>?
	//         api-version=7.0&includeWorkItemRefs=false
	//   ->  to get sourceRefName + targetRefName + lastMergeSourceCommit
	// Then GET _apis/git/repositories/<repo>/diffs/commits?
	//         baseVersion.version=<targetCommit>&targetVersion.version=<sourceCommit>
	//
	// For v1 simplicity, just emit a friendly "ADO patch fetching not yet
	// implemented for end-to-end" error — the wiring exists, but the rich
	// diff path takes substantial work. Return ErrReferenceFetchFailed.
	if f.adoToken == "" {
		return FetchedPatch{}, fmt.Errorf("%w: ADO token not configured (set MULTICA_ADO_PAT)", ErrReferenceFetchFailed)
	}
	return FetchedPatch{}, fmt.Errorf("%w: ADO PR diff fetching is a follow-up; paste the patch manually for now", ErrReferenceFetchFailed)
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
