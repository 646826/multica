package jira

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestValidateSettingsTable(t *testing.T) {
	base := DefaultSettings(ModeJiraLeads)
	cases := []struct {
		name    string
		mutate  func(*Settings)
		wantErr string
	}{
		{"defaults valid", func(s *Settings) {}, ""},
		{"bad mode", func(s *Settings) { s.Mode = "bidirectional" }, "mode"},
		{"bad leading", func(s *Settings) { s.LeadingSystem = "github" }, "leading_system"},
		{"mirror with create_to_jira", func(s *Settings) { s.Mode = ModeMirror; s.CreateToJira = true }, "mirror"},
		{"cadence too fast", func(s *Settings) { s.CycleIntervalSeconds = 5 }, "cycle_interval_seconds"},
		{"cadence too slow", func(s *Settings) { s.CycleIntervalSeconds = 600 }, "cycle_interval_seconds"},
		{"empty issue type", func(s *Settings) { s.OutboundIssueType = "" }, "outbound_issue_type"},
		{"status map bad target", func(s *Settings) { s.StatusMap = []byte(`{"in":{"10001":"doing"}}`) }, "not a Multica status"},
		{"status map bad out key", func(s *Settings) { s.StatusMap = []byte(`{"out":{"doing":"10001"}}`) }, "not a Multica status"},
		{"status map unknown field", func(s *Settings) { s.StatusMap = []byte(`{"inn":{}}`) }, "status_map"},
		{"status map valid", func(s *Settings) { s.StatusMap = []byte(`{"in":{"10001":"in_progress"},"out":{"done":"31"}}`) }, ""},
		{"field map bad direction", func(s *Settings) {
			s.FieldMap = []byte(`[{"external_field":"customfield_1","property_id":"p","direction":"sideways"}]`)
		}, "direction"},
		{"field map missing property", func(s *Settings) { s.FieldMap = []byte(`[{"external_field":"customfield_1"}]`) }, "property_id"},
		{"tag rule bad type", func(s *Settings) {
			s.TagRules = []byte(`[{"match_type":"component","match_value":"x","agent_id":"a"}]`)
		}, "match_type"},
		{"tag rule valid", func(s *Settings) {
			s.TagRules = []byte(`[{"match_type":"label","match_value":"agent:fixer","agent_id":"aaaa"}]`)
		}, ""},
	}
	for _, tc := range cases {
		s := base
		tc.mutate(&s)
		err := ValidateSettings(s)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: want error containing %q, got %v", tc.name, tc.wantErr, err)
		}
	}
}

func TestDefaultSettingsPerMode(t *testing.T) {
	jl := DefaultSettings(ModeJiraLeads)
	if !jl.CreateFromJira || jl.CreateToJira || jl.LeadingSystem != LeadJira {
		t.Fatalf("jira_leads defaults wrong: %+v", jl)
	}
	ml := DefaultSettings(ModeMulticaLeads)
	if ml.CreateFromJira || !ml.CreateToJira || ml.LeadingSystem != LeadMultica {
		t.Fatalf("multica_leads defaults wrong: %+v", ml)
	}
	mi := DefaultSettings(ModeMirror)
	if mi.CreateToJira {
		t.Fatalf("mirror must not default create_to_jira on: %+v", mi)
	}
	for _, s := range []Settings{jl, ml, mi} {
		if err := ValidateSettings(s); err != nil {
			t.Fatalf("defaults must validate: %v", err)
		}
	}
}

func newTestBox(t *testing.T) *secretbox.Box {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

// connectFake wires a fakeJira with the standard happy-path probe handlers.
func connectFake(t *testing.T, allPerms bool) *fakeJira {
	f := newFakeJira(t)
	f.mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Myself{AccountID: "acc-bot", DisplayName: "Sync Bot"})
	})
	f.mux.HandleFunc("/rest/api/3/mypermissions", func(w http.ResponseWriter, r *http.Request) {
		perms := map[string]map[string]bool{}
		for _, p := range requiredPermissions {
			perms[p] = map[string]bool{"havePermission": allPerms || p == "BROWSE_PROJECTS"}
		}
		json.NewEncoder(w).Encode(map[string]any{"permissions": perms})
	})
	return f
}

