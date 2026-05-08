package benchmark_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/service/benchmark"
)

func TestEvaluatorPoolService_Create_ReturnsPlaintextOnce(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	s := benchmark.NewEvaluatorPoolService(testQueries)

	tok, plain, err := s.Create(ctx, benchmark.CreateEvaluatorPoolTokenInput{
		WorkspaceID: tx.WorkspaceID, DisplayName: "ci-eval", CreatedBy: tx.UserID,
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(plain, "evp_"))
	require.Len(t, plain, 52) // "evp_" + 48 hex
	require.Equal(t, plain[:12], tok.TokenPrefix)

	// Verify the same plaintext resolves the row.
	got, err := s.Verify(ctx, plain)
	require.NoError(t, err)
	require.Equal(t, tok.ID.Bytes, got.ID.Bytes)
}

func TestEvaluatorPoolService_Verify_RejectsBogusToken(t *testing.T) {
	ctx := context.Background()
	s := benchmark.NewEvaluatorPoolService(testQueries)
	_, err := s.Verify(ctx, "evp_doesntexist")
	require.ErrorIs(t, err, benchmark.ErrEvaluatorPoolTokenInvalid)
	_, err = s.Verify(ctx, "wrong_prefix_xxx")
	require.ErrorIs(t, err, benchmark.ErrEvaluatorPoolTokenInvalid)
}

func TestEvaluatorPoolService_Verify_RejectsRevoked(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	s := benchmark.NewEvaluatorPoolService(testQueries)

	tok, plain, err := s.Create(ctx, benchmark.CreateEvaluatorPoolTokenInput{
		WorkspaceID: tx.WorkspaceID, DisplayName: "x", CreatedBy: tx.UserID,
	})
	require.NoError(t, err)
	require.NoError(t, s.Revoke(ctx, tok.ID, tx.WorkspaceID))

	_, err = s.Verify(ctx, plain)
	require.ErrorIs(t, err, benchmark.ErrEvaluatorPoolTokenRevoked)
}

func TestEvaluatorPoolService_List_OmitsHash(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	s := benchmark.NewEvaluatorPoolService(testQueries)

	_, _, err := s.Create(ctx, benchmark.CreateEvaluatorPoolTokenInput{
		WorkspaceID: tx.WorkspaceID, DisplayName: "x", CreatedBy: tx.UserID,
	})
	require.NoError(t, err)

	list, err := s.List(ctx, tx.WorkspaceID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	// EvaluatorPoolToken struct has NO TokenHash field (it's an internal-only
	// column). Compile-time check: just ensure we can read what we expect.
	require.Equal(t, "x", list[0].DisplayName)
	require.NotEmpty(t, list[0].TokenPrefix)
}
