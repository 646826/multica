# Multica × ProgramBench: First-Class Benchmarking Integration

**Status:** Design draft, awaiting review.
**Date:** 2026-05-07.
**Target repos:** `multica/`, `multica-helm-chart/`, new evaluator binary inside `multica/`.
**Out of scope:** changes to `multica-agentbox/` and the daemon protocol.

## 1. Overview

Multica today can run ProgramBench tasks only via the out-of-band orchestration in `multica-ark-ops`: shell scripts create issues, agents return `submission.tar.gz` attachments, Python scripts compute summaries and leaderboards, and an operator-only dashboard renders the loop. The integration is real but lives outside Multica.

This design moves the benchmark loop inside Multica as a first-class feature available to every self-host user. The product promise is the iterate cycle:

> Run a benchmark → tweak an agent → run again → see which version was better, in one product, without leaving the browser.

The implementation is grounded in three architectural commitments:

- **Hybrid data model.** A new top-level `BenchmarkRun` primitive owns suites, profiles, summaries, and leaderboards. Each task inside a run is delivered through an existing Multica `Issue` so agent execution, comments, attachments, and WebSocket events are reused unchanged.
- **Adapter pattern.** A `BenchmarkAdapter` Go interface separates benchmark-agnostic orchestration from benchmark-specific knowledge. The single v1 implementation is `programbench`. Future adapters (SWE-bench, custom internal evals) plug in without schema changes.
- **Optional evaluator pool.** Eval execution lives in a new `multica-evaluator` binary deployed as a separate Helm-chart Deployment. The base Multica install runs without it (in `imported` mode); turning the chart flag on enables fully managed end-to-end runs.

The design is intended to be re-implemented from scratch against this spec rather than ported from the existing Python in `multica-ark-ops`. The conceptual contract carries over; the code does not.

## 2. Goals and non-goals

### Goals

- A Multica self-host user can create a benchmark suite, capture an agent profile, run the suite, and see a comparison against a previous run, all in the web UI.
- Benchmark runs reuse the existing daemon, agent, attachment, and WebSocket machinery. No new daemon protocol, no changes to `multica-agentbox`.
- A benchmark suite can be re-run repeatedly against different agent profiles to support an iterate-and-compare loop.
- ProgramBench task metadata stays sourced from the official `programbench` Python package; Multica caches per-task metadata only for run reproducibility.
- Run summaries, comparisons, and leaderboards are computed deterministically from stored eval results — recompute is idempotent and survives schema-compatible migrations.
- The evaluator deployment is opt-in and isolated from the base server; absence of an evaluator does not break suites, profiles, or `imported`-mode runs.

### Non-goals (v1.0)

| Area | Excluded |
|---|---|
| Adapters | Anything other than ProgramBench. SWE-bench / custom evals are explicit follow-ups. |
| Daemon | No changes to daemon protocol or to `multica-agentbox` image. |
| Issues | No new column on `issue`; benchmark-source detection extends the existing `issue.origin_type` enum (added in migrations 042/060) with a new value `benchmark_run`. `issue.origin_id` references `benchmark_runs.id`. |
| Retries | No per-task submission retry. No run-level retry. Evaluator-side retries only (infra failure recovery). |
| Edits | Profiles immutable. Suites immutable after first use; runs snapshot the instance list at start. |
| Cross-workspace | No cross-workspace leaderboard or profile sharing. |
| Multi-tenant evaluator | One evaluator deployment serves one workspace token. |
| Hardware | No GPU-aware scheduling. |
| Mobile | Compare and run-detail views are desktop-first. |
| Export | No CSV / Markdown export from UI in v1. |
| Logs | No live-tail of evaluator logs in UI. |

## 3. Architecture

```
apps/web (Next.js)
  /[workspaceSlug]/benchmarks/{runs,suites,profiles,leaderboard}
  packages/views/src/benchmarks/   reusable views (zero next/* imports)
  packages/core/src/benchmarks/    TanStack Query hooks + Zustand stores

server (Go)
  internal/service/benchmark/
    run_lifecycle.go     start/cancel/finalize
    task_dispatcher.go   creates kind=benchmark-source issues, watches submissions
    profile_snapshot.go  immutable agent capture
    summary.go           resolved/pass-rate aggregation
    comparison.go        run-vs-run delta function
    leaderboard.go       best-run-per-profile aggregation
    adapter/
      adapter.go         BenchmarkAdapter interfaces
      programbench.go    first implementation
  internal/handler/benchmark.go    HTTP routes
  pkg/db/queries/benchmark.sql     sqlc
  migrations/0NN_benchmark.up.sql
  cmd/evaluator/                   NEW BINARY
    main.go              adapter wiring, claim loop
    runner.go            workspace mgmt, submission download, eval, result POST

multica-evaluator pod (linux/amd64, optional)
  - polls server eval queue via authenticated HTTPS
  - downloads submission.tar.gz from server attachment storage
  - runs adapter-specific eval (uvx programbench eval ...)
  - posts eval JSON back to server
```

