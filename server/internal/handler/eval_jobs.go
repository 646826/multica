package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service/benchmark"
	"github.com/multica-ai/multica/server/internal/util"
)

// EvalJobsHandler exposes the /api/internal/eval-jobs/* routes used by the
// evaluator daemon pool. These routes are authenticated by an
// evaluator-pool token (see middleware.RequireEvaluatorPoolAuth) — never
// by a user JWT — so they live on a separate handler from the
// admin-facing BenchmarkHandler.
//
// The route surface is intentionally narrow: claim a batch of pending
// jobs, mark a claimed job complete (with eval result payload), or mark
// it failed (with a last-error message). Stuck-job recovery happens
// out-of-band in a server-side watchdog (Phase 1b T08+) and is not
// exposed over HTTP.
type EvalJobsHandler struct {
	svc *benchmark.EvalJobService
}

// NewEvalJobsHandler constructs an EvalJobsHandler bound to the given
// service. The handler does not own the service — callers wire the
// shared *EvalJobService into both this handler and any background
// watchdog goroutines.
func NewEvalJobsHandler(svc *benchmark.EvalJobService) *EvalJobsHandler {
	return &EvalJobsHandler{svc: svc}
}

// claimRequest is the JSON body for POST /api/internal/eval-jobs/claim.
// EvaluatorID identifies the calling daemon for the claimed_by audit
// column; it does NOT have to match anything the server tracks. The
// adapter_kinds list is intersected with the workspace's pending jobs.
// MaxConcurrent is clamped to a sane range — see Claim handler below.
type claimRequest struct {
	EvaluatorID   string   `json:"evaluator_id"`
	AdapterKinds  []string `json:"adapter_kinds"`
	MaxConcurrent int32    `json:"max_concurrent"`
}

// claimedJobResponse is the per-job JSON shape returned by Claim. The
// SubmissionDownloadURL is a relative path the evaluator hits with the
// same bearer token to fetch the submission archive (the attachments
// route accepts evaluator-pool tokens as of T08+).
type claimedJobResponse struct {
	JobID                 string          `json:"job_id"`
	TaskID                string          `json:"task_id"`
	InstanceID            string          `json:"instance_id"`
	InstanceMeta          json.RawMessage `json:"instance_meta"`
	AdapterKind           string          `json:"adapter_kind"`
	AttachmentID          string          `json:"attachment_id,omitempty"`
	SubmissionDownloadURL string          `json:"submission_download_url,omitempty"`
}

// Claim handles POST /api/internal/eval-jobs/claim. It validates the
// request, defers to EvalJobService.Claim for the FOR-UPDATE-SKIP-LOCKED
// pickup, and returns the resulting batch as JSON. The route is wrapped
// by middleware.RequireEvaluatorPoolAuth so a verified token is always
// present in context — we still defensively check, since a misconfigured
// router could otherwise leak jobs to anonymous callers.
//
// TODO(phase-1b-cleanup): EvalJobService.Claim does not yet filter by
// the token's workspace_id. In a single-workspace deployment this is
// safe (every token can only see its own workspace's jobs), but as
// soon as a second workspace mints tokens we MUST plumb the
// workspaceID through to the service so jobs cannot cross-leak. See
// the T05 plan for the migration path.
func (h *EvalJobsHandler) Claim(w http.ResponseWriter, r *http.Request) {
	tok, ok := middleware.EvaluatorTokenFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, errUnauthenticated)
		return
	}
	var req claimRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errBadBody)
		return
	}
	if req.EvaluatorID == "" {
		writeError(w, http.StatusBadRequest, "evaluator_id_required")
		return
	}
	if len(req.AdapterKinds) == 0 {
		writeError(w, http.StatusBadRequest, "adapter_kinds_required")
		return
	}
	// Clamp max_concurrent to a defensive range. Zero/negative is
	// treated as "use default 5" rather than rejected because the
	// evaluator client may legitimately omit it on a probe call. The
	// upper bound prevents a misbehaving client from yanking the
	// entire backlog into one process.
	if req.MaxConcurrent <= 0 || req.MaxConcurrent > 50 {
		req.MaxConcurrent = 5
	}
	jobs, err := h.svc.Claim(r.Context(), req.EvaluatorID, req.AdapterKinds, req.MaxConcurrent)
	if err != nil {
		slog.Warn("eval_jobs.claim_failed", "err", err, "evaluator_id", req.EvaluatorID)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	out := make([]claimedJobResponse, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, claimedJobResponse{
			JobID:                 util.UUIDToString(j.JobID),
			TaskID:                util.UUIDToString(j.TaskID),
			InstanceID:            j.InstanceID,
			InstanceMeta:          j.InstanceMeta,
			AdapterKind:           j.AdapterKind,
			AttachmentID:          uuidToStringIfValid(j.AttachmentID),
			SubmissionDownloadURL: j.SubmissionDownloadURL,
		})
	}
	_ = tok // see TODO above — token will be consumed once the service
	// filters by workspace.
	writeJSON(w, http.StatusOK, out)
}

