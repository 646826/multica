package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/service/benchmark"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// evalAuthFixtureSeq makes per-test workspace slugs/emails unique so parallel
// runs don't collide on the slug unique constraint.
var evalAuthFixtureSeq atomic.Uint64

// noopHandler is the next handler used in evaluator-auth middleware tests.
func noopHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// TestRequireEvaluatorPoolAuth_NoHeader_401 pins the missing-Authorization
// branch — no DB needed because Verify is never called.
func TestRequireEvaluatorPoolAuth_NoHeader_401(t *testing.T) {
	rec := httptest.NewRecorder()
	h := RequireEvaluatorPoolAuth(nil)(noopHandler())
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "unauthenticated")
}

// TestRequireEvaluatorPoolAuth_BogusToken_401 exercises the Verify failure
// branch: a well-formed Bearer header carrying an unknown token.
func TestRequireEvaluatorPoolAuth_BogusToken_401(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()
	svc := benchmark.NewEvaluatorPoolService(db.New(pool))

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer evp_bogus")
	rec := httptest.NewRecorder()
	RequireEvaluatorPoolAuth(svc)(noopHandler()).ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "unauthenticated")
}

// TestRequireEvaluatorPoolAuth_ValidToken_PopulatesCtx mints a real token,
// passes it through the middleware, and asserts the next handler can read
// the verified EvaluatorPoolToken from request context.
func TestRequireEvaluatorPoolAuth_ValidToken_PopulatesCtx(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()
	queries := db.New(pool)
	svc := benchmark.NewEvaluatorPoolService(queries)

	wsID, userID, cleanup := setupEvaluatorAuthFixture(t, pool)
	defer cleanup()

	tok, plain, err := svc.Create(context.Background(), benchmark.CreateEvaluatorPoolTokenInput{
		WorkspaceID: wsID,
		DisplayName: "middleware-test",
		CreatedBy:   userID,
	})
	require.NoError(t, err)

	var seen benchmark.EvaluatorPoolToken
	var sawCtx bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, sawCtx = EvaluatorTokenFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	RequireEvaluatorPoolAuth(svc)(next).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, sawCtx, "next handler must observe EvaluatorTokenFromContext == true")
	require.Equal(t, tok.ID.Bytes, seen.ID.Bytes)
	require.Equal(t, tok.TokenPrefix, seen.TokenPrefix)
}

// setupEvaluatorAuthFixture inserts a workspace + owner user scoped to the
// current test. CASCADE on workspace removes the evaluator_pool_token rows
// minted during the test. Mirrors the inline pattern used by workspace_test.go
// and benchmark/fixtures_test.go — Multica has no shared testfixture helper.
func setupEvaluatorAuthFixture(t *testing.T, pool *pgxpool.Pool) (pgtype.UUID, pgtype.UUID, func()) {
	t.Helper()
	ctx := context.Background()
	seq := evalAuthFixtureSeq.Add(1)
	email := fmt.Sprintf("middleware-evp-%d-%d@multica.test", os.Getpid(), seq)
	slug := fmt.Sprintf("middleware-evp-%d-%d", os.Getpid(), seq)

	var userIDStr string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ($1, $2)
		RETURNING id
	`, "Middleware Evp Tester", email).Scan(&userIDStr))

	var workspaceIDStr string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Middleware Evp Workspace", slug, "evaluator-pool middleware test", "MEP").Scan(&workspaceIDStr))

	_, err := pool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceIDStr, userIDStr)
	require.NoError(t, err)

	cleanup := func() {
		ctx := context.Background()
		if _, err := pool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug); err != nil {
			t.Logf("cleanup workspace: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, email); err != nil {
			t.Logf("cleanup user: %v", err)
		}
	}

	return mustParseUUID(t, workspaceIDStr), mustParseUUID(t, userIDStr), cleanup
}

func mustParseUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	require.NoError(t, u.Scan(s))
	return u
}