The daemon and `multica-agentbox` are not part of the diagram intentionally: a benchmark task is delivered as an ordinary Multica issue, so daemon-side code is unchanged.

## 4. Data model

All tables are workspace-scoped. UUID primary keys. Hard-delete with cascade, matching existing Multica conventions. The migration `0NN_benchmark.up.sql` introduces every table below in a single block.

```sql
CREATE TABLE benchmark_suites (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  slug            text NOT NULL,
  display_name    text NOT NULL,
  adapter_kind    text NOT NULL,
  instance_ids    text[] NOT NULL,
  description     text NOT NULL DEFAULT '',
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid NOT NULL REFERENCES users(id),
  UNIQUE (workspace_id, slug)
);

CREATE TABLE benchmark_agent_profiles (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  slug            text NOT NULL,
  display_name    text NOT NULL,
  agent_id        uuid NOT NULL REFERENCES agents(id),
  agent_name      text NOT NULL,
  model           text NOT NULL,
  prompt_source   text NOT NULL,
  prompt_hash     text NOT NULL,
  attached_skills jsonb NOT NULL,
  captured_at     timestamptz NOT NULL DEFAULT now(),
  captured_by     uuid NOT NULL REFERENCES users(id),
  UNIQUE (workspace_id, slug)
);

CREATE TABLE benchmark_runs (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id       uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  suite_id           uuid NOT NULL REFERENCES benchmark_suites(id) ON DELETE RESTRICT,
  suite_instance_ids text[] NOT NULL,                 -- snapshot at start
  profile_id         uuid NOT NULL REFERENCES benchmark_agent_profiles(id) ON DELETE RESTRICT,
  base_run_id        uuid REFERENCES benchmark_runs(id) ON DELETE SET NULL,
  display_name       text NOT NULL,
  status             text NOT NULL,
  status_reason      text NOT NULL DEFAULT '',
  notes              text NOT NULL DEFAULT '',
  evaluator_mode     text NOT NULL,                   -- 'managed' | 'imported'
  adapter_version    text NOT NULL DEFAULT '',        -- e.g. ProgramBench package version
  submission_timeout_seconds int NOT NULL DEFAULT 7200, -- per-task agent timeout, default 2h
  created_at         timestamptz NOT NULL DEFAULT now(),
  created_by         uuid NOT NULL REFERENCES users(id),
  started_at         timestamptz,
  completed_at       timestamptz
);
CREATE INDEX ON benchmark_runs (workspace_id, suite_id, status);

CREATE TABLE benchmark_tasks (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id          uuid NOT NULL REFERENCES benchmark_runs(id) ON DELETE CASCADE,
  workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  instance_id     text NOT NULL,
  instance_meta   jsonb NOT NULL,
  issue_id        uuid REFERENCES issues(id) ON DELETE SET NULL,
  attachment_id   uuid REFERENCES attachments(id) ON DELETE SET NULL,
  status          text NOT NULL,
  status_reason   text NOT NULL DEFAULT '',
  created_at      timestamptz NOT NULL DEFAULT now(),
  submitted_at    timestamptz,
  scored_at       timestamptz,
  UNIQUE (run_id, instance_id)
);
CREATE INDEX ON benchmark_tasks (run_id, status);

CREATE TABLE benchmark_eval_jobs (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  task_id         uuid NOT NULL UNIQUE REFERENCES benchmark_tasks(id) ON DELETE CASCADE,
  workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  adapter_kind    text NOT NULL,
  state           text NOT NULL,                     -- pending|claimed|done|failed
  attempt         int  NOT NULL DEFAULT 0,
  claimed_by      text,
  claimed_at      timestamptz,
  enqueued_at     timestamptz NOT NULL DEFAULT now(),
  finished_at     timestamptz,
  last_error      text NOT NULL DEFAULT ''
);
CREATE INDEX ON benchmark_eval_jobs (state, enqueued_at)
  WHERE state IN ('pending', 'claimed');

CREATE TABLE benchmark_eval_results (
  task_id            uuid PRIMARY KEY REFERENCES benchmark_tasks(id) ON DELETE CASCADE,
  workspace_id       uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  resolved           boolean NOT NULL,
  passed_tests       int NOT NULL,
  total_tests        int NOT NULL,
  pass_rate          numeric(6,5) NOT NULL,
  raw_eval_json      jsonb NOT NULL,
  failed_categories  jsonb NOT NULL DEFAULT '[]',
  evaluated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE benchmark_run_summaries (
  run_id              uuid PRIMARY KEY REFERENCES benchmark_runs(id) ON DELETE CASCADE,
  workspace_id        uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  resolved_count      int NOT NULL,
  total_count         int NOT NULL,
  aggregate_pass_rate numeric(6,5) NOT NULL,
  average_pass_rate   numeric(6,5) NOT NULL,
  errored_count       int NOT NULL,
  failure_categories  jsonb NOT NULL DEFAULT '[]',
  computed_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE evaluator_pool_tokens (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id    uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  token_prefix    text NOT NULL,                     -- 'evp_xxxxxxxx', for UI display
  token_hash      text NOT NULL UNIQUE,              -- sha256(full token)
  display_name    text NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  created_by      uuid NOT NULL REFERENCES users(id),
  last_used_at    timestamptz,
  revoked_at      timestamptz
);
```

