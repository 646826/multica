// Package adapter defines benchmark-adapter contracts. The server-side
// runtime knows nothing about benchmark-specific data; adapters mediate.
package adapter

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"
)

// SkillRef mirrors benchmark.SkillRef for adapter consumers (no service import).
type SkillRef struct {
	Slug    string `json:"slug"`
	Version string `json:"version"`
}

// Instance is the adapter's typed view of a single benchmark task.
// Meta is opaque JSON owned by the adapter; the server round-trips it
// without interpreting.
type Instance struct {
	ID         string
	Language   string
	Difficulty string
	Meta       json.RawMessage
}

type ListFilter struct {
	Language   string
	Difficulty string
	Limit      int
	Offset     int
}

type RunRef struct {
	ID          pgtype.UUID
	SuiteID     pgtype.UUID
	ProfileID   pgtype.UUID
	DisplayName string
	WorkspaceID pgtype.UUID
}

type TaskRef struct {
	ID         pgtype.UUID
	InstanceID string
}

// Catalog: server-side. Resolves instance ids to typed metadata.
type Catalog interface {
	Kind() string
	Resolve(ctx context.Context, instanceID string) (Instance, error)
	List(ctx context.Context, filter ListFilter) ([]Instance, error)
}

type ComposeInput struct {
	Run      RunRef
	Task     TaskRef
	Instance Instance
}

type ComposeOutput struct {
	Title              string
	Description        string
	AssigneeAgentName  string
	SubmissionFilename string
}

// IssueComposer: server-side. Builds the runtime issue an agent will solve.
type IssueComposer interface {
	Kind() string
	Compose(ctx context.Context, in ComposeInput) (ComposeOutput, error)
}

type Attachment struct {
	Filename  string
	MimeType  string
	SizeBytes int64
}

// SubmissionParser: server-side. Validates an attached file as a submission.
type SubmissionParser interface {
	Kind() string
	Validate(ctx context.Context, att Attachment) error
}

type EvaluateInput struct {
	Task           TaskRef
	Instance       Instance
	SubmissionPath string
	WorkDir        string
}

type EvaluateOutput struct {
	RawEvalJSON      json.RawMessage
	Resolved         bool
	PassedTests      int
	TotalTests       int
	FailedCategories []string
}

// Evaluator: evaluator-side. Runs adapter eval inside Docker. Server binary
// does not implement; evaluator binary in Phase 1b does.
type Evaluator interface {
	Kind() string
	Evaluate(ctx context.Context, in EvaluateInput) (EvaluateOutput, error)
}

// Registry stores adapters by Kind(). Server registers Catalog/Composer/Parser;
// evaluator binary registers Evaluator.
type Registry struct {
	catalogs   map[string]Catalog
	composers  map[string]IssueComposer
	parsers    map[string]SubmissionParser
	evaluators map[string]Evaluator
}

func NewRegistry() *Registry {
	return &Registry{
		catalogs:   map[string]Catalog{},
		composers:  map[string]IssueComposer{},
		parsers:    map[string]SubmissionParser{},
		evaluators: map[string]Evaluator{},
	}
}

func (r *Registry) RegisterCatalog(c Catalog)         { r.catalogs[c.Kind()] = c }
func (r *Registry) RegisterComposer(c IssueComposer)  { r.composers[c.Kind()] = c }
func (r *Registry) RegisterParser(p SubmissionParser) { r.parsers[p.Kind()] = p }
func (r *Registry) RegisterEvaluator(e Evaluator)     { r.evaluators[e.Kind()] = e }

func (r *Registry) Catalog(kind string) (Catalog, bool) { c, ok := r.catalogs[kind]; return c, ok }
func (r *Registry) Composer(kind string) (IssueComposer, bool) {
	c, ok := r.composers[kind]
	return c, ok
}
func (r *Registry) Parser(kind string) (SubmissionParser, bool) {
	p, ok := r.parsers[kind]
	return p, ok
}
func (r *Registry) Evaluator(kind string) (Evaluator, bool) {
	e, ok := r.evaluators[kind]
	return e, ok
}
