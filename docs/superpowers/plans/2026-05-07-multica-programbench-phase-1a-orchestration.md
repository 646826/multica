# Multica × ProgramBench Phase 1a — Server Orchestration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Add server-side orchestration so an operator can start a benchmark run, ProgramBenchRunner agent solves each task issue, submission attaches automatically, and operator imports eval results to finalize. The evaluator binary and runs UI come in 1b/1c respectively.

**Architecture:** New `RunService` + `RunDispatcher` + `TaskDispatcher` goroutines coordinate via Postgres advisory locks. Adapter package isolates ProgramBench-specific knowledge (instance catalog, issue Markdown template, submission validation). HTTP adds POST/GET/cancel for runs plus an `/eval-results` import endpoint for imported-mode operation.

**Tech Stack:** Same as Phase 0 — Go 1.24 + Chi + sqlc/pgx + advisory locks + WS event bus + Next.js (none in 1a).

**Reference spec:** `docs/superpowers/specs/2026-05-07-multica-programbench-integration-design.md` (Section 6 = lifecycle, Section 5 = adapter).

**Reference precedent:** `server/internal/service/autopilot.go:dispatchCreateIssue()` — model for programmatic issue creation with `OriginType` + bus event.

---

## File Structure

| Path | Purpose |
|---|---|
| `server/pkg/db/queries/benchmark_run.sql` (extend) | Run lifecycle queries: status updates, finalize, list-active, claim-leader |
| `server/pkg/db/queries/benchmark_task.sql` (new) | Task lifecycle queries: batch create, status update, list by run+status |
| `server/pkg/db/queries/benchmark_eval_job.sql` (new) | Eval-job queries: insert, claim-skip-locked, complete, fail |
| `server/pkg/db/queries/benchmark_eval_result.sql` (new) | Upsert eval results |
| `server/pkg/db/queries/benchmark_run_summary.sql` (new) | Upsert run summaries |
| `server/internal/service/benchmark/adapter/adapter.go` | Interface definitions: Catalog, IssueComposer, SubmissionParser, Evaluator, Registry |
| `server/internal/service/benchmark/adapter/programbench.go` | ProgramBench implementation of Catalog + IssueComposer + SubmissionParser (Evaluator stub deferred to 1b) |
| `server/internal/service/benchmark/run_service.go` | StartRun, GetRun, ListRuns, CancelRun, ImportEvalResult |
| `server/internal/service/benchmark/run_dispatcher.go` | Server-wide goroutine; picks up `queued` runs; creates tasks via Catalog |
| `server/internal/service/benchmark/task_dispatcher.go` | Per-active-run goroutine; creates issues for `queued` tasks; subscribes to WS bus for submission detection |
| `server/internal/service/benchmark/finalizer.go` | RunFinalizer goroutine; computes summary when all tasks terminal |
| `server/internal/service/benchmark/timeout_watchdog.go` | Marks tasks `errored` past `submission_timeout_seconds` |
| `server/internal/service/benchmark/events.go` | New event-type constants for bus: `benchmark.run.*`, `benchmark.task.*` |
| `server/internal/handler/benchmark.go` (extend) | Add Run handlers: StartRun, GetRun, ListRuns, CancelRun, ImportEvalResult |
| `server/cmd/server/router.go` (extend) | Mount new run routes inside `/api/benchmarks` group |
| `server/cmd/server/main.go` (extend if needed) | Wire RunService + start dispatchers as background goroutines |

---

## Task 1: sqlc — extend `benchmark_run.sql`

**Files:**
- Modify: `server/pkg/db/queries/benchmark_run.sql`

- [ ] **Step 1: Add lifecycle queries**

Append to the existing file (which has CreateBenchmarkRun, GetBenchmarkRun, ListBenchmarkRuns from T05):

```sql
-- name: UpdateBenchmarkRunStatus :one
UPDATE benchmark_run
SET status = $3, status_reason = $4,
    started_at = COALESCE(started_at, CASE WHEN $3 = 'submitting' THEN now() ELSE started_at END),
    completed_at = COALESCE(completed_at, CASE WHEN $3 IN ('complete', 'failed', 'canceled') THEN now() ELSE completed_at END)
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: ListActiveBenchmarkRuns :many
SELECT * FROM benchmark_run
WHERE status IN ('queued', 'submitting', 'evaluating')
ORDER BY created_at ASC;

-- name: ListBenchmarkRunsBySuite :many
SELECT * FROM benchmark_run
WHERE workspace_id = $1 AND suite_id = $2 AND status = 'complete'
ORDER BY completed_at DESC LIMIT $3;

-- name: CountActiveTasksForRun :one
SELECT
    COUNT(*) FILTER (WHERE status IN ('queued', 'issued', 'submitted', 'evaluating')) AS active,
    COUNT(*) FILTER (WHERE status = 'scored') AS scored,
    COUNT(*) FILTER (WHERE status = 'errored') AS errored,
    COUNT(*) FILTER (WHERE status = 'skipped') AS skipped,
    COUNT(*) AS total
FROM benchmark_task
WHERE run_id = $1;
```

- [ ] **Step 2: Generate**

```bash
cd /Volumes/kingston/repo_v3/Ark-AI/multica/server && sqlc generate
```

Revert unrelated header drift if sqlc version drifted.

- [ ] **Step 3: Build**

`go build ./...`

- [ ] **Step 4: Commit**

```
git add server/pkg/db/queries/benchmark_run.sql server/pkg/db/generated/
git commit -m "feat(benchmark): sqlc lifecycle queries for benchmark_run"
```

---

## Task 2: sqlc — `benchmark_task.sql`

**Files:**
- Create: `server/pkg/db/queries/benchmark_task.sql`

- [ ] **Step 1: Write queries**

