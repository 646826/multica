package benchmark_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/service/benchmark"
)

func TestProfileService_Capture_StoresImmutableSnapshot(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	agentID := newFixtureAgent(t, tx, agentSpec{
		Name:         "ProgramBenchRunner",
		Model:        "claude-opus-4-7",
		PromptSource: "# system\ndo benchmark things\n",
	})
	s := benchmark.NewProfileService(testQueries)

	got, err := s.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: tx.WorkspaceID,
		AgentID:     agentID,
		Slug:        "current",
		DisplayName: "Current",
		CapturedBy:  tx.UserID,
	})
	require.NoError(t, err)
	require.Equal(t, "ProgramBenchRunner", got.AgentName)
	require.Equal(t, "claude-opus-4-7", got.Model)
	require.Equal(t, "# system\ndo benchmark things\n", got.PromptSource)
	require.Len(t, got.PromptHash, 64)
	require.Nil(t, got.DuplicateOf)
	require.True(t, got.ID.Valid, "returned profile should have a valid ID")
}

func TestProfileService_Capture_DetectsDuplicateHash(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	agentID := newFixtureAgent(t, tx, agentSpec{Name: "X", Model: "m", PromptSource: "p"})
	s := benchmark.NewProfileService(testQueries)

	first, err := s.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: tx.WorkspaceID, AgentID: agentID, Slug: "v1",
		DisplayName: "V1", CapturedBy: tx.UserID,
	})
	require.NoError(t, err)

	second, err := s.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: tx.WorkspaceID, AgentID: agentID, Slug: "v2",
		DisplayName: "V2", CapturedBy: tx.UserID,
	})
	require.NoError(t, err)
	require.NotNil(t, second.DuplicateOf, "second capture with identical content should set DuplicateOf")
	require.Equal(t, first.ID.Bytes, second.DuplicateOf.Bytes)
	require.True(t, second.ID.Valid, "duplicate is still saved as a new row")
	require.NotEqual(t, first.ID.Bytes, second.ID.Bytes, "duplicate gets its own id")
}

func TestProfileService_Capture_RejectsDuplicateSlug(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	agentID := newFixtureAgent(t, tx, agentSpec{Name: "X", Model: "m", PromptSource: "p"})
	s := benchmark.NewProfileService(testQueries)

	in := benchmark.CaptureProfileInput{
		WorkspaceID: tx.WorkspaceID, AgentID: agentID, Slug: "same",
		DisplayName: "Same", CapturedBy: tx.UserID,
	}
	_, err := s.Capture(ctx, in)
	require.NoError(t, err)
	_, err = s.Capture(ctx, in)
	require.ErrorIs(t, err, benchmark.ErrProfileSlugTaken)
}

func TestProfileService_Capture_RejectsMissingAgent(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	s := benchmark.NewProfileService(testQueries)

	bogus := newRandomUUID(t)
	_, err := s.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: tx.WorkspaceID, AgentID: bogus, Slug: "x",
		DisplayName: "X", CapturedBy: tx.UserID,
	})
	require.ErrorIs(t, err, benchmark.ErrCaptureAgent)
}

func TestProfileService_Capture_RejectsCrossWorkspaceAgent(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	other := newFixtureWorkspace(t)
	otherAgent := newFixtureAgent(t, other, agentSpec{Name: "Other", Model: "m", PromptSource: "p"})
	s := benchmark.NewProfileService(testQueries)

	_, err := s.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: tx.WorkspaceID, AgentID: otherAgent, Slug: "x",
		DisplayName: "X", CapturedBy: tx.UserID,
	})
	require.ErrorIs(t, err, benchmark.ErrCaptureAgent)
}

func TestProfileService_Get_ReturnsCaptured(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	agentID := newFixtureAgent(t, tx, agentSpec{Name: "N", Model: "m", PromptSource: "p"})
	s := benchmark.NewProfileService(testQueries)

	created, err := s.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: tx.WorkspaceID, AgentID: agentID, Slug: "g",
		DisplayName: "G", CapturedBy: tx.UserID,
	})
	require.NoError(t, err)

	got, err := s.Get(ctx, created.ID, tx.WorkspaceID)
	require.NoError(t, err)
	require.Equal(t, created.ID.Bytes, got.ID.Bytes)
	require.Equal(t, "g", got.Slug)
}

func TestProfileService_Get_ReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	s := benchmark.NewProfileService(testQueries)

	bogus := newRandomUUID(t)
	_, err := s.Get(ctx, bogus, tx.WorkspaceID)
	require.ErrorIs(t, err, benchmark.ErrProfileNotFound)
}

func TestProfileService_Delete_RemovesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	agentID := newFixtureAgent(t, tx, agentSpec{Name: "N", Model: "m", PromptSource: "p"})
	s := benchmark.NewProfileService(testQueries)

	created, err := s.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: tx.WorkspaceID, AgentID: agentID, Slug: "to-del",
		DisplayName: "D", CapturedBy: tx.UserID,
	})
	require.NoError(t, err)

	require.NoError(t, s.Delete(ctx, created.ID, tx.WorkspaceID))
	require.ErrorIs(t, s.Delete(ctx, created.ID, tx.WorkspaceID), benchmark.ErrProfileNotFound)
}

func TestProfileService_Capture_EmptySkillsRoundTrips(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	agentID := newFixtureAgent(t, tx, agentSpec{Name: "NoSkills", Model: "m", PromptSource: "p"})
	s := benchmark.NewProfileService(testQueries)

	captured, err := s.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: tx.WorkspaceID, AgentID: agentID, Slug: "empty-skills",
		DisplayName: "Empty Skills", CapturedBy: tx.UserID,
	})
	require.NoError(t, err)
	require.NotNil(t, captured.AttachedSkills, "Capture must return non-nil empty slice, not nil")
	require.Empty(t, captured.AttachedSkills)

	got, err := s.Get(ctx, captured.ID, tx.WorkspaceID)
	require.NoError(t, err)
	require.NotNil(t, got.AttachedSkills, "Get must return non-nil empty slice, not nil")
	require.Empty(t, got.AttachedSkills)

	listed, err := s.List(ctx, tx.WorkspaceID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.NotNil(t, listed[0].AttachedSkills, "List must return non-nil empty slice, not nil")
	require.Empty(t, listed[0].AttachedSkills)
}

// newRandomUUID returns a random valid pgtype.UUID. Used to feed the service
// "this id does not exist" without inserting anything.
func newRandomUUID(t *testing.T) pgtype.UUID {
	t.Helper()
	u := uuid.New()
	var p pgtype.UUID
	if err := p.Scan(u.String()); err != nil {
		t.Fatalf("scan uuid: %v", err)
	}
	return p
}
