package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Jira Cloud REST v3 client. All Jira I/O for the integration goes through
// this one client so the rate-limit envelope lives in exactly one place
// (AD-13): bounded retries with exponential backoff + jitter honoring
// Retry-After on 429 and transient 5xx, and a per-instance request counter
// the reconcile worker surfaces into Health budgets. Callers never retry
// around it.

const (
	// defaultRequestTimeout is the per-call HTTP timeout. Jira Cloud search
	// and transition calls are normally well under a second; headroom covers
	// cross-region latency from self-hosted deployments.
	defaultRequestTimeout = 30 * time.Second

	// maxAttempts bounds the retry loop per logical call (initial try + 3
	// retries). Exhaustion surfaces the last *APIError to the caller, which
	// classifies it for Health.
	maxAttempts = 4

	// maxBackoff caps a single retry sleep even when Retry-After asks for
	// more; longer waits belong to the next reconcile cycle, not an in-cycle
	// stall (the cycle cadence is the coarse retry mechanism).
	maxBackoff = 30 * time.Second
)

// APIError is the typed failure for non-2xx Jira responses after retries.
type APIError struct {
	Status     int
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("jira: HTTP %d", e.Status)
	}
	return fmt.Sprintf("jira: HTTP %d: %s", e.Status, e.Message)
}

// IsAuth reports an invalid/expired credential (Health class auth_expired).
func (e *APIError) IsAuth() bool { return e.Status == http.StatusUnauthorized }

// IsForbidden reports a permission problem distinct from bad credentials.
func (e *APIError) IsForbidden() bool { return e.Status == http.StatusForbidden }

// Client talks to one Jira Cloud site with one service account.
type Client struct {
	baseURL *url.URL
	email   string
	token   string
	httpc   *http.Client
	sleep   func(time.Duration)

	// requests counts every HTTP request sent (including retries); the
	// worker snapshots it per cycle for the SM-C1 budget counters.
	requests atomic.Int64
}

// ErrScopedTokenBase rejects the api.atlassian.com gateway host: scoped API
// tokens (which require that regime) are unsupported in v1 — setup validates
// this at save time instead of failing cryptically on first sync.
var ErrScopedTokenBase = errors.New("jira: api.atlassian.com base URLs (scoped-token regime) are not supported; use your site URL, e.g. https://your-site.atlassian.net, with a regular API token")

// NewClient validates the site URL shape and builds a client. It performs no
// network I/O; credential validation is a separate live probe (Service.Connect).
func NewClient(siteURL, email, token string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(siteURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("jira: invalid site URL %q", siteURL)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("jira: site URL must use https, got %q", siteURL)
	}
	if strings.EqualFold(u.Host, "api.atlassian.com") {
		return nil, ErrScopedTokenBase
	}
	if email == "" || token == "" {
		return nil, errors.New("jira: email and API token are required")
	}
	return &Client{
		baseURL: u,
		email:   email,
		token:   token,
		httpc:   &http.Client{Timeout: defaultRequestTimeout},
		sleep:   time.Sleep,
	}, nil
}

// Requests returns the total HTTP requests sent by this client instance.
func (c *Client) Requests() int64 { return c.requests.Load() }

// do performs one logical API call with the retry envelope. body (when non-nil)
// is JSON-encoded; a 2xx response is decoded into out (when non-nil).
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("jira: encode request: %w", err)
		}
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		u := *c.baseURL
		u.Path = strings.TrimRight(u.Path, "/") + path
		if query != nil {
			u.RawQuery = query.Encode()
		}
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
		if err != nil {
			return fmt.Errorf("jira: build request: %w", err)
		}
		req.SetBasicAuth(c.email, c.token)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		c.requests.Add(1)
		resp, err := c.httpc.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("jira: %s %s: %w", method, path, err)
			if ctx.Err() != nil {
				return lastErr
			}
			c.backoff(attempt, 0)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			defer resp.Body.Close()
			if out == nil {
				io.Copy(io.Discard, resp.Body)
				return nil
			}
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				return fmt.Errorf("jira: decode %s %s: %w", method, path, err)
			}
			return nil
		}

		apiErr := &APIError{Status: resp.StatusCode, Message: readErrorMessage(resp.Body)}
		resp.Body.Close()
		if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
			apiErr.RetryAfter = ra
		}
		lastErr = apiErr

		if !retryable(resp.StatusCode) || attempt == maxAttempts {
			return apiErr
		}
		c.backoff(attempt, apiErr.RetryAfter)
	}
	return lastErr
}

