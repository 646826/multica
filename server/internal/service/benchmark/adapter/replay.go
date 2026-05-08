package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
)

const replayKind = "multica_replay"
const replayResolvedThreshold = 0.85

// ReplayInstanceMeta is the adapter-opaque blob stored on benchmark_task.instance_meta.
type ReplayInstanceMeta struct {
	SourceIssueID          string `json:"source_issue_id"`
	SourceIssueNumber      int32  `json:"source_issue_number"`
	SourceIssueTitle       string `json:"source_issue_title"`
	SourceIssueDescription string `json:"source_issue_description"`
	ReferenceSolution      string `json:"reference_solution"`
	ReferencePRURL         string `json:"reference_pr_url,omitempty"`
}

// ReplayCatalog is a pass-through. Resolution happens at suite-creation time via
// CreateReplaySuite, which stores the full meta in benchmark_suite.instance_meta_overrides.
// At run time the dispatcher consults the override map FIRST and skips Catalog.Resolve.
// This Catalog impl exists for shape-compat with the Registry, but its methods are
// effectively no-ops (and explicitly error on List).
type ReplayCatalog struct{}

func NewReplayCatalog() *ReplayCatalog { return &ReplayCatalog{} }
func (c *ReplayCatalog) Kind() string  { return replayKind }

func (c *ReplayCatalog) Resolve(ctx context.Context, instanceID string) (Instance, error) {
	if _, err := parseReplayInstanceID(instanceID); err != nil {
		return Instance{}, err
	}
	// Return shape-only. Real meta comes from suite override map.
	return Instance{ID: instanceID, Meta: nil}, nil
}

func (c *ReplayCatalog) List(ctx context.Context, _ ListFilter) ([]Instance, error) {
	return nil, errors.New("multica_replay: List not supported; use suite-creation flow")
}

// ReplayComposer renders the source issue's title+description as the task prompt.
type ReplayComposer struct{}

func NewReplayComposer() *ReplayComposer { return &ReplayComposer{} }
func (c *ReplayComposer) Kind() string   { return replayKind }

func (c *ReplayComposer) Compose(ctx context.Context, in ComposeInput) (ComposeOutput, error) {
	var meta ReplayInstanceMeta
	if len(in.Instance.Meta) > 0 {
		if err := json.Unmarshal(in.Instance.Meta, &meta); err != nil {
			return ComposeOutput{}, fmt.Errorf("decode instance_meta: %w", err)
		}
	}

	desc := fmt.Sprintf(
		"# %s — %s\n\n"+
			"**Replay benchmark.** This task replays Multica issue #%d (%q). "+
			"Solve it as a fresh task — do not consult the original PR.\n\n"+
			"## Source description\n\n%s\n\n"+
			"## Submission contract\n"+
			"Attach a unified diff named exactly `solution.patch` to a comment on this issue. "+
			"The diff should apply from the repository root with `git apply`.\n",
		in.Run.DisplayName, in.Task.InstanceID,
		meta.SourceIssueNumber, meta.SourceIssueTitle, meta.SourceIssueDescription,
	)

	return ComposeOutput{
		Title:              fmt.Sprintf("[Replay] %s · %s", in.Run.DisplayName, meta.SourceIssueTitle),
		Description:        desc,
		AssigneeAgentName:  "",
		SubmissionFilename: "solution.patch",
	}, nil
}

// ReplayParser accepts solution.patch (or solution.diff) up to 10 MiB.
type ReplayParser struct{}

func NewReplayParser() *ReplayParser { return &ReplayParser{} }
func (p *ReplayParser) Kind() string  { return replayKind }

func (p *ReplayParser) Validate(ctx context.Context, att Attachment) error {
	if att.Filename != "solution.patch" && att.Filename != "solution.diff" {
		return errors.New("submission filename must be solution.patch or solution.diff")
	}
	if att.SizeBytes > 10*1024*1024 {
		return fmt.Errorf("submission too large: %d bytes (max 10 MiB)", att.SizeBytes)
	}
	return nil
}

// ReplayEvaluator scores submission against reference via line-Jaccard similarity.
// Pure Go — runs in-process; no Docker needed.
type ReplayEvaluator struct{}

func NewReplayEvaluator() *ReplayEvaluator { return &ReplayEvaluator{} }
func (e *ReplayEvaluator) Kind() string    { return replayKind }

func (e *ReplayEvaluator) Evaluate(ctx context.Context, in EvaluateInput) (EvaluateOutput, error) {
	submitted, err := os.ReadFile(in.SubmissionPath)
	if err != nil {
		return EvaluateOutput{}, fmt.Errorf("read submission: %w", err)
	}

	var meta ReplayInstanceMeta
	if len(in.Instance.Meta) > 0 {
		_ = json.Unmarshal(in.Instance.Meta, &meta)
	}
	if meta.ReferenceSolution == "" {
		return EvaluateOutput{}, errors.New("reference_solution missing from instance_meta")
	}

	sim := jaccardLines(string(submitted), meta.ReferenceSolution)
	out := EvaluateOutput{
		Resolved:    sim >= replayResolvedThreshold,
		PassedTests: int(sim * 1000),
		TotalTests:  1000,
	}
	raw, _ := json.Marshal(map[string]any{
		"similarity":      sim,
		"threshold":       replayResolvedThreshold,
		"reference_lines": len(strings.Split(meta.ReferenceSolution, "\n")),
		"submitted_lines": len(strings.Split(string(submitted), "\n")),
	})
	out.RawEvalJSON = raw
	if !out.Resolved {
		out.FailedCategories = []string{categorize(sim)}
	}
	return out, nil
}

// jaccardLines: line-level Jaccard similarity, ignoring whitespace-only lines
// and unified-diff metadata lines.
func jaccardLines(a, b string) float64 {
	setA := lineSet(a)
	setB := lineSet(b)
	if len(setA) == 0 && len(setB) == 0 {
		return 1.0
	}
	inter := 0
	for line := range setA {
		if _, ok := setB[line]; ok {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func lineSet(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") ||
			strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "diff --git") ||
			strings.HasPrefix(line, "index ") {
			continue
		}
		out[line] = struct{}{}
	}
	return out
}

func categorize(sim float64) string {
	switch {
	case sim < 0.10:
		return "diff_unrelated"
	case sim < 0.40:
		return "diff_partial"
	case sim < replayResolvedThreshold:
		return "diff_close_but_below_threshold"
	default:
		return "diff_match"
	}
}

func parseReplayInstanceID(s string) (uuid.UUID, error) {
	const prefix = "multica-issue:"
	if !strings.HasPrefix(s, prefix) {
		return uuid.Nil, fmt.Errorf("replay instance id missing %q prefix", prefix)
	}
	id, err := uuid.Parse(strings.TrimPrefix(s, prefix))
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid uuid in instance id: %w", err)
	}
	return id, nil
}
