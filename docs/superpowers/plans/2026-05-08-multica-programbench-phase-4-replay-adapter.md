# Multica × ProgramBench Phase 4 — Replay Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** Add a `multica_replay` adapter so operators can use already-completed Multica issues (with reference solutions in ADO/Git/Jira) as benchmark instances. Run a different agent (or the same agent with different prompt) on the same task; grade the new solution by text-similarity to the reference. Super-simple MVP — no live ADO/Jira fetching at run time; operator captures the reference patch when creating the suite.

**Use case:** "Did my prompt change degrade the agent on tasks it could already solve?" — grab 10 closed-and-merged issues from the workspace, capture their reference solutions, and run a candidate agent against them.

**Architecture:**
- New adapter `multica_replay`, registered alongside `programbench`.
- Reuses ALL existing Phase 0–3 infrastructure (suites/profiles/runs/dispatchers/finalizer/UI). Adapter contract is the right abstraction; this just adds another impl.
- `instance_id` format: `multica-issue:<uuid>` (issue id from this workspace).
- `instance_meta` carries:
  ```json
  {
    "source_issue_id": "<uuid>",
    "source_issue_number": 42,
    "source_issue_title": "...",
    "reference_solution": "<full unified-diff text>",
    "reference_pr_url": "https://..." // optional, informational
  }
  ```
- Catalog `Resolve` reads from local Multica DB (no external API calls).
- Composer renders issue title+description as the task prompt to the agent.
- Parser accepts any attachment named exactly `solution.patch` (or `.diff`).
- Evaluator computes line-Jaccard similarity between submitted patch and reference patch; score becomes pass-rate; resolved if score ≥ 0.85.

---

## File Structure

| Path | Purpose |
|---|---|
| `server/internal/service/benchmark/adapter/replay.go` | Catalog + Composer + Parser + Evaluator (server + evaluator both register it; the eval is pure-Go text similarity, no Docker) |
| `server/internal/service/benchmark/adapter/replay_test.go` | Unit tests |
| `server/internal/service/benchmark/replay_suite_service.go` | Helper for suite-creation flow: enumerate workspace's completed issues + capture reference solutions |
| `server/internal/handler/benchmark.go` (extend) | New endpoint `GET /api/benchmarks/replay/eligible-issues` and helper for "build suite from issues" |
| `packages/views/benchmarks/SuiteCreate.tsx` (modify) | Add "Replay" mode tab — instead of pasting instance ids, pick from a list of completed issues + paste reference patch per issue |
| Locale keys |  |

---

## Task 1: Replay adapter (Catalog + Composer + Parser + Evaluator)

**Files:**
- `server/internal/service/benchmark/adapter/replay.go`
- `server/internal/service/benchmark/adapter/replay_test.go`

- [ ] **Step 1:** Implement.

