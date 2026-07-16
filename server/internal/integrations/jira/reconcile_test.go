package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func searchIssueJSON(key string, updated time.Time, status string, labels []string) string {
	l, _ := json.Marshal(labels)
	return fmt.Sprintf(`{"id":"id-%s","key":%q,"fields":{
		"summary":"Issue %s",
		"description":{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"body of %s"}]}]},
		"status":{"id":"10001","name":%q},
		"labels":%s,
		"updated":%q,
		"customfield_9001":"forty-two"
	}}`, key, key, key, key, status, l, updated.Format(jiraTimeLayout))
}

func TestSearchUpdatedPaginatesWithFieldsAndRelativeJQL(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	var seenJQL, seenFields string
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		seenJQL = r.URL.Query().Get("jql")
		seenFields = r.URL.Query().Get("fields")
		if r.URL.Query().Get("nextPageToken") == "" {
			fmt.Fprintf(w, `{"issues":[%s],"nextPageToken":"tok2","isLast":false}`,
				searchIssueJSON("GAME-1", now.Add(-2*time.Minute), "In Progress", []string{"bug"}))
			return
		}
		fmt.Fprintf(w, `{"issues":[%s],"isLast":true}`,
			searchIssueJSON("GAME-2", now.Add(-1*time.Minute), "To Do", nil))
	})
	c, _ := testClient(t, f)

	issues, truncated, err := c.SearchUpdated(context.Background(), "GAME", "labels = ai", 90, []string{"customfield_9001"}, 500)
	if err != nil || truncated {
		t.Fatalf("search: err=%v truncated=%v", err, truncated)
	}
	if len(issues) != 2 || issues[0].Key != "GAME-1" || issues[1].Key != "GAME-2" {
		t.Fatalf("pagination wrong: %+v", issues)
	}
	if !strings.Contains(seenJQL, `project = "GAME"`) ||
		!strings.Contains(seenJQL, `(labels = ai)`) ||
		!strings.Contains(seenJQL, `updated >= "-90m"`) ||
		!strings.Contains(seenJQL, "ORDER BY updated ASC") {
		t.Fatalf("jql shape wrong: %q", seenJQL)
	}
	if !strings.Contains(seenFields, "customfield_9001") || !strings.Contains(seenFields, "status") {
		t.Fatalf("fields param wrong: %q", seenFields)
	}
	if issues[0].StatusName != "In Progress" || issues[0].Labels[0] != "bug" {
		t.Fatalf("raw snapshot extraction wrong: %+v", issues[0])
	}
	if string(issues[0].Fields["customfield_9001"]) != `"forty-two"` {
		t.Fatalf("mapped raw field missing: %+v", issues[0].Fields)
	}
}

func TestSearchUpdatedHonorsPageCap(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		var rows []string
		for i := 0; i < 100; i++ {
			rows = append(rows, searchIssueJSON(fmt.Sprintf("GAME-%d", i), now, "To Do", nil))
		}
		fmt.Fprintf(w, `{"issues":[%s],"nextPageToken":"more","isLast":false}`, strings.Join(rows, ","))
	})
	c, _ := testClient(t, f)

	issues, truncated, err := c.SearchUpdated(context.Background(), "GAME", "", 0, nil, 150)
	if err != nil || !truncated {
		t.Fatalf("cap: err=%v truncated=%v", err, truncated)
	}
	if len(issues) != 150 {
		t.Fatalf("cap must bound results: got %d", len(issues))
	}
	if c.Requests() != 2 {
		t.Fatalf("cap must stop paging: %d requests", c.Requests())
	}
}

func TestObserveJiraPreciseCursorFilterAndConversion(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	cursor := now.Add(-10 * time.Minute)
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		// The minute-coarse JQL window over-returns: one issue is older than
		// the precise cut and must be filtered client-side.
		fmt.Fprintf(w, `{"issues":[%s,%s],"isLast":true}`,
			searchIssueJSON("GAME-OLD", cursor.Add(-30*time.Minute), "To Do", nil),
			searchIssueJSON("GAME-NEW", now.Add(-1*time.Minute), "In Progress", []string{"ai"}))
	})
	c, _ := testClient(t, f)

	conn := db.JiraConnection{
		ProjectKey: "GAME",
		JiraCursor: pgtype.Timestamptz{Time: cursor, Valid: true},
	}
	issues, truncated, err := observeJira(context.Background(), c, conn, []string{"customfield_9001"})
	if err != nil || truncated {
		t.Fatalf("observe: err=%v truncated=%v", err, truncated)
	}
	if len(issues) != 1 || issues[0].Key != "GAME-NEW" {
		t.Fatalf("precise filter wrong: %+v", issues)
	}
	if issues[0].DescriptionMD != "body of GAME-NEW" || issues[0].DescriptionLossy {
		t.Fatalf("conversion wrong: %+v", issues[0])
	}
	if issues[0].Fields["customfield_9001"] != `"forty-two"` {
		t.Fatalf("mapped field not carried: %+v", issues[0].Fields)
	}
}

func TestObserveJiraFullScanWhenCursorUnset(t *testing.T) {
	f := newFakeJira(t)
	var seenJQL string
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		seenJQL = r.URL.Query().Get("jql")
		fmt.Fprint(w, `{"issues":[],"isLast":true}`)
	})
	c, _ := testClient(t, f)

	_, _, err := observeJira(context.Background(), c, db.JiraConnection{ProjectKey: "GAME"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(seenJQL, "updated >=") {
		t.Fatalf("first-enable scan must not carry an updated bound: %q", seenJQL)
	}
}