Comparison and leaderboard are not materialized tables. Comparison is computed per request from two run summaries plus a JOIN on `benchmark_eval_results` by `instance_id`. Leaderboard is computed by aggregating `benchmark_run_summaries` for a suite and selecting the best run per profile.

`adapter_kind` is denormalized onto suite, run, and eval-job to route work without joins. `instance_meta` is opaque JSON owned by the adapter; the server never interprets cleanroom image names or eval image tags.

## 5. Adapter interface

The adapter package lives in `server/internal/service/benchmark/adapter/`. Catalog, IssueComposer, and SubmissionParser execute in the main `multica-server` process. Evaluator executes in `multica-evaluator`. Both binaries import the same package; build tags or registration order pick which interfaces are active.

```go
type Catalog interface {
    Kind() string
    Resolve(ctx context.Context, instanceID string) (Instance, error)
    List(ctx context.Context, filter ListFilter) ([]Instance, error)
}

type Instance struct {
    ID         string
    Language   string
    Difficulty string
    Meta       json.RawMessage   // adapter-opaque, persisted into benchmark_tasks.instance_meta
}

type ListFilter struct {
    Language, Difficulty string
    Limit, Offset        int
}

type IssueComposer interface {
    Compose(ctx context.Context, in ComposeInput) (ComposeOutput, error)
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
    SubmissionFilename string   // dispatcher watch-target
}

type SubmissionParser interface {
    Validate(ctx context.Context, att Attachment) error
}

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
    RawEvalJSON       json.RawMessage
    Resolved          bool
    PassedTests       int
    TotalTests        int
    FailedCategories  []string
}
```

There is no RPC contract between server and evaluator. They communicate exclusively through the database queue (`benchmark_eval_jobs`) and the existing Multica REST API for attachments. This keeps the two binaries decoupled at the wire level.

The v1 ProgramBench adapter:

| Method | Implementation |
|---|---|
| `Catalog.Resolve` / `List` | Shells out to `uvx --from programbench python -c "..."` to read packaged `task.yaml`. Results cached in-process per server lifetime. |
| `IssueComposer.Compose` | Renders a Go-template Markdown (compiled into the binary) embedding instance id, cleanroom image (`__` → `_1776_` rule), source repo, language, difficulty, active test count, ban list (no internet, no decompilation, no strace), and required output (`compile.sh` + `./executable`). |
| `SubmissionParser.Validate` | Filename equals `submission.tar.gz`, MIME and size guards. |
| `Evaluator.Evaluate` | Writes `submission.tar.gz` into ProgramBench's expected layout under `WorkDir`, runs `uvx programbench eval`, parses `<instance-id>.eval.json`, applies adapter-side filtering of ignored branches and ignored tests, returns normalized output. |

The Compose template is compiled into the adapter binary in v1. Per-workspace template editing is a follow-up; in v1 the prompt contract is part of the adapter and is unit-tested.