```go
package adapter

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "strings"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5/pgtype"
    db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const replayKind = "multica_replay"

// ReplayInstanceMeta is the adapter-opaque blob stored on benchmark_task.instance_meta.
type ReplayInstanceMeta struct {
    SourceIssueID      string `json:"source_issue_id"`
    SourceIssueNumber  int32  `json:"source_issue_number"`
    SourceIssueTitle   string `json:"source_issue_title"`
    SourceIssueDescription string `json:"source_issue_description"`
    ReferenceSolution  string `json:"reference_solution"` // unified-diff text
    ReferencePRURL     string `json:"reference_pr_url,omitempty"`
}

// ReplayCatalog resolves instance ids of the form "multica-issue:<uuid>".
// It reads from the Multica DB to fetch the source issue at run time; the
// reference_solution must already be in instance_meta (captured at suite
// creation time — see ReplaySuiteService).
type ReplayCatalog struct {
    q *db.Queries
}

func NewReplayCatalog(q *db.Queries) *ReplayCatalog { return &ReplayCatalog{q: q} }

func (c *ReplayCatalog) Kind() string { return replayKind }

// Resolve refreshes title/description from the live issue but preserves the
// captured reference_solution from suite-creation time. Caller is expected to
// pass instance_meta from the suite/task; we don't do live discovery here.
//
// In practice the dispatcher passes the existing instance_meta verbatim to the
// task; this method exists for shape-compat with the Catalog interface but is
// effectively a no-op pass-through for replay.
func (c *ReplayCatalog) Resolve(ctx context.Context, instanceID string) (Instance, error) {
    issueID, err := parseReplayInstanceID(instanceID)
    if err != nil {
        return Instance{}, err
    }
    // Verify the issue still exists. We don't fetch description here because
    // we don't carry workspace_id; the suite-creation path captured the meta.
    _ = issueID // intentionally not used at resolve time
    return Instance{ID: instanceID, Language: "", Difficulty: "", Meta: nil}, nil
}

func (c *ReplayCatalog) List(ctx context.Context, _ ListFilter) ([]Instance, error) {
    // Listing is not supported via Catalog for replay — operators pick issues
    // through the dedicated suite-creation flow.
    return nil, errors.New("multica_replay: List not supported; use suite-creation flow")
}

// ReplayComposer builds the task description from instance_meta.
type ReplayComposer struct{}

func NewReplayComposer() *ReplayComposer { return &ReplayComposer{} }

func (c *ReplayComposer) Kind() string { return replayKind }

func (c *ReplayComposer) Compose(ctx context.Context, in ComposeInput) (ComposeOutput, error) {
    var meta ReplayInstanceMeta
    if len(in.Instance.Meta) > 0 {
        if err := json.Unmarshal(in.Instance.Meta, &meta); err != nil {
            return ComposeOutput{}, fmt.Errorf("decode instance_meta: %w", err)
        }
    }

    desc := fmt.Sprintf(
        "# %s — %s\n\n"+
            "**Replay benchmark.** This task is a replay of Multica issue %d (%q). "+
            "Solve it as if it were a new task; do not consult the original PR.\n\n"+
            "## Source description\n\n%s\n\n"+
            "## Submission contract\n"+
            "Attach a unified diff named exactly `solution.patch` to a comment on this issue. "+
            "The diff should be applicable from the repository root with `git apply`.\n",
        in.Run.DisplayName, in.Task.InstanceID,
        meta.SourceIssueNumber, meta.SourceIssueTitle, meta.SourceIssueDescription,
    )

    return ComposeOutput{
        Title:              fmt.Sprintf("[Replay] %s · %s", in.Run.DisplayName, meta.SourceIssueTitle),
        Description:        desc,
        AssigneeAgentName:  "", // run dispatcher uses profile.AgentID directly
        SubmissionFilename: "solution.patch",
    }, nil
}

// ReplayParser accepts solution.patch (or .diff) up to 10 MiB.
type ReplayParser struct{}

func NewReplayParser() *ReplayParser { return &ReplayParser{} }

func (p *ReplayParser) Kind() string { return replayKind }

func (p *ReplayParser) Validate(ctx context.Context, att Attachment) error {
    if att.Filename != "solution.patch" && att.Filename != "solution.diff" {
        return errors.New("submission filename must be solution.patch or solution.diff")
    }
    if att.SizeBytes > 10*1024*1024 {
        return fmt.Errorf("submission too large: %d bytes (max 10 MiB)", att.SizeBytes)
    }
    return nil
}

// ReplayEvaluator scores a submitted patch against the reference patch by
// line-level Jaccard similarity. Resolved if similarity >= 0.85.
//
// This evaluator is pure Go and runs in-process — no Docker, no external
// tooling. Suitable for both the server and the evaluator binary.
type ReplayEvaluator struct{}

func NewReplayEvaluator() *ReplayEvaluator { return &ReplayEvaluator{} }

func (e *ReplayEvaluator) Kind() string { return replayKind }

const replayResolvedThreshold = 0.85

func (e *ReplayEvaluator) Evaluate(ctx context.Context, in EvaluateInput) (EvaluateOutput, error) {
    // The evaluator binary is given a path to the submission file, not the
    // raw bytes. Read it here.
    submitted, err := readPatchFile(in.SubmissionPath)
    if err != nil {
        return EvaluateOutput{}, fmt.Errorf("read submission: %w", err)
    }

    var meta ReplayInstanceMeta
    if len(in.Instance.Meta) > 0 {
        _ = json.Unmarshal(in.Instance.Meta, &meta)
    }
    if meta.ReferenceSolution == "" {
        return EvaluateOutput{}, errors.New("reference_solution missing from instance_meta")
    }

    sim := jaccardLines(submitted, meta.ReferenceSolution)
    out := EvaluateOutput{
        Resolved:    sim >= replayResolvedThreshold,
        PassedTests: int(sim * 1000),  // approximate "score" projected to integer
        TotalTests:  1000,
    }
    raw, _ := json.Marshal(map[string]any{
        "similarity":      sim,
        "threshold":       replayResolvedThreshold,
        "reference_lines": len(strings.Split(meta.ReferenceSolution, "\n")),
        "submitted_lines": len(strings.Split(submitted, "\n")),
    })
    out.RawEvalJSON = raw

    if !out.Resolved {
        out.FailedCategories = []string{categorize(sim)}
    }
    return out, nil
}

// jaccardLines: line-level Jaccard similarity, ignoring whitespace-only lines
// and unified-diff metadata lines (lines starting with "+++" or "---" or
// "@@" or "diff --git"). Cheap, robust to context-line drift.
func jaccardLines(a, b string) float64 {
    setA := lineSet(a)
    setB := lineSet(b)
    if len(setA) == 0 && len(setB) == 0 {
        return 1.0
    }
    inter := 0
    for line := range setA {
        if _, ok := setB[line]; ok { inter++ }
    }
    union := len(setA) + len(setB) - inter
    if union == 0 { return 0 }
    return float64(inter) / float64(union)
}

func lineSet(s string) map[string]struct{} {
    out := map[string]struct{}{}
    for _, line := range strings.Split(s, "\n") {
        line = strings.TrimRight(line, " \t\r")
        if line == "" { continue }
        if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") ||
            strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "diff --git") ||
            strings.HasPrefix(line, "index ") {
            continue
        }
        out[line] = struct{}{}
    }
    return out
}

func categorize(sim float64) string {
    switch {
    case sim < 0.10:
        return "diff_unrelated"
    case sim < 0.40:
        return "diff_partial"
    case sim < replayResolvedThreshold:
        return "diff_close_but_below_threshold"
    default:
        return "diff_match"
    }
}

func parseReplayInstanceID(s string) (uuid.UUID, error) {
    const prefix = "multica-issue:"
    if !strings.HasPrefix(s, prefix) {
        return uuid.Nil, fmt.Errorf("replay instance id missing %q prefix", prefix)
    }
    id, err := uuid.Parse(strings.TrimPrefix(s, prefix))
    if err != nil {
        return uuid.Nil, fmt.Errorf("invalid uuid in instance id: %w", err)
    }
    return id, nil
}

func readPatchFile(path string) (string, error) {
    raw, err := os.ReadFile(path)
    if err != nil { return "", err }
    return string(raw), nil
}

// pgtypeUUIDFromUUID is a convenience helper if needed by callers wiring
// instance_meta back into pgtype.UUID — not used by the adapter itself.
func pgtypeUUIDFromUUID(id uuid.UUID) pgtype.UUID {
    var p pgtype.UUID
    copy(p.Bytes[:], id[:])
    p.Valid = true
    return p
}
```

