package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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
	Suites        *benchmark.SuiteService
	Profiles      *benchmark.ProfileService
	Runs          *benchmark.RunService
	EvaluatorPool *benchmark.EvaluatorPoolService
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
	errBadBody                 = "bad_body"
	errBadID                   = "bad_id"
	errInternal                = "internal_error"
	errUnauthenticated         = "unauthenticated"
	errWorkspaceRequired       = "workspace_required"
	errBadWorkspaceID          = "bad_workspace_id"
	errBadUserID               = "bad_user_id"
	errInstanceListEmpty       = "instance_list_empty"
	errSlugTaken               = "slug_taken"
	errSuiteNotFound           = "suite_not_found"
	errProfileNotFound         = "profile_not_found"
	errAgentNotFound           = "agent_not_found"
	errInvalidEvaluatorMode    = "invalid_evaluator_mode"
	errSuiteOrProfileNotFound  = "suite_or_profile_not_found"
	errRunNotFound             = "run_not_found"
	errTaskNotFoundForInstance = "task_not_found_for_instance"
	errDisplayNameRequired     = "display_name_required"
	errBaseRequired            = "base_required"
	errSuiteRequired           = "suite_required"
	errRunNotComplete          = "run_not_complete"
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

// RunResponse is the JSON shape for a benchmark_run at the handler boundary.
//
// BaseRunID is `omitempty` because not every run has a baseline (it is
// optional metadata for diff-style comparisons). suite_instance_ids is the
// frozen list captured from the suite at run creation time — see
// RunService.StartRun for snapshot semantics.
type RunResponse struct {
	ID                       string   `json:"id"`
	WorkspaceID              string   `json:"workspace_id"`
	SuiteID                  string   `json:"suite_id"`
	SuiteInstanceIDs         []string `json:"suite_instance_ids"`
	ProfileID                string   `json:"profile_id"`
	BaseRunID                string   `json:"base_run_id,omitempty"`
	DisplayName              string   `json:"display_name"`
	Status                   string   `json:"status"`
	StatusReason             string   `json:"status_reason"`
	Notes                    string   `json:"notes"`
	EvaluatorMode            string   `json:"evaluator_mode"`
	AdapterVersion           string   `json:"adapter_version"`
	SubmissionTimeoutSeconds int32    `json:"submission_timeout_seconds"`
	CreatedBy                string   `json:"created_by"`
}

func benchmarkRunToResponse(r benchmark.Run) RunResponse {
	resp := RunResponse{
		ID:                       uuidToString(r.ID),
		WorkspaceID:              uuidToString(r.WorkspaceID),
		SuiteID:                  uuidToString(r.SuiteID),
		SuiteInstanceIDs:         r.SuiteInstanceIDs,
		ProfileID:                uuidToString(r.ProfileID),
		DisplayName:              r.DisplayName,
		Status:                   r.Status,
		StatusReason:             r.StatusReason,
		Notes:                    r.Notes,
		EvaluatorMode:            r.EvaluatorMode,
		AdapterVersion:           r.AdapterVersion,
		SubmissionTimeoutSeconds: r.SubmissionTimeoutSeconds,
		CreatedBy:                uuidToString(r.CreatedBy),
	}
	if r.BaseRunID.Valid {
		resp.BaseRunID = uuidToString(r.BaseRunID)
	}
	if resp.SuiteInstanceIDs == nil {
		resp.SuiteInstanceIDs = []string{}
	}
	return resp
}

// startRunRequest is the inbound JSON payload for POST /api/benchmarks/runs.
//
// BaseRunID and AdapterVersion are optional. EvaluatorMode is validated by
// the service layer (must be "managed" or "imported"); a bogus value here
// surfaces as 400 invalid_evaluator_mode.
type startRunRequest struct {
	SuiteID        string `json:"suite_id"`
	ProfileID      string `json:"profile_id"`
	BaseRunID      string `json:"base_run_id,omitempty"`
	DisplayName    string `json:"display_name"`
	Notes          string `json:"notes,omitempty"`
	EvaluatorMode  string `json:"evaluator_mode"`
	AdapterVersion string `json:"adapter_version,omitempty"`
}