// completeRequest is the JSON body for
// POST /api/internal/eval-jobs/{id}/complete. Mirrors the shape of
// CompleteEvalJobInput so the handler is a pure pass-through after
// minor null-coalescing on the optional raw_eval_json /
// failed_categories fields.
type completeRequest struct {
	Resolved         bool            `json:"resolved"`
	PassedTests      int             `json:"passed_tests"`
	TotalTests       int             `json:"total_tests"`
	PassRate         float64         `json:"pass_rate"`
	RawEvalJSON      json.RawMessage `json:"raw_eval_json"`
	FailedCategories []string        `json:"failed_categories"`
}

// Complete handles POST /api/internal/eval-jobs/{id}/complete. On
// success it returns 204 No Content; the evaluator does not need any
// information back beyond the HTTP status. A missing job (deleted
// out-of-band, or never existed) yields 410 Gone — distinct from a
// 404 because the evaluator may already have done useful work and the
// idempotent retry should not loop on an unrecoverable state.
func (h *EvalJobsHandler) Complete(w http.ResponseWriter, r *http.Request) {
	if _, ok := middleware.EvaluatorTokenFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, errUnauthenticated)
		return
	}
	jobID, ok := parseBenchmarkURLID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req completeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errBadBody)
		return
	}
	if req.RawEvalJSON == nil {
		req.RawEvalJSON = json.RawMessage(`null`)
	}
	if req.FailedCategories == nil {
		req.FailedCategories = []string{}
	}
	err := h.svc.Complete(r.Context(), benchmark.CompleteEvalJobInput{
		JobID:            jobID,
		Resolved:         req.Resolved,
		PassedTests:      req.PassedTests,
		TotalTests:       req.TotalTests,
		PassRate:         req.PassRate,
		RawEvalJSON:      req.RawEvalJSON,
		FailedCategories: req.FailedCategories,
	})
	switch {
	case errors.Is(err, benchmark.ErrEvalJobNotFound):
		writeError(w, http.StatusGone, "eval_job_not_found")
		return
	case err != nil:
		slog.Warn("eval_jobs.complete_failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// failRequest is the JSON body for POST /api/internal/eval-jobs/{id}/fail.
// LastError is stored verbatim on the job row for operator inspection
// and surfaced in the EventBenchmarkTaskStatus payload on permanent
// failure.
type failRequest struct {
	LastError string `json:"last_error"`
}

// Fail handles POST /api/internal/eval-jobs/{id}/fail. The service
// layer decides whether the job retries (state=pending) or flips to
// permanent failure (state=failed, task=errored) based on attempt
// count; the handler only translates errors and status codes.
func (h *EvalJobsHandler) Fail(w http.ResponseWriter, r *http.Request) {
	if _, ok := middleware.EvaluatorTokenFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, errUnauthenticated)
		return
	}
	jobID, ok := parseBenchmarkURLID(w, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req failRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errBadBody)
		return
	}
	err := h.svc.Fail(r.Context(), jobID, req.LastError)
	switch {
	case errors.Is(err, benchmark.ErrEvalJobNotFound):
		writeError(w, http.StatusGone, "eval_job_not_found")
		return
	case err != nil:
		slog.Warn("eval_jobs.fail_failed", "err", err)
		writeError(w, http.StatusInternalServerError, errInternal)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// uuidToStringIfValid is a small helper that returns the empty string
// for a NULL UUID column. Kept private because the rest of the package
// already has util.UUIDToString for the always-Valid case and we don't
// want to encourage routine NULL-tolerance — eval-jobs is one of the
// few places where AttachmentID is genuinely optional.
func uuidToStringIfValid(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return util.UUIDToString(id)
}
