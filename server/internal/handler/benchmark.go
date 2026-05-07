package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service/benchmark"
	"github.com/multica-ai/multica/server/internal/util"
)

// BenchmarkDeps wires the benchmark service layer into the HTTP handler.
// Profiles is included now (T10) so T11 can plug profile methods into the
// same handler without changing the constructor signature.
type BenchmarkDeps struct {
	Suites   *benchmark.SuiteService
	Profiles *benchmark.ProfileService
}

// BenchmarkHandler exposes /api/benchmarks/* routes. It is a sibling of
// the main *Handler — kept separate so the benchmark feature can be wired
// into the router (T12) and removed independently if rolled back.
type BenchmarkHandler struct {
	deps BenchmarkDeps
}

// NewBenchmarkHandler constructs a BenchmarkHandler from its dependencies.
func NewBenchmarkHandler(deps BenchmarkDeps) *BenchmarkHandler {
	return &BenchmarkHandler{deps: deps}
}

// SuiteResponse is the JSON shape for a benchmark_suite at the handler boundary.
type SuiteResponse struct {
	ID          string   `json:"id"`
	WorkspaceID string   `json:"workspace_id"`
	Slug        string   `json:"slug"`
	DisplayName string   `json:"display_name"`
	AdapterKind string   `json:"adapter_kind"`
	InstanceIDs []string `json:"instance_ids"`
	Description string   `json:"description"`
	CreatedAt   string   `json:"created_at"`
	CreatedBy   string   `json:"created_by"`
}

func suiteToResponse(s benchmark.Suite) SuiteResponse {
	instances := s.InstanceIDs
	if instances == nil {
		instances = []string{}
	}
	return SuiteResponse{
		ID:          util.UUIDToString(s.ID),
		WorkspaceID: util.UUIDToString(s.WorkspaceID),
		Slug:        s.Slug,
		DisplayName: s.DisplayName,
		AdapterKind: s.AdapterKind,
		InstanceIDs: instances,
		Description: s.Description,
		CreatedAt:   util.TimestampToString(s.CreatedAt),
		CreatedBy:   util.UUIDToString(s.CreatedBy),
	}
}

// createSuiteRequest is the inbound JSON payload for POST /api/benchmarks/suites.
type createSuiteRequest struct {
	Slug        string   `json:"slug"`
	DisplayName string   `json:"display_name"`
	AdapterKind string   `json:"adapter_kind"`
	InstanceIDs []string `json:"instance_ids"`
	Description string   `json:"description"`
}

// resolveBenchmarkContext returns (workspaceUUID, userUUID, ok).
// On failure it has already written a 4xx response.
//
// Mirrors the resolution chain used by Handler.requireUserID +
// Handler.resolveWorkspaceID, minus the slug → DB lookup. The benchmark
// routes will be mounted behind the workspace middleware (T12), so the
// context fast path covers the slug case in production; the X-User-ID /
// X-Workspace-ID headers and ?workspace_id query keep the CLI/daemon
// compatibility used elsewhere in the package.
func resolveBenchmarkContext(w http.ResponseWriter, r *http.Request) (wsUUID, userUUID pgtype.UUID, ok bool) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "user not authenticated")
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	uid, err := util.ParseUUID(userID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return pgtype.UUID{}, pgtype.UUID{}, false
	}

	workspaceID := workspaceIDFromHeaders(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace_id")
		return pgtype.UUID{}, pgtype.UUID{}, false
	}

	return ws, uid, true
}

// CreateSuite handles POST /api/benchmarks/suites.
func (h *BenchmarkHandler) CreateSuite(w http.ResponseWriter, r *http.Request) {
	var req createSuiteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	wsUUID, userUUID, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}

	suite, err := h.deps.Suites.Create(r.Context(), benchmark.CreateSuiteInput{
		WorkspaceID: wsUUID,
		Slug:        req.Slug,
		DisplayName: req.DisplayName,
		AdapterKind: req.AdapterKind,
		InstanceIDs: req.InstanceIDs,
		Description: req.Description,
		CreatedBy:   userUUID,
	})
	switch {
	case errors.Is(err, benchmark.ErrSuiteInstanceListEmpty):
		writeError(w, http.StatusBadRequest, "instance_list_empty")
		return
	case errors.Is(err, benchmark.ErrSuiteSlugTaken):
		writeError(w, http.StatusConflict, "slug_taken")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "failed to create suite")
		return
	}

	writeJSON(w, http.StatusCreated, suiteToResponse(suite))
}

// ListSuites handles GET /api/benchmarks/suites.
func (h *BenchmarkHandler) ListSuites(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}
	suites, err := h.deps.Suites.List(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list suites")
		return
	}
	items := make([]SuiteResponse, 0, len(suites))
	for _, s := range suites {
		items = append(items, suiteToResponse(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// GetSuite handles GET /api/benchmarks/suites/{id}.
func (h *BenchmarkHandler) GetSuite(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	idUUID, ok := parseUUIDOrBadRequest(w, id, "suite id")
	if !ok {
		return
	}
	suite, err := h.deps.Suites.Get(r.Context(), idUUID, wsUUID)
	if errors.Is(err, benchmark.ErrSuiteNotFound) {
		writeError(w, http.StatusNotFound, "suite not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load suite")
		return
	}
	writeJSON(w, http.StatusOK, suiteToResponse(suite))
}

// DeleteSuite handles DELETE /api/benchmarks/suites/{id}.
func (h *BenchmarkHandler) DeleteSuite(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	idUUID, ok := parseUUIDOrBadRequest(w, id, "suite id")
	if !ok {
		return
	}
	err := h.deps.Suites.Delete(r.Context(), idUUID, wsUUID)
	if errors.Is(err, benchmark.ErrSuiteNotFound) {
		writeError(w, http.StatusNotFound, "suite not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete suite")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// workspaceIDFromHeaders mirrors the priority order used by
// middleware.ResolveWorkspaceIDFromRequest, but without a slug → DB lookup.
// BenchmarkHandler routes will be wired behind the workspace middleware
// (T12), which puts the resolved UUID into r.Context() — so the context
// fast path covers the slug case in production. The header/query fallbacks
// match the CLI/daemon compat path used elsewhere in the package.
func workspaceIDFromHeaders(r *http.Request) string {
	if id := ctxWorkspaceID(r.Context()); id != "" {
		return id
	}
	if id := r.Header.Get("X-Workspace-ID"); id != "" {
		return id
	}
	return r.URL.Query().Get("workspace_id")
}
