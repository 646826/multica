package benchmark_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/service/benchmark"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// suite_service_test.go uses an inline test fixture against a real
// Postgres (same convention as server/cmd/server/integration_test.go and
// server/internal/handler/handler_test.go — Multica has no shared
// `testfixture` helper). If DATABASE_URL is unreachable, the package
// is skipped, mirroring those tests.

var (
	testPool   *pgxpool.Pool
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

func TestSuiteService_Create_StoresAndReturns(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	got, err := s.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: tx.WorkspaceID,
		Slug:        "smoke-cli-v1",
		DisplayName: "Smoke CLI v1",
		AdapterKind: "programbench",
		InstanceIDs: []string{"abishekvashok__cmatrix.5c082c6"},
		CreatedBy:   tx.UserID,
	})
	require.NoError(t, err)
	require.Equal(t, "smoke-cli-v1", got.Slug)
	require.Equal(t, []string{"abishekvashok__cmatrix.5c082c6"}, got.InstanceIDs)
	require.Equal(t, tx.WorkspaceID, got.WorkspaceID)
	require.True(t, got.ID.Valid, "returned suite should have a valid ID")
}

func TestSuiteService_Create_RejectsEmptyInstanceList(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	_, err := s.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: tx.WorkspaceID, Slug: "empty", DisplayName: "Empty",
		AdapterKind: "programbench", InstanceIDs: nil, CreatedBy: tx.UserID,
	})
	require.ErrorIs(t, err, benchmark.ErrSuiteInstanceListEmpty)
}

func TestSuiteService_Create_RejectsDuplicateSlug(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	in := benchmark.CreateSuiteInput{
		WorkspaceID: tx.WorkspaceID, Slug: "dup", DisplayName: "A",
		AdapterKind: "programbench", InstanceIDs: []string{"a"}, CreatedBy: tx.UserID,
	}
	_, err := s.Create(ctx, in)
	require.NoError(t, err)
	_, err = s.Create(ctx, in)
	require.ErrorIs(t, err, benchmark.ErrSuiteSlugTaken)
}

func TestSuiteService_List_ReturnsWorkspaceScoped(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	other := newFixtureWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	_, err := s.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: tx.WorkspaceID, Slug: "a", DisplayName: "A",
		AdapterKind: "programbench", InstanceIDs: []string{"x"}, CreatedBy: tx.UserID,
	})
	require.NoError(t, err)
	_, err = s.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: other.WorkspaceID, Slug: "b", DisplayName: "B",
		AdapterKind: "programbench", InstanceIDs: []string{"y"}, CreatedBy: other.UserID,
	})
	require.NoError(t, err)

	got, err := s.List(ctx, tx.WorkspaceID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "a", got[0].Slug)
}
