package benchmark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service/benchmark/adapter"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Service-layer error sentinels. Callers can use errors.Is to distinguish
// validation/conflict errors from infrastructure errors.
var (
	ErrSuiteInstanceListEmpty       = errors.New("benchmark: suite instance list cannot be empty")
	ErrSuiteSlugTaken               = errors.New("benchmark: suite slug already used in workspace")
	ErrSuiteNotFound                = errors.New("benchmark: suite not found")
	ErrSuiteAdapterUnknown          = errors.New("benchmark: suite adapter not registered")
	ErrReplayReferenceSolutionEmpty = errors.New("benchmark: replay instance missing reference_solution")
	ErrReplaySourceIssueNotFound    = errors.New("benchmark: replay source issue not found in workspace")
)

// Suite is the service-layer representation of benchmark_suite. It mirrors
// the generated db.BenchmarkSuite struct but stays in the benchmark package
// so handlers and other services can depend on this layer rather than on
// sqlc types directly.
type Suite struct {
	ID          pgtype.UUID
	WorkspaceID pgtype.UUID
	Slug        string
	DisplayName string
	AdapterKind string
	InstanceIDs []string
	// InstanceMetaOverrides is a per-instance map of opaque adapter meta blobs
	// captured at suite-creation time. Used by adapters (e.g. multica_replay)
	// that need to freeze the exact prompt/reference for an instance instead
	// of re-resolving from a live catalog. Empty map for adapters that do not
	// use overrides.
	InstanceMetaOverrides map[string]json.RawMessage
	Description           string
	CreatedAt             pgtype.Timestamptz
	CreatedBy             pgtype.UUID
}

// CreateSuiteInput is the validated input to SuiteService.Create.
type CreateSuiteInput struct {
	WorkspaceID pgtype.UUID
	Slug        string
	DisplayName string
	AdapterKind string
	InstanceIDs []string
	Description string
	CreatedBy   pgtype.UUID
}

// SuiteService is a thin CRUD wrapper around the sqlc-generated benchmark_suite
// queries. It validates inputs, normalizes Postgres errors into typed sentinels,
// and converts generated row types to the package-local Suite type.
type SuiteService struct {
	q *db.Queries
}

// NewSuiteService constructs a SuiteService bound to the given query set.
func NewSuiteService(q *db.Queries) *SuiteService {
	return &SuiteService{q: q}
}

// Create inserts a new benchmark suite.
// Returns ErrSuiteInstanceListEmpty if the instance list is empty,
// or ErrSuiteSlugTaken if (workspace_id, slug) already exists.
func (s *SuiteService) Create(ctx context.Context, in CreateSuiteInput) (Suite, error) {
	if len(in.InstanceIDs) == 0 {
		return Suite{}, ErrSuiteInstanceListEmpty
	}
	in.Slug = strings.TrimSpace(in.Slug)

	row, err := s.q.CreateBenchmarkSuite(ctx, db.CreateBenchmarkSuiteParams{
		WorkspaceID: in.WorkspaceID,
		Slug:        in.Slug,
		DisplayName: in.DisplayName,
		AdapterKind: in.AdapterKind,
		InstanceIds: in.InstanceIDs,
		Description: in.Description,
		CreatedBy:   in.CreatedBy,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Suite{}, ErrSuiteSlugTaken
		}
		return Suite{}, err
	}
	return rowToSuite(row), nil
}

// Get fetches a single suite by id, scoped to the workspace.
// Returns ErrSuiteNotFound when the row does not exist.
func (s *SuiteService) Get(ctx context.Context, id, workspaceID pgtype.UUID) (Suite, error) {
	row, err := s.q.GetBenchmarkSuite(ctx, db.GetBenchmarkSuiteParams{
		ID:          id,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Suite{}, ErrSuiteNotFound
	}
	if err != nil {
		return Suite{}, err
	}
	return rowToSuite(row), nil
}

// List returns all suites for a workspace, newest first (per the sqlc query).
func (s *SuiteService) List(ctx context.Context, workspaceID pgtype.UUID) ([]Suite, error) {
	rows, err := s.q.ListBenchmarkSuites(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]Suite, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToSuite(r))
	}
	return out, nil
}

