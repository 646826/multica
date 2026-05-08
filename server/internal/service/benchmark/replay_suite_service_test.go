package benchmark_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/service/benchmark"
	"github.com/multica-ai/multica/server/internal/service/benchmark/adapter"
	"github.com/multica-ai/multica/server/internal/util"
)

func TestSuiteService_CreateReplaySuite_StoresOverrides(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	issueID, issueNumber, title, desc := newFixtureIssue(t, tx,
		"Fix login redirect", "Reproduce: log in twice; expected: stays signed in.")

	suite, err := s.CreateReplaySuite(ctx, benchmark.CreateReplaySuiteInput{
		WorkspaceID: tx.WorkspaceID,
		Slug:        "replay-stores-overrides",
		DisplayName: "Replay Stores Overrides",
		Description: "fixture",
		Instances: []benchmark.ReplayInstanceInput{{
			SourceIssueID:     issueID,
			ReferenceSolution: "diff --git a/login.go b/login.go\n-bug\n+fix",
			ReferencePRURL:    "https://example.test/pr/42",
		}},
		CreatedBy: tx.UserID,
	})
	require.NoError(t, err)
	require.Equal(t, "multica_replay", suite.AdapterKind)

	expectedInstanceID := "multica-issue:" + util.UUIDToString(issueID)
	require.Equal(t, []string{expectedInstanceID}, suite.InstanceIDs)
	require.Contains(t, suite.InstanceMetaOverrides, expectedInstanceID)

	var got adapter.ReplayInstanceMeta
	require.NoError(t, json.Unmarshal(suite.InstanceMetaOverrides[expectedInstanceID], &got))
	require.Equal(t, util.UUIDToString(issueID), got.SourceIssueID)
	require.Equal(t, issueNumber, got.SourceIssueNumber)
	require.Equal(t, title, got.SourceIssueTitle)
	require.Equal(t, desc, got.SourceIssueDescription)
	require.Equal(t, "diff --git a/login.go b/login.go\n-bug\n+fix", got.ReferenceSolution)
	require.Equal(t, "https://example.test/pr/42", got.ReferencePRURL)
}

func TestSuiteService_CreateReplaySuite_RejectsEmptyInstances(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	_, err := s.CreateReplaySuite(ctx, benchmark.CreateReplaySuiteInput{
		WorkspaceID: tx.WorkspaceID,
		Slug:        "replay-empty",
		DisplayName: "Empty",
		CreatedBy:   tx.UserID,
	})
	require.ErrorIs(t, err, benchmark.ErrSuiteInstanceListEmpty)
}

func TestSuiteService_CreateReplaySuite_RejectsMissingReference(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	issueID, _, _, _ := newFixtureIssue(t, tx, "No reference", "body")

	_, err := s.CreateReplaySuite(ctx, benchmark.CreateReplaySuiteInput{
		WorkspaceID: tx.WorkspaceID,
		Slug:        "replay-no-ref",
		DisplayName: "No Reference",
		Instances: []benchmark.ReplayInstanceInput{{
			SourceIssueID:     issueID,
			ReferenceSolution: "  ", // whitespace-only
		}},
		CreatedBy: tx.UserID,
	})
	require.ErrorIs(t, err, benchmark.ErrReplayReferenceSolutionEmpty)
}

func TestSuiteService_CreateReplaySuite_RejectsUnknownIssue(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	bogus := mustParseUUID(t, "11111111-2222-3333-4444-555555555555")

	_, err := s.CreateReplaySuite(ctx, benchmark.CreateReplaySuiteInput{
		WorkspaceID: tx.WorkspaceID,
		Slug:        "replay-bogus-issue",
		DisplayName: "Bogus",
		Instances: []benchmark.ReplayInstanceInput{{
			SourceIssueID:     bogus,
			ReferenceSolution: "diff --solution",
		}},
		CreatedBy: tx.UserID,
	})
	require.ErrorIs(t, err, benchmark.ErrReplaySourceIssueNotFound)
}

func TestSuiteService_CreateReplaySuite_RejectsCrossWorkspaceIssue(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	other := newFixtureWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	// Insert into the other workspace; tx workspace should not see it.
	otherIssueID, _, _, _ := newFixtureIssue(t, other, "Other workspace", "body")

	_, err := s.CreateReplaySuite(ctx, benchmark.CreateReplaySuiteInput{
		WorkspaceID: tx.WorkspaceID,
		Slug:        "replay-cross-ws",
		DisplayName: "Cross-WS",
		Instances: []benchmark.ReplayInstanceInput{{
			SourceIssueID:     otherIssueID,
			ReferenceSolution: "diff --solution",
		}},
		CreatedBy: tx.UserID,
	})
	require.ErrorIs(t, err, benchmark.ErrReplaySourceIssueNotFound)
}

func TestSuiteService_CreateReplaySuite_DuplicateSlug(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	issueID, _, _, _ := newFixtureIssue(t, tx, "Issue 1", "body")

	in := benchmark.CreateReplaySuiteInput{
		WorkspaceID: tx.WorkspaceID,
		Slug:        "replay-dup-slug",
		DisplayName: "Replay Dup Slug",
		Instances: []benchmark.ReplayInstanceInput{{
			SourceIssueID:     issueID,
			ReferenceSolution: "diff --solution",
		}},
		CreatedBy: tx.UserID,
	}
	_, err := s.CreateReplaySuite(ctx, in)
	require.NoError(t, err)
	_, err = s.CreateReplaySuite(ctx, in)
	require.ErrorIs(t, err, benchmark.ErrSuiteSlugTaken)
}

// Sanity check: existing non-replay rowToSuite path returns an empty-but-
// non-nil InstanceMetaOverrides map (driven by the JSONB DEFAULT '{}').
func TestSuiteService_Create_DefaultMetaOverridesIsEmptyMap(t *testing.T) {
	ctx := context.Background()
	tx := newFixtureWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	got, err := s.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: tx.WorkspaceID,
		Slug:        "non-replay-default",
		DisplayName: "Non-replay default",
		AdapterKind: "programbench",
		InstanceIDs: []string{"x"},
		CreatedBy:   tx.UserID,
	})
	require.NoError(t, err)
	require.NotNil(t, got.InstanceMetaOverrides)
	require.Empty(t, got.InstanceMetaOverrides)
}