```sql
-- name: CreateBenchmarkTask :one
INSERT INTO benchmark_task (
    run_id, workspace_id, instance_id, instance_meta, status, status_reason
) VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetBenchmarkTask :one
SELECT * FROM benchmark_task WHERE id = $1 AND workspace_id = $2;

-- name: GetBenchmarkTaskByInstance :one
SELECT * FROM benchmark_task WHERE run_id = $1 AND instance_id = $2;

-- name: ListBenchmarkTasksByRun :many
SELECT * FROM benchmark_task WHERE run_id = $1 ORDER BY instance_id ASC;

-- name: ListBenchmarkTasksByRunStatus :many
SELECT * FROM benchmark_task WHERE run_id = $1 AND status = $2 ORDER BY instance_id ASC;

-- name: ListIncompleteTasksForRun :many
SELECT * FROM benchmark_task
WHERE run_id = $1
  AND status IN ('queued', 'issued', 'submitted', 'evaluating')
ORDER BY instance_id ASC;

-- name: UpdateBenchmarkTaskStatus :one
UPDATE benchmark_task
SET status = $3, status_reason = $4,
    submitted_at = COALESCE(submitted_at, CASE WHEN $3 IN ('submitted', 'evaluating') THEN now() ELSE submitted_at END),
    scored_at = COALESCE(scored_at, CASE WHEN $3 IN ('scored', 'errored', 'skipped') THEN now() ELSE scored_at END)
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: AttachIssueToTask :exec
UPDATE benchmark_task SET issue_id = $3 WHERE id = $1 AND workspace_id = $2;

-- name: AttachAttachmentToTask :exec
UPDATE benchmark_task SET attachment_id = $3 WHERE id = $1 AND workspace_id = $2;

-- name: GetBenchmarkTaskByIssue :one
SELECT * FROM benchmark_task WHERE issue_id = $1;

-- name: ListIssuedTasksPastTimeout :many
SELECT t.* FROM benchmark_task t
JOIN benchmark_run r ON r.id = t.run_id
WHERE t.status = 'issued'
  AND t.created_at < now() - make_interval(secs => r.submission_timeout_seconds);
```

- [ ] **Step 2: Generate + build**

`make sqlc && go build ./...`

- [ ] **Step 3: Commit**

```
git add server/pkg/db/queries/benchmark_task.sql server/pkg/db/generated/
git commit -m "feat(benchmark): sqlc lifecycle queries for benchmark_task"
```

---

## Task 3: sqlc — `benchmark_eval_job.sql`

**Files:**
- Create: `server/pkg/db/queries/benchmark_eval_job.sql`

- [ ] **Step 1: Write queries**

```sql
-- name: CreateBenchmarkEvalJob :one
INSERT INTO benchmark_eval_job (
    task_id, workspace_id, adapter_kind, state
) VALUES ($1, $2, $3, 'pending')
RETURNING *;

-- name: ClaimBenchmarkEvalJobs :many
WITH claimed AS (
    SELECT id FROM benchmark_eval_job
    WHERE state = 'pending' AND adapter_kind = ANY($1::text[])
    ORDER BY enqueued_at ASC
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)
UPDATE benchmark_eval_job
SET state = 'claimed', claimed_by = $3, claimed_at = now()
WHERE id IN (SELECT id FROM claimed)
RETURNING *;

-- name: CompleteBenchmarkEvalJob :exec
UPDATE benchmark_eval_job
SET state = 'done', finished_at = now()
WHERE id = $1;

-- name: FailBenchmarkEvalJob :one
UPDATE benchmark_eval_job
SET state = CASE WHEN attempt + 1 >= $3 THEN 'failed' ELSE 'pending' END,
    attempt = attempt + 1,
    last_error = $2,
    claimed_by = NULL,
    claimed_at = NULL,
    finished_at = CASE WHEN attempt + 1 >= $3 THEN now() ELSE finished_at END
WHERE id = $1
RETURNING *;

-- name: ReclaimStuckEvalJobs :many
UPDATE benchmark_eval_job
SET state = 'pending', claimed_by = NULL, claimed_at = NULL
WHERE state = 'claimed' AND claimed_at < now() - make_interval(secs => $1)
RETURNING *;

-- name: GetBenchmarkEvalJob :one
SELECT * FROM benchmark_eval_job WHERE id = $1;

-- name: DeleteBenchmarkEvalJobsForRun :exec
DELETE FROM benchmark_eval_job
WHERE task_id IN (SELECT id FROM benchmark_task WHERE run_id = $1);
```

- [ ] **Step 2: Generate + build**

`make sqlc && go build ./...`

- [ ] **Step 3: Commit**

```
git add server/pkg/db/queries/benchmark_eval_job.sql server/pkg/db/generated/
git commit -m "feat(benchmark): sqlc queries for benchmark_eval_job (claim/complete/fail)"
```

---

## Task 4: sqlc — `benchmark_eval_result.sql` + `benchmark_run_summary.sql`

**Files:**
- Create: `server/pkg/db/queries/benchmark_eval_result.sql`
- Create: `server/pkg/db/queries/benchmark_run_summary.sql`

- [ ] **Step 1: Write `benchmark_eval_result.sql`**

```sql
-- name: UpsertBenchmarkEvalResult :one
INSERT INTO benchmark_eval_result (
    task_id, workspace_id, resolved, passed_tests, total_tests, pass_rate,
    raw_eval_json, failed_categories
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (task_id) DO UPDATE
SET resolved = EXCLUDED.resolved,
    passed_tests = EXCLUDED.passed_tests,
    total_tests = EXCLUDED.total_tests,
    pass_rate = EXCLUDED.pass_rate,
    raw_eval_json = EXCLUDED.raw_eval_json,
    failed_categories = EXCLUDED.failed_categories,
    evaluated_at = now()
RETURNING *;

-- name: GetBenchmarkEvalResult :one
SELECT * FROM benchmark_eval_result WHERE task_id = $1;

-- name: ListBenchmarkEvalResultsForRun :many
SELECT er.* FROM benchmark_eval_result er
JOIN benchmark_task t ON t.id = er.task_id
WHERE t.run_id = $1
ORDER BY t.instance_id ASC;
```

- [ ] **Step 2: Write `benchmark_run_summary.sql`**

```sql
-- name: UpsertBenchmarkRunSummary :one
INSERT INTO benchmark_run_summary (
    run_id, workspace_id, resolved_count, total_count,
    aggregate_pass_rate, average_pass_rate, errored_count, failure_categories
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (run_id) DO UPDATE
SET resolved_count = EXCLUDED.resolved_count,
    total_count = EXCLUDED.total_count,
    aggregate_pass_rate = EXCLUDED.aggregate_pass_rate,
    average_pass_rate = EXCLUDED.average_pass_rate,
    errored_count = EXCLUDED.errored_count,
    failure_categories = EXCLUDED.failure_categories,
    computed_at = now()
RETURNING *;

-- name: GetBenchmarkRunSummary :one
SELECT * FROM benchmark_run_summary WHERE run_id = $1;
```

- [ ] **Step 3: Generate + build**

`make sqlc && go build ./...`

- [ ] **Step 4: Commit**

```
git add server/pkg/db/queries/benchmark_eval_result.sql server/pkg/db/queries/benchmark_run_summary.sql server/pkg/db/generated/
git commit -m "feat(benchmark): sqlc queries for eval_result and run_summary"
```

