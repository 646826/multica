package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
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

// Error codes returned by /api/benchmarks/* handlers.
//
// Frontends switch on these snake_case identifiers; do not change a value
// without coordinating with web/src/api/benchmarks.ts. Prose user-facing
// strings live on the frontend, mapped from these codes.
const (
	errBadBody           = "bad_body"
	errBadID             = "bad_id"
	errInternal          = "internal_error"
	errUnauthenticated   = "unauthenticated"
	errWorkspaceRequired = "workspace_required"
	errBadWorkspaceID    = "bad_workspace_id"
	errBadUserID         = "bad_user_id"
	errInstanceListEmpty = "instance_list_empty"
	errSlugTaken         = "slug_taken"
	errSuiteNotFound     = "suite_not_found"
	errProfileNotFound   = "profile_not_found"
	errAgentNotFound     = "agent_not_found"
)

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
		ID:          uuidToString(s.ID),
		WorkspaceID: uuidToString(s.WorkspaceID),
		Slug:        s.Slug,
		DisplayName: s.DisplayName,
		AdapterKind: s.AdapterKind,
		InstanceIDs: instances,
		Description: s.Description,
		CreatedAt:   timestampToString(s.CreatedAt),
		CreatedBy:   uuidToString(s.CreatedBy),
	}
}

// ProfileResponse is the JSON shape for a benchmark_agent_profile at the
// handler boundary.
//
// DuplicateOf is `omitempty` and only set when the captured profile shares a
// prompt_hash with another profile in the same workspace — informational, the
// snapshot is still saved (see ProfileService.Capture).
type ProfileResponse struct {
	ID             string               `json:"id"`
	WorkspaceID    string               `json:"workspace_id"`
	Slug           string               `json:"slug"`
	DisplayName    string               `json:"display_name"`
	AgentID        string               `json:"agent_id"`
	AgentName      string               `json:"agent_name"`
	Model          string               `json:"model"`
	PromptSource   string               `json:"prompt_source"`
	PromptHash     string               `json:"prompt_hash"`
	AttachedSkills []benchmark.SkillRef `json:"attached_skills"`
	CapturedBy     string               `json:"captured_by"`
	DuplicateOf    *string              `json:"duplicate_of,omitempty"`
}

func profileToResponse(p benchmark.Profile) ProfileResponse {
	skills := p.AttachedSkills
	if skills == nil {
		skills = []benchmark.SkillRef{}
	}
	resp := ProfileResponse{
		ID:             uuidToString(p.ID),
		WorkspaceID:    uuidToString(p.WorkspaceID),
		Slug:           p.Slug,
		DisplayName:    p.DisplayName,
		AgentID:        uuidToString(p.AgentID),
		AgentName:      p.AgentName,
		Model:          p.Model,
		PromptSource:   p.PromptSource,
		PromptHash:     p.PromptHash,
		AttachedSkills: skills,
		CapturedBy:     uuidToString(p.CapturedBy),
	}
	if p.DuplicateOf != nil {
		s := uuidToString(*p.DuplicateOf)
		resp.DuplicateOf = &s
	}
	return resp
}

// createSuiteRequest is the inbound JSON payload for POST /api/benchmarks/suites.
type createSuiteRequest struct {
	Slug        string   `json:"slug"`
	DisplayName string   `json:"display_name"`
	AdapterKind string   `json:"adapter_kind"`
	InstanceIDs []string `json:"instance_ids"`
	Description string   `json:"description"`
}

// captureProfileRequest is the inbound JSON payload for
// POST /api/benchmarks/profiles. AgentName / Model / PromptSource are NOT
// in the request — Capture reads them from the live agent row so the
// snapshot is authoritative.
type captureProfileRequest struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	AgentID     string `json:"agent_id"`
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
		writeError(w, http.StatusUnauthorized, errUnauthenticated)
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	uid, err := util.ParseUUID(userID)
	if err != nil {
		writeError(w, http.StatusBadRequest, errBadUserID)
		return pgtype.UUID{}, pgtype.UUID{}, false
	}

	workspaceID := workspaceIDFromHeaders(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, errWorkspaceRequired)
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, errBadWorkspaceID)
		return pgtype.UUID{}, pgtype.UUID{}, false
	}

	return ws, uid, true
}