Adapter versioning: `Kind()` returns `"programbench"`. A future breaking change to ProgramBench publishes a second adapter with `Kind() == "programbench-v2"`. Existing runs keep working because `benchmark_runs.adapter_kind` and `adapter_version` are stored.

## 6. Lifecycle and orchestration

### Run state machine

```
benchmark_runs.status:
  queued → submitting → evaluating → complete
                │             │             ▲
                ▼             ▼             │
              failed ← canceled ← (terminal except complete)

benchmark_tasks.status:
  queued → issued → submitted → evaluating → scored
              errored ← (any phase, retry-bounded) ←─┘
              skipped ← (suite filter or unsupported instance)

benchmark_eval_jobs.state:
  pending → claimed → done
                │
                └→ failed (after N attempts)
```

### Loops

`RunDispatcher` (one server-wide goroutine, leader-elected via `pg_advisory_xact_lock`) picks up `queued` runs. For each run it resolves `Catalog` for every `instance_id` from the suite snapshot, inserts `benchmark_tasks` rows (or `skipped` if unresolved), advances run to `submitting`, and hands off to `TaskDispatcher`.

`TaskDispatcher` (per active run) creates issues for `queued` tasks under a concurrency cap. The cap is `min(agent.max_concurrent_tasks at run start, workspace.max_parallel_benchmark_issues)`. The dispatcher subscribes to the existing realtime hub. When a comment arrives on a benchmark-bound issue, the dispatcher runs `SubmissionParser.Validate` on the attachment. On accept, the dispatcher advances task to `submitted`, closes the issue with `reason='benchmark_submitted'`, and inserts a `benchmark_eval_jobs` row when `evaluator_mode='managed'`. If `evaluator_mode='imported'`, the task remains `submitted` until eval results are POSTed by an external system.

Submission timeout is the per-run `submission_timeout_seconds` column (default 7200, matching `MULTICA_AGENT_TIMEOUT`). Tasks past timeout move to `errored` with `status_reason='submission_timeout'` and the issue is closed with the same reason. The dispatcher checks every 30 seconds.

`Evaluator` workers use a pull model. They authenticate to `POST /api/internal/eval-jobs/claim` with the workspace evaluator token and receive batches of pending jobs:

```
{ evaluator_id, adapter_kinds: ["programbench"], max_concurrent }
```

The server claims atomically with `SELECT ... FOR UPDATE SKIP LOCKED`, sets `state='claimed'`, `claimed_by`, `claimed_at`. There are no heartbeats in v1; instead a watchdog returns `claimed` jobs older than `claim_ttl` (default 1h) to `pending` every 5 minutes.

An evaluator finishes a job with `POST /api/internal/eval-jobs/<id>/complete` containing parsed result fields plus the raw eval JSON. The server transactionally inserts `benchmark_eval_results`, advances the task to `scored`, marks the job `done`, and emits `benchmark.task.scored`. Failures call `/fail` with `last_error`; the job returns to `pending` if `attempt < max_attempts (3)`, otherwise marks `failed` and the task moves to `errored`.

`RunFinalizer` listens on `benchmark.task.scored` and `benchmark.task.errored`. When all tasks in a run are terminal (`scored`, `errored`, `skipped`), it computes and writes `benchmark_run_summaries`, advances the run to `complete` (or `failed` if every task errored), and emits `benchmark.run.completed`.

Cancel: `POST /api/benchmarks/runs/<id>/cancel` flips the run to `canceled`, closes any `issued` task issues with `reason='benchmark_canceled'` (existing daemon cancel path interrupts the agent), and deletes `pending` and `claimed` eval jobs. An evaluator that calls `/complete` for a deleted job receives `410 Gone` and discards the work.

### WebSocket events

The integration emits new event types on the existing hub. The daemon WebSocket protocol is unchanged.

| Event | Payload |
|---|---|
| `benchmark.run.created` | run id, suite, profile, status |
| `benchmark.run.status` | run id, status, started_at, completed_at |
| `benchmark.task.status` | task id, run id, status, status_reason |
| `benchmark.task.scored` | task id, run id, resolved, pass_rate |
| `benchmark.run.completed` | run id, summary |

UI clients subscribe to `workspace:<id>:benchmarks`. Events flow through the existing Redis relay so multi-pod servers work without changes.

### Explicitly excluded behavior