(Imports `os`. Verify the actual path of `db.Queries` import. Also: `_ = issueID` is a placeholder — kept to silence the unused warning since Resolve doesn't need it; consider just dropping the variable assignment.)

- [ ] **Step 2: Tests:**
  - `TestReplayParser_AcceptsSolutionPatch` / rejects wrong filename / rejects oversize
  - `TestJaccardLines_IdenticalIs1`
  - `TestJaccardLines_DisjointIs0`
  - `TestJaccardLines_IgnoresDiffMetadata` — two patches that differ only in @@ headers should score 1.0
  - `TestReplayEvaluator_ResolvedAtThreshold`
  - `TestReplayComposer_BuildsTaskDescription`
- [ ] **Step 3:** Commit `feat(benchmark): multica_replay adapter (catalog+composer+parser+evaluator)`.

---

## Task 2: Suite-creation HTTP endpoint for replay imports

**Files:**
- Modify `server/internal/service/benchmark/suite_service.go` — add `CreateReplaySuite`
- Modify `server/internal/handler/benchmark.go` — add handler + `GET /api/benchmarks/replay/eligible-issues` lister
- Wire route

- [ ] **Step 1: Service layer**

```go
type ReplayInstance struct {
    SourceIssueID     pgtype.UUID
    ReferenceSolution string
    ReferencePRURL    string // optional
}

type CreateReplaySuiteInput struct {
    WorkspaceID  pgtype.UUID
    Slug         string
    DisplayName  string
    Description  string
    Instances    []ReplayInstance
    CreatedBy    pgtype.UUID
}

// CreateReplaySuite materializes a suite from completed Multica issues.
// For each ReplayInstance, reads issue title/description from DB and packs
// the full ReplayInstanceMeta blob into the future task's instance_meta.
//
// The suite stores instance_ids = ["multica-issue:<uuid>"] and the meta
// is persisted to a per-suite map (jsonb column or sibling table).
//
// SIMPLIFICATION FOR MVP: store the instance_meta blob directly inside the
// suite as a JSONB sidecar. Add column benchmark_suite.instance_meta_overrides JSONB
// keyed by instance_id. RunDispatcher will check this map first and use it
// instead of calling Catalog.Resolve.
func (s *SuiteService) CreateReplaySuite(ctx context.Context, in CreateReplaySuiteInput) (Suite, error)
```

Schema change required: add a JSONB column to `benchmark_suite`:
```sql
ALTER TABLE benchmark_suite ADD COLUMN instance_meta_overrides JSONB NOT NULL DEFAULT '{}'::jsonb;
```

Migration: `0NN_benchmark_suite_meta_overrides.up.sql`.

The RunDispatcher (Phase 1a T09) needs a tiny extension: before calling `cat.Resolve(instance_id)`, check if `suite.InstanceMetaOverrides[instance_id]` exists; if so, build the Instance directly from that override.

- [ ] **Step 2: Eligible-issues lister**

```go
// GET /api/benchmarks/replay/eligible-issues?limit=50
// Returns: {items: [{id, number, title, status, completed_at}]}
// Filter: workspace's issues with status='completed' or 'closed' (workspace-scoped).
func (h *BenchmarkHandler) ListReplayEligibleIssues(w http.ResponseWriter, r *http.Request)
```

Uses existing issue queries (might need a new sqlc query `ListCompletedIssuesByWorkspace` if no convenient existing one fits).

- [ ] **Step 3: Create-replay-suite endpoint**

```go
// POST /api/benchmarks/replay/suites
// body: {slug, display_name, description, instances: [{source_issue_id, reference_solution, reference_pr_url?}]}
// 201 with the created suite
func (h *BenchmarkHandler) CreateReplaySuite(w http.ResponseWriter, r *http.Request)
```

- [ ] **Step 4:** Wire routes inside `/api/benchmarks/replay` sub-block.

- [ ] **Step 5:** Tests + commit `feat(benchmark): replay suite creation API`.

---

## Task 3: Hook RunDispatcher up to instance_meta_overrides

**Files:**
- Modify `server/internal/service/benchmark/run_dispatcher.go`
- Modify `server/pkg/db/queries/benchmark_suite.sql` (add a query that returns InstanceMetaOverrides too — or use the existing GetBenchmarkSuite which now exposes the column post-migration)

- [ ] **Step 1: Update dispatchOne**

In the per-instance loop:
```go
for _, instanceID := range run.SuiteInstanceIds {
    var meta json.RawMessage
    var status, reason = "queued", ""

    // Replay path: if suite.InstanceMetaOverrides has an entry, use that.
    if override, ok := overridesByID[instanceID]; ok {
        meta = override
    } else {
        inst, rerr := cat.Resolve(ctx, instanceID)
        if rerr != nil {
            status, reason = "skipped", "unknown_instance"
            meta = json.RawMessage("{}")
        } else if len(inst.Meta) > 0 {
            meta = inst.Meta
        } else {
            meta = json.RawMessage("{}")
        }
    }

    qtx.CreateBenchmarkTask(ctx, ...with meta...)
}
```

Where `overridesByID` is `map[string]json.RawMessage` decoded from `suite.InstanceMetaOverrides`.

- [ ] **Step 2:** Update + add test asserting that a replay suite produces tasks with the override meta intact.
- [ ] **Step 3:** Commit `feat(benchmark): RunDispatcher honors suite instance_meta_overrides`.

---

## Task 4: Frontend — Replay-mode in suite create

**Files:**
- Modify `packages/views/benchmarks/SuiteCreate.tsx` — add a tab/toggle for "Replay" mode
- Modify `packages/core/types/benchmark.ts` — `EligibleIssue`, `CreateReplaySuiteRequest` types
- Modify `packages/core/api/client.ts` — methods
- Modify `packages/core/benchmarks/queries.ts` + `mutations.ts` — hooks

- [ ] **Step 1: Types + ApiClient**

```ts
export interface EligibleIssue {
  id: string;
  number: number;
  title: string;
  status: string;
  completed_at?: string;
}

export interface CreateReplaySuiteRequest {
  slug: string;
  display_name: string;
  description?: string;
  instances: Array<{
    source_issue_id: string;
    reference_solution: string;
    reference_pr_url?: string;
  }>;
}
```

ApiClient:
```ts
async listReplayEligibleIssues(): Promise<{items: EligibleIssue[]}> {...}
async createReplaySuite(input: CreateReplaySuiteRequest): Promise<BenchmarkSuite> {...}
```

- [ ] **Step 2: Suite-create UI**

Add a tab toggle at the top of SuiteCreate:
- **ProgramBench** (existing form, default)
- **Replay** (new flow)

For replay flow:
1. Slug + display name + description (same as existing).
2. Issue picker: a multi-select list rendering `useQuery(benchmarkReplayEligibleIssuesOptions(wsId))`.
3. For each selected issue, render a textarea where the operator pastes the reference solution patch.
4. Submit calls `useCreateReplaySuite()`.

Make the picker simple — checkbox list with title + issue number. After picking, render an editable section per issue with a textarea labeled "Reference patch" + optional "Reference PR URL" input.

- [ ] **Step 3:** i18n keys.
- [ ] **Step 4:** Commit `feat(benchmark): suite-create replay mode (multica issues + reference patches)`.

---

## Task 5: Wire adapter in router + final check + push

- [ ] **Step 1:** In router.go, register the replay adapter:
  ```go
  benchmarkRegistry.RegisterCatalog(adapter.NewReplayCatalog(queries))
  benchmarkRegistry.RegisterComposer(adapter.NewReplayComposer())
  benchmarkRegistry.RegisterParser(adapter.NewReplayParser())
  benchmarkRegistry.RegisterEvaluator(adapter.NewReplayEvaluator())
  ```

- [ ] **Step 2:** Final check: `go test ./server/...`, `pnpm typecheck`, `pnpm -F @multica/views test parity`.
- [ ] **Step 3:** Push to fork.
- [ ] **Step 4:** Commit final wiring.

---

## Self-Review

**MVP scope (intentional):**
- ✅ Reuses ALL Phase 0–3 infra; one new adapter, no new run-engine work.
- ✅ Reference solutions captured at suite creation time — no live ADO/Jira/Git fetching at run time.
- ✅ Pure-Go evaluator (Jaccard line similarity); resolves locally without Docker.
- ⏭ ADO/Jira/Git integration to auto-pull reference patches — explicit follow-up.
- ⏭ Test-execution-based evaluation (run the project's tests on the agent's branch) — much heavier; defer.
- ⏭ Score thresholds tunable per-suite — defer.

**Why Jaccard:** dumb but works. Two patches that fix the same bug usually share most of their `+ added lines` and `- removed lines`. Drift in `@@` headers and surrounding context is filtered out. Won't catch semantically-equivalent rewrites — fine for v1.