func retryable(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// backoff sleeps for max(retryAfter, expo(attempt)) plus jitter, capped.
func (c *Client) backoff(attempt int, retryAfter time.Duration) {
	d := time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
	if retryAfter > d {
		d = retryAfter
	}
	d += time.Duration(rand.Int64N(int64(250 * time.Millisecond)))
	if d > maxBackoff {
		d = maxBackoff
	}
	c.sleep(d)
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

// readErrorMessage extracts Jira's standard error envelope
// ({"errorMessages":[...],"errors":{...}}) into one line, bounded.
func readErrorMessage(r io.Reader) string {
	body, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil || len(body) == 0 {
		return ""
	}
	var envelope struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		parts := append([]string{}, envelope.ErrorMessages...)
		for k, v := range envelope.Errors {
			parts = append(parts, k+": "+v)
		}
		if len(parts) > 0 {
			msg := strings.Join(parts, "; ")
			if len(msg) > 300 {
				msg = msg[:300]
			}
			return msg
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// --- v1.2 API surface (probe + catalogs) ---

// Myself identifies the service account (echo-filter identity seed).
type Myself struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
}

func (c *Client) Myself(ctx context.Context) (Myself, error) {
	var me Myself
	err := c.do(ctx, http.MethodGet, "/rest/api/3/myself", nil, nil, &me)
	return me, err
}

// Project is one row of the connect-time project picker.
type Project struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// SearchProjects lists projects the token can browse (paginated).
func (c *Client) SearchProjects(ctx context.Context) ([]Project, error) {
	var all []Project
	startAt := 0
	for {
		var page struct {
			Values  []Project `json:"values"`
			IsLast  bool      `json:"isLast"`
			StartAt int       `json:"startAt"`
			Total   int       `json:"total"`
		}
		q := url.Values{"startAt": {strconv.Itoa(startAt)}, "maxResults": {"50"}}
		if err := c.do(ctx, http.MethodGet, "/rest/api/3/project/search", q, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Values...)
		if page.IsLast || len(page.Values) == 0 || len(all) >= 500 {
			return all, nil
		}
		startAt += len(page.Values)
	}
}

// MyPermissions reports which of the requested project permissions the
// service account holds on the given project.
func (c *Client) MyPermissions(ctx context.Context, projectKey string, permissions []string) (map[string]bool, error) {
	var resp struct {
		Permissions map[string]struct {
			HavePermission bool `json:"havePermission"`
		} `json:"permissions"`
	}
	q := url.Values{
		"projectKey":  {projectKey},
		"permissions": {strings.Join(permissions, ",")},
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/mypermissions", q, nil, &resp); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(resp.Permissions))
	for name, p := range resp.Permissions {
		out[name] = p.HavePermission
	}
	return out, nil
}

// --- Observation (Epic 2) ---

// RemoteIssue is one raw Jira-side issue observation: side-local values only
// (AD-5 raw-source detection); mapping happens at plan time.
type RemoteIssue struct {
	ID             string
	Key            string
	Summary        string
	DescriptionADF json.RawMessage
	StatusID       string
	StatusName     string
	StatusCategory string // Jira status category key: new | indeterminate | done
	Labels         []string
	Fields         map[string]json.RawMessage
	Updated        time.Time
}

// jiraTimeLayout is Jira Cloud's issue timestamp format.
const jiraTimeLayout = "2006-01-02T15:04:05.000-0700"

// SearchUpdated pages through /rest/api/3/search/jql for the project's issues
// changed within the last sinceMinutes (0 = no time bound, the first-enable
// import scan). The JQL window is RELATIVE ("-Nm"): JQL datetime literals are
// interpreted in the service account's timezone and truncate to minutes, so a
// relative bound is the only timezone-proof shape; callers re-filter with the
// precise RFC3339 timestamps carried on each result. Results are ordered by
// updated ascending; pageCap bounds one observation pass (excess reported via
// truncated=true, never silently dropped).
func (c *Client) SearchUpdated(ctx context.Context, projectKey, extraJQL string, sinceMinutes int, fieldIDs []string, pageCap int) (issues []RemoteIssue, truncated bool, err error) {
	jql := fmt.Sprintf("project = %q", projectKey)
	if extraJQL = strings.TrimSpace(extraJQL); extraJQL != "" {
		jql += " AND (" + extraJQL + ")"
	}
	if sinceMinutes > 0 {
		jql += fmt.Sprintf(" AND updated >= \"-%dm\"", sinceMinutes)
	}
	jql += " ORDER BY updated ASC"

	fields := append([]string{"summary", "description", "status", "labels", "updated"}, fieldIDs...)

	nextPageToken := ""
	for {
		q := url.Values{
			"jql":        {jql},
			"fields":     {strings.Join(fields, ",")},
			"maxResults": {"100"},
		}
		if nextPageToken != "" {
			q.Set("nextPageToken", nextPageToken)
		}
		var page struct {
			Issues []struct {
				ID     string                     `json:"id"`
				Key    string                     `json:"key"`
				Fields map[string]json.RawMessage `json:"fields"`
			} `json:"issues"`
			NextPageToken string `json:"nextPageToken"`
			IsLast        bool   `json:"isLast"`
		}
		if err := c.do(ctx, http.MethodGet, "/rest/api/3/search/jql", q, nil, &page); err != nil {
			return nil, false, err
		}
		for _, it := range page.Issues {
			ri := parseRemoteIssue(it.ID, it.Key, it.Fields, fieldIDs)
			issues = append(issues, ri)
			if len(issues) >= pageCap {
				return issues, true, nil
			}
		}
		if page.IsLast || page.NextPageToken == "" {
			return issues, false, nil
		}
		nextPageToken = page.NextPageToken
	}
}

// ProjectStatus is one status of the connected project's workflows.
type ProjectStatus struct {
	ID       string
	Name     string
	Category string // new | indeterminate | done
}

// ProjectStatuses lists the distinct statuses reachable in the project's
// workflows (the suggested-mapping source, FR-15, and the /statuses catalog).
func (c *Client) ProjectStatuses(ctx context.Context, projectKey string) ([]ProjectStatus, error) {
	var payload []struct {
		Statuses []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			StatusCategory struct {
				Key string `json:"key"`
			} `json:"statusCategory"`
		} `json:"statuses"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/project/"+url.PathEscape(projectKey)+"/statuses", nil, nil, &payload); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []ProjectStatus
	for _, issueType := range payload {
		for _, st := range issueType.Statuses {
			if seen[st.ID] {
				continue
			}
			seen[st.ID] = true
			out = append(out, ProjectStatus{ID: st.ID, Name: st.Name, Category: st.StatusCategory.Key})
		}
	}
	return out, nil
}

// parseRemoteIssue extracts the raw observation snapshot from a Jira issue's
// fields map (shared by search pages and single-issue fetches).
func parseRemoteIssue(id, key string, fields map[string]json.RawMessage, fieldIDs []string) RemoteIssue {
	ri := RemoteIssue{ID: id, Key: key, Fields: map[string]json.RawMessage{}}
	if v, ok := fields["summary"]; ok {
		_ = json.Unmarshal(v, &ri.Summary)
	}
	if v, ok := fields["description"]; ok && string(v) != "null" {
		ri.DescriptionADF = v
	}
	if v, ok := fields["status"]; ok {
		var st struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			StatusCategory struct {
				Key string `json:"key"`
			} `json:"statusCategory"`
		}
		_ = json.Unmarshal(v, &st)
		ri.StatusID, ri.StatusName, ri.StatusCategory = st.ID, st.Name, st.StatusCategory.Key
	}
	if v, ok := fields["labels"]; ok {
		_ = json.Unmarshal(v, &ri.Labels)
	}
	if v, ok := fields["updated"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			if ts, perr := time.Parse(jiraTimeLayout, s); perr == nil {
				ri.Updated = ts
			}
		}
	}
	for _, fid := range fieldIDs {
		if v, ok := fields[fid]; ok && string(v) != "null" {
			ri.Fields[fid] = v
		}
	}
	return ri
}

// GetIssue fetches one issue's observation snapshot by immutable id (the
// dirty-retry refresh path, AD-2).
func (c *Client) GetIssue(ctx context.Context, issueID string, fieldIDs []string) (RemoteIssue, error) {
	fields := append([]string{"summary", "description", "status", "labels", "updated"}, fieldIDs...)
	var payload struct {
		ID     string                     `json:"id"`
		Key    string                     `json:"key"`
		Fields map[string]json.RawMessage `json:"fields"`
	}
	q := url.Values{"fields": {strings.Join(fields, ",")}}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(issueID), q, nil, &payload); err != nil {
		return RemoteIssue{}, err
	}
	return parseRemoteIssue(payload.ID, payload.Key, payload.Fields, fieldIDs), nil
}

// SearchJQLIDs runs an arbitrary bounded JQL returning only issue ids (the
// scope-membership check for the unseen sweep).
func (c *Client) SearchJQLIDs(ctx context.Context, jql string, limit int) ([]string, bool, error) {
	q := url.Values{
		"jql":        {jql},
		"fields":     {"id"},
		"maxResults": {strconv.Itoa(limit)},
	}
	var page struct {
		Issues []struct {
			ID string `json:"id"`
		} `json:"issues"`
		IsLast bool `json:"isLast"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/search/jql", q, nil, &page); err != nil {
		return nil, false, err
	}
	ids := make([]string, 0, len(page.Issues))
	for _, it := range page.Issues {
		ids = append(ids, it.ID)
	}
	return ids, page.IsLast, nil
}
