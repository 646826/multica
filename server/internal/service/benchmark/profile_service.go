package benchmark

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Service-layer error sentinels for ProfileService.
var (
	ErrProfileNotFound  = errors.New("benchmark: profile not found")
	ErrProfileSlugTaken = errors.New("benchmark: profile slug already used in workspace")
	// ErrCaptureAgent indicates the agent referenced by CaptureProfileInput.AgentID
	// either does not exist or is not visible to the requested workspace.
	ErrCaptureAgent = errors.New("benchmark: agent for capture not found in workspace")
)

// Profile is the service-layer representation of benchmark_agent_profile.
// DuplicateOf is non-nil when another profile in the same workspace was found
// with the same prompt_hash at capture time. Duplicates are saved anyway —
// the field is informational so the caller can warn the user.
type Profile struct {
	ID             pgtype.UUID
	WorkspaceID    pgtype.UUID
	Slug           string
	DisplayName    string
	AgentID        pgtype.UUID
	AgentName      string
	Model          string
	PromptSource   string
	PromptHash     string
	AttachedSkills []SkillRef
	CapturedAt     pgtype.Timestamptz
	CapturedBy     pgtype.UUID
	DuplicateOf    *pgtype.UUID
}

// CaptureProfileInput is the validated input to ProfileService.Capture.
// AgentName / Model / PromptSource are NOT in the input — Capture reads the
// live agent row so the snapshot is authoritative.
type CaptureProfileInput struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	Slug        string
	DisplayName string
	CapturedBy  pgtype.UUID
}

// ProfileService captures immutable agent snapshots into benchmark_agent_profile.
// Snapshots are content-addressed by prompt_hash; duplicate-content captures are
// allowed (and flagged via DuplicateOf) so the user can label/track them, while
// duplicate slugs are rejected.
type ProfileService struct {
	q *db.Queries
}

// NewProfileService constructs a ProfileService bound to the given query set.
func NewProfileService(q *db.Queries) *ProfileService {
	return &ProfileService{q: q}
}

// Capture reads the live agent + its attached skills, computes the canonical
// prompt hash, checks for an existing profile with the same hash in the
// workspace (informational), and inserts a new snapshot row.
func (s *ProfileService) Capture(ctx context.Context, in CaptureProfileInput) (Profile, error) {
	in.Slug = strings.TrimSpace(in.Slug)

	agent, err := s.q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID:          in.AgentID,
		WorkspaceID: in.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrCaptureAgent
	}
	if err != nil {
		return Profile{}, err
	}

	skills, err := s.collectAttachedSkills(ctx, agent.ID)
	if err != nil {
		return Profile{}, err
	}

	model := ""
	if agent.Model.Valid {
		model = agent.Model.String
	}

	hash := ComputePromptHash(PromptHashInput{
		AgentName:      agent.Name,
		Model:          model,
		PromptSource:   agent.Instructions,
		AttachedSkills: skills,
	})

	dup, err := s.findDuplicate(ctx, in.WorkspaceID, hash)
	if err != nil {
		return Profile{}, err
	}

	skillsJSON, err := json.Marshal(skills)
	if err != nil {
		// SkillRef is a tiny string-only struct; Marshal cannot fail.
		return Profile{}, err
	}

	row, err := s.q.CreateBenchmarkProfile(ctx, db.CreateBenchmarkProfileParams{
		WorkspaceID:    in.WorkspaceID,
		Slug:           in.Slug,
		DisplayName:    in.DisplayName,
		AgentID:        agent.ID,
		AgentName:      agent.Name,
		Model:          model,
		PromptSource:   agent.Instructions,
		PromptHash:     hash,
		AttachedSkills: skillsJSON,
		CapturedBy:     in.CapturedBy,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Profile{}, ErrProfileSlugTaken
		}
		return Profile{}, err
	}

	return rowToProfile(row, skills, dup), nil
}

// Get fetches a single profile by id, scoped to the workspace.
func (s *ProfileService) Get(ctx context.Context, id, workspaceID pgtype.UUID) (Profile, error) {
	row, err := s.q.GetBenchmarkProfile(ctx, db.GetBenchmarkProfileParams{
		ID:          id,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrProfileNotFound
	}
	if err != nil {
		return Profile{}, err
	}
	skills, err := decodeSkills(row.AttachedSkills)
	if err != nil {
		return Profile{}, err
	}
	return rowToProfile(row, skills, nil), nil
}

// List returns all profiles for a workspace, newest first.
func (s *ProfileService) List(ctx context.Context, workspaceID pgtype.UUID) ([]Profile, error) {
	rows, err := s.q.ListBenchmarkProfiles(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]Profile, 0, len(rows))
	for _, r := range rows {
		skills, err := decodeSkills(r.AttachedSkills)
		if err != nil {
			return nil, err
		}
		out = append(out, rowToProfile(r, skills, nil))
	}
	return out, nil
}

// Delete removes a profile scoped to the workspace. Returns ErrProfileNotFound
// if no row matches (T08 fix-up pattern).
func (s *ProfileService) Delete(ctx context.Context, id, workspaceID pgtype.UUID) error {
	n, err := s.q.DeleteBenchmarkProfile(ctx, db.DeleteBenchmarkProfileParams{
		ID:          id,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrProfileNotFound
	}
	return nil
}

// collectAttachedSkills loads the skills attached to the agent and reduces
// them to canonical SkillRefs. The skill table has no `version` column, so
// we use updated_at (RFC3339Nano) as the version — a content edit bumps
// updated_at, which yields a different hash and lets duplicate-detection
// notice that "same skills attached" but "skill bodies were edited" is in
// fact a different snapshot.
func (s *ProfileService) collectAttachedSkills(ctx context.Context, agentID pgtype.UUID) ([]SkillRef, error) {
	rows, err := s.q.ListAgentSkills(ctx, agentID)
	if err != nil {
		return nil, err
	}
	out := make([]SkillRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, SkillRef{
			Slug:    r.Name,
			Version: skillVersion(r.UpdatedAt),
		})
	}
	return out, nil
}

func skillVersion(ts pgtype.Timestamptz) string {
	if !ts.Valid {
		return ""
	}
	return ts.Time.UTC().Format(time.RFC3339Nano)
}

// findDuplicate returns a pointer to the id of an existing profile in the
// workspace that shares the given hash, or nil if none exists.
func (s *ProfileService) findDuplicate(ctx context.Context, workspaceID pgtype.UUID, hash string) (*pgtype.UUID, error) {
	row, err := s.q.FindProfileByHash(ctx, db.FindProfileByHashParams{
		WorkspaceID: workspaceID,
		PromptHash:  hash,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id := row.ID
	return &id, nil
}

func decodeSkills(raw []byte) ([]SkillRef, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out []SkillRef
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func rowToProfile(r db.BenchmarkAgentProfile, skills []SkillRef, dup *pgtype.UUID) Profile {
	return Profile{
		ID:             r.ID,
		WorkspaceID:    r.WorkspaceID,
		Slug:           r.Slug,
		DisplayName:    r.DisplayName,
		AgentID:        r.AgentID,
		AgentName:      r.AgentName,
		Model:          r.Model,
		PromptSource:   r.PromptSource,
		PromptHash:     r.PromptHash,
		AttachedSkills: skills,
		CapturedAt:     r.CapturedAt,
		CapturedBy:     r.CapturedBy,
		DuplicateOf:    dup,
	}
}
