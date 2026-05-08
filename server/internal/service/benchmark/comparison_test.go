package benchmark

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompare_AllShared_Improved(t *testing.T) {
	base := RunSummaryView{RunID: "b", ResolvedCount: 0, TotalCount: 2, AveragePassRate: 0.5}
	cand := RunSummaryView{RunID: "c", ResolvedCount: 1, TotalCount: 2, AveragePassRate: 0.75}
	bRes := map[string]EvalResultView{
		"i1": {InstanceID: "i1", PassRate: 0.5}, "i2": {InstanceID: "i2", PassRate: 0.5},
	}
	cRes := map[string]EvalResultView{
		"i1": {InstanceID: "i1", PassRate: 1.0, Resolved: true}, "i2": {InstanceID: "i2", PassRate: 0.5},
	}
	out := Compare(base, cand, bRes, cRes)
	require.Len(t, out.Improved, 1)
	require.Equal(t, "i1", out.Improved[0].InstanceID)
	require.Len(t, out.NewlyResolved, 1)
	require.False(t, out.Partial)
	require.Equal(t, 1, out.Delta.Resolved)
	require.InDelta(t, 0.25, out.Delta.AvgPassRate, 1e-9)
}

func TestCompare_LostResolved(t *testing.T) {
	bRes := map[string]EvalResultView{"i": {InstanceID: "i", PassRate: 1.0, Resolved: true}}
	cRes := map[string]EvalResultView{"i": {InstanceID: "i", PassRate: 0.5}}
	out := Compare(RunSummaryView{}, RunSummaryView{}, bRes, cRes)
	require.Len(t, out.LostResolved, 1)
	require.Len(t, out.Regressed, 1)
}

func TestCompare_PartialWhenInstancesDiffer(t *testing.T) {
	bRes := map[string]EvalResultView{"a": {InstanceID: "a", PassRate: 1}}
	cRes := map[string]EvalResultView{"b": {InstanceID: "b", PassRate: 1}}
	out := Compare(RunSummaryView{}, RunSummaryView{}, bRes, cRes)
	require.True(t, out.Partial)
	require.Equal(t, []string{"b"}, out.MissingInBase)
	require.Equal(t, []string{"a"}, out.MissingInCand)
}

func TestCompare_FailureCategoriesAddedAndCleared(t *testing.T) {
	base := RunSummaryView{FailureCategories: []string{"compile_error", "wrong_output"}}
	cand := RunSummaryView{FailureCategories: []string{"wrong_output", "timeout"}}
	out := Compare(base, cand, nil, nil)
	require.Equal(t, []string{"timeout"}, out.Categories.Added)
	require.Equal(t, []string{"compile_error"}, out.Categories.Cleared)
}

func TestCompare_NoChangeBelowEpsilon(t *testing.T) {
	bRes := map[string]EvalResultView{"i": {InstanceID: "i", PassRate: 0.5}}
	cRes := map[string]EvalResultView{"i": {InstanceID: "i", PassRate: 0.5005}} // < 0.001
	out := Compare(RunSummaryView{}, RunSummaryView{}, bRes, cRes)
	require.Empty(t, out.Improved)
	require.Empty(t, out.Regressed)
}