// CreateSuite handles POST /api/benchmarks/suites.
func (h *BenchmarkHandler) CreateSuite(w http.ResponseWriter, r *http.Request) {
	var req createSuiteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errBadBody)
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
		writeError(w, http.StatusBadRequest, errInstanceListEmpty)
		return
	case errors.Is(err, benchmark.ErrSuiteSlugTaken):
		writeError(w, http.StatusConflict, errSlugTaken)
		return
	case err != nil:
		slog.Warn("benchmark.create_suite_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID), "slug", req.Slug)...)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}

	slog.Info("benchmark.suite_created",
		append(logger.RequestAttrs(r), "workspace_id", uuidToString(wsUUID), "suite_id", uuidToString(suite.ID), "slug", suite.Slug)...)
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
		slog.Warn("benchmark.list_suites_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID))...)
		writeError(w, http.StatusInternalServerError, errInternal)
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
	idUUID, ok := parseBenchmarkURLID(w, id)
	if !ok {
		return
	}
	suite, err := h.deps.Suites.Get(r.Context(), idUUID, wsUUID)
	if errors.Is(err, benchmark.ErrSuiteNotFound) {
		writeError(w, http.StatusNotFound, errSuiteNotFound)
		return
	}
	if err != nil {
		slog.Warn("benchmark.get_suite_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID), "suite_id", id)...)
		writeError(w, http.StatusInternalServerError, errInternal)
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
	idUUID, ok := parseBenchmarkURLID(w, id)
	if !ok {
		return
	}
	err := h.deps.Suites.Delete(r.Context(), idUUID, wsUUID)
	if errors.Is(err, benchmark.ErrSuiteNotFound) {
		writeError(w, http.StatusNotFound, errSuiteNotFound)
		return
	}
	if err != nil {
		slog.Warn("benchmark.delete_suite_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID), "suite_id", id)...)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	slog.Info("benchmark.suite_deleted",
		append(logger.RequestAttrs(r), "workspace_id", uuidToString(wsUUID), "suite_id", id)...)
	w.WriteHeader(http.StatusNoContent)
}

// CaptureProfile handles POST /api/benchmarks/profiles.
//
// Captures an immutable snapshot of an agent's prompt + attached skills.
// Duplicate prompt_hash within the workspace is allowed and surfaces as the
// `duplicate_of` field; duplicate slug is rejected with 409.
func (h *BenchmarkHandler) CaptureProfile(w http.ResponseWriter, r *http.Request) {
	var req captureProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errBadBody)
		return
	}

	wsUUID, userUUID, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}

	agentUUID, ok := parseBenchmarkURLID(w, req.AgentID)
	if !ok {
		return
	}

	profile, err := h.deps.Profiles.Capture(r.Context(), benchmark.CaptureProfileInput{
		WorkspaceID: wsUUID,
		AgentID:     agentUUID,
		Slug:        req.Slug,
		DisplayName: req.DisplayName,
		CapturedBy:  userUUID,
	})
	switch {
	case errors.Is(err, benchmark.ErrCaptureAgent):
		writeError(w, http.StatusBadRequest, errAgentNotFound)
		return
	case errors.Is(err, benchmark.ErrProfileSlugTaken):
		writeError(w, http.StatusConflict, errSlugTaken)
		return
	case err != nil:
		slog.Warn("benchmark.capture_profile_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID), "agent_id", req.AgentID, "slug", req.Slug)...)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}

	logAttrs := append(logger.RequestAttrs(r),
		"workspace_id", uuidToString(wsUUID),
		"profile_id", uuidToString(profile.ID),
		"agent_id", uuidToString(profile.AgentID),
		"slug", profile.Slug,
		"prompt_hash", profile.PromptHash,
	)
	if profile.DuplicateOf != nil {
		logAttrs = append(logAttrs, "duplicate_of", uuidToString(*profile.DuplicateOf))
	}
	slog.Info("benchmark.profile_captured", logAttrs...)
	writeJSON(w, http.StatusCreated, profileToResponse(profile))
}

// ListProfiles handles GET /api/benchmarks/profiles.
func (h *BenchmarkHandler) ListProfiles(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}
	profiles, err := h.deps.Profiles.List(r.Context(), wsUUID)
	if err != nil {
		slog.Warn("benchmark.list_profiles_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID))...)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	items := make([]ProfileResponse, 0, len(profiles))
	for _, p := range profiles {
		items = append(items, profileToResponse(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// GetProfile handles GET /api/benchmarks/profiles/{id}.
func (h *BenchmarkHandler) GetProfile(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	idUUID, ok := parseBenchmarkURLID(w, id)
	if !ok {
		return
	}
	profile, err := h.deps.Profiles.Get(r.Context(), idUUID, wsUUID)
	if errors.Is(err, benchmark.ErrProfileNotFound) {
		writeError(w, http.StatusNotFound, errProfileNotFound)
		return
	}
	if err != nil {
		slog.Warn("benchmark.get_profile_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID), "profile_id", id)...)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, profileToResponse(profile))
}

// DeleteProfile handles DELETE /api/benchmarks/profiles/{id}.
func (h *BenchmarkHandler) DeleteProfile(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	idUUID, ok := parseBenchmarkURLID(w, id)
	if !ok {
		return
	}
	err := h.deps.Profiles.Delete(r.Context(), idUUID, wsUUID)
	if errors.Is(err, benchmark.ErrProfileNotFound) {
		writeError(w, http.StatusNotFound, errProfileNotFound)
		return
	}
	if err != nil {
		slog.Warn("benchmark.delete_profile_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID), "profile_id", id)...)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	slog.Info("benchmark.profile_deleted",
		append(logger.RequestAttrs(r), "workspace_id", uuidToString(wsUUID), "profile_id", id)...)
	w.WriteHeader(http.StatusNoContent)
}

// parseBenchmarkURLID validates a UUID string from URL params or request
// body, writing 400 errBadID on failure. Differs from the package-wide
// parseUUIDOrBadRequest only in the response body shape: benchmark routes
// return a stable machine code instead of a free-form prose message.
func parseBenchmarkURLID(w http.ResponseWriter, s string) (pgtype.UUID, bool) {
	u, err := util.ParseUUID(s)
	if err != nil {
		writeError(w, http.StatusBadRequest, errBadID)
		return pgtype.UUID{}, false
	}
	return u, true
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
