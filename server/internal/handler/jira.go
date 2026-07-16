package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/integrations/jira"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Native Jira sync — Connection management endpoints (PRD FR-1..FR-5).
// Member-visible read; owner/admin writes (router-gated). The API token is
// write-only: no response shape ever includes it. When the deployment lacks
// MULTICA_JIRA_SECRET_KEY the write endpoints return 503 (Composio/Lark
// precedent) and the read endpoint reports configured:false.

// JiraConnectionResponse is the wire shape of a Connection (snake_case; no
// credential material).
type JiraConnectionResponse struct {
	ID                   string          `json:"id"`
	SiteURL              string          `json:"site_url"`
	ProjectKey           string          `json:"project_key"`
	ProjectID            string          `json:"project_id"`
	Email                string          `json:"email"`
	Enabled              bool            `json:"enabled"`
	Mode                 string          `json:"mode"`
	LeadingSystem        string          `json:"leading_system"`
	CommentsEnabled      bool            `json:"comments_enabled"`
	LabelsEnabled        bool            `json:"labels_enabled"`
	CustomFieldsEnabled  bool            `json:"custom_fields_enabled"`
	CreateFromJira       bool            `json:"create_from_jira"`
	CreateToJira         bool            `json:"create_to_jira"`
	JQLFilter            string          `json:"jql_filter"`
	LabelPrefix          string          `json:"label_prefix"`
	MentionBridgeEnabled bool            `json:"mention_bridge_enabled"`
	OutboundIssueType    string          `json:"outbound_issue_type"`
	StatusMap            json.RawMessage `json:"status_map"`
	FieldMap             json.RawMessage `json:"field_map"`
	TagRules             json.RawMessage `json:"tag_rules"`
	CycleIntervalSeconds int32           `json:"cycle_interval_seconds"`
	Health               json.RawMessage `json:"health"`
	JiraCursor           string          `json:"jira_cursor,omitempty"`
	CreatedAt            string          `json:"created_at"`
	UpdatedAt            string          `json:"updated_at"`
}