- No re-running individual tasks. To repeat one instance, create a new run with a one-instance suite.
- No run prioritization beyond FIFO with concurrency caps.
- No submission-side retries. Retries exist only on the evaluator side, where failures are typically infrastructural (Docker pull, timeout) rather than semantic.

## 7. Evaluator pod

The evaluator is a separate Go binary at `server/cmd/evaluator/` and a separate Helm-chart Deployment, off by default.

### Authentication

Evaluators authenticate with workspace-scoped tokens stored in `evaluator_pool_tokens`. Tokens are minted in the workspace settings UI, displayed once at creation, and stored as `sha256` digests. The token prefix (`evp_`) and a short visible suffix are kept for UI listing. Endpoints `/api/internal/eval-jobs/*` require this scope only — personal access tokens and daemon tokens are rejected.

### Docker access

ProgramBench runs cleanroom Docker images during evaluation. Three options exist:

1. **Host docker socket mount** — simplest, but root-equivalent on the node. Not supported in v1 chart.
2. **Docker-in-Docker sidecar** — `docker:dind` container with `privileged: true`. Default in v1 chart.
3. **Sysbox / Kata** runtime classes — unprivileged DinD. Documented as preferred for production with operator-side runtime-class configuration.

Option 1 is rejected as a default for an open-source chart. The `docker-compose.selfhost.yml` variant gets an `evaluator` profile (off by default) using socket mount, since single-node compose is operator-root by definition.

### Cleanroom network policy

ProgramBench rules forbid the agent from reading the original source online. The adapter passes `--network=none` when running cleanroom containers. The evaluator pod itself has internet access (`uvx programbench eval` may sync blobs). NetworkPolicy in the chart denies egress except to `multica-server` and configurable allowlist hosts.

### Configuration

```yaml
evaluator:
  enabled: true
  replicaCount: 1
  image:
    repository: ghcr.io/multica-ai/multica-evaluator
    tag: v0.x.x
  serverUrl: http://multica-backend:8080
  tokenSecret:
    name: multica-evaluator-token
    key: token
  adapters:
    - kind: programbench
      # Informational: the actual binding is the version baked into
      # the evaluator image at build time. The chart records it here
      # so operators can see what their pod is running. The evaluator
      # binary reads it back and reports it on /api/internal/eval-jobs/claim
      # so the server can stamp benchmark_runs.adapter_version.
      programbenchVersion: "X.Y.Z"
  maxConcurrent: 2
  jobClaimTtlSeconds: 3600
  workspaceDir: /var/lib/multica-evaluator/workspaces
  docker:
    mode: dind                          # dind | runtime-class
    runtimeClassName: ""
  resources:
    requests: { cpu: 1, memory: 2Gi }
    limits:   { cpu: 4, memory: 8Gi }
  storage:
    ephemeral: 20Gi
```

`replicaCount: 0` is the chart default. Operators must opt in by providing both the token Secret and a positive replica count.

### Image

Multi-stage build: `debian:bookworm-slim` base (matching agentbox motivation: avoid Alpine glibc issues with packaged Python wheels), with `uv`, Docker CLI, `tini`, and a baked `uv tool install programbench==<pinned>`. Final image target is around 250 MB. The pinned ProgramBench version is recorded into `benchmark_runs.adapter_version` at run start so old runs remain reproducible across image upgrades.

### Lifecycle

On `SIGTERM` the evaluator stops the claim ticker, drains in-flight jobs up to the Kubernetes `terminationGracePeriodSeconds` (set to 30 minutes for long evals), and posts `/fail` with `last_error="evaluator_shutdown"` for anything still running. The watchdog returns those jobs to `pending` for the next pod to pick up.

### Observability

- `/healthz` returns 200 if the server's internal-health endpoint is reachable.
- `/metrics` exposes `evaluator_jobs_claimed_total`, `_completed_total`, `_failed_total`, `evaluator_eval_duration_seconds{adapter,outcome}`, `evaluator_active_jobs`.
- Logs are JSON-structured to match the rest of Multica.

### Excluded from v1

- Multi-tenant pools (one deployment per workspace token).
- GPU scheduling.
- Autoscaling (HPA can be added by operator without core changes).
- Per-adapter image variants (single image with config-selected adapters).

## 8. Web UI

Routes follow the Multica workspace-scoped convention.