// Delete removes a suite scoped to the workspace. Returns ErrSuiteNotFound if no row matches.
func (s *SuiteService) Delete(ctx context.Context, id, workspaceID pgtype.UUID) error {
	n, err := s.q.DeleteBenchmarkSuite(ctx, db.DeleteBenchmarkSuiteParams{
		ID:          id,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrSuiteNotFound
	}
	return nil
}

// SuiteSyncResult is the outcome of SuiteService.SyncFromCatalog. v1 is
// informational only — it does not mutate the suite. Callers can compare the
// resolved/unresolved buckets against the live suite to decide whether the
// instance set has drifted.
type SuiteSyncResult struct {
	SuiteID     pgtype.UUID
	AdapterKind string
	Resolved    []string
	Unresolved  []string
}

// SyncFromCatalog re-resolves a suite's instance_ids against the registered
// Catalog for its adapter_kind. It does not mutate the suite — every id is
// either appended to Resolved (Catalog.Resolve returned no error) or to
// Unresolved (Resolve returned any error). Returns ErrSuiteNotFound when no
// row matches (id, workspaceID), and ErrSuiteAdapterUnknown when the suite
// references an adapter the registry does not know about.
func (s *SuiteService) SyncFromCatalog(
	ctx context.Context,
	id, workspaceID pgtype.UUID,
	registry *adapter.Registry,
) (SuiteSyncResult, error) {
	row, err := s.q.GetBenchmarkSuite(ctx, db.GetBenchmarkSuiteParams{
		ID:          id,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SuiteSyncResult{}, ErrSuiteNotFound
	}
	if err != nil {
		return SuiteSyncResult{}, err
	}

	cat, ok := registry.Catalog(row.AdapterKind)
	if !ok {
		return SuiteSyncResult{}, fmt.Errorf("%w: %s", ErrSuiteAdapterUnknown, row.AdapterKind)
	}

	out := SuiteSyncResult{
		SuiteID:     row.ID,
		AdapterKind: row.AdapterKind,
		Resolved:    []string{},
		Unresolved:  []string{},
	}
	for _, instanceID := range row.InstanceIds {
		if _, err := cat.Resolve(ctx, instanceID); err != nil {
			out.Unresolved = append(out.Unresolved, instanceID)
		} else {
			out.Resolved = append(out.Resolved, instanceID)
		}
	}
	return out, nil
}

func rowToSuite(r db.BenchmarkSuite) Suite {
	overrides := map[string]json.RawMessage{}
	// instance_meta_overrides is JSONB NOT NULL DEFAULT '{}' (see migration
	// 071), so every row carries at least an empty object. A decode failure
	// here would mean a corrupt blob — fall back to empty rather than panicking
	// the caller, since rowToSuite is also used on read paths where surfacing
	// the underlying error is awkward.
	if len(r.InstanceMetaOverrides) > 0 {
		_ = json.Unmarshal(r.InstanceMetaOverrides, &overrides)
	}
	return Suite{
		ID:                    r.ID,
		WorkspaceID:           r.WorkspaceID,
		Slug:                  r.Slug,
		DisplayName:           r.DisplayName,
		AdapterKind:           r.AdapterKind,
		InstanceIDs:           r.InstanceIds,
		InstanceMetaOverrides: overrides,
		Description:           r.Description,
		CreatedAt:             r.CreatedAt,
		CreatedBy:             r.CreatedBy,
	}
}

// ReplayInstanceInput is the per-instance input to CreateReplaySuite.
// SourceIssueID points at a Multica issue in the same workspace; the issue's
// title and description are captured into the instance_meta_overrides blob
// so the suite remains stable even if the source issue is later edited.
type ReplayInstanceInput struct {
	SourceIssueID     pgtype.UUID
	ReferenceSolution string
	ReferencePRURL    string
}

// CreateReplaySuiteInput is the validated input to SuiteService.CreateReplaySuite.
type CreateReplaySuiteInput struct {
	WorkspaceID pgtype.UUID
	Slug        string
	DisplayName string
	Description string
	Instances   []ReplayInstanceInput
	CreatedBy   pgtype.UUID
}

// CreateReplaySuite materializes a multica_replay benchmark suite from a list
// of completed Multica issues. For each instance the source issue is read
// from the database and its title/description are frozen into the suite's
// instance_meta_overrides blob — keyed by the synthetic instance_id
// (`multica-issue:<uuid>`) — so the run dispatcher can compose tasks without
// touching the live issue row.
//
// Returns ErrSuiteInstanceListEmpty when the input has no instances,
// ErrReplayReferenceSolutionEmpty when any instance lacks a reference,
// ErrReplaySourceIssueNotFound when an issue id is unknown in the workspace,
// and ErrSuiteSlugTaken on (workspace_id, slug) conflict.
func (s *SuiteService) CreateReplaySuite(ctx context.Context, in CreateReplaySuiteInput) (Suite, error) {
	if len(in.Instances) == 0 {
		return Suite{}, ErrSuiteInstanceListEmpty
	}
	in.Slug = strings.TrimSpace(in.Slug)

	ids := make([]string, 0, len(in.Instances))
	overrides := make(map[string]adapter.ReplayInstanceMeta, len(in.Instances))
	for _, inst := range in.Instances {
		if strings.TrimSpace(inst.ReferenceSolution) == "" {
			return Suite{}, ErrReplayReferenceSolutionEmpty
		}
		issueRow, err := s.q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
			ID:          inst.SourceIssueID,
			WorkspaceID: in.WorkspaceID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return Suite{}, fmt.Errorf("%w: %s", ErrReplaySourceIssueNotFound, util.UUIDToString(inst.SourceIssueID))
		}
		if err != nil {
			return Suite{}, fmt.Errorf("benchmark: fetch replay source issue: %w", err)
		}
		instID := "multica-issue:" + util.UUIDToString(inst.SourceIssueID)
		ids = append(ids, instID)
		desc := ""
		if issueRow.Description.Valid {
			desc = issueRow.Description.String
		}
		overrides[instID] = adapter.ReplayInstanceMeta{
			SourceIssueID:          util.UUIDToString(inst.SourceIssueID),
			SourceIssueNumber:      issueRow.Number,
			SourceIssueTitle:       issueRow.Title,
			SourceIssueDescription: desc,
			ReferenceSolution:      inst.ReferenceSolution,
			ReferencePRURL:         inst.ReferencePRURL,
		}
	}

	overridesJSON, err := json.Marshal(overrides)
	if err != nil {
		return Suite{}, fmt.Errorf("benchmark: marshal replay overrides: %w", err)
	}

	row, err := s.q.CreateBenchmarkReplaySuite(ctx, db.CreateBenchmarkReplaySuiteParams{
		WorkspaceID:           in.WorkspaceID,
		Slug:                  in.Slug,
		DisplayName:           in.DisplayName,
		InstanceIds:           ids,
		InstanceMetaOverrides: overridesJSON,
		Description:           in.Description,
		CreatedBy:             in.CreatedBy,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Suite{}, ErrSuiteSlugTaken
		}
		return Suite{}, err
	}
	return rowToSuite(row), nil
}