func jiraConnectionToResponse(c db.JiraConnection) JiraConnectionResponse {
	resp := JiraConnectionResponse{
		ID:                   uuidToString(c.ID),
		SiteURL:              c.SiteUrl,
		ProjectKey:           c.ProjectKey,
		ProjectID:            c.ProjectID,
		Email:                c.Email,
		Enabled:              c.Enabled,
		Mode:                 c.Mode,
		LeadingSystem:        c.LeadingSystem,
		CommentsEnabled:      c.CommentsEnabled,
		LabelsEnabled:        c.LabelsEnabled,
		CustomFieldsEnabled:  c.CustomFieldsEnabled,
		CreateFromJira:       c.CreateFromJira,
		CreateToJira:         c.CreateToJira,
		JQLFilter:            c.JqlFilter,
		LabelPrefix:          c.LabelPrefix,
		MentionBridgeEnabled: c.MentionBridgeEnabled,
		OutboundIssueType:    c.OutboundIssueType,
		StatusMap:            json.RawMessage(c.StatusMap),
		FieldMap:             json.RawMessage(c.FieldMap),
		TagRules:             json.RawMessage(c.TagRules),
		CycleIntervalSeconds: c.CycleIntervalSeconds,
		Health:               json.RawMessage(c.Health),
		CreatedAt:            c.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:            c.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if c.JiraCursor.Valid {
		resp.JiraCursor = c.JiraCursor.Time.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return resp
}

func (h *Handler) jiraConfigured() bool { return h.Jira != nil && h.Jira.Configured() }

// GetJiraConnection returns the workspace's Connection (or its absence) to
// any member, with the configured / can_manage gating hints the UI needs.
func (h *Handler) GetJiraConnection(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	member, _ := middleware.MemberFromContext(r.Context())
	canManage := roleAllowed(member.Role, "owner", "admin")

	payload := map[string]any{
		"configured": h.jiraConfigured(),
		"can_manage": canManage,
		"connection": nil,
	}
	conn, err := h.Queries.GetJiraConnectionByWorkspace(r.Context(), wsUUID)
	switch {
	case err == nil:
		resp := jiraConnectionToResponse(conn)
		payload["connection"] = resp
	case errors.Is(err, pgx.ErrNoRows):
		// no connection yet — payload stays nil
	default:
		writeError(w, http.StatusInternalServerError, "failed to load jira connection")
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

type connectJiraRequest struct {
	SiteURL    string `json:"site_url"`
	Email      string `json:"email"`
	Token      string `json:"token"`
	ProjectKey string `json:"project_key"`
	ProjectID  string `json:"project_id"`
	Mode       string `json:"mode"`
}

// ConnectJira validates credentials live and creates the Connection (FR-1).
func (h *Handler) ConnectJira(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	if !h.jiraConfigured() {
		writeError(w, http.StatusServiceUnavailable, "jira integration is not configured on this deployment")
		return
	}
	member, _ := middleware.MemberFromContext(r.Context())

	var req connectJiraRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	conn, err := h.Jira.Connect(r.Context(), jira.ConnectInput{
		WorkspaceID:   wsUUID,
		ConnectedByID: member.UserID,
		SiteURL:       req.SiteURL,
		Email:         req.Email,
		Token:         req.Token,
		ProjectKey:    req.ProjectKey,
		ProjectID:     req.ProjectID,
		Mode:          req.Mode,
	})
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already connected") || strings.Contains(err.Error(), "already has a Jira connection") {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	h.journalJira(r, conn, jira.JournalConfigChanged, map[string]any{"action": "connect"})
	writeJSON(w, http.StatusCreated, jiraConnectionToResponse(conn))
}

// patchJiraRequest carries optional settings updates; pointer fields
// distinguish "absent" from zero values. Unknown keys are rejected.
type patchJiraRequest struct {
	Enabled              *bool            `json:"enabled"`
	Mode                 *string          `json:"mode"`
	LeadingSystem        *string          `json:"leading_system"`
	CommentsEnabled      *bool            `json:"comments_enabled"`
	LabelsEnabled        *bool            `json:"labels_enabled"`
	CustomFieldsEnabled  *bool            `json:"custom_fields_enabled"`
	CreateFromJira       *bool            `json:"create_from_jira"`
	CreateToJira         *bool            `json:"create_to_jira"`
	JQLFilter            *string          `json:"jql_filter"`
	LabelPrefix          *string          `json:"label_prefix"`
	MentionBridgeEnabled *bool            `json:"mention_bridge_enabled"`
	OutboundIssueType    *string          `json:"outbound_issue_type"`
	StatusMap            *json.RawMessage `json:"status_map"`
	FieldMap             *json.RawMessage `json:"field_map"`
	TagRules             *json.RawMessage `json:"tag_rules"`
	CycleIntervalSeconds *int32           `json:"cycle_interval_seconds"`
	// Credential rotation: both must be present together; validated live.
	Email *string `json:"email"`
	Token *string `json:"token"`
}

// PatchJiraConnection merges, validates (strictly), and persists settings.
func (h *Handler) PatchJiraConnection(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	conn, err := h.Queries.GetJiraConnectionByWorkspace(r.Context(), wsUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "no jira connection for this workspace")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load jira connection")
		return
	}

	var req patchJiraRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Merge current row + patch into a Settings candidate.
	s := jira.Settings{
		Enabled:              conn.Enabled,
		Mode:                 conn.Mode,
		LeadingSystem:        conn.LeadingSystem,
		CommentsEnabled:      conn.CommentsEnabled,
		LabelsEnabled:        conn.LabelsEnabled,
		CustomFieldsEnabled:  conn.CustomFieldsEnabled,
		CreateFromJira:       conn.CreateFromJira,
		CreateToJira:         conn.CreateToJira,
		JQLFilter:            conn.JqlFilter,
		LabelPrefix:          conn.LabelPrefix,
		MentionBridgeEnabled: conn.MentionBridgeEnabled,
		OutboundIssueType:    conn.OutboundIssueType,
		StatusMap:            conn.StatusMap,
		FieldMap:             conn.FieldMap,
		TagRules:             conn.TagRules,
		CycleIntervalSeconds: conn.CycleIntervalSeconds,
	}
	if req.Enabled != nil {
		s.Enabled = *req.Enabled
	}
	if req.Mode != nil {
		s.Mode = *req.Mode
	}
	if req.LeadingSystem != nil {
		s.LeadingSystem = *req.LeadingSystem
	}
	if req.CommentsEnabled != nil {
		s.CommentsEnabled = *req.CommentsEnabled
	}
	if req.LabelsEnabled != nil {
		s.LabelsEnabled = *req.LabelsEnabled
	}
	if req.CustomFieldsEnabled != nil {
		s.CustomFieldsEnabled = *req.CustomFieldsEnabled
	}
	if req.CreateFromJira != nil {
		s.CreateFromJira = *req.CreateFromJira
	}
	if req.CreateToJira != nil {
		s.CreateToJira = *req.CreateToJira
	}
	if req.JQLFilter != nil {
		s.JQLFilter = *req.JQLFilter
	}
	if req.LabelPrefix != nil {
		s.LabelPrefix = *req.LabelPrefix
	}
	if req.MentionBridgeEnabled != nil {
		s.MentionBridgeEnabled = *req.MentionBridgeEnabled
	}
	if req.OutboundIssueType != nil {
		s.OutboundIssueType = *req.OutboundIssueType
	}
	if req.StatusMap != nil {
		s.StatusMap = []byte(*req.StatusMap)
	}
	if req.FieldMap != nil {
		s.FieldMap = []byte(*req.FieldMap)
	}
	if req.TagRules != nil {
		s.TagRules = []byte(*req.TagRules)
	}
	if req.CycleIntervalSeconds != nil {
		s.CycleIntervalSeconds = *req.CycleIntervalSeconds
	}
	if err := jira.ValidateSettings(s); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Optional credential rotation, validated live before persisting.
	if (req.Email == nil) != (req.Token == nil) {
		writeError(w, http.StatusBadRequest, "email and token must be rotated together")
		return
	}
	if req.Email != nil {
		if !h.jiraConfigured() {
			writeError(w, http.StatusServiceUnavailable, "jira integration is not configured on this deployment")
			return
		}
		if err := h.Jira.RotateToken(r.Context(), conn, *req.Email, *req.Token); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	updated, err := h.Queries.UpdateJiraConnectionConfig(r.Context(), db.UpdateJiraConnectionConfigParams{
		ID:                   conn.ID,
		Enabled:              s.Enabled,
		Mode:                 s.Mode,
		LeadingSystem:        s.LeadingSystem,
		CommentsEnabled:      s.CommentsEnabled,
		LabelsEnabled:        s.LabelsEnabled,
		CustomFieldsEnabled:  s.CustomFieldsEnabled,
		CreateFromJira:       s.CreateFromJira,
		CreateToJira:         s.CreateToJira,
		JqlFilter:            s.JQLFilter,
		LabelPrefix:          s.LabelPrefix,
		MentionBridgeEnabled: s.MentionBridgeEnabled,
		OutboundIssueType:    s.OutboundIssueType,
		StatusMap:            s.StatusMap,
		FieldMap:             s.FieldMap,
		TagRules:             s.TagRules,
		CycleIntervalSeconds: s.CycleIntervalSeconds,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update jira connection")
		return
	}
	h.journalJira(r, updated, jira.JournalConfigChanged, map[string]any{"action": "patch"})
	writeJSON(w, http.StatusOK, jiraConnectionToResponse(updated))
}

// DeleteJiraConnection removes the Connection and its bookkeeping in one
// transaction; issues and comments on both sides remain (FR-3).
func (h *Handler) DeleteJiraConnection(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	conn, err := h.Queries.GetJiraConnectionByWorkspace(r.Context(), wsUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "no jira connection for this workspace")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load jira connection")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.DeleteJiraLinksByConnection(r.Context(), conn.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete jira links")
		return
	}
	if _, err := qtx.DeleteJiraJournalByConnection(r.Context(), conn.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete jira journal")
		return
	}
	if err := qtx.DeleteJiraConnection(r.Context(), conn.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete jira connection")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete jira connection")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// ListJiraProjects lists projects for the setup picker. Credentials may be
// supplied in the body (pre-create) or omitted to use the stored Connection.
func (h *Handler) ListJiraProjects(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	if !h.jiraConfigured() {
		writeError(w, http.StatusServiceUnavailable, "jira integration is not configured on this deployment")
		return
	}
	var req struct {
		SiteURL string `json:"site_url"`
		Email   string `json:"email"`
		Token   string `json:"token"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	// An entirely empty body (io.EOF) means "use the stored connection".
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	var client *jira.Client
	var err error
	if req.SiteURL != "" {
		client, err = h.Jira.NewClient(req.SiteURL, req.Email, req.Token)
	} else {
		var conn db.JiraConnection
		conn, err = h.Queries.GetJiraConnectionByWorkspace(r.Context(), wsUUID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "provide site_url, email and token, or connect first")
			return
		}
		if err == nil {
			client, err = h.Jira.ClientFor(conn)
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	projects, err := client.SearchProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to list projects: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

// journalJira best-effort records a management event; failures are logged
// via the response path only (journal is observability, never correctness).
func (h *Handler) journalJira(r *http.Request, conn db.JiraConnection, kind jira.JournalKind, detail map[string]any) {
	payload, err := json.Marshal(detail)
	if err != nil {
		payload = []byte(`{}`)
	}
	_, _ = h.Queries.InsertJiraJournal(r.Context(), db.InsertJiraJournalParams{
		ConnectionID: conn.ID,
		WorkspaceID:  conn.WorkspaceID,
		Kind:         string(kind),
		JiraKey:      "",
		Detail:       payload,
	})
}

// GetIssueJiraLink powers the issue-page badge (FR-13): the linked Jira key,
// deep link, and sync state for one issue. Member-visible; empty state is a
// 200 with linked:false so the UI needs no error branch.
func (h *Handler) GetIssueJiraLink(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	link, err := h.Queries.GetJiraLinkByIssueID(r.Context(), issue.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{"linked": false})
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load jira link")
		return
	}
	conn, err := h.Queries.GetJiraConnectionByID(r.Context(), link.ConnectionID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"linked": false})
		return
	}
	state := link.State
	if link.Dirty {
		state = "retrying"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"linked":   true,
		"jira_key": link.JiraKey,
		"url":      conn.SiteUrl + "/browse/" + link.JiraKey,
		"state":    state,
	})
}
