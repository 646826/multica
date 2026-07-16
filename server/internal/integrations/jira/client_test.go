package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeJira is the httptest-backed Jira Cloud stand-in used across the
// integration's tests (AD-11): handlers are registered per path; every
// request must carry Basic auth.

type fakeJira struct {
	t   *testing.T
	mux *http.ServeMux
	srv *httptest.Server
}

func newFakeJira(t *testing.T) *fakeJira {
	t.Helper()
	f := &fakeJira{t: t, mux: http.NewServeMux()}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); !ok {
			t.Errorf("request %s lacks basic auth", r.URL.Path)
		}
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// testClient builds a client against the fake with sleeps recorded, not slept.
func testClient(t *testing.T, f *fakeJira) (*Client, *[]time.Duration) {
	t.Helper()
	// httptest serves plain http; construct via NewClient then rewire the
	// parsed URL so URL-shape validation still runs on a realistic input.
	c, err := NewClient("https://placeholder.atlassian.net", "bot@example.test", "tok")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	u := *c.baseURL
	c.httpc = f.srv.Client()
	parsed, err := c.baseURL.Parse(f.srv.URL)
	if err != nil {
		t.Fatalf("parse fake url: %v", err)
	}
	_ = u
	c.baseURL = parsed
	var sleeps []time.Duration
	c.sleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	return c, &sleeps
}

func TestNewClientValidation(t *testing.T) {
	cases := []struct {
		name, site, email, token string
		wantErr                  string
	}{
		{"http rejected", "http://x.atlassian.net", "e@x", "t", "https"},
		{"garbage url", "://bad", "e@x", "t", "invalid site URL"},
		{"scoped gateway rejected", "https://api.atlassian.com/ex/jira/abc", "e@x", "t", "scoped-token"},
		{"missing token", "https://x.atlassian.net", "e@x", "", "required"},
		{"ok", "https://x.atlassian.net/", "e@x", "t", ""},
	}
	for _, tc := range cases {
		_, err := NewClient(tc.site, tc.email, tc.token)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: want error containing %q, got %v", tc.name, tc.wantErr, err)
		}
	}
}

func TestClientRetriesOn429HonoringRetryAfter(t *testing.T) {
	f := newFakeJira(t)
	var calls atomic.Int64
	f.mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		json.NewEncoder(w).Encode(Myself{AccountID: "acc-1", DisplayName: "Bot"})
	})
	c, sleeps := testClient(t, f)

	me, err := c.Myself(context.Background())
	if err != nil || me.AccountID != "acc-1" {
		t.Fatalf("Myself after retry: %v %+v", err, me)
	}
	if calls.Load() != 2 {
		t.Fatalf("want 2 attempts, got %d", calls.Load())
	}
	if len(*sleeps) != 1 || (*sleeps)[0] < 7*time.Second {
		t.Fatalf("Retry-After not honored: sleeps=%v", *sleeps)
	}
	if c.Requests() != 2 {
		t.Fatalf("request counter: want 2, got %d", c.Requests())
	}
}

func TestClientDoesNotRetryPermanent4xx(t *testing.T) {
	f := newFakeJira(t)
	var calls atomic.Int64
	f.mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"errorMessages":["bad credentials"]}`))
	})
	c, _ := testClient(t, f)

	_, err := c.Myself(context.Background())
	apiErr := &APIError{}
	if err == nil || !strings.Contains(err.Error(), "bad credentials") {
		t.Fatalf("want auth error with message, got %v", err)
	}
	if ok := errorsAs(err, &apiErr); !ok || !apiErr.IsAuth() {
		t.Fatalf("want APIError IsAuth, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("401 must not retry; attempts=%d", calls.Load())
	}
}

func TestClientRetriesTransient5xxThenFails(t *testing.T) {
	f := newFakeJira(t)
	var calls atomic.Int64
	f.mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c, sleeps := testClient(t, f)

	_, err := c.Myself(context.Background())
	if err == nil {
		t.Fatal("want error after exhausting retries")
	}
	if calls.Load() != maxAttempts {
		t.Fatalf("want %d attempts, got %d", maxAttempts, calls.Load())
	}
	if len(*sleeps) != maxAttempts-1 {
		t.Fatalf("want %d backoffs, got %d", maxAttempts-1, len(*sleeps))
	}
}

func TestSearchProjectsPaginates(t *testing.T) {
	f := newFakeJira(t)
	f.mux.HandleFunc("/rest/api/3/project/search", func(w http.ResponseWriter, r *http.Request) {
		start := r.URL.Query().Get("startAt")
		if start == "0" {
			w.Write([]byte(`{"values":[{"id":"1","key":"AAA","name":"A"}],"isLast":false,"startAt":0,"total":2}`))
			return
		}
		w.Write([]byte(`{"values":[{"id":"2","key":"BBB","name":"B"}],"isLast":true,"startAt":1,"total":2}`))
	})
	c, _ := testClient(t, f)

	projects, err := c.SearchProjects(context.Background())
	if err != nil || len(projects) != 2 || projects[1].Key != "BBB" {
		t.Fatalf("paginated projects: err=%v got=%+v", err, projects)
	}
}

func TestMyPermissions(t *testing.T) {
	f := newFakeJira(t)
	f.mux.HandleFunc("/rest/api/3/mypermissions", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("projectKey"); got != "GAME" {
			t.Errorf("projectKey=%q", got)
		}
		w.Write([]byte(`{"permissions":{"BROWSE_PROJECTS":{"havePermission":true},"EDIT_ISSUES":{"havePermission":false}}}`))
	})
	c, _ := testClient(t, f)

	perms, err := c.MyPermissions(context.Background(), "GAME", []string{"BROWSE_PROJECTS", "EDIT_ISSUES"})
	if err != nil || !perms["BROWSE_PROJECTS"] || perms["EDIT_ISSUES"] {
		t.Fatalf("permissions: err=%v got=%v", err, perms)
	}
}

// errorsAs avoids importing errors twice with the test's tiny needs.
func errorsAs(err error, target **APIError) bool {
	for err != nil {
		if e, ok := err.(*APIError); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
