package benchmark

import "sort"

type LeaderboardRow struct {
	Rank               int     `json:"rank"`
	ProfileID          string  `json:"profile_id"`
	ProfileSlug        string  `json:"profile_slug"`
	ProfileDisplayName string  `json:"profile_display_name"`
	BestRunID          string  `json:"best_run_id"`
	BestRunDisplayName string  `json:"best_run_display_name"`
	ResolvedCount      int     `json:"resolved_count"`
	TotalCount         int     `json:"total_count"`
	AveragePassRate    float64 `json:"average_pass_rate"`
	AggregatePassRate  float64 `json:"aggregate_pass_rate"`
	ErroredCount       int     `json:"errored_count"`
	CompletedAt        string  `json:"completed_at"`
}

type LeaderboardRunRow struct {
	RunID              string
	RunDisplayName     string
	ProfileID          string
	ProfileSlug        string
	ProfileDisplayName string
	ResolvedCount      int
	TotalCount         int
	AveragePassRate    float64
	AggregatePassRate  float64
	ErroredCount       int
	CompletedAt        string // RFC3339
}

// Leaderboard groups runs by profile, picks the best run per profile, and
// produces a dense-ranked output ordered by:
//  1. resolved_count DESC
//  2. average_pass_rate DESC
//  3. aggregate_pass_rate DESC
//  4. errored_count ASC
//  5. completed_at ASC (earlier wins ties)
func Leaderboard(runs []LeaderboardRunRow) []LeaderboardRow {
	if len(runs) == 0 {
		return []LeaderboardRow{}
	}

	groups := map[string][]LeaderboardRunRow{}
	for _, r := range runs {
		groups[r.ProfileID] = append(groups[r.ProfileID], r)
	}

	bestPerProfile := make([]LeaderboardRunRow, 0, len(groups))
	for _, gs := range groups {
		sort.SliceStable(gs, func(i, j int) bool { return betterRun(gs[i], gs[j]) })
		bestPerProfile = append(bestPerProfile, gs[0])
	}
	sort.SliceStable(bestPerProfile, func(i, j int) bool { return betterRun(bestPerProfile[i], bestPerProfile[j]) })

	out := make([]LeaderboardRow, 0, len(bestPerProfile))
	rank := 0
	for i, r := range bestPerProfile {
		if i == 0 || !sameTier(r, bestPerProfile[i-1]) {
			rank = i + 1
		}
		out = append(out, LeaderboardRow{
			Rank:               rank,
			ProfileID:          r.ProfileID,
			ProfileSlug:        r.ProfileSlug,
			ProfileDisplayName: r.ProfileDisplayName,
			BestRunID:          r.RunID,
			BestRunDisplayName: r.RunDisplayName,
			ResolvedCount:      r.ResolvedCount,
			TotalCount:         r.TotalCount,
			AveragePassRate:    r.AveragePassRate,
			AggregatePassRate:  r.AggregatePassRate,
			ErroredCount:       r.ErroredCount,
			CompletedAt:        r.CompletedAt,
		})
	}
	return out
}

func betterRun(a, b LeaderboardRunRow) bool {
	if a.ResolvedCount != b.ResolvedCount {
		return a.ResolvedCount > b.ResolvedCount
	}
	if a.AveragePassRate != b.AveragePassRate {
		return a.AveragePassRate > b.AveragePassRate
	}
	if a.AggregatePassRate != b.AggregatePassRate {
		return a.AggregatePassRate > b.AggregatePassRate
	}
	if a.ErroredCount != b.ErroredCount {
		return a.ErroredCount < b.ErroredCount
	}
	return a.CompletedAt < b.CompletedAt
}

// sameTier checks whether two rows have identical primary score keys (ignores
// errored_count and completed_at, which are tiebreakers and shouldn't tie ranks).
func sameTier(a, b LeaderboardRunRow) bool {
	return a.ResolvedCount == b.ResolvedCount &&
		a.AveragePassRate == b.AveragePassRate &&
		a.AggregatePassRate == b.AggregatePassRate
}