// StartRun handles POST /api/benchmarks/runs. Decodes the request, validates
// the suite/profile UUIDs, and delegates to RunService.StartRun. The service
// performs workspace-scoped suite+profile resolution and inserts a run with
// status='queued'. Returns 201 with the freshly created RunResponse on success.
func (h *BenchmarkHandler) StartRun(w http.ResponseWriter, r *http.Request) {
	var req startRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errBadBody)
		return
	}

	wsUUID, userUUID, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}

	suiteID, ok := parseBenchmarkURLID(w, req.SuiteID)
	if !ok {
		return
	}
	profileID, ok := parseBenchmarkURLID(w, req.ProfileID)
	if !ok {
		return
	}
	var baseRunID pgtype.UUID
	if req.BaseRunID != "" {
		parsed, ok := parseBenchmarkURLID(w, req.BaseRunID)
		if !ok {
			return
		}
		baseRunID = parsed
	}

	run, err := h.deps.Runs.StartRun(r.Context(), benchmark.StartRunInput{
		WorkspaceID:    wsUUID,
		SuiteID:        suiteID,
		ProfileID:      profileID,
		BaseRunID:      baseRunID,
		DisplayName:    req.DisplayName,
		Notes:          req.Notes,
		EvaluatorMode:  req.EvaluatorMode,
		AdapterVersion: req.AdapterVersion,
		CreatedBy:      userUUID,
	})
	switch {
	case errors.Is(err, benchmark.ErrInvalidEvaluator):
		writeError(w, http.StatusBadRequest, errInvalidEvaluatorMode)
		return
	case errors.Is(err, benchmark.ErrSuiteResolution):
		writeError(w, http.StatusNotFound, errSuiteOrProfileNotFound)
		return
	case err != nil:
		slog.Warn("benchmark.start_run_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID))...)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}

	slog.Info("benchmark.run_created",
		append(logger.RequestAttrs(r),
			"workspace_id", uuidToString(run.WorkspaceID),
			"run_id", uuidToString(run.ID),
			"suite_id", uuidToString(run.SuiteID),
			"profile_id", uuidToString(run.ProfileID),
		)...)
	writeJSON(w, http.StatusCreated, benchmarkRunToResponse(run))
}

// ListRuns handles GET /api/benchmarks/runs. Returns the most recent runs
// in the workspace, newest first. Optional ?limit query param caps the
// returned count between 1 and 200; defaults to 50.
func (h *BenchmarkHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}

	limit := int32(50)
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			if v > 200 {
				v = 200
			}
			limit = int32(v)
		}
	}

	runs, err := h.deps.Runs.ListRuns(r.Context(), wsUUID, limit)
	if err != nil {
		slog.Warn("benchmark.list_runs_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID))...)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	items := make([]RunResponse, 0, len(runs))
	for _, run := range runs {
		items = append(items, benchmarkRunToResponse(run))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// GetRun handles GET /api/benchmarks/runs/{id}.
func (h *BenchmarkHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	idUUID, ok := parseBenchmarkURLID(w, id)
	if !ok {
		return
	}
	run, err := h.deps.Runs.GetRun(r.Context(), idUUID, wsUUID)
	switch {
	case errors.Is(err, benchmark.ErrRunNotFound):
		writeError(w, http.StatusNotFound, errRunNotFound)
		return
	case err != nil:
		slog.Warn("benchmark.get_run_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID), "run_id", id)...)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, benchmarkRunToResponse(run))
}

// CancelRun handles DELETE /api/benchmarks/runs/{id}.
//
// Marks the run as 'canceled' / 'user_canceled'. Pending downstream
// dispatches detect the new status and skip work; this method only flips
// the row. 204 on success, 404 if the run is not in the workspace.
func (h *BenchmarkHandler) CancelRun(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	idUUID, ok := parseBenchmarkURLID(w, id)
	if !ok {
		return
	}
	err := h.deps.Runs.CancelRun(r.Context(), idUUID, wsUUID)
	switch {
	case errors.Is(err, benchmark.ErrRunNotFound):
		writeError(w, http.StatusNotFound, errRunNotFound)
		return
	case err != nil:
		slog.Warn("benchmark.cancel_run_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID), "run_id", id)...)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	slog.Info("benchmark.run_canceled",
		append(logger.RequestAttrs(r), "workspace_id", uuidToString(wsUUID), "run_id", id)...)
	w.WriteHeader(http.StatusNoContent)
}

// importEvalResultRequest is the inbound JSON payload for
// POST /api/benchmarks/runs/{id}/eval-results/{instance_id}.
//
// Used by the imported-evaluator flow: an external evaluator (CI / Modal /
// human reviewer) has produced scoring data for a single (run, instance_id)
// and posts it back so Multica can advance the task to 'scored' and persist
// the eval row. RawEvalJSON is opaque adapter-specific JSON; the service
// layer stores it verbatim. FailedCategories is normalized server-side.
type importEvalResultRequest struct {
	Resolved         bool            `json:"resolved"`
	PassedTests      int             `json:"passed_tests"`
	TotalTests       int             `json:"total_tests"`
	PassRate         float64         `json:"pass_rate"`
	RawEvalJSON      json.RawMessage `json:"raw_eval_json"`
	FailedCategories []string        `json:"failed_categories"`
}

// ImportEvalResult handles POST /api/benchmarks/runs/{id}/eval-results/{instance_id}.
//
// Imported-evaluator path: the caller has run the suite's evaluator out of
// band and posts the scoring payload here. The service-layer call is
// transactional (eval_result upsert + task status flip) so a partial failure
// never leaves a task scored without an eval row, or vice versa. Returns
// 204 on success, 404 if no benchmark_task exists for (run_id, instance_id)
// in this workspace.
func (h *BenchmarkHandler) ImportEvalResult(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}

	runID, ok := parseBenchmarkURLID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	instanceID := chi.URLParam(r, "instance_id")
	if instanceID == "" {
		writeError(w, http.StatusBadRequest, errBadID)
		return
	}

	var req importEvalResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errBadBody)
		return
	}
	// Pass through raw JSON verbatim; an absent field becomes the literal
	// `null` so the service-side jsonb column never sees a Go-nil byte slice.
	if req.RawEvalJSON == nil {
		req.RawEvalJSON = json.RawMessage(`null`)
	}
	if req.FailedCategories == nil {
		req.FailedCategories = []string{}
	}

	err := h.deps.Runs.ImportEvalResult(r.Context(), benchmark.ImportEvalResultInput{
		WorkspaceID:      wsUUID,
		RunID:            runID,
		InstanceID:       instanceID,
		Resolved:         req.Resolved,
		PassedTests:      req.PassedTests,
		TotalTests:       req.TotalTests,
		PassRate:         req.PassRate,
		RawEvalJSON:      req.RawEvalJSON,
		FailedCategories: req.FailedCategories,
	})
	switch {
	case errors.Is(err, benchmark.ErrTaskNotFoundForInstance):
		writeError(w, http.StatusNotFound, errTaskNotFoundForInstance)
		return
	case err != nil:
		slog.Warn("benchmark.import_eval_result_failed",
			append(logger.RequestAttrs(r),
				"err", err,
				"workspace_id", uuidToString(wsUUID),
				"run_id", uuidToString(runID),
				"instance_id", instanceID,
			)...)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}

	slog.Info("benchmark.eval_result_imported",
		append(logger.RequestAttrs(r),
			"workspace_id", uuidToString(wsUUID),
			"run_id", uuidToString(runID),
			"instance_id", instanceID,
		)...)
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

// EvaluatorTokenResponse is the JSON shape for an evaluator_pool_token at the
// handler boundary. The plain-text token is intentionally NOT in this struct —
// only CreateEvaluatorTokenResponse exposes it, and only at mint time.
type EvaluatorTokenResponse struct {
	ID          string `json:"id"`
	Prefix      string `json:"prefix"`
	DisplayName string `json:"display_name"`
	CreatedBy   string `json:"created_by"`
	CreatedAt   string `json:"created_at"`
	LastUsedAt  string `json:"last_used_at,omitempty"`
	RevokedAt   string `json:"revoked_at,omitempty"`
}

// CreateEvaluatorTokenResponse extends EvaluatorTokenResponse with the
// plain-text token. PlaintextToken is only populated on the POST /create
// response — once returned, the server only retains a SHA-256 hash, so this
// is the single moment the caller can read it.
type CreateEvaluatorTokenResponse struct {
	EvaluatorTokenResponse
	PlaintextToken string `json:"plaintext_token"`
}

// createEvaluatorTokenRequest is the inbound JSON payload for
// POST /api/benchmarks/evaluator-tokens.
type createEvaluatorTokenRequest struct {
	DisplayName string `json:"display_name"`
}

func evpToResponse(t benchmark.EvaluatorPoolToken) EvaluatorTokenResponse {
	out := EvaluatorTokenResponse{
		ID:          util.UUIDToString(t.ID),
		Prefix:      t.TokenPrefix,
		DisplayName: t.DisplayName,
		CreatedBy:   util.UUIDToString(t.CreatedBy),
		CreatedAt:   util.TimestampToString(t.CreatedAt),
	}
	if t.LastUsedAt.Valid {
		out.LastUsedAt = util.TimestampToString(t.LastUsedAt)
	}
	if t.RevokedAt.Valid {
		out.RevokedAt = util.TimestampToString(t.RevokedAt)
	}
	return out
}

// CreateEvaluatorToken handles POST /api/benchmarks/evaluator-tokens.
//
// Mints a fresh evaluator pool token. The plain-text token is only returned
// here — only its SHA-256 hash is stored — so callers must capture it
// immediately. The display_name field is required (used by operators to
// identify which deployment a token belongs to in the list view).
func (h *BenchmarkHandler) CreateEvaluatorToken(w http.ResponseWriter, r *http.Request) {
	wsUUID, userUUID, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}

	var req createEvaluatorTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errBadBody)
		return
	}
	if strings.TrimSpace(req.DisplayName) == "" {
		writeError(w, http.StatusBadRequest, errDisplayNameRequired)
		return
	}

	tok, plain, err := h.deps.EvaluatorPool.Create(r.Context(), benchmark.CreateEvaluatorPoolTokenInput{
		WorkspaceID: wsUUID,
		DisplayName: req.DisplayName,
		CreatedBy:   userUUID,
	})
	if err != nil {
		slog.Warn("benchmark.evaluator_token_create_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID))...)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}

	slog.Info("benchmark.evaluator_token_created",
		append(logger.RequestAttrs(r),
			"workspace_id", uuidToString(wsUUID),
			"token_id", uuidToString(tok.ID),
			"prefix", tok.TokenPrefix,
		)...)
	writeJSON(w, http.StatusCreated, CreateEvaluatorTokenResponse{
		EvaluatorTokenResponse: evpToResponse(tok),
		PlaintextToken:         plain,
	})
}

// ListEvaluatorTokens handles GET /api/benchmarks/evaluator-tokens.
//
// Returns all tokens for the workspace, newest first. The plain-text token
// and the SHA-256 hash are never included in this response by design.
func (h *BenchmarkHandler) ListEvaluatorTokens(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}
	rows, err := h.deps.EvaluatorPool.List(r.Context(), wsUUID)
	if err != nil {
		slog.Warn("benchmark.list_evaluator_tokens_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID))...)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	items := make([]EvaluatorTokenResponse, 0, len(rows))
	for _, t := range rows {
		items = append(items, evpToResponse(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// RevokeEvaluatorToken handles DELETE /api/benchmarks/evaluator-tokens/{id}.
//
// Marks the token revoked. The underlying sqlc :exec query gives no rowcount,
// so this is best-effort: the response is 204 even if no row matched (already
// revoked / unknown id within the workspace). An infra error is logged but
// still returns 204 to keep the operator UX uniform — operators relist the
// tokens after a delete to confirm the revoked_at column.
func (h *BenchmarkHandler) RevokeEvaluatorToken(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}
	idUUID, ok := parseBenchmarkURLID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if err := h.deps.EvaluatorPool.Revoke(r.Context(), idUUID, wsUUID); err != nil {
		slog.Warn("benchmark.evaluator_token_revoke_err",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID), "token_id", uuidToString(idUUID))...)
	} else {
		slog.Info("benchmark.evaluator_token_revoked",
			append(logger.RequestAttrs(r), "workspace_id", uuidToString(wsUUID), "token_id", uuidToString(idUUID))...)
	}
	w.WriteHeader(http.StatusNoContent)
}

// CompareRun handles GET /api/benchmarks/runs/{id}/compare?base=<base-id>.
//
// Returns a service-layer ComparisonResult diffing the candidate run (the {id}
// path param) against the base run (the ?base= query param). Both runs must
// be complete and in the caller's workspace; otherwise the handler maps the
// service error to a stable machine code (run_not_found / run_not_complete).
func (h *BenchmarkHandler) CompareRun(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}
	candID, ok := parseBenchmarkURLID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	baseStr := r.URL.Query().Get("base")
	if baseStr == "" {
		writeError(w, http.StatusBadRequest, errBaseRequired)
		return
	}
	baseID, ok := parseBenchmarkURLID(w, baseStr)
	if !ok {
		return
	}

	out, err := h.deps.Runs.CompareRuns(r.Context(), baseID, candID, wsUUID)
	switch {
	case errors.Is(err, benchmark.ErrRunNotFound):
		writeError(w, http.StatusNotFound, errRunNotFound)
		return
	case errors.Is(err, benchmark.ErrRunNotComplete):
		writeError(w, http.StatusBadRequest, errRunNotComplete)
		return
	case err != nil:
		slog.Warn("benchmark.compare_runs_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID),
				"base_run_id", uuidToString(baseID), "cand_run_id", uuidToString(candID))...)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Leaderboard handles GET /api/benchmarks/leaderboard?suite=<suite-slug>.
//
// Returns a dense-ranked best-run-per-profile list for the named suite within
// the caller's workspace. Unknown suite slug → 404 suite_not_found; missing
// ?suite query param → 400 suite_required.
func (h *BenchmarkHandler) Leaderboard(w http.ResponseWriter, r *http.Request) {
	wsUUID, _, ok := resolveBenchmarkContext(w, r)
	if !ok {
		return
	}
	slug := r.URL.Query().Get("suite")
	if slug == "" {
		writeError(w, http.StatusBadRequest, errSuiteRequired)
		return
	}

	rows, err := h.deps.Runs.LeaderboardForSuite(r.Context(), wsUUID, slug)
	switch {
	case errors.Is(err, benchmark.ErrSuiteResolution):
		writeError(w, http.StatusNotFound, errSuiteNotFound)
		return
	case err != nil:
		slog.Warn("benchmark.leaderboard_failed",
			append(logger.RequestAttrs(r), "err", err, "workspace_id", uuidToString(wsUUID), "suite_slug", slug)...)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}
