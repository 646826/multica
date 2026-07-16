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