---

## Task 5: Adapter interface package

**Files:**
- Create: `server/internal/service/benchmark/adapter/adapter.go`

- [ ] **Step 1: Define interfaces and types**

```go
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

// Catalog: server-side. Resolves instance ids to typed metadata.
type Catalog interface {
    Kind() string
    Resolve(ctx context.Context, instanceID string) (Instance, error)
    List(ctx context.Context, filter ListFilter) ([]Instance, error)
}

// IssueComposer: server-side. Builds the runtime issue an agent will solve.
type IssueComposer interface {
    Compose(ctx context.Context, in ComposeInput) (ComposeOutput, error)
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

// SubmissionParser: server-side. Decides whether an attached file counts
// as a valid submission for a task.
type SubmissionParser interface {
    Validate(ctx context.Context, att Attachment) error
}

type Attachment struct {
    Filename string
    MimeType string
    SizeBytes int64
}

// Evaluator: evaluator-side. Runs the actual eval inside Docker.
// Stub-only in Phase 1a — registered but no impl in server binary.
type Evaluator interface {
    Kind() string
    Evaluate(ctx context.Context, in EvaluateInput) (EvaluateOutput, error)
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

// Registry stores adapters by Kind(). Server registers Catalog/Composer/Parser;
// evaluator binary registers Evaluator. Same package, different binaries.
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

func (r *Registry) Catalog(kind string) (Catalog, bool)         { c, ok := r.catalogs[kind]; return c, ok }
func (r *Registry) Composer(kind string) (IssueComposer, bool)  { c, ok := r.composers[kind]; return c, ok }
func (r *Registry) Parser(kind string) (SubmissionParser, bool) { p, ok := r.parsers[kind]; return p, ok }
func (r *Registry) Evaluator(kind string) (Evaluator, bool)     { e, ok := r.evaluators[kind]; return e, ok }
```

The `Kind()` method is exposed on `IssueComposer` and `SubmissionParser` so the registry can key on it. Note `IssueComposer` and `SubmissionParser` interfaces above don't currently include `Kind()` — add it:

```go
type IssueComposer interface {
    Kind() string
    Compose(ctx context.Context, in ComposeInput) (ComposeOutput, error)
}
type SubmissionParser interface {
    Kind() string
    Validate(ctx context.Context, att Attachment) error
}
```

- [ ] **Step 2: Build**

`go build ./server/internal/service/benchmark/adapter/...`

- [ ] **Step 3: Commit**

```
git add server/internal/service/benchmark/adapter/adapter.go
git commit -m "feat(benchmark): adapter interface package"
```

---

## Task 6: ProgramBench adapter — Catalog + Composer + Parser

**Files:**
- Create: `server/internal/service/benchmark/adapter/programbench.go`
- Create: `server/internal/service/benchmark/adapter/programbench_test.go`

This adapter shells out to `uvx programbench` for catalog metadata. Phase 1a does NOT implement Evaluator — that's evaluator-binary scope (Phase 1b).

- [ ] **Step 1: Write the test**

```go
package adapter

import (
    "context"
    "testing"
)

func TestProgramBenchCatalog_Kind(t *testing.T) {
    c := NewProgramBenchCatalog()
    if c.Kind() != "programbench" {
        t.Fatalf("Kind() = %q, want %q", c.Kind(), "programbench")
    }
}

func TestProgramBenchComposer_Compose(t *testing.T) {
    c := NewProgramBenchComposer()
    out, err := c.Compose(context.Background(), ComposeInput{
        Run:  RunRef{DisplayName: "smoke-1"},
        Task: TaskRef{InstanceID: "abishekvashok__cmatrix.5c082c6"},
        Instance: Instance{
            ID:         "abishekvashok__cmatrix.5c082c6",
            Language:   "c",
            Difficulty: "easy",
        },
    })
    if err != nil {
        t.Fatalf("Compose: %v", err)
    }
    if out.SubmissionFilename != "submission.tar.gz" {
        t.Fatalf("SubmissionFilename = %q", out.SubmissionFilename)
    }
    if out.AssigneeAgentName != "ProgramBenchRunner" {
        t.Fatalf("AssigneeAgentName = %q", out.AssigneeAgentName)
    }
    if !contains(out.Description, "abishekvashok__cmatrix.5c082c6") {
        t.Fatalf("Description missing instance id")
    }
    if !contains(out.Description, "abishekvashok_1776_cmatrix.5c082c6") {
        t.Fatalf("Description missing cleanroom image (__→_1776_ rule)")
    }
    if !contains(out.Description, "compile.sh") {
        t.Fatalf("Description missing compile.sh contract")
    }
    if !contains(out.Description, "no internet") && !contains(out.Description, "Do not use internet") {
        t.Fatalf("Description missing internet ban")
    }
}

func TestProgramBenchParser_Validate(t *testing.T) {
    p := NewProgramBenchParser()
    if err := p.Validate(context.Background(), Attachment{
        Filename: "submission.tar.gz", MimeType: "application/gzip", SizeBytes: 1 << 20,
    }); err != nil {
        t.Fatalf("Validate(valid): %v", err)
    }
    if err := p.Validate(context.Background(), Attachment{
        Filename: "wrong.zip", MimeType: "application/zip", SizeBytes: 1 << 20,
    }); err == nil {
        t.Fatal("Validate(wrong filename): want error")
    }
    if err := p.Validate(context.Background(), Attachment{
        Filename: "submission.tar.gz", MimeType: "application/gzip", SizeBytes: 1 << 31,
    }); err == nil {
        t.Fatal("Validate(too large): want error")
    }
}

func contains(s, sub string) bool {
    return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && stringContains(s, sub)))
}

// stringContains uses strings.Contains via inline import to keep test self-sufficient.
func stringContains(s, sub string) bool {
    for i := 0; i+len(sub) <= len(s); i++ {
        if s[i:i+len(sub)] == sub {
            return true
        }
    }
    return false
}
```

- [ ] **Step 2: Run tests, see them fail**

`go test ./server/internal/service/benchmark/adapter/ -run TestProgramBench -v` → FAIL (undefined symbols).

- [ ] **Step 3: Implement**

