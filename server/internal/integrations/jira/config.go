package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Connector configuration: enums, strict validation, mode defaults, and the
// Connect service (live credential probe → encrypted persist). Config is data
// on jira_connection (AD-8); every write path validates here — the handler
// merges PATCH payloads and calls ValidateSettings before persisting.

// KeyEnv holds the base64 32-byte master key that encrypts Jira API tokens at
// rest (secretbox, Slack/Lark precedent). Unset ⇒ the integration is not
// configured on this deployment and no Connection can be created.
const KeyEnv = "MULTICA_JIRA_SECRET_KEY"

// Sync modes (PRD §4.2).
const (
	ModeMirror       = "mirror"
	ModeJiraLeads    = "jira_leads"
	ModeMulticaLeads = "multica_leads"
	ModeTwoWay       = "two_way"
)

// Leading systems.
const (
	LeadJira    = "jira"
	LeadMultica = "multica"
)

// multicaStatuses is the platform's fixed status enum (001_init.up.sql);
// status-map targets must be members.
var multicaStatuses = map[string]bool{
	"backlog": true, "todo": true, "in_progress": true, "in_review": true,
	"done": true, "blocked": true, "cancelled": true,
}

var validModes = map[string]bool{ModeMirror: true, ModeJiraLeads: true, ModeMulticaLeads: true, ModeTwoWay: true}
var validLeads = map[string]bool{LeadJira: true, LeadMultica: true}

// StatusMap is the canonical status_map JSONB shape: "in" maps Jira status id
// → Multica status; "out" maps Multica status → Jira status id (transition
// target). Stored raw; mappings are applied at plan time only (AD-5).
type StatusMap struct {
	In  map[string]string `json:"in"`
	Out map[string]string `json:"out"`
}

// ParseStatusMap validates shape and Multica-status membership (FR-15).
func ParseStatusMap(raw []byte) (StatusMap, error) {
	var sm StatusMap
	if len(raw) == 0 || string(raw) == "{}" {
		return StatusMap{In: map[string]string{}, Out: map[string]string{}}, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sm); err != nil {
		return sm, fmt.Errorf("status_map: %w", err)
	}
	for jiraID, target := range sm.In {
		if !multicaStatuses[target] {
			return sm, fmt.Errorf("status_map.in[%s]: %q is not a Multica status", jiraID, target)
		}
	}
	for from := range sm.Out {
		if !multicaStatuses[from] {
			return sm, fmt.Errorf("status_map.out: key %q is not a Multica status", from)
		}
	}
	if sm.In == nil {
		sm.In = map[string]string{}
	}
	if sm.Out == nil {
		sm.Out = map[string]string{}
	}
	return sm, nil
}

// FieldMapRow binds one Jira field to one Multica issue property. Type-matrix
// enforcement against live catalogs lands with the field-mapping story; this
// story pins the structural contract.
type FieldMapRow struct {
	ExternalField string `json:"external_field"`
	PropertyID    string `json:"property_id"`
	Direction     string `json:"direction"` // inherit|pull|push|two_way
}

var validFieldDirections = map[string]bool{"inherit": true, "pull": true, "push": true, "two_way": true}

// ParseFieldMap validates the structural shape of field_map JSONB.
func ParseFieldMap(raw []byte) ([]FieldMapRow, error) {
	if len(raw) == 0 || string(raw) == "[]" {
		return nil, nil
	}
	var rows []FieldMapRow
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rows); err != nil {
		return nil, fmt.Errorf("field_map: %w", err)
	}
	for i, r := range rows {
		if r.ExternalField == "" || r.PropertyID == "" {
			return nil, fmt.Errorf("field_map[%d]: external_field and property_id are required", i)
		}
		if r.Direction == "" {
			rows[i].Direction = "inherit"
		} else if !validFieldDirections[r.Direction] {
			return nil, fmt.Errorf("field_map[%d]: invalid direction %q", i, r.Direction)
		}
	}
	return rows, nil
}

// TagRule maps a Jira signal to a workspace agent (FR-26). Agent existence is
// validated at the API layer where workspace context is available.
type TagRule struct {
	MatchType  string `json:"match_type"` // label|assignee
	MatchValue string `json:"match_value"`
	AgentID    string `json:"agent_id"`
}

