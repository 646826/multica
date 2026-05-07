package benchmark

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputePromptHash_Deterministic(t *testing.T) {
	in := PromptHashInput{
		AgentName:    "ProgramBenchRunner",
		Model:        "claude-opus-4-7",
		PromptSource: "# system\nyou are a benchmark runner\n",
		AttachedSkills: []SkillRef{
			{Slug: "tar-pack", Version: "1.0.0"},
			{Slug: "verify-executable", Version: "0.3.0"},
		},
	}
	a := ComputePromptHash(in)
	b := ComputePromptHash(in)
	require.Equal(t, a, b)
	require.Len(t, a, 64) // sha256 hex
	require.True(t, strings.IndexFunc(a, func(r rune) bool {
		return !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'))
	}) == -1, "lowercase hex only")
}

func TestComputePromptHash_OrderingOfSkillsDoesNotMatter(t *testing.T) {
	a := ComputePromptHash(PromptHashInput{
		AgentName: "X", Model: "m", PromptSource: "p",
		AttachedSkills: []SkillRef{{Slug: "a", Version: "1"}, {Slug: "b", Version: "2"}},
	})
	b := ComputePromptHash(PromptHashInput{
		AgentName: "X", Model: "m", PromptSource: "p",
		AttachedSkills: []SkillRef{{Slug: "b", Version: "2"}, {Slug: "a", Version: "1"}},
	})
	require.Equal(t, a, b)
}

func TestComputePromptHash_DifferentForDifferentInputs(t *testing.T) {
	base := PromptHashInput{AgentName: "X", Model: "m", PromptSource: "p"}

	cases := []struct {
		name string
		mut  func(p *PromptHashInput)
	}{
		{"different agent name", func(p *PromptHashInput) { p.AgentName = "Y" }},
		{"different model", func(p *PromptHashInput) { p.Model = "claude-opus-4-6" }},
		{"different prompt source - whitespace counts",
			func(p *PromptHashInput) { p.PromptSource = "p " /* trailing space */ }},
		{"added skill", func(p *PromptHashInput) {
			p.AttachedSkills = []SkillRef{{Slug: "s", Version: "1"}}
		}},
		{"different skill version",
			func(p *PromptHashInput) { p.AttachedSkills = []SkillRef{{Slug: "s", Version: "2"}} }},
	}

	baseHash := ComputePromptHash(base)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mut := base
			c.mut(&mut)
			require.NotEqual(t, baseHash, ComputePromptHash(mut))
		})
	}
}