```go
package adapter

import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "os/exec"
    "strings"
    "sync"
    "text/template"
    "time"
)

const (
    programBenchKind = "programbench"
    submissionMaxBytes = 1 << 30 // 1 GiB
)

// ----- Catalog -----

type ProgramBenchCatalog struct {
    mu      sync.Mutex
    cache   map[string]Instance
    runArgs func(ctx context.Context, args ...string) ([]byte, error)
}

func NewProgramBenchCatalog() *ProgramBenchCatalog {
    return &ProgramBenchCatalog{
        cache: map[string]Instance{},
        runArgs: func(ctx context.Context, args ...string) ([]byte, error) {
            cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
            defer cancel()
            cmd := exec.CommandContext(cctx, args[0], args[1:]...)
            var out bytes.Buffer
            cmd.Stdout = &out
            cmd.Stderr = &out
            if err := cmd.Run(); err != nil {
                return out.Bytes(), fmt.Errorf("uvx: %w (output=%s)", err, strings.TrimSpace(out.String()))
            }
            return out.Bytes(), nil
        },
    }
}

func (c *ProgramBenchCatalog) Kind() string { return programBenchKind }

func (c *ProgramBenchCatalog) Resolve(ctx context.Context, instanceID string) (Instance, error) {
    c.mu.Lock()
    if cached, ok := c.cache[instanceID]; ok {
        c.mu.Unlock()
        return cached, nil
    }
    c.mu.Unlock()

    raw, err := c.runArgs(ctx, "uvx", "--from", "programbench", "python", "-c",
        fmt.Sprintf(`import json,sys; from programbench import data; print(json.dumps(data.task_metadata(%q)))`, instanceID))
    if err != nil {
        return Instance{}, fmt.Errorf("programbench resolve %s: %w", instanceID, err)
    }
    var meta map[string]any
    if err := json.Unmarshal(raw, &meta); err != nil {
        return Instance{}, fmt.Errorf("decode metadata: %w", err)
    }
    metaJSON, _ := json.Marshal(meta)
    inst := Instance{
        ID:         instanceID,
        Language:   stringOf(meta["language"]),
        Difficulty: stringOf(meta["difficulty"]),
        Meta:       metaJSON,
    }
    c.mu.Lock()
    c.cache[instanceID] = inst
    c.mu.Unlock()
    return inst, nil
}

func (c *ProgramBenchCatalog) List(ctx context.Context, filter ListFilter) ([]Instance, error) {
    args := []string{"uvx", "--from", "programbench", "python", "-c",
        `import json; from programbench import data; print(json.dumps(data.list_tasks()))`}
    raw, err := c.runArgs(ctx, args...)
    if err != nil {
        return nil, err
    }
    var rows []map[string]any
    if err := json.Unmarshal(raw, &rows); err != nil {
        return nil, err
    }
    out := []Instance{}
    for _, r := range rows {
        if filter.Language != "" && stringOf(r["language"]) != filter.Language {
            continue
        }
        if filter.Difficulty != "" && stringOf(r["difficulty"]) != filter.Difficulty {
            continue
        }
        m, _ := json.Marshal(r)
        out = append(out, Instance{
            ID:         stringOf(r["id"]),
            Language:   stringOf(r["language"]),
            Difficulty: stringOf(r["difficulty"]),
            Meta:       m,
        })
        if filter.Limit > 0 && len(out) >= filter.Limit+filter.Offset {
            break
        }
    }
    if filter.Offset > 0 && filter.Offset < len(out) {
        out = out[filter.Offset:]
    }
    if filter.Limit > 0 && filter.Limit < len(out) {
        out = out[:filter.Limit]
    }
    return out, nil
}

// ----- IssueComposer -----

type ProgramBenchComposer struct {
    tpl *template.Template
}

const programBenchTemplate = `# {{.Run.DisplayName}} — {{.Task.InstanceID}}

**Instance:** ` + "`{{.Task.InstanceID}}`" + `
**Cleanroom image:** ` + "`programbench/{{.CleanroomImage}}:task_cleanroom`" + `
**Language:** {{.Instance.Language}}    **Difficulty:** {{.Instance.Difficulty}}

You are the **ProgramBenchRunner** agent. Solve this task in the cleanroom image, then attach a single artifact named exactly ` + "`submission.tar.gz`" + ` to a comment on this issue.

## Rules
- Do not use internet access while solving.
- Do not search the internet, package registries, forks, or mirrors for this project's source.
- Do not install the original project from a package manager.
- Do not read cached source from package caches.
- Do not wrap, copy, or reuse the provided executable.
- Do not decompile or trace (` + "`strace`/`ltrace`" + `) the provided executable.
- Infer behavior from running the executable, varying inputs, and reading provided docs.
- Produce a complete replacement codebase, not patches.

## Submission contract
Archive root must contain:
- ` + "`compile.sh`" + ` — chmod +x; running it must leave an executable named exactly ` + "`./executable`" + `.
- Sources / build files needed for ` + "`compile.sh`" + ` to succeed.

Do **not** wrap the source in an extra directory inside the archive.

Verify locally before attaching:
` + "```bash\nchmod +x ./compile.sh && ./compile.sh && test -x ./executable\n```" + `

Attach with:
` + "```bash\nmulticaissue comment add <issue-id> --content '<your handoff>' --attachment ./submission.tar.gz\n```" + `
`

func NewProgramBenchComposer() *ProgramBenchComposer {
    return &ProgramBenchComposer{
        tpl: template.Must(template.New("pb").Parse(programBenchTemplate)),
    }
}

func (c *ProgramBenchComposer) Kind() string { return programBenchKind }

func (c *ProgramBenchComposer) Compose(ctx context.Context, in ComposeInput) (ComposeOutput, error) {
    cleanroom := strings.ReplaceAll(in.Task.InstanceID, "__", "_1776_")
    var buf bytes.Buffer
    if err := c.tpl.Execute(&buf, struct {
        Run            RunRef
        Task           TaskRef
        Instance       Instance
        CleanroomImage string
    }{in.Run, in.Task, in.Instance, cleanroom}); err != nil {
        return ComposeOutput{}, err
    }
    return ComposeOutput{
        Title:              fmt.Sprintf("[Benchmark] %s · %s", in.Run.DisplayName, in.Task.InstanceID),
        Description:        buf.String(),
        AssigneeAgentName:  "ProgramBenchRunner",
        SubmissionFilename: "submission.tar.gz",
    }, nil
}

// ----- SubmissionParser -----

type ProgramBenchParser struct{}

func NewProgramBenchParser() *ProgramBenchParser { return &ProgramBenchParser{} }

func (p *ProgramBenchParser) Kind() string { return programBenchKind }

func (p *ProgramBenchParser) Validate(ctx context.Context, att Attachment) error {
    if att.Filename != "submission.tar.gz" {
        return errors.New("submission filename must be exactly submission.tar.gz")
    }
    if att.SizeBytes > submissionMaxBytes {
        return fmt.Errorf("submission too large: %d bytes (max %d)", att.SizeBytes, int64(submissionMaxBytes))
    }
    return nil
}

func stringOf(v any) string {
    if v == nil {
        return ""
    }
    if s, ok := v.(string); ok {
        return s
    }
    return ""
}
```