// ParseTagRules validates the structural shape of tag_rules JSONB.
func ParseTagRules(raw []byte) ([]TagRule, error) {
	if len(raw) == 0 || string(raw) == "[]" {
		return nil, nil
	}
	var rules []TagRule
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rules); err != nil {
		return nil, fmt.Errorf("tag_rules: %w", err)
	}
	for i, r := range rules {
		if r.MatchType != "label" && r.MatchType != "assignee" {
			return nil, fmt.Errorf("tag_rules[%d]: invalid match_type %q", i, r.MatchType)
		}
		if r.MatchValue == "" || r.AgentID == "" {
			return nil, fmt.Errorf("tag_rules[%d]: match_value and agent_id are required", i)
		}
	}
	return rules, nil
}

// Settings is the mutable configuration set of a Connection.
type Settings struct {
	Enabled              bool
	Mode                 string
	LeadingSystem        string
	CommentsEnabled      bool
	LabelsEnabled        bool
	CustomFieldsEnabled  bool
	CreateFromJira       bool
	CreateToJira         bool
	JQLFilter            string
	LabelPrefix          string
	MentionBridgeEnabled bool
	OutboundIssueType    string
	StatusMap            []byte
	FieldMap             []byte
	TagRules             []byte
	CycleIntervalSeconds int32
}

// ValidateSettings enforces every config rule the schema cannot (AD-8):
// enum membership, cadence bounds, Mirror×creation, JSONB shapes.
func ValidateSettings(s Settings) error {
	if !validModes[s.Mode] {
		return fmt.Errorf("mode: invalid value %q", s.Mode)
	}
	if !validLeads[s.LeadingSystem] {
		return fmt.Errorf("leading_system: invalid value %q", s.LeadingSystem)
	}
	if s.Mode == ModeMirror && s.CreateToJira {
		return errors.New("create_to_jira cannot be enabled in mirror mode (mirror performs no Jira writes)")
	}
	if s.CycleIntervalSeconds < 30 || s.CycleIntervalSeconds > 300 {
		return fmt.Errorf("cycle_interval_seconds: %d outside 30..300", s.CycleIntervalSeconds)
	}
	if s.OutboundIssueType == "" {
		return errors.New("outbound_issue_type: required")
	}
	if _, err := ParseStatusMap(s.StatusMap); err != nil {
		return err
	}
	if _, err := ParseFieldMap(s.FieldMap); err != nil {
		return err
	}
	if _, err := ParseTagRules(s.TagRules); err != nil {
		return err
	}
	return nil
}

// DefaultSettings returns the PRD's per-mode defaults for a new Connection.
func DefaultSettings(mode string) Settings {
	s := Settings{
		Mode:                 mode,
		LeadingSystem:        LeadJira,
		CommentsEnabled:      true,
		LabelsEnabled:        true,
		CustomFieldsEnabled:  true,
		CreateFromJira:       true,
		CreateToJira:         false,
		MentionBridgeEnabled: true,
		OutboundIssueType:    "Task",
		StatusMap:            []byte(`{}`),
		FieldMap:             []byte(`[]`),
		TagRules:             []byte(`[]`),
		CycleIntervalSeconds: 45,
	}
	switch mode {
	case ModeMulticaLeads:
		s.LeadingSystem = LeadMultica
		s.CreateFromJira = false
		s.CreateToJira = true
	case ModeMirror:
		s.LeadingSystem = LeadJira
		s.CreateToJira = false
	}
	return s
}

// requiredPermissions are the Jira project permissions the connector needs to
// operate as a writer (CREATE_ISSUES is additionally re-checked when the
// Multica→Jira creation flow is enabled).
var requiredPermissions = []string{"BROWSE_PROJECTS", "EDIT_ISSUES", "ADD_COMMENTS", "TRANSITION_ISSUES"}

// ErrNotConfigured gates every Connection-create path on the deployment key.
var ErrNotConfigured = errors.New("jira integration is not configured on this deployment (set " + KeyEnv + ")")

// Service owns Connection lifecycle operations. Box is nil when KeyEnv is
// unset — the deployment-level "not configured" state (FR-2).
type Service struct {
	Q   *db.Queries
	Box *secretbox.Box
	// NewClient is injectable for tests; defaults to the real constructor.
	NewClient func(siteURL, email, token string) (*Client, error)
}

func NewService(q *db.Queries, box *secretbox.Box) *Service {
	return &Service{Q: q, Box: box, NewClient: NewClient}
}

// Configured reports whether the deployment can hold Jira credentials.
func (s *Service) Configured() bool { return s.Box != nil }

// ConnectInput carries the validated setup form.
type ConnectInput struct {
	WorkspaceID   pgtype.UUID
	ConnectedByID pgtype.UUID
	SiteURL       string
	Email         string
	Token         string
	ProjectKey    string
	ProjectID     string
	Mode          string
}