```
apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/
  layout.tsx                       sub-nav: Runs · Suites · Profiles · Leaderboard
  runs/page.tsx                    list + Start-run CTA
  runs/[runId]/page.tsx            run detail
  runs/[runId]/compare/[baseId]/page.tsx
  suites/page.tsx
  suites/[suiteId]/page.tsx
  suites/new/page.tsx
  profiles/page.tsx
  profiles/new/page.tsx
  leaderboard/page.tsx

packages/views/src/benchmarks/     reusable views (zero next/* imports)
packages/core/src/benchmarks/      TanStack Query hooks + Zustand stores
```

### Key views

**Runs list.** Table of display name, suite, profile, status with progress bar (`scored/total`), resolved count, created at, base run. Filters by suite, profile, status. The "Start run" CTA opens the wizard. The list updates live via WS subscription on `workspace:<id>:benchmarks`.

**Run detail.** Three sections. Header has the display name, status pill, timestamps, suite and profile badges, and a "Compare with…" dropdown that routes to `/compare/<baseId>`. Summary card (when `complete`) shows resolved, aggregate and average pass-rates, and top failure categories pulled from `benchmark_run_summaries`. Tasks table is virtualized: instance id, status icon, pass-rate bar, "Open issue" link, "View raw eval" JSON modal. Clicking a row opens a side-panel with the existing IssueDrawer so operators can inspect the agent's conversation without leaving the benchmark context.

**Comparison.** Client-side diff of two run summaries plus eval-result JOIN by instance:

```
                  base      candidate    Δ
resolved          12/30     14/30        +2
average pass-rate 0.612     0.683        +0.071
errored           1         0            -1
─────────────────────────────────────────
improved (8)      <instance> ▲ 0.42 → 0.71
regressed (2)     <instance> ▼ 0.88 → 0.61
newly resolved    <instance> · 0.71 → 1.00
lost resolved     —
new failure cats  —
cleared cats      "compile_error"
```

If suite snapshots differ between the two runs, the response is flagged `partial: true` with `missing_in_base[]` / `missing_in_cand[]` and the UI shows a warning banner.

**Leaderboard.** Per-suite ranking. Columns: rank, profile display name, best run (link), resolved, average pass-rate, aggregate pass-rate, last completed. A profile only appears when at least one of its runs for the suite has reached `complete`.

**Suite detail.** Instance list with adapter-resolved metadata. "Sync from catalog" re-runs adapter resolve and shows a diff with explicit add/remove counts. Edit-mode allows manual instance selection plus a filter-from-catalog mini-form; once a suite has been used in any run, it becomes immutable and editing forces a "Duplicate" action.

**Profile detail.** Read-only: agent name, model, prompt source (collapsible Markdown), prompt-hash badge, attached skills, captured-at. "Re-capture from current agent" creates a new profile with auto-suffixed slug and highlights the prompt diff.

### Start-run wizard

A drawer with three steps:

1. **Suite** — recent suites + searchable list. Inline shortcut to create a new suite from catalog filters.
2. **Profile** — recent profiles + "Capture current ProgramBenchRunner now". Capture-now creates the profile transparently before the run starts.
3. **Compare with** *(optional)* — best recent runs of the same suite. Selecting one sets `base_run_id` so the run-detail "Compare" CTA is immediately available.

Submission posts `POST /api/benchmarks/runs` and redirects to run-detail, which renders live via WS.

### Architectural compliance

- TanStack Query owns server state. WS events invalidate queries; they never write to Zustand. Workspace-scoped queries key on `wsId`.
- Zustand stores client state only — list filters, drawer open state, comparison selection. Stores live in `packages/core/src/benchmarks/stores/`.
- `packages/views/src/benchmarks/` imports nothing from `next/*` or routing libraries. Navigation uses Multica's `NavigationAdapter`. Desktop app gets the views for free.
- All user-facing strings flow through `packages/views/locales/`, with `benchmarks.*` keys added for `en` and `zh-CN`. The Chinese voice guide in `apps/docs/content/docs/developers/conventions.zh.mdx` applies to translations.

## 9. Profile, comparison, and leaderboard semantics

### Profile hashing

`prompt_hash` answers "is this the same profile?". Canonical normalization before hashing:

```go
func normalize(p Profile) []byte {
    return canonicalJSON(struct {
        AgentName      string
        Model          string
        PromptSource   string
        AttachedSkills []SkillRef     // sorted by slug
    }{...})
}
prompt_hash = sha256(normalize(p))[:32 hex]
```

There is no whitespace trim or Markdown normalization: any meaningful diff in the prompt is a different profile. `SkillRef` normalizes to `{slug, version}` because skill ids are not stable across workspaces. `display_name`, `slug`, and `captured_at` are excluded.

