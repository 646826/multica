package benchmark

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service/benchmark/adapter"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Service-layer error sentinels. Callers can use errors.Is to distinguish
// validation/conflict errors from infrastructure errors.
var (
	ErrSuiteInstanceListEmpty = errors.New("benchmark: suite instance list cannot be empty")
	ErrSuiteSlugTaken         = errors.New("benchmark: suite slug already used in workspace")
	ErrSuiteNotFound          = errors.New("benchmark: suite not found")
	ErrSuiteAdapterUnknown    = errors.New("benchmark: suite adapter not registered")
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
	Description string
	CreatedAt   pgtype.Timestamptz
	CreatedBy   pgtype.UUID
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
	return Suite{
		ID:          r.ID,
		WorkspaceID: r.WorkspaceID,
		Slug:        r.Slug,
		DisplayName: r.DisplayName,
		AdapterKind: r.AdapterKind,
		InstanceIDs: r.InstanceIds,
		Description: r.Description,
		CreatedAt:   r.CreatedAt,
		CreatedBy:   r.CreatedBy,
	}
}
