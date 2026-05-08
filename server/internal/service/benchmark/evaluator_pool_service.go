package benchmark

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Service-layer error sentinels for the evaluator pool. Callers can use
// errors.Is to distinguish auth/validation errors from infrastructure errors.
var (
	ErrEvaluatorPoolTokenInvalid  = errors.New("benchmark: evaluator pool token invalid")
	ErrEvaluatorPoolTokenRevoked  = errors.New("benchmark: evaluator pool token revoked")
	ErrEvaluatorPoolTokenNotFound = errors.New("benchmark: evaluator pool token not found")
)

// EvaluatorPoolToken is the service-layer representation of
// evaluator_pool_token. It deliberately omits TokenHash — that column is
// internal-only and never crosses the service boundary.
type EvaluatorPoolToken struct {
	ID          pgtype.UUID
	WorkspaceID pgtype.UUID
	TokenPrefix string
	DisplayName string
	CreatedAt   pgtype.Timestamptz
	CreatedBy   pgtype.UUID
	LastUsedAt  pgtype.Timestamptz
	RevokedAt   pgtype.Timestamptz
}

// CreateEvaluatorPoolTokenInput is the validated input to
// EvaluatorPoolService.Create.
type CreateEvaluatorPoolTokenInput struct {
	WorkspaceID pgtype.UUID
	DisplayName string
	CreatedBy   pgtype.UUID
}

// EvaluatorPoolService is a thin wrapper around the sqlc-generated
// evaluator_pool_token queries. It mints, lists, revokes, and verifies tokens
// used by the evaluator daemon pool.
type EvaluatorPoolService struct {
	q *db.Queries
}

// NewEvaluatorPoolService constructs an EvaluatorPoolService bound to the
// given query set.
func NewEvaluatorPoolService(q *db.Queries) *EvaluatorPoolService {
	return &EvaluatorPoolService{q: q}
}

// Create mints a new evaluator pool token. It returns the persisted record
// AND the plain-text token, which is only available at creation time —
// callers must surface it to the user immediately because only the SHA-256
// hash is stored.
func (s *EvaluatorPoolService) Create(ctx context.Context, in CreateEvaluatorPoolTokenInput) (EvaluatorPoolToken, string, error) {
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return EvaluatorPoolToken{}, "", err
	}
	plain := "evp_" + hex.EncodeToString(secret) // 4 + 48 = 52 chars
	prefix := plain[:12]                         // "evp_xxxxxxxx"
	hash := hashToken(plain)

	row, err := s.q.CreateEvaluatorPoolToken(ctx, db.CreateEvaluatorPoolTokenParams{
		WorkspaceID: in.WorkspaceID,
		TokenPrefix: prefix,
		TokenHash:   hash,
		DisplayName: in.DisplayName,
		CreatedBy:   in.CreatedBy,
	})
	if err != nil {
		return EvaluatorPoolToken{}, "", err
	}
	return rowToEvaluatorPoolToken(row), plain, nil
}

// List returns all tokens for a workspace, newest first (per the sqlc query).
// The TokenHash column is never returned to callers.
func (s *EvaluatorPoolService) List(ctx context.Context, workspaceID pgtype.UUID) ([]EvaluatorPoolToken, error) {
	rows, err := s.q.ListEvaluatorPoolTokens(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]EvaluatorPoolToken, 0, len(rows))
	for _, r := range rows {
		out = append(out, EvaluatorPoolToken{
			ID:          r.ID,
			WorkspaceID: r.WorkspaceID,
			TokenPrefix: r.TokenPrefix,
			DisplayName: r.DisplayName,
			CreatedAt:   r.CreatedAt,
			CreatedBy:   r.CreatedBy,
			LastUsedAt:  r.LastUsedAt,
			RevokedAt:   r.RevokedAt,
		})
	}
	return out, nil
}

// Revoke marks the token revoked. The underlying sqlc query is :exec, which
// gives no rowcount, so a successful return does not necessarily mean a row
// was updated — callers that need not-found semantics should follow up with
// a List or a hash lookup.
func (s *EvaluatorPoolService) Revoke(ctx context.Context, id, workspaceID pgtype.UUID) error {
	return s.q.RevokeEvaluatorPoolToken(ctx, db.RevokeEvaluatorPoolTokenParams{
		ID:          id,
		WorkspaceID: workspaceID,
	})
}

// Verify is the auth path. It hashes the supplied plain-text token, looks
// it up, and returns the row if it is valid and not revoked. last_used_at
// is touched as a best-effort side effect — failures there do not fail the
// auth call.
func (s *EvaluatorPoolService) Verify(ctx context.Context, plain string) (EvaluatorPoolToken, error) {
	if !strings.HasPrefix(plain, "evp_") {
		return EvaluatorPoolToken{}, ErrEvaluatorPoolTokenInvalid
	}
	hash := hashToken(plain)
	row, err := s.q.GetEvaluatorPoolTokenByHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return EvaluatorPoolToken{}, ErrEvaluatorPoolTokenInvalid
	}
	if err != nil {
		return EvaluatorPoolToken{}, err
	}
	if row.RevokedAt.Valid {
		return EvaluatorPoolToken{}, ErrEvaluatorPoolTokenRevoked
	}
	_ = s.q.TouchEvaluatorPoolToken(ctx, row.ID) // best-effort
	return rowToEvaluatorPoolToken(row), nil
}

func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func rowToEvaluatorPoolToken(r db.EvaluatorPoolToken) EvaluatorPoolToken {
	return EvaluatorPoolToken{
		ID:          r.ID,
		WorkspaceID: r.WorkspaceID,
		TokenPrefix: r.TokenPrefix,
		DisplayName: r.DisplayName,
		CreatedAt:   r.CreatedAt,
		CreatedBy:   r.CreatedBy,
		LastUsedAt:  r.LastUsedAt,
		RevokedAt:   r.RevokedAt,
	}
}