// serviceWithFake builds a Service whose clients hit the fake server.
func serviceWithFake(t *testing.T, q *db.Queries, box *secretbox.Box, f *fakeJira) *Service {
	s := NewService(q, box)
	s.NewClient = func(siteURL, email, token string) (*Client, error) {
		c, err := NewClient(siteURL, email, token)
		if err != nil {
			return nil, err
		}
		parsed, err := c.baseURL.Parse(f.srv.URL)
		if err != nil {
			return nil, err
		}
		c.baseURL = parsed
		c.httpc = f.srv.Client()
		c.sleep = func(time.Duration) {}
		return c, nil
	}
	return s
}

func TestConnectRequiresDeploymentKey(t *testing.T) {
	s := NewService(nil, nil)
	_, err := s.Connect(context.Background(), ConnectInput{ProjectKey: "GAME"})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("want not-configured error, got %v", err)
	}
}

func TestConnectHappyPathPersistsEncryptedToken(t *testing.T) {
	q, _ := openTestQueries(t)
	box := newTestBox(t)
	f := connectFake(t, true)
	s := serviceWithFake(t, q, box, f)

	ws := testUUID()
	conn, err := s.Connect(context.Background(), ConnectInput{
		WorkspaceID:   ws,
		ConnectedByID: testUUID(),
		SiteURL:       "https://happy.atlassian.net",
		Email:         "bot@example.test",
		Token:         "secret-token-123",
		ProjectKey:    "GAME",
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = q.DeleteJiraConnection(context.Background(), conn.ID) })

	if string(conn.TokenEncrypted) == "secret-token-123" {
		t.Fatal("token stored in plaintext")
	}
	plain, err := box.Open(conn.TokenEncrypted)
	if err != nil || string(plain) != "secret-token-123" {
		t.Fatalf("token roundtrip: %v %q", err, plain)
	}
	if conn.Mode != ModeJiraLeads || !conn.CreateFromJira || conn.CreateToJira || conn.Enabled {
		t.Fatalf("defaults not applied: %+v", conn)
	}

	// Same site+project from another workspace → friendly pre-check refusal.
	_, err = s.Connect(context.Background(), ConnectInput{
		WorkspaceID:   testUUID(),
		ConnectedByID: testUUID(),
		SiteURL:       "https://happy.atlassian.net",
		Email:         "bot@example.test",
		Token:         "another",
		ProjectKey:    "GAME",
	})
	if err == nil || !strings.Contains(err.Error(), "already connected") {
		t.Fatalf("want double-writer refusal, got %v", err)
	}
}

func TestConnectRefusesOnMissingPermission(t *testing.T) {
	q, _ := openTestQueries(t)
	box := newTestBox(t)
	f := connectFake(t, false) // only BROWSE_PROJECTS granted
	s := serviceWithFake(t, q, box, f)

	_, err := s.Connect(context.Background(), ConnectInput{
		WorkspaceID:   testUUID(),
		ConnectedByID: testUUID(),
		SiteURL:       "https://noperm.atlassian.net",
		Email:         "bot@example.test",
		Token:         "tok",
		ProjectKey:    "GAME",
	})
	if err == nil || !strings.Contains(err.Error(), "EDIT_ISSUES") {
		t.Fatalf("want missing-permission error naming the permission, got %v", err)
	}
	if _, err := q.GetJiraConnectionBySiteProject(context.Background(), db.GetJiraConnectionBySiteProjectParams{
		SiteHost: "noperm.atlassian.net", ProjectKey: "GAME",
	}); err == nil {
		t.Fatal("failed connect must not persist a row")
	}
}

func TestConnectRefusesBadCredentials(t *testing.T) {
	q, _ := openTestQueries(t)
	box := newTestBox(t)
	f := newFakeJira(t)
	f.mux.HandleFunc("/rest/api/3/myself", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	s := serviceWithFake(t, q, box, f)

	_, err := s.Connect(context.Background(), ConnectInput{
		WorkspaceID:   testUUID(),
		ConnectedByID: testUUID(),
		SiteURL:       "https://badcred.atlassian.net",
		Email:         "bot@example.test",
		Token:         "expired",
		ProjectKey:    "GAME",
	})
	if err == nil || !strings.Contains(err.Error(), "token invalid or expired") {
		t.Fatalf("want auth classification, got %v", err)
	}
}
