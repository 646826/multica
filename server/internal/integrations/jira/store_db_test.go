package jira

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Live-DB roundtrip for the generated jira queries. Follows the repo pattern:
// requires DATABASE_URL (worktree DB with migrations applied); skips when the
// database is unreachable.

func openTestQueries(t *testing.T) (*db.Queries, *pgxpool.Pool) {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping DB test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("database not available: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database not reachable: %v", err)
	}
	t.Cleanup(pool.Close)
	return db.New(pool), pool
}

func testUUID() pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.New(), Valid: true}
}

func createTestConnection(t *testing.T, q *db.Queries, ws pgtype.UUID) db.JiraConnection {
	t.Helper()
	host := fmt.Sprintf("t-%s.example.test", uuid.NewString()[:8])
	conn, err := q.CreateJiraConnection(context.Background(), db.CreateJiraConnectionParams{
		WorkspaceID:          ws,
		SiteUrl:              "https://" + host,
		SiteHost:             host,
		ProjectKey:           "GAME",
		ProjectID:            "10001",
		Email:                "bot@example.test",
		TokenEncrypted:       []byte{0x01, 0x02},
		ConnectedByID:        testUUID(),
		Mode:                 "jira_leads",
		LeadingSystem:        "jira",
		CommentsEnabled:      true,
		LabelsEnabled:        true,
		CustomFieldsEnabled:  true,
		CreateFromJira:       true,
		CreateToJira:         false,
		JqlFilter:            "",
		LabelPrefix:          "",
		MentionBridgeEnabled: true,
		OutboundIssueType:    "Task",
		StatusMap:            []byte(`{}`),
		CycleIntervalSeconds: 45,
	})
	if err != nil {
		t.Fatalf("create connection: %v", err)
	}
	t.Cleanup(func() {
		_ = q.DeleteJiraConnection(context.Background(), conn.ID)
	})
	return conn
}

