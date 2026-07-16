package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Story 5.3 — mapped custom-field value sync (FR-24/FR-25).

func TestInboundFieldValueSyncAndSkipNotBlock(t *testing.T) {
	f := newFakeJira(t)
	now := time.Now().UTC()
	f.mux.HandleFunc("/rest/api/3/search/jql", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issues":[{"id":"id-GAME-80","key":"GAME-80","fields":{
			"summary":"Fielded","description":{"type":"doc","version":1,"content":[]},
			"status":{"id":"100","name":"To Do","statusCategory":{"key":"new"}},
			"labels":[],
			"customfield_ok":{"value":"High"},
			"customfield_bad":{"value":"NotANumber"},
			"updated":%q}}],"isLast":true}`, now.Format(jiraTimeLayout))
	})
	f.mux.HandleFunc("/rest/api/3/issue/id-GAME-80/comment", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"comments":[],"startAt":0,"maxResults":100,"total":0}`))
	})
	w, conn, q := importFixture(t, f)
	ctx := context.Background()

	// Two property definitions: a select (compatible) and a number (the Jira
	// option value can't coerce → skip, not block).
	selProp, err := q.CreateIssueProperty(ctx, db.CreateIssuePropertyParams{
		WorkspaceID: conn.WorkspaceID, Name: "Priority", Type: "select",
		Description: "", Icon: "", Config: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	numProp, err := q.CreateIssueProperty(ctx, db.CreateIssuePropertyParams{
		WorkspaceID: conn.WorkspaceID, Name: "Points", Type: "number",
		Description: "", Icon: "", Config: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	fieldMap := fmt.Sprintf(`[{"external_field":"customfield_ok","property_id":%q,"direction":"pull"},{"external_field":"customfield_bad","property_id":%q,"direction":"pull"}]`,
		uuidStr(selProp.ID), uuidStr(numProp.ID))
	conn, _ = q.UpdateJiraConnectionConfig(ctx, db.UpdateJiraConnectionConfigParams{
		ID: conn.ID, Enabled: true, Mode: conn.Mode, LeadingSystem: conn.LeadingSystem,
		CommentsEnabled: true, LabelsEnabled: true, CustomFieldsEnabled: true,
		CreateFromJira: true, CreateToJira: false, JqlFilter: "", LabelPrefix: "",
		MentionBridgeEnabled: true, OutboundIssueType: "Task",
		StatusMap: []byte(`{"in":{"100":"todo"},"out":{"todo":"100"}}`),
		FieldMap:  []byte(fieldMap), TagRules: []byte(`[]`), CycleIntervalSeconds: 45,
	})

	if _, err := w.runCycle(ctx, conn); err != nil {
		t.Fatalf("import: %v", err)
	}
	link, _ := q.GetJiraLinkByJiraIssueID(ctx, db.GetJiraLinkByJiraIssueIDParams{ConnectionID: conn.ID, JiraIssueID: "id-GAME-80"})
	rewind := func() db.JiraConnection {
		c, _ := q.GetJiraConnectionByID(ctx, conn.ID)
		_ = q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
			ID: conn.ID, JiraCursor: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true}, LocalCursor: c.LocalCursor})
		c, _ = q.GetJiraConnectionByID(ctx, conn.ID)
		return c
	}
	if _, err := w.runCycle(ctx, rewind()); err != nil {
		t.Fatalf("field cycle: %v", err)
	}

	issue, _ := q.GetIssue(ctx, link.IssueID)
	var props map[string]json.RawMessage
	_ = json.Unmarshal(issue.Properties, &props)
	if string(props[uuidStr(selProp.ID)]) != `"High"` {
		t.Fatalf("compatible field must sync: %s", props[uuidStr(selProp.ID)])
	}
	if _, present := props[uuidStr(numProp.ID)]; present {
		t.Fatalf("unmappable field must be skipped, not written: %s", props[uuidStr(numProp.ID)])
	}
	rows, _ := q.ListJiraJournalRecent(ctx, db.ListJiraJournalRecentParams{ConnectionID: conn.ID, Limit: 30})
	skipped := false
	for _, r := range rows {
		if r.Kind == string(JournalFieldSkipped) {
			skipped = true
		}
	}
	if !skipped {
		t.Fatal("unmappable field value must journal field_skipped")
	}
}