When capturing, the server checks for an existing identical hash. The UI shows a warning ("Identical profile already exists as `<slug>`") with options to reuse or to confirm a duplicate (useful for "snapshot before risky edit").

### Per-task pass rate

```
pass_rate = passed_tests / total_tests   (numeric(6,5), 0..1)

edge cases:
  total_tests == 0      → pass_rate = 0; status_reason='no_active_tests'; task counted as 'skipped'
  resolved == true      → pass_rate ≡ 1.0 (assertion; adapter contract violation if differs)
```

`resolved` is adapter-defined. ProgramBench: `passed_tests == total_tests AND total_tests > 0` after filtering ignored branches and tests. The adapter performs this filtering inside `Evaluator.Evaluate`; the server receives already-filtered numbers.

### Run summary

`benchmark_run_summaries` is computed by `RunFinalizer` and on `POST /summary/recompute`:

```
S = { tasks where status='scored' }
total_count        = |all tasks|                        # incl errored/skipped, for visibility
resolved_count     = |s ∈ S : s.resolved|
errored_count      = |tasks where status='errored'|

aggregate_pass_rate = Σ s.passed_tests / Σ s.total_tests   over s ∈ S   (0 if denominator == 0)
average_pass_rate   = mean(s.pass_rate for s ∈ S)                       (0 if S empty)

failure_categories  = top-K (default K=5) buckets across all errored tasks plus
                      scored-but-not-resolved tasks; categories supplied by adapter
                      per task. Sorted by count DESC, name ASC. K read from the
                      workspace-context setting `benchmark_failure_categories_top_k`,
                      default 5.
```

Two pass-rates by design. Aggregate weights by task size; average weights every instance equally. Both are useful for different regression questions.

### Comparison

Comparison is a pure function over two completed runs, computed on demand and not persisted in v1.

```
shared_instances    = base.instances ∩ cand.instances
delta.resolved      = cand.resolved_count − base.resolved_count
delta.avg_pass_rate = cand.average_pass_rate − base.average_pass_rate
delta.agg_pass_rate = cand.aggregate_pass_rate − base.aggregate_pass_rate
delta.errored       = cand.errored_count − base.errored_count

improved        = { i ∈ shared : cand[i].pass_rate − base[i].pass_rate > ε }   ε = 0.001
regressed       = { i ∈ shared : base[i].pass_rate − cand[i].pass_rate > ε }
newly_resolved  = { i ∈ shared : !base[i].resolved &&  cand[i].resolved }
lost_resolved   = { i ∈ shared :  base[i].resolved && !cand[i].resolved }

categories.added   = cand.failure_categories \ base.failure_categories
categories.cleared = base.failure_categories \ cand.failure_categories
```

If `shared_instances ⊊ base.instances ∪ cand.instances` the response is `partial: true` with `missing_in_base[]` and `missing_in_cand[]`.

### Leaderboard

Per-suite, per-workspace.

```
input  = all benchmark_runs where suite_id=S, status='complete'
group by profile_id
  best_run = argmax(resolved_count, then average_pass_rate, then -errored_count, then -completed_at)
output sorted by (best_run.resolved_count DESC,
                  best_run.average_pass_rate DESC,
                  best_run.aggregate_pass_rate DESC,
                  best_run.completed_at ASC)
rank = dense_rank() over the same key
```

A profile appears only if at least one of its runs reached `complete` for the suite. "Best" is at the run level; mixing best task-results across runs of one profile would be a dishonest composite.

### Determinism

Identical `benchmark_eval_results` for identical `instance_id` set under the same `adapter_kind` produce identical `benchmark_run_summaries`. Recompute is idempotent. Leaderboard rankings are reproducible across migrations and across adapter version bumps because runs carry their `adapter_kind` and `adapter_version`.

### Adapter failure category contract

```go
type EvaluateOutput struct {
    ...
    FailedCategories []string  // adapter-defined, deduped, lowercase, snake_case-ish
}
```

ProgramBench v1 categories: `compile_error`, `runtime_crash`, `wrong_output`, `timeout`, `missing_executable`, `submission_unpack_error`, derived from `<instance>.eval.json`. The server does not interpret category strings; it aggregates by count.

## 10. Phasing

Each phase is one PR. Phase boundaries match natural verification points.