func TestJiraConnectionRoundtrip(t *testing.T) {
	q, _ := openTestQueries(t)
	ctx := context.Background()
	ws := testUUID()

	conn := createTestConnection(t, q, ws)

	byWS, err := q.GetJiraConnectionByWorkspace(ctx, ws)
	if err != nil || byWS.ID != conn.ID {
		t.Fatalf("get by workspace: %v (id match=%v)", err, byWS.ID == conn.ID)
	}
	bySite, err := q.GetJiraConnectionBySiteProject(ctx, db.GetJiraConnectionBySiteProjectParams{
		SiteHost:   conn.SiteHost,
		ProjectKey: conn.ProjectKey,
	})
	if err != nil || bySite.ID != conn.ID {
		t.Fatalf("get by site/project: %v", err)
	}

	if !conn.Enabled {
		// default is disabled until explicitly enabled
	} else {
		t.Fatal("new connection must default to disabled")
	}

	upd, err := q.UpdateJiraConnectionConfig(ctx, db.UpdateJiraConnectionConfigParams{
		ID:                   conn.ID,
		Enabled:              true,
		Mode:                 "two_way",
		LeadingSystem:        "multica",
		CommentsEnabled:      true,
		LabelsEnabled:        false,
		CustomFieldsEnabled:  true,
		CreateFromJira:       true,
		CreateToJira:         true,
		JqlFilter:            "labels = ai",
		LabelPrefix:          "sync-",
		MentionBridgeEnabled: false,
		OutboundIssueType:    "Bug",
		StatusMap:            []byte(`{"10012":"in_progress"}`),
		FieldMap:             []byte(`[]`),
		TagRules:             []byte(`[]`),
		CycleIntervalSeconds: 60,
	})
	if err != nil {
		t.Fatalf("update config: %v", err)
	}
	if upd.Mode != "two_way" || upd.LeadingSystem != "multica" || !upd.Enabled || upd.CycleIntervalSeconds != 60 {
		t.Fatalf("update config not persisted: %+v", upd)
	}

	enabled, err := q.ListEnabledJiraConnections(ctx)
	if err != nil {
		t.Fatalf("list enabled: %v", err)
	}
	found := false
	for _, c := range enabled {
		if c.ID == conn.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("enabled connection missing from ListEnabledJiraConnections")
	}

	now := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	if err := q.UpdateJiraConnectionCursors(ctx, db.UpdateJiraConnectionCursorsParams{
		ID: conn.ID, JiraCursor: now, LocalCursor: now,
	}); err != nil {
		t.Fatalf("update cursors: %v", err)
	}
	if err := q.UpdateJiraConnectionHealth(ctx, db.UpdateJiraConnectionHealthParams{
		ID: conn.ID, Health: []byte(`{"state":"ok"}`),
	}); err != nil {
		t.Fatalf("update health: %v", err)
	}

	if err := q.DeleteJiraConnection(ctx, conn.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := q.GetJiraConnectionByID(ctx, conn.ID); err == nil {
		t.Fatal("connection still readable after delete")
	}
}

func TestJiraConnectionUniqueGuards(t *testing.T) {
	q, _ := openTestQueries(t)
	ctx := context.Background()
	ws := testUUID()
	conn := createTestConnection(t, q, ws)

	// Same workspace, second connection → refused by idx_jira_connection_workspace.
	_, err := q.CreateJiraConnection(ctx, db.CreateJiraConnectionParams{
		WorkspaceID:          ws,
		SiteUrl:              "https://other.example.test",
		SiteHost:             "other.example.test",
		ProjectKey:           "OTHER",
		Email:                "bot@example.test",
		TokenEncrypted:       []byte{0x01},
		ConnectedByID:        testUUID(),
		Mode:                 "jira_leads",
		LeadingSystem:        "jira",
		CommentsEnabled:      true,
		LabelsEnabled:        true,
		CustomFieldsEnabled:  true,
		CreateFromJira:       true,
		CreateToJira:         false,
		MentionBridgeEnabled: true,
		OutboundIssueType:    "Task",
		StatusMap:            []byte(`{}`),
		CycleIntervalSeconds: 45,
	})
	if err == nil {
		t.Fatal("second connection for same workspace must violate unique index")
	}

	// Same site+project from a different workspace → deployment-wide double-writer guard (FR-1).
	_, err = q.CreateJiraConnection(ctx, db.CreateJiraConnectionParams{
		WorkspaceID:          testUUID(),
		SiteUrl:              conn.SiteUrl,
		SiteHost:             conn.SiteHost,
		ProjectKey:           conn.ProjectKey,
		Email:                "bot@example.test",
		TokenEncrypted:       []byte{0x01},
		ConnectedByID:        testUUID(),
		Mode:                 "jira_leads",
		LeadingSystem:        "jira",
		CommentsEnabled:      true,
		LabelsEnabled:        true,
		CustomFieldsEnabled:  true,
		CreateFromJira:       true,
		CreateToJira:         false,
		MentionBridgeEnabled: true,
		OutboundIssueType:    "Task",
		StatusMap:            []byte(`{}`),
		CycleIntervalSeconds: 45,
	})
	if err == nil {
		t.Fatal("same site+project in another workspace must violate the double-writer unique index")
	}
}

func TestJiraJournalRoundtripAndPrune(t *testing.T) {
	q, _ := openTestQueries(t)
	ctx := context.Background()
	conn := createTestConnection(t, q, testUUID())

	cycle := testUUID()
	for i := 0; i < 5; i++ {
		if _, err := q.InsertJiraJournal(ctx, db.InsertJiraJournalParams{
			ConnectionID: conn.ID,
			WorkspaceID:  conn.WorkspaceID,
			CycleID:      cycle,
			Kind:         "status_unmapped",
			IssueID:      pgtype.UUID{},
			JiraKey:      fmt.Sprintf("GAME-%d", i+1),
			Detail:       []byte(`{"status":"Waiting for vendor"}`),
		}); err != nil {
			t.Fatalf("insert journal: %v", err)
		}
	}

	recent, err := q.ListJiraJournalRecent(ctx, db.ListJiraJournalRecentParams{
		ConnectionID: conn.ID, Limit: 10,
	})
	if err != nil || len(recent) != 5 {
		t.Fatalf("list recent: err=%v len=%d want 5", err, len(recent))
	}

	counts, err := q.CountJiraJournalByKindSince(ctx, db.CountJiraJournalByKindSinceParams{
		ConnectionID: conn.ID,
		CreatedAt:    pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
	})
	if err != nil || len(counts) != 1 || counts[0].Kind != "status_unmapped" || counts[0].Count != 5 {
		t.Fatalf("count by kind: err=%v counts=%+v", err, counts)
	}

	// Count-based retention: keep newest 2.
	pruned, err := q.PruneJiraJournalByCount(ctx, db.PruneJiraJournalByCountParams{
		ConnectionID: conn.ID, Offset: 2,
	})
	if err != nil || pruned != 3 {
		t.Fatalf("prune by count: err=%v pruned=%d want 3", err, pruned)
	}

	// Age-based retention: everything older than the future is pruned.
	pruned, err = q.PruneJiraJournalByAge(ctx, db.PruneJiraJournalByAgeParams{
		ConnectionID: conn.ID,
		CreatedAt:    pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	if err != nil || pruned != 2 {
		t.Fatalf("prune by age: err=%v pruned=%d want 2", err, pruned)
	}
}
