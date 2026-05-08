package benchmark

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLeaderboard_RanksByResolvedThenAvgPR(t *testing.T) {
	runs := []LeaderboardRunRow{
		{RunID: "a", ProfileID: "p1", ResolvedCount: 5, AveragePassRate: 0.8},
		{RunID: "b", ProfileID: "p2", ResolvedCount: 7, AveragePassRate: 0.9},
		{RunID: "c", ProfileID: "p3", ResolvedCount: 7, AveragePassRate: 0.7},
	}
	out := Leaderboard(runs)
	require.Equal(t, []string{"p2", "p3", "p1"}, []string{out[0].ProfileID, out[1].ProfileID, out[2].ProfileID})
	require.Equal(t, 1, out[0].Rank)
	require.Equal(t, 2, out[1].Rank)
	require.Equal(t, 3, out[2].Rank)
}

func TestLeaderboard_BestRunPerProfile(t *testing.T) {
	runs := []LeaderboardRunRow{
		{RunID: "a", ProfileID: "p1", ResolvedCount: 5, AveragePassRate: 0.7},
		{RunID: "b", ProfileID: "p1", ResolvedCount: 6, AveragePassRate: 0.8}, // better
		{RunID: "c", ProfileID: "p2", ResolvedCount: 4, AveragePassRate: 0.9},
	}
	out := Leaderboard(runs)
	require.Len(t, out, 2)
	// p1 should appear via run "b" (the better run), not "a"
	var p1 *LeaderboardRow
	for i, r := range out {
		if r.ProfileID == "p1" {
			p1 = &out[i]
			break
		}
	}
	require.NotNil(t, p1)
	require.Equal(t, "b", p1.BestRunID)
}

func TestLeaderboard_DenseRankOnTies(t *testing.T) {
	runs := []LeaderboardRunRow{
		{RunID: "a", ProfileID: "p1", ResolvedCount: 5, AveragePassRate: 0.8, AggregatePassRate: 0.9, CompletedAt: "2026-01-01T00:00:00Z"},
		{RunID: "b", ProfileID: "p2", ResolvedCount: 5, AveragePassRate: 0.8, AggregatePassRate: 0.9, CompletedAt: "2026-01-02T00:00:00Z"},
		{RunID: "c", ProfileID: "p3", ResolvedCount: 4, AveragePassRate: 0.7},
	}
	out := Leaderboard(runs)
	require.Len(t, out, 3)
	require.Equal(t, 1, out[0].Rank)
	require.Equal(t, 1, out[1].Rank) // same primary score → same rank
	require.Equal(t, 3, out[2].Rank) // dense_rank: skips 2
}

func TestLeaderboard_EmptyInput(t *testing.T) {
	out := Leaderboard(nil)
	require.NotNil(t, out)
	require.Empty(t, out)
}
