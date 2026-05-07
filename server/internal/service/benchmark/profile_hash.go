package benchmark

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// SkillRef is the canonical reference shape used in profile hashing.
// Skill ids are not stable across workspaces, so slug+version is the identity.
type SkillRef struct {
	Slug    string `json:"slug"`
	Version string `json:"version"`
}

// PromptHashInput is the canonical, normalized profile content that goes into
// the hash. Anything not in this struct is metadata and does not affect hash.
type PromptHashInput struct {
	AgentName      string
	Model          string
	PromptSource   string
	AttachedSkills []SkillRef
}

// ComputePromptHash returns sha256(canonical_json(input)) hex-encoded.
// Order of attached skills does not affect the result.
func ComputePromptHash(in PromptHashInput) string {
	skills := append([]SkillRef(nil), in.AttachedSkills...)
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].Slug != skills[j].Slug {
			return skills[i].Slug < skills[j].Slug
		}
		return skills[i].Version < skills[j].Version
	})

	canonical := struct {
		AgentName      string     `json:"agent_name"`
		Model          string     `json:"model"`
		PromptSource   string     `json:"prompt_source"`
		AttachedSkills []SkillRef `json:"attached_skills"`
	}{in.AgentName, in.Model, in.PromptSource, skills}

	body, err := json.Marshal(canonical)
	if err != nil {
		// Inputs are plain strings and slices of strings. json.Marshal cannot
		// fail on these. If it ever does, that is a runtime invariant break.
		panic(err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
