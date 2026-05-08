package benchmark

import "sort"

const passRateEpsilon = 0.001

type ComparisonResult struct {
	BaseRunID string `json:"base_run_id"`
	CandRunID string `json:"cand_run_id"`
	Partial   bool   `json:"partial"`

	Delta ComparisonDelta `json:"delta"`

	Improved      []ComparisonInstance `json:"improved"`
	Regressed     []ComparisonInstance `json:"regressed"`
	NewlyResolved []ComparisonInstance `json:"newly_resolved"`
	LostResolved  []ComparisonInstance `json:"lost_resolved"`

	Categories ComparisonCategories `json:"categories"`

	MissingInBase []string `json:"missing_in_base,omitempty"`
	MissingInCand []string `json:"missing_in_cand,omitempty"`
}

type ComparisonDelta struct {
	Resolved    int     `json:"resolved"`
	AvgPassRate float64 `json:"avg_pass_rate"`
	AggPassRate float64 `json:"agg_pass_rate"`
	Errored     int     `json:"errored"`
}

type ComparisonInstance struct {
	InstanceID   string  `json:"instance_id"`
	BasePassRate float64 `json:"base_pass_rate"`
	CandPassRate float64 `json:"cand_pass_rate"`
}

type ComparisonCategories struct {
	Added   []string `json:"added"`
	Cleared []string `json:"cleared"`
}

type RunSummaryView struct {
	RunID             string
	ResolvedCount     int
	TotalCount        int
	ErroredCount      int
	AggregatePassRate float64
	AveragePassRate   float64
	FailureCategories []string
}

type EvalResultView struct {
	InstanceID string
	Resolved   bool
	PassRate   float64
}

// Compare is a pure function over two RunSummaryView and per-instance eval result maps.
func Compare(base, cand RunSummaryView, baseResults, candResults map[string]EvalResultView) ComparisonResult {
	out := ComparisonResult{
		BaseRunID: base.RunID, CandRunID: cand.RunID,
		Improved: []ComparisonInstance{}, Regressed: []ComparisonInstance{},
		NewlyResolved: []ComparisonInstance{}, LostResolved: []ComparisonInstance{},
		Categories: ComparisonCategories{Added: []string{}, Cleared: []string{}},
	}
	out.Delta.Resolved = cand.ResolvedCount - base.ResolvedCount
	out.Delta.AvgPassRate = cand.AveragePassRate - base.AveragePassRate
	out.Delta.AggPassRate = cand.AggregatePassRate - base.AggregatePassRate
	out.Delta.Errored = cand.ErroredCount - base.ErroredCount

	shared := []string{}
	for id := range baseResults {
		if _, ok := candResults[id]; ok {
			shared = append(shared, id)
		}
	}
	sort.Strings(shared)

	for _, id := range shared {
		b := baseResults[id]
		c := candResults[id]
		ci := ComparisonInstance{InstanceID: id, BasePassRate: b.PassRate, CandPassRate: c.PassRate}
		switch d := c.PassRate - b.PassRate; {
		case d > passRateEpsilon:
			out.Improved = append(out.Improved, ci)
		case d < -passRateEpsilon:
			out.Regressed = append(out.Regressed, ci)
		}
		if !b.Resolved && c.Resolved {
			out.NewlyResolved = append(out.NewlyResolved, ci)
		} else if b.Resolved && !c.Resolved {
			out.LostResolved = append(out.LostResolved, ci)
		}
	}

	out.Categories.Added = stringSetDiff(cand.FailureCategories, base.FailureCategories)
	out.Categories.Cleared = stringSetDiff(base.FailureCategories, cand.FailureCategories)

	for id := range baseResults {
		if _, ok := candResults[id]; !ok {
			out.MissingInCand = append(out.MissingInCand, id)
		}
	}
	for id := range candResults {
		if _, ok := baseResults[id]; !ok {
			out.MissingInBase = append(out.MissingInBase, id)
		}
	}
	sort.Strings(out.MissingInBase)
	sort.Strings(out.MissingInCand)
	out.Partial = len(out.MissingInBase) > 0 || len(out.MissingInCand) > 0
	return out
}

func stringSetDiff(a, b []string) []string {
	seen := map[string]struct{}{}
	for _, x := range b {
		seen[x] = struct{}{}
	}
	out := []string{}
	for _, x := range a {
		if _, ok := seen[x]; !ok {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}