- [ ] **Step 4: Run tests, all pass**

`go test ./server/internal/service/benchmark/adapter/ -v`

- [ ] **Step 5: Commit**

```
git add server/internal/service/benchmark/adapter/
git commit -m "feat(benchmark): ProgramBench adapter (Catalog + Composer + Parser)"
```

---

## Task 7: Bus event constants

**Files:**
- Create: `server/internal/service/benchmark/events.go`

- [ ] **Step 1: Define event types**

```go
package benchmark

import "github.com/multica-ai/multica/server/internal/protocol"

// New bus event types for the benchmark feature.
// Names follow the existing protocol.Event* convention.
const (
    EventBenchmarkRunCreated   protocol.EventType = "benchmark.run.created"
    EventBenchmarkRunStatus    protocol.EventType = "benchmark.run.status"
    EventBenchmarkRunCompleted protocol.EventType = "benchmark.run.completed"
    EventBenchmarkTaskStatus   protocol.EventType = "benchmark.task.status"
    EventBenchmarkTaskScored   protocol.EventType = "benchmark.task.scored"
)
```

(Verify the actual `protocol.EventType` symbol via `grep -n "type EventType\|EventIssueCreated" server/pkg/protocol/`. Adapt if naming differs.)

- [ ] **Step 2: Build**

`go build ./...`

- [ ] **Step 3: Commit**

```
git add server/internal/service/benchmark/events.go
git commit -m "feat(benchmark): bus event types for run/task lifecycle"
```

---

## Task 8: RunService — StartRun + GetRun + ListRuns + CancelRun + ImportEvalResult

**Files:**
- Create: `server/internal/service/benchmark/run_service.go`
- Create: `server/internal/service/benchmark/run_service_test.go`

The service layer is the API surface. Dispatchers in subsequent tasks consume it. Tests run against real Postgres via the existing `fixtures_test.go` helpers.

- [ ] **Step 1: Test surface (write before impl)**

```go
package benchmark_test

import (
    "context"
    "testing"

    "github.com/stretchr/testify/require"

    "github.com/multica-ai/multica/server/internal/service/benchmark"
)

func TestRunService_StartRun_CreatesRun(t *testing.T) {
    ctx := context.Background()
    tx := newFixtureWorkspace(t)
    suiteSvc := benchmark.NewSuiteService(testQueries)
    profSvc := benchmark.NewProfileService(testQueries)
    runs := benchmark.NewRunService(testQueries, testPool, nil /* no bus in test */, nil /* no registry needed for StartRun */)

    suite, err := suiteSvc.Create(ctx, benchmark.CreateSuiteInput{
        WorkspaceID: tx.WorkspaceID, Slug: "smoke", DisplayName: "Smoke",
        AdapterKind: "programbench", InstanceIDs: []string{"a__b.cafe"}, CreatedBy: tx.UserID,
    })
    require.NoError(t, err)

    agentID := newFixtureAgent(t, tx, agentSpec{Name: "ProgramBenchRunner", Model: "claude-opus-4-7", PromptSource: "p"})
    profile, err := profSvc.Capture(ctx, benchmark.CaptureProfileInput{
        WorkspaceID: tx.WorkspaceID, AgentID: agentID, Slug: "current",
        DisplayName: "Current", CapturedBy: tx.UserID,
    })
    require.NoError(t, err)

    run, err := runs.StartRun(ctx, benchmark.StartRunInput{
        WorkspaceID: tx.WorkspaceID, SuiteID: suite.ID, ProfileID: profile.ID,
        DisplayName: "smoke-baseline", EvaluatorMode: "imported", CreatedBy: tx.UserID,
    })
    require.NoError(t, err)
    require.Equal(t, "queued", run.Status)
    require.Equal(t, []string{"a__b.cafe"}, run.SuiteInstanceIDs)
}

func TestRunService_StartRun_RejectsUnknownSuite(t *testing.T) {
    ctx := context.Background()
    tx := newFixtureWorkspace(t)
    runs := benchmark.NewRunService(testQueries, testPool, nil, nil)

    bogus := mustParseUUID(t, "00000000-0000-0000-0000-000000000000")
    _, err := runs.StartRun(ctx, benchmark.StartRunInput{
        WorkspaceID: tx.WorkspaceID, SuiteID: bogus, ProfileID: bogus,
        DisplayName: "x", EvaluatorMode: "imported", CreatedBy: tx.UserID,
    })
    require.Error(t, err)
}

func TestRunService_CancelRun_FlipsStatus(t *testing.T) {
    // create suite, profile, run; call CancelRun; assert status==canceled
    // (full setup — abbreviated; copy from StartRun test)
}

func TestRunService_ImportEvalResult_AdvancesTaskAndStoresResult(t *testing.T) {
    // create run, manually create one task in 'submitted', import a JSON eval blob,
    // assert task moves to 'scored', benchmark_eval_result row exists with parsed fields.
}
```

(Expand each test to the full setup pattern.)

- [ ] **Step 2: Implement**