**Phase 0 — Foundations.**
Migration, sqlc queries, CRUD service skeleton, handler routes for `/api/benchmarks/{suites,profiles}` (GET/POST/DELETE), UI for Suites and Profiles tabs with capture-from-agent. Demo: an operator can create a suite from instance ids and capture a ProgramBenchRunner profile snapshot; no runs yet.

**Phase 1 — Orchestration end-to-end.**
Adapter package and `ProgramBenchAdapter` (Catalog / Composer / Parser / Evaluator). RunDispatcher and TaskDispatcher loops, WS event emission. Endpoints `/api/benchmarks/runs` and `/api/internal/eval-jobs/*`. The `cmd/evaluator/` binary. Helm-chart `evaluator` Deployment with `replicaCount: 0` default. Summary computation. UI: Run list, Run detail (no comparison yet), Start-run wizard. Demo: start a one-task smoke run end-to-end and watch it complete.

**Phase 2 — Comparison and leaderboard.**
Comparison function and endpoint. Leaderboard endpoint. UI: Compare view, Leaderboard tab, "Compare with" wiring in Run detail and the wizard. Demo: run the same suite with two profiles, see side-by-side improved / regressed / newly_resolved, and watch the leaderboard rank profiles.

**Phase 3 — Imported eval and docs.**
`evaluator_mode='imported'` flow with `POST /api/benchmarks/runs/<id>/eval-results/<instance_id>` for external CI uploads. Suite-detail "Sync from catalog" UI. Failure-category display polish. Documentation in `apps/docs/`: getting-started, evaluator deploy guide, adapter-developer guide. i18n keys for `en` and `zh-CN`. Demo: a Multica install without an evaluator deployment runs the full loop with CI-uploaded eval results.

Each phase is independently demoable. Phase 1 can be piloted internally before Phase 2 lands upstream.

## 11. Resolved decisions

The following questions were raised during design and are now resolved as written here.

| Question | Decision |
|---|---|
| Issue-source signal | Extend the existing `issue.origin_type` CHECK constraint with `benchmark_run`; set `issue.origin_id = benchmark_runs.id` when the dispatcher creates the issue. Inbox/board/activity queries filter `origin_type != 'benchmark_run'` for default views. The benchmark-source extension is part of the Phase 0 migration. |
| `benchmark_runs.display_name` uniqueness | Not unique. UI auto-suffixes `(2)` on duplicates at create time. |
| Suite mutability after first run | Suite mutable; each run snapshots its `suite_instance_ids[]` at start. Once a suite has any run, the UI forces "Duplicate" for edits. |
| Concurrency limit | Live-read from `agents.max_concurrent_tasks` at run start, capped by a workspace setting `max_parallel_benchmark_issues` (default 4). The setting lives in the existing workspace-context store (migration `006_workspace_context`); no new table. Not stored on profile. |
| Issue Compose template location | Hard-coded Go template in `adapter/programbench.go`, unit-tested. Per-workspace editing is a follow-up. |
| Run URL form | Short UUID in URL, `display_name` in title. Matches Multica's existing pattern for issues. |
| ProgramBench package version in evaluator | Pre-baked in evaluator image at a pinned version. Version recorded in `benchmark_runs.adapter_version` so old runs stay reproducible. |

## 12. Glossary

| Term | Definition |
|---|---|
| Suite | A named, stable list of adapter instance ids. Source of truth for "what tasks are in this benchmark." |
| Profile | An immutable snapshot of an agent's name, model, prompt source, and attached skills, identified by `prompt_hash`. |
| Run | One execution of (suite × profile) producing per-task submissions, eval results, and an aggregate summary. |
| Task | A single (run × instance) row, backed by a Multica Issue while in flight. |
| Adapter | A Go package implementing the benchmark-specific catalog, issue composition, submission validation, and eval execution. v1 ships one: `programbench`. |
| Evaluator | Optional `multica-evaluator` pod that pulls eval jobs and runs adapter-specific evaluation in Docker. |
| Managed mode | `evaluator_mode='managed'` — server enqueues eval jobs for the evaluator pool. |
| Imported mode | `evaluator_mode='imported'` — eval results arrive via external HTTP POST instead of an evaluator pod. |
| Comparison | A pure function over two complete runs producing per-instance deltas plus aggregate deltas. Not persisted. |
| Leaderboard | Per-suite ranking selecting the best run per profile by `(resolved, avg_pass_rate, -errored, -completed_at)`. |
