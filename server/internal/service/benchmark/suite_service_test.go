package benchmark_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/service/benchmark"
)

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

func TestSuiteService_Delete_RemovesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	created, err := s.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: tx.WorkspaceID, Slug: "to-delete", DisplayName: "X",
		AdapterKind: "programbench", InstanceIDs: []string{"x"}, CreatedBy: tx.UserID,
	})
	require.NoError(t, err)

	require.NoError(t, s.Delete(ctx, created.ID, tx.WorkspaceID))
	require.ErrorIs(t, s.Delete(ctx, created.ID, tx.WorkspaceID), benchmark.ErrSuiteNotFound)
}

func TestSuiteService_Delete_RejectsCrossWorkspace(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	other := newFixtureWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	created, err := s.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: tx.WorkspaceID, Slug: "scoped", DisplayName: "X",
		AdapterKind: "programbench", InstanceIDs: []string{"x"}, CreatedBy: tx.UserID,
	})
	require.NoError(t, err)

	require.ErrorIs(t, s.Delete(ctx, created.ID, other.WorkspaceID), benchmark.ErrSuiteNotFound)
}