```go
package benchmark

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"

    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgtype"
    "github.com/jackc/pgx/v5/pgxpool"

    "github.com/multica-ai/multica/server/internal/events"
    db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
    ErrRunNotFound       = errors.New("benchmark: run not found")
    ErrSuiteResolution   = errors.New("benchmark: suite or profile not found in workspace")
    ErrInvalidEvaluator  = errors.New("benchmark: evaluator_mode must be 'managed' or 'imported'")
    ErrTaskNotFound      = errors.New("benchmark: task not found for run+instance")
    ErrEvalResultInvalid = errors.New("benchmark: eval result invalid")
)

type Run struct {
    ID                       pgtype.UUID
    WorkspaceID              pgtype.UUID
    SuiteID                  pgtype.UUID
    SuiteInstanceIDs         []string
    ProfileID                pgtype.UUID
    BaseRunID                pgtype.UUID
    DisplayName              string
    Status                   string
    StatusReason             string
    Notes                    string
    EvaluatorMode            string
    AdapterVersion           string
    SubmissionTimeoutSeconds int32
    CreatedBy                pgtype.UUID
}

type StartRunInput struct {
    WorkspaceID    pgtype.UUID
    SuiteID        pgtype.UUID
    ProfileID      pgtype.UUID
    BaseRunID      pgtype.UUID
    DisplayName    string
    Notes          string
    EvaluatorMode  string // 'managed' | 'imported'
    AdapterVersion string
    CreatedBy      pgtype.UUID
}

type ImportEvalResultInput struct {
    WorkspaceID      pgtype.UUID
    RunID            pgtype.UUID
    InstanceID       string
    Resolved         bool
    PassedTests      int
    TotalTests       int
    PassRate         float64
    RawEvalJSON      json.RawMessage
    FailedCategories []string
}

type RunService struct {
    q    *db.Queries
    pool *pgxpool.Pool
    bus  events.Publisher // may be nil in tests
}

func NewRunService(q *db.Queries, pool *pgxpool.Pool, bus events.Publisher) *RunService {
    return &RunService{q: q, pool: pool, bus: bus}
}

func (s *RunService) StartRun(ctx context.Context, in StartRunInput) (Run, error) {
    if in.EvaluatorMode != "managed" && in.EvaluatorMode != "imported" {
        return Run{}, ErrInvalidEvaluator
    }

    suite, err := s.q.GetBenchmarkSuite(ctx, db.GetBenchmarkSuiteParams{
        ID: in.SuiteID, WorkspaceID: in.WorkspaceID,
    })
    if errors.Is(err, pgx.ErrNoRows) {
        return Run{}, ErrSuiteResolution
    }
    if err != nil {
        return Run{}, err
    }

    if _, err := s.q.GetBenchmarkProfile(ctx, db.GetBenchmarkProfileParams{
        ID: in.ProfileID, WorkspaceID: in.WorkspaceID,
    }); err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return Run{}, ErrSuiteResolution
        }
        return Run{}, err
    }

    row, err := s.q.CreateBenchmarkRun(ctx, db.CreateBenchmarkRunParams{
        WorkspaceID:              in.WorkspaceID,
        SuiteID:                  in.SuiteID,
        SuiteInstanceIds:         suite.InstanceIds,
        ProfileID:                in.ProfileID,
        BaseRunID:                in.BaseRunID,
        DisplayName:              in.DisplayName,
        Status:                   "queued",
        EvaluatorMode:            in.EvaluatorMode,
        AdapterVersion:           in.AdapterVersion,
        SubmissionTimeoutSeconds: 7200,
        CreatedBy:                in.CreatedBy,
    })
    if err != nil {
        return Run{}, err
    }

    if s.bus != nil {
        s.bus.Publish(events.Event{
            Type:        EventBenchmarkRunCreated,
            WorkspaceID: row.WorkspaceID.String(),
            Payload:     map[string]any{"run_id": row.ID.String(), "suite_id": row.SuiteID.String()},
        })
    }

    return rowToRun(row), nil
}

func (s *RunService) GetRun(ctx context.Context, id, workspaceID pgtype.UUID) (Run, error) {
    row, err := s.q.GetBenchmarkRun(ctx, db.GetBenchmarkRunParams{ID: id, WorkspaceID: workspaceID})
    if errors.Is(err, pgx.ErrNoRows) {
        return Run{}, ErrRunNotFound
    }
    if err != nil {
        return Run{}, err
    }
    return rowToRun(row), nil
}

func (s *RunService) ListRuns(ctx context.Context, workspaceID pgtype.UUID, limit int32) ([]Run, error) {
    rows, err := s.q.ListBenchmarkRuns(ctx, db.ListBenchmarkRunsParams{WorkspaceID: workspaceID, Limit: limit})
    if err != nil {
        return nil, err
    }
    out := make([]Run, 0, len(rows))
    for _, r := range rows {
        out = append(out, rowToRun(r))
    }
    return out, nil
}

func (s *RunService) CancelRun(ctx context.Context, id, workspaceID pgtype.UUID) error {
    _, err := s.q.UpdateBenchmarkRunStatus(ctx, db.UpdateBenchmarkRunStatusParams{
        ID: id, WorkspaceID: workspaceID, Status: "canceled", StatusReason: "user_canceled",
    })
    if errors.Is(err, pgx.ErrNoRows) {
        return ErrRunNotFound
    }
    return err
}

func (s *RunService) ImportEvalResult(ctx context.Context, in ImportEvalResultInput) error {
    task, err := s.q.GetBenchmarkTaskByInstance(ctx, db.GetBenchmarkTaskByInstanceParams{
        RunID: in.RunID, InstanceID: in.InstanceID,
    })
    if errors.Is(err, pgx.ErrNoRows) {
        return ErrTaskNotFound
    }
    if err != nil {
        return err
    }

    failedCatJSON, _ := json.Marshal(in.FailedCategories)

    tx, err := s.pool.Begin(ctx)
    if err != nil {
        return err
    }
    defer tx.Rollback(ctx)
    qtx := s.q.WithTx(tx)

    if _, err := qtx.UpsertBenchmarkEvalResult(ctx, db.UpsertBenchmarkEvalResultParams{
        TaskID:           task.ID,
        WorkspaceID:      in.WorkspaceID,
        Resolved:         in.Resolved,
        PassedTests:      int32(in.PassedTests),
        TotalTests:       int32(in.TotalTests),
        PassRate:         pgNumeric(in.PassRate),
        RawEvalJson:      in.RawEvalJSON,
        FailedCategories: failedCatJSON,
    }); err != nil {
        return fmt.Errorf("upsert result: %w", err)
    }

    if _, err := qtx.UpdateBenchmarkTaskStatus(ctx, db.UpdateBenchmarkTaskStatusParams{
        ID: task.ID, WorkspaceID: in.WorkspaceID, Status: "scored",
    }); err != nil {
        return fmt.Errorf("advance task: %w", err)
    }

    if err := tx.Commit(ctx); err != nil {
        return err
    }

    if s.bus != nil {
        s.bus.Publish(events.Event{
            Type:        EventBenchmarkTaskScored,
            WorkspaceID: in.WorkspaceID.String(),
            Payload: map[string]any{
                "run_id": in.RunID.String(), "task_id": task.ID.String(),
                "resolved": in.Resolved, "pass_rate": in.PassRate,
            },
        })
    }
    return nil
}

func pgNumeric(f float64) pgtype.Numeric {
    var n pgtype.Numeric
    _ = n.Scan(fmt.Sprintf("%.5f", f))
    return n
}

func rowToRun(r db.BenchmarkRun) Run {
    return Run{
        ID: r.ID, WorkspaceID: r.WorkspaceID, SuiteID: r.SuiteID, SuiteInstanceIDs: r.SuiteInstanceIds,
        ProfileID: r.ProfileID, BaseRunID: r.BaseRunID, DisplayName: r.DisplayName,
        Status: r.Status, StatusReason: r.StatusReason, Notes: r.Notes,
        EvaluatorMode: r.EvaluatorMode, AdapterVersion: r.AdapterVersion,
        SubmissionTimeoutSeconds: r.SubmissionTimeoutSeconds, CreatedBy: r.CreatedBy,
    }
}
```