// Connect validates credentials and permissions live against Jira, enforces
// the deployment-wide double-writer guard, and persists the Connection with
// the token encrypted at rest. Nothing is persisted on any failure (FR-1).
func (s *Service) Connect(ctx context.Context, in ConnectInput) (db.JiraConnection, error) {
	var zero db.JiraConnection
	if !s.Configured() {
		return zero, ErrNotConfigured
	}
	if in.ProjectKey == "" {
		return zero, errors.New("project_key: required")
	}
	mode := in.Mode
	if mode == "" {
		mode = ModeJiraLeads
	}
	if !validModes[mode] {
		return zero, fmt.Errorf("mode: invalid value %q", mode)
	}

	client, err := s.NewClient(in.SiteURL, in.Email, in.Token)
	if err != nil {
		return zero, err
	}
	if _, err := client.Myself(ctx); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.IsAuth() {
			return zero, fmt.Errorf("credential validation failed: token invalid or expired (Jira API tokens expire after at most one year): %w", err)
		}
		return zero, fmt.Errorf("credential validation failed: %w", err)
	}
	perms, err := client.MyPermissions(ctx, in.ProjectKey, requiredPermissions)
	if err != nil {
		return zero, fmt.Errorf("permission probe failed for project %s: %w", in.ProjectKey, err)
	}
	for _, p := range requiredPermissions {
		if !perms[p] {
			return zero, fmt.Errorf("service account lacks required Jira permission %s on project %s", p, in.ProjectKey)
		}
	}

	siteHost := client.baseURL.Host
	if existing, err := s.Q.GetJiraConnectionBySiteProject(ctx, db.GetJiraConnectionBySiteProjectParams{
		SiteHost: siteHost, ProjectKey: in.ProjectKey,
	}); err == nil && existing.ID.Valid {
		return zero, fmt.Errorf("jira project %s on %s is already connected to another workspace in this deployment", in.ProjectKey, siteHost)
	}

	encrypted, err := s.Box.Seal([]byte(in.Token))
	if err != nil {
		return zero, fmt.Errorf("encrypt token: %w", err)
	}

	defaults := DefaultSettings(mode)
	conn, err := s.Q.CreateJiraConnection(ctx, db.CreateJiraConnectionParams{
		WorkspaceID:          in.WorkspaceID,
		SiteUrl:              client.baseURL.String(),
		SiteHost:             siteHost,
		ProjectKey:           in.ProjectKey,
		ProjectID:            in.ProjectID,
		Email:                in.Email,
		TokenEncrypted:       encrypted,
		ConnectedByID:        in.ConnectedByID,
		Mode:                 defaults.Mode,
		LeadingSystem:        defaults.LeadingSystem,
		CommentsEnabled:      defaults.CommentsEnabled,
		LabelsEnabled:        defaults.LabelsEnabled,
		CustomFieldsEnabled:  defaults.CustomFieldsEnabled,
		CreateFromJira:       defaults.CreateFromJira,
		CreateToJira:         defaults.CreateToJira,
		JqlFilter:            defaults.JQLFilter,
		LabelPrefix:          defaults.LabelPrefix,
		MentionBridgeEnabled: defaults.MentionBridgeEnabled,
		OutboundIssueType:    defaults.OutboundIssueType,
		StatusMap:            defaults.StatusMap,
		CycleIntervalSeconds: defaults.CycleIntervalSeconds,
	})
	if err != nil {
		if strings.Contains(err.Error(), "idx_jira_connection_site_project") {
			return zero, fmt.Errorf("jira project %s on %s is already connected to another workspace in this deployment", in.ProjectKey, siteHost)
		}
		if strings.Contains(err.Error(), "idx_jira_connection_workspace") {
			return zero, errors.New("this workspace already has a Jira connection")
		}
		return zero, err
	}
	return conn, nil
}

// ClientFor decrypts the Connection's token and builds its API client.
func (s *Service) ClientFor(conn db.JiraConnection) (*Client, error) {
	if !s.Configured() {
		return nil, ErrNotConfigured
	}
	token, err := s.Box.Open(conn.TokenEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt token: %w", err)
	}
	return s.NewClient(conn.SiteUrl, conn.Email, string(token))
}

// normalizeSiteHost is used by tests and future callers that need the host
// without constructing a client.
func normalizeSiteHost(siteURL string) (string, error) {
	u, err := url.Parse(strings.TrimRight(siteURL, "/"))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("jira: invalid site URL %q", siteURL)
	}
	return u.Host, nil
}
