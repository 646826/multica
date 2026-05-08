package benchmark_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// fixtures_test.go uses an inline test fixture against a real
// Postgres (same convention as server/cmd/server/integration_test.go and
// server/internal/handler/handler_test.go — Multica has no shared
// `testfixture` helper). If DATABASE_URL is unreachable, the package
// is skipped, mirroring those tests.

var (
	testPool    *pgxpool.Pool
	testQueries *db.Queries

	// Process-unique counter for fixture slugs/emails so tests are isolated.
	fixtureSeq atomic.Uint64
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Printf("Skipping benchmark service tests: could not connect to database: %v\n", err)
		os.Exit(0)
	}
	if err := pool.Ping(ctx); err != nil {
		fmt.Printf("Skipping benchmark service tests: database not reachable: %v\n", err)
		pool.Close()
		os.Exit(0)
	}

	testPool = pool
	testQueries = db.New(pool)

	code := m.Run()
	pool.Close()
	os.Exit(code)
}

// fixtureWorkspace is the equivalent of the plan's `testfixture.NewWorkspace(t)`
// — a fresh workspace + owner user scoped to the current test, cleaned up
// via t.Cleanup. CASCADE on workspace removes any benchmark_suite rows
// inserted during the test.
type fixtureWorkspace struct {
	WorkspaceID pgtype.UUID
	UserID      pgtype.UUID
	Queries     *db.Queries
}

func newFixtureWorkspace(t *testing.T) fixtureWorkspace {
	t.Helper()
	if testPool == nil {
		t.Skip("test pool not initialized")
	}
	ctx := context.Background()
	seq := fixtureSeq.Add(1)
	email := fmt.Sprintf("benchmark-svc-%d-%d@multica.test", os.Getpid(), seq)
	slug := fmt.Sprintf("benchmark-svc-%d-%d", os.Getpid(), seq)

	var userIDStr string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ($1, $2)
		RETURNING id
	`, "Benchmark Svc Tester", email).Scan(&userIDStr); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	var workspaceIDStr string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Benchmark Svc Workspace", slug, "Benchmark service test workspace", "BNS").Scan(&workspaceIDStr); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}

	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceIDStr, userIDStr); err != nil {
		t.Fatalf("insert member: %v", err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		// Delete workspace first (cascades to benchmark_suite, member),
		// then user.
		if _, err := testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug); err != nil {
			t.Logf("cleanup workspace: %v", err)
		}
		if _, err := testPool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, email); err != nil {
			t.Logf("cleanup user: %v", err)
		}
	})

	return fixtureWorkspace{
		WorkspaceID: mustParseUUID(t, workspaceIDStr),
		UserID:      mustParseUUID(t, userIDStr),
		Queries:     testQueries,
	}
}

func mustParseUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return u
}

// agentSpec is the minimal description ProfileService tests need to insert
// an agent fixture. Other agent columns (description, runtime config, etc.)
// are filled with sensible defaults that satisfy the table's NOT NULL /
// CHECK constraints.
type agentSpec struct {
	Name         string
	Model        string
	PromptSource string
}

// newFixtureAgent inserts an `agent_runtime` + `agent` pair scoped to the
// fixture workspace. Returns the agent id. Cleanup happens via the workspace
// CASCADE registered in newFixtureWorkspace, so no separate t.Cleanup is
// needed here.
func newFixtureAgent(t *testing.T, tx fixtureWorkspace, spec agentSpec) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	seq := fixtureSeq.Add(1)

	// agent.runtime_id is nullable in the schema, but the production code
	// path always points to a real agent_runtime row. Insert one so the
	// fixture matches what handlers see in practice.
	var runtimeID pgtype.UUID
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider,
			status, device_info, metadata, last_seen_at
		)
		VALUES ($1, NULL, $2, 'cloud', $3, 'online', $4, '{}'::jsonb, now())
		RETURNING id
	`,
		tx.WorkspaceID,
		fmt.Sprintf("Benchmark Svc Runtime %d", seq),
		fmt.Sprintf("benchmark_svc_runtime_%d", seq),
		"benchmark service test runtime",
	).Scan(&runtimeID); err != nil {
		t.Fatalf("insert agent_runtime: %v", err)
	}

	var agentID pgtype.UUID
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id,
			instructions, custom_env, custom_args, mcp_config, model
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 1, $4, $5, '{}'::jsonb, '[]'::jsonb, NULL, $6)
		RETURNING id
	`,
		tx.WorkspaceID,
		spec.Name,
		runtimeID,
		tx.UserID,
		spec.PromptSource,
		spec.Model,
	).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	return agentID
}

// newFixtureIssue inserts a `done` issue tied to the fixture workspace.
// Returns the issue id, number, title, and description so tests can assert
// against them. Cleanup happens via the workspace CASCADE registered in
// newFixtureWorkspace.
func newFixtureIssue(t *testing.T, tx fixtureWorkspace, title, description string) (id pgtype.UUID, number int32, retTitle, retDesc string) {
	t.Helper()
	ctx := context.Background()
	seq := fixtureSeq.Add(1)
	num := int32(seq) //nolint:gosec // seq fits int32 for the lifetime of a test process

	var idStr string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (
			workspace_id, title, description, status, priority,
			creator_type, creator_id, number, position
		)
		VALUES ($1, $2, $3, 'done', 'none', 'member', $4, $5, 0)
		RETURNING id
	`, tx.WorkspaceID, title, description, tx.UserID, num).Scan(&idStr); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	return mustParseUUID(t, idStr), num, title, description
}