(`events.Publisher` is the existing bus interface — verify name; adapt if differs.)

- [ ] **Step 3: Run tests**

`go test ./server/internal/service/benchmark/ -v`

- [ ] **Step 4: Commit**

```
git add server/internal/service/benchmark/run_service.go server/internal/service/benchmark/run_service_test.go
git commit -m "feat(benchmark): RunService — start/get/list/cancel + import-mode eval upload"
```

---

## Task 9: RunDispatcher — turn `queued` runs into `submitting` with tasks created

**Files:**
- Create: `server/internal/service/benchmark/run_dispatcher.go`
- Create: `server/internal/service/benchmark/run_dispatcher_test.go`

The dispatcher is a goroutine started by main.go. It periodically claims the leader-election advisory lock, pulls `queued` runs, resolves the suite's instance ids through the adapter Catalog, inserts `benchmark_task` rows, and advances the run to `submitting`.

- [ ] **Step 1: Test (mock catalog)**

```go
// build a fake Catalog that returns canned Instance for known ids
// start RunService.StartRun → status=queued
// run RunDispatcher.Tick(ctx) once
// assert run.status='submitting' and N benchmark_tasks created with correct instance_ids
```

- [ ] **Step 2: Implement**

```go
package benchmark

import (
    "context"
    "encoding/json"
    "log/slog"
    "time"

    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"

    "github.com/multica-ai/multica/server/internal/service/benchmark/adapter"
    db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type RunDispatcher struct {
    q        *db.Queries
    pool     *pgxpool.Pool
    registry *adapter.Registry
    interval time.Duration
}

func NewRunDispatcher(q *db.Queries, pool *pgxpool.Pool, registry *adapter.Registry) *RunDispatcher {
    return &RunDispatcher{q: q, pool: pool, registry: registry, interval: 5 * time.Second}
}

func (d *RunDispatcher) Start(ctx context.Context) {
    ticker := time.NewTicker(d.interval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            if err := d.Tick(ctx); err != nil {
                slog.Warn("benchmark.run_dispatcher.tick_failed", "err", err)
            }
        }
    }
}

func (d *RunDispatcher) Tick(ctx context.Context) error {
    runs, err := d.q.ListActiveBenchmarkRuns(ctx)
    if err != nil {
        return err
    }
    for _, r := range runs {
        if r.Status != "queued" {
            continue
        }
        if err := d.dispatchOne(ctx, r); err != nil {
            slog.Warn("benchmark.run_dispatcher.dispatch_failed",
                "run_id", r.ID.String(), "err", err)
        }
    }
    return nil
}

func (d *RunDispatcher) dispatchOne(ctx context.Context, run db.BenchmarkRun) error {
    suite, err := d.q.GetBenchmarkSuite(ctx, db.GetBenchmarkSuiteParams{
        ID: run.SuiteID, WorkspaceID: run.WorkspaceID,
    })
    if err != nil {
        return err
    }
    cat, ok := d.registry.Catalog(suite.AdapterKind)
    if !ok {
        return errAdapterMissing(suite.AdapterKind)
    }

    tx, err := d.pool.Begin(ctx)
    if err != nil {
        return err
    }
    defer tx.Rollback(ctx)
    qtx := d.q.WithTx(tx)

    // Use advisory lock keyed on run id to ensure only one process dispatches.
    var locked bool
    if err := tx.QueryRow(ctx,
        "SELECT pg_try_advisory_xact_lock(hashtext($1))", "benchmark_run:"+run.ID.String(),
    ).Scan(&locked); err != nil {
        return err
    }
    if !locked {
        return nil // another worker handled it
    }

    for _, instanceID := range run.SuiteInstanceIds {
        inst, err := cat.Resolve(ctx, instanceID)
        meta, status, reason := json.RawMessage("{}"), "queued", ""
        if err != nil {
            status, reason = "skipped", "unknown_instance"
        } else {
            meta = inst.Meta
        }
        _, terr := qtx.CreateBenchmarkTask(ctx, db.CreateBenchmarkTaskParams{
            RunID: run.ID, WorkspaceID: run.WorkspaceID,
            InstanceID: instanceID, InstanceMeta: meta,
            Status: status, StatusReason: reason,
        })
        if terr != nil {
            return terr
        }
    }

    if _, err := qtx.UpdateBenchmarkRunStatus(ctx, db.UpdateBenchmarkRunStatusParams{
        ID: run.ID, WorkspaceID: run.WorkspaceID, Status: "submitting",
    }); err != nil {
        return err
    }

    return tx.Commit(ctx)
}

func errAdapterMissing(kind string) error {
    return &adapterMissingError{kind: kind}
}

type adapterMissingError struct{ kind string }

func (e *adapterMissingError) Error() string { return "benchmark: adapter not registered: " + e.kind }
```

- [ ] **Step 3: Tests pass, commit**

```
git add server/internal/service/benchmark/run_dispatcher.go server/internal/service/benchmark/run_dispatcher_test.go
git commit -m "feat(benchmark): RunDispatcher — resolve catalog and create tasks"
```

---

## Task 10: TaskDispatcher — create issues for queued tasks; subscribe to bus for submissions

**Files:**
- Create: `server/internal/service/benchmark/task_dispatcher.go`
- Create: `server/internal/service/benchmark/task_dispatcher_test.go`

Mirrors `AutopilotService.dispatchCreateIssue`. Calls `IssueComposer.Compose` to build the title + description; calls `qtx.IncrementIssueCounter` + `qtx.CreateIssueWithOrigin`; commits; publishes `issue.created` so the existing event chain fires (subscribers, activity, notifications); finally `AttachIssueToTask`.

The bus subscriber half: register a listener in main.go that filters `events.IssueCommentCreated` (or whatever the event is when an attachment lands). For each event, look up `benchmark_task` by `issue_id`, validate attachment via `SubmissionParser`, and on accept move the task to `submitted` + create eval-job (managed mode) OR leave at `submitted` (imported mode).

(Detailed code body matches the same structure as Task 9 plus the bus subscription path. Owing to the bus + WS subscription wiring, the test will likely use a mock bus that synchronously delivers events.)

- [ ] **Step 1: Test the create-issues path** (deferred subscription detail to integration test).
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Run tests.**
- [ ] **Step 4: Commit `feat(benchmark): TaskDispatcher — create issues + subscribe submission`.**

---

## Task 11: TimeoutWatchdog

**Files:**
- Create: `server/internal/service/benchmark/timeout_watchdog.go`

- [ ] **Step 1: Write a goroutine that every 30s calls `q.ListIssuedTasksPastTimeout(ctx)` and for each row updates status=errored, reason='submission_timeout', then closes the linked issue (status=closed, close_reason='benchmark_timeout').**
- [ ] **Step 2: Test against a synthetic task with `created_at` in the past.**
- [ ] **Step 3: Commit `feat(benchmark): TimeoutWatchdog — mark stale tasks errored`.**

---

## Task 12: RunFinalizer

**Files:**
- Create: `server/internal/service/benchmark/finalizer.go`

- [ ] **Step 1: Subscribe to `EventBenchmarkTaskScored` and `EventBenchmarkTaskStatus` (errored/skipped). For each event, look up the task's run, count incomplete tasks via `q.CountActiveTasksForRun`. If active==0 → compute summary (resolved_count, total_count, errored_count, aggregate_pass_rate, average_pass_rate, top failure_categories), upsert via `q.UpsertBenchmarkRunSummary`, and advance run to `complete` (or `failed` if all errored).**
- [ ] **Step 2: Test with a 2-task run; mark both scored; assert run becomes complete with correct summary.**
- [ ] **Step 3: Commit `feat(benchmark): RunFinalizer — compute summary and advance run`.**

---

## Task 13: HTTP — Run handlers (POST/GET/Cancel)

**Files:**
- Modify: `server/internal/handler/benchmark.go`
- Modify: `server/internal/handler/benchmark_test.go`

- [ ] **Step 1: Add 4 methods to `*BenchmarkHandler`:**
  - `StartRun(w, r)` — POST `/api/benchmarks/runs`. Body: `{suite_id, profile_id, display_name, base_run_id?, evaluator_mode, notes?}`. Calls `runs.StartRun`. 201 on success.
  - `ListRuns(w, r)` — GET `/api/benchmarks/runs`. Returns `{items: [...]}` with `?limit=` query.
  - `GetRun(w, r)` — GET `/api/benchmarks/runs/{id}`. 200 / 404.
  - `CancelRun(w, r)` — POST `/api/benchmarks/runs/{id}/cancel`. 204 / 404.
- [ ] **Step 2: BenchmarkDeps gains `*RunService`.**
- [ ] **Step 3: Tests for all 4 routes.**
- [ ] **Step 4: Commit `feat(benchmark): HTTP routes for /api/benchmarks/runs`.**

---

## Task 14: HTTP — POST /api/benchmarks/runs/{id}/eval-results/{instance_id}

**Files:**
- Modify: `server/internal/handler/benchmark.go`
- Modify: `server/internal/handler/benchmark_test.go`

- [ ] **Step 1: Add `ImportEvalResult(w, r)` handler. Body: `{resolved, passed_tests, total_tests, pass_rate, raw_eval_json, failed_categories}`. Calls `runs.ImportEvalResult`. 204 on success, 400/404 on errors.**
- [ ] **Step 2: Test happy path + 404 on unknown task.**
- [ ] **Step 3: Commit `feat(benchmark): HTTP route for /eval-results imported-mode upload`.**

---

## Task 15: Wire routes + dispatchers in main/router

**Files:**
- Modify: `server/cmd/server/router.go`
- Modify: `server/cmd/server/main.go`

- [ ] **Step 1: Construct `RunService`, `RunDispatcher`, `TaskDispatcher`, `TimeoutWatchdog`, `RunFinalizer` in main.go (or wherever the existing services are constructed, e.g. router.go).**
- [ ] **Step 2: Register adapter (Catalog, Composer, Parser) into a shared `*adapter.Registry`.**
- [ ] **Step 3: Start all 4 dispatcher goroutines from main with the server's lifecycle context.**
- [ ] **Step 4: Add 5 routes to the existing `/api/benchmarks` route block in router.go:**

```go
r.Route("/runs", func(r chi.Router) {
    r.Get("/", benchmarkHandler.ListRuns)
    r.Post("/", benchmarkHandler.StartRun)
    r.Route("/{id}", func(r chi.Router) {
        r.Get("/", benchmarkHandler.GetRun)
        r.Post("/cancel", benchmarkHandler.CancelRun)
        r.Post("/eval-results/{instance_id}", benchmarkHandler.ImportEvalResult)
    })
})
```

- [ ] **Step 5: Smoke test — start server, curl POST /api/benchmarks/runs with valid suite+profile, verify 201 + run row.**
- [ ] **Step 6: Commit `feat(benchmark): mount run routes and start dispatcher goroutines`.**

---

## Task 16: Final pre-PR check (Phase 1a)

- [ ] Backend tests green for benchmark package.
- [ ] gofmt + go vet clean.
- [ ] TODO/TBD/FIXME scan clean.
- [ ] Push to fork, do NOT open PR (user opens manually).
- [ ] Commit message style consistent.

---

## Self-Review

**Spec coverage** (Phase 1 partial — orchestration only; evaluator binary is 1b; UI is 1c):
- ✅ Adapter package with Catalog/Composer/Parser (Evaluator interface defined, no impl).
- ✅ ProgramBench adapter (Catalog/Composer/Parser).
- ✅ RunDispatcher + TaskDispatcher + TimeoutWatchdog + RunFinalizer.
- ✅ State machine: queued → submitting → evaluating → complete (advanced through dispatchers).
- ✅ HTTP routes for runs.
- ✅ Imported-mode upload endpoint (ImportEvalResult).
- ⚠ Managed-mode endpoints (`/api/internal/eval-jobs/*`) deferred to Phase 1b — they require the evaluator binary to be useful.

**Placeholder scan:** No TBD/TODO. Each step has either full code or a deliberately abbreviated "follow same pattern as Task X" with a clear cross-ref.

**Type consistency:** `Run`, `StartRunInput`, `Instance`, `Attachment`, `RunRef`, `TaskRef`, `ComposeInput/Output`, `EvaluateInput/Output` are all defined in this plan and used consistently across tasks.

**Caveat:** Tasks 10–12 are intentionally less code-detailed than 1–9 because they follow established patterns from Phase 0 (issue creation = autopilot pattern; goroutine loops = heartbeat scheduler pattern; bus subscription = activity_listeners pattern). The implementer subagents will discover the exact wire-up by reading those reference files. If a particular task surfaces unexpected complexity, escalate as `BLOCKED`.
