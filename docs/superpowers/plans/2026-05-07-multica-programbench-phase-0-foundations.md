# Multica × ProgramBench Phase 0 — Foundations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the first-class data model and CRUD UI for Multica benchmark suites and agent profiles, so operators can define what gets benchmarked and capture immutable agent snapshots before any run orchestration is introduced.

**Architecture:** New `benchmark_*` tables alongside existing Multica primitives, sqlc-generated queries, a `service/benchmark/` package with pure-function profile hashing and CRUD services, Chi-router endpoints under `/api/benchmarks`, and a Next.js `/benchmarks` workspace tab with TanStack Query hooks and shared views. No orchestration, no evaluator, no runs in this phase — Phase 1 picks up from here.

**Tech Stack:** Go 1.24 (Chi router, sqlc with pgx/v5, testify), PostgreSQL 17 (pgvector image), Next.js 15 App Router, pnpm/Turborepo monorepo, TanStack Query v5, Zustand, Vitest, Playwright.

**Reference spec:** `docs/superpowers/specs/2026-05-07-multica-programbench-integration-design.md` — every design choice in this plan derives from there.

---

## File Structure

### Server (Go)

| Path | Purpose |
|---|---|
| `server/migrations/070_benchmark_foundations.up.sql` | New tables + extension of `issue.origin_type` CHECK |
| `server/migrations/070_benchmark_foundations.down.sql` | Reverse |
| `server/pkg/db/queries/benchmark_suite.sql` | sqlc queries for suites |
| `server/pkg/db/queries/benchmark_profile.sql` | sqlc queries for agent profiles |
| `server/pkg/db/queries/benchmark_run.sql` | shells for run/task/eval/summary tables (used in later phases; must compile in Phase 0) |
| `server/pkg/db/queries/evaluator_pool_token.sql` | sqlc queries for evaluator tokens |
| `server/internal/service/benchmark/profile_hash.go` | Pure prompt-hash function |
| `server/internal/service/benchmark/profile_hash_test.go` | Tests for the hash function |
| `server/internal/service/benchmark/suite_service.go` | Suite CRUD service |
| `server/internal/service/benchmark/suite_service_test.go` | Tests |
| `server/internal/service/benchmark/profile_service.go` | Profile capture/list/get/delete service |
| `server/internal/service/benchmark/profile_service_test.go` | Tests |
| `server/internal/handler/benchmark.go` | HTTP handler |
| `server/internal/handler/benchmark_test.go` | Handler tests |
| `server/cmd/server/router.go` | (modify) wire `/api/benchmarks` routes |

### Frontend

| Path | Purpose |
|---|---|
| `packages/core/src/benchmarks/api.ts` | API client |
| `packages/core/src/benchmarks/types.ts` | Shared TS types |
| `packages/core/src/benchmarks/queries.ts` | TanStack Query hooks |
| `packages/core/src/benchmarks/store.ts` | Zustand store for filters/UI state |
| `packages/views/src/benchmarks/SuitesList.tsx` | List view |
| `packages/views/src/benchmarks/SuiteCreate.tsx` | Create form |
| `packages/views/src/benchmarks/SuiteDetail.tsx` | Detail view |
| `packages/views/src/benchmarks/ProfilesList.tsx` | List view |
| `packages/views/src/benchmarks/ProfileCapture.tsx` | Capture form |
| `packages/views/src/benchmarks/ProfileDetail.tsx` | Detail view |
| `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/layout.tsx` | Sub-nav |
| `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/suites/page.tsx` | Route |
| `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/suites/new/page.tsx` | Route |
| `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/suites/[suiteId]/page.tsx` | Route |
| `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/profiles/page.tsx` | Route |
| `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/profiles/new/page.tsx` | Route |
| `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/profiles/[profileId]/page.tsx` | Route |
| `packages/views/locales/en/benchmarks.json` | i18n |
| `packages/views/locales/zh-Hans/benchmarks.json` | i18n |
| `packages/views/src/sidebar/sidebar-items.tsx` | (modify) add Benchmarks entry |
| `e2e/benchmarks-foundations.spec.ts` | Playwright smoke |

---

## Task 1: Migration up — `070_benchmark_foundations.up.sql`

**Files:**
- Create: `server/migrations/070_benchmark_foundations.up.sql`

- [ ] **Step 1: Write the migration**

```sql
-- 070_benchmark_foundations.up.sql
-- Phase 0 of the ProgramBench integration. Creates the data model for suites,
-- profiles, runs, tasks, eval jobs, eval results, run summaries, and evaluator
-- tokens. Phase 0 only writes to suites/profiles/tokens; the rest are created
-- now so phase 1 does not need a second schema migration mid-feature.

-- Extend issue origin_type CHECK to allow benchmark_run-sourced issues.
ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
    CHECK (origin_type IN ('autopilot', 'quick_create', 'benchmark_run'));

CREATE TABLE benchmark_suite (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    slug            TEXT NOT NULL,
    display_name    TEXT NOT NULL,
    adapter_kind    TEXT NOT NULL,
    instance_ids    TEXT[] NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by      UUID NOT NULL REFERENCES app_user(id),
    UNIQUE (workspace_id, slug)
);
CREATE INDEX idx_benchmark_suite_workspace ON benchmark_suite(workspace_id);

CREATE TABLE benchmark_agent_profile (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    slug            TEXT NOT NULL,
    display_name    TEXT NOT NULL,
    agent_id        UUID NOT NULL REFERENCES agent(id) ON DELETE RESTRICT,
    agent_name      TEXT NOT NULL,
    model           TEXT NOT NULL,
    prompt_source   TEXT NOT NULL,
    prompt_hash     TEXT NOT NULL,
    attached_skills JSONB NOT NULL,
    captured_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    captured_by     UUID NOT NULL REFERENCES app_user(id),
    UNIQUE (workspace_id, slug)
);
CREATE INDEX idx_benchmark_profile_workspace ON benchmark_agent_profile(workspace_id);
CREATE INDEX idx_benchmark_profile_agent ON benchmark_agent_profile(agent_id);
CREATE INDEX idx_benchmark_profile_hash ON benchmark_agent_profile(workspace_id, prompt_hash);

CREATE TABLE benchmark_run (
    id                          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id                UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    suite_id                    UUID NOT NULL REFERENCES benchmark_suite(id) ON DELETE RESTRICT,
    suite_instance_ids          TEXT[] NOT NULL,
    profile_id                  UUID NOT NULL REFERENCES benchmark_agent_profile(id) ON DELETE RESTRICT,
    base_run_id                 UUID REFERENCES benchmark_run(id) ON DELETE SET NULL,
    display_name                TEXT NOT NULL,
    status                      TEXT NOT NULL,
    status_reason               TEXT NOT NULL DEFAULT '',
    notes                       TEXT NOT NULL DEFAULT '',
    evaluator_mode              TEXT NOT NULL,
    adapter_version             TEXT NOT NULL DEFAULT '',
    submission_timeout_seconds  INTEGER NOT NULL DEFAULT 7200,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by                  UUID NOT NULL REFERENCES app_user(id),
    started_at                  TIMESTAMPTZ,
    completed_at                TIMESTAMPTZ
);
CREATE INDEX idx_benchmark_run_workspace_status ON benchmark_run(workspace_id, suite_id, status);

CREATE TABLE benchmark_task (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id          UUID NOT NULL REFERENCES benchmark_run(id) ON DELETE CASCADE,
    workspace_id    UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    instance_id     TEXT NOT NULL,
    instance_meta   JSONB NOT NULL,
    issue_id        UUID REFERENCES issue(id) ON DELETE SET NULL,
    attachment_id   UUID REFERENCES attachment(id) ON DELETE SET NULL,
    status          TEXT NOT NULL,
    status_reason   TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    submitted_at    TIMESTAMPTZ,
    scored_at       TIMESTAMPTZ,
    UNIQUE (run_id, instance_id)
);
CREATE INDEX idx_benchmark_task_run_status ON benchmark_task(run_id, status);

CREATE TABLE benchmark_eval_job (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id         UUID NOT NULL UNIQUE REFERENCES benchmark_task(id) ON DELETE CASCADE,
    workspace_id    UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    adapter_kind    TEXT NOT NULL,
    state           TEXT NOT NULL,
    attempt         INTEGER NOT NULL DEFAULT 0,
    claimed_by      TEXT,
    claimed_at      TIMESTAMPTZ,
    enqueued_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ,
    last_error      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_benchmark_eval_job_pending ON benchmark_eval_job(state, enqueued_at)
    WHERE state IN ('pending', 'claimed');

CREATE TABLE benchmark_eval_result (
    task_id           UUID PRIMARY KEY REFERENCES benchmark_task(id) ON DELETE CASCADE,
    workspace_id      UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    resolved          BOOLEAN NOT NULL,
    passed_tests      INTEGER NOT NULL,
    total_tests       INTEGER NOT NULL,
    pass_rate         NUMERIC(6,5) NOT NULL,
    raw_eval_json     JSONB NOT NULL,
    failed_categories JSONB NOT NULL DEFAULT '[]'::jsonb,
    evaluated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE benchmark_run_summary (
    run_id              UUID PRIMARY KEY REFERENCES benchmark_run(id) ON DELETE CASCADE,
    workspace_id        UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    resolved_count      INTEGER NOT NULL,
    total_count         INTEGER NOT NULL,
    aggregate_pass_rate NUMERIC(6,5) NOT NULL,
    average_pass_rate   NUMERIC(6,5) NOT NULL,
    errored_count       INTEGER NOT NULL,
    failure_categories  JSONB NOT NULL DEFAULT '[]'::jsonb,
    computed_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE evaluator_pool_token (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    token_prefix    TEXT NOT NULL,
    token_hash      TEXT NOT NULL UNIQUE,
    display_name    TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by      UUID NOT NULL REFERENCES app_user(id),
    last_used_at    TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ
);
CREATE INDEX idx_evaluator_token_workspace ON evaluator_pool_token(workspace_id) WHERE revoked_at IS NULL;
```

**Note on table names:** Multica uses singular table names (`issue`, `agent`, `workspace`, `app_user`, `attachment`) — this migration follows the same convention. Confirm `app_user` is the user table name by `grep "REFERENCES app_user" server/migrations/*.up.sql | head` before running.

- [ ] **Step 2: Verify table-name conventions**

Run: `grep -h "REFERENCES " server/migrations/*.up.sql | sort -u | head -20`
Expected: confirm singular forms `workspace`, `agent`, `app_user`, `issue`, `attachment` are used.
If any names differ, fix the migration before proceeding.

- [ ] **Step 3: Apply locally**

Run: `make migrate-up`
Expected: `070_benchmark_foundations` applied; no errors. Subsequent `make migrate-up` is a no-op.

- [ ] **Step 4: Inspect schema**

Run: `psql $MULTICA_DB_URL -c "\d benchmark_suite" -c "\d benchmark_agent_profile" -c "\d evaluator_pool_token"`
Expected: tables present with the columns and indexes from Step 1.

- [ ] **Step 5: Commit**

```bash
git add server/migrations/070_benchmark_foundations.up.sql
git commit -m "feat(benchmark): phase 0 schema — suites, profiles, runs, tokens"
```

---

## Task 2: Migration down + roundtrip

**Files:**
- Create: `server/migrations/070_benchmark_foundations.down.sql`

- [ ] **Step 1: Write the down migration**

```sql
-- 070_benchmark_foundations.down.sql
DROP TABLE IF EXISTS evaluator_pool_token;
DROP TABLE IF EXISTS benchmark_run_summary;
DROP TABLE IF EXISTS benchmark_eval_result;
DROP TABLE IF EXISTS benchmark_eval_job;
DROP TABLE IF EXISTS benchmark_task;
DROP TABLE IF EXISTS benchmark_run;
DROP TABLE IF EXISTS benchmark_agent_profile;
DROP TABLE IF EXISTS benchmark_suite;

-- Restore prior issue.origin_type CHECK (matches migration 060).
ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
    CHECK (origin_type IN ('autopilot', 'quick_create'));
```

- [ ] **Step 2: Roundtrip test**

Run:
```bash
make migrate-down N=1 && make migrate-up
psql $MULTICA_DB_URL -c "SELECT 1 FROM benchmark_suite LIMIT 1"
```
Expected: down succeeds, up succeeds, final SELECT returns 0 rows (table exists, empty).

- [ ] **Step 3: Commit**

```bash
git add server/migrations/070_benchmark_foundations.down.sql
git commit -m "feat(benchmark): phase 0 schema — down migration"
```

---

## Task 3: sqlc queries — `benchmark_suite`

**Files:**
- Create: `server/pkg/db/queries/benchmark_suite.sql`

- [ ] **Step 1: Write the query file**

```sql
-- name: CreateBenchmarkSuite :one
INSERT INTO benchmark_suite (
    workspace_id, slug, display_name, adapter_kind, instance_ids, description, created_by
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetBenchmarkSuite :one
SELECT * FROM benchmark_suite WHERE id = $1 AND workspace_id = $2;

-- name: GetBenchmarkSuiteBySlug :one
SELECT * FROM benchmark_suite WHERE workspace_id = $1 AND slug = $2;

-- name: ListBenchmarkSuites :many
SELECT * FROM benchmark_suite
WHERE workspace_id = $1
ORDER BY created_at DESC;

-- name: UpdateBenchmarkSuite :one
UPDATE benchmark_suite
SET display_name = $3, instance_ids = $4, description = $5
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteBenchmarkSuite :exec
DELETE FROM benchmark_suite WHERE id = $1 AND workspace_id = $2;

-- name: CountBenchmarkRunsForSuite :one
SELECT COUNT(*) FROM benchmark_run WHERE suite_id = $1;
```

- [ ] **Step 2: Generate**

Run: `make sqlc`
Expected: `pkg/db/generated/benchmark_suite.sql.go` is regenerated; `go build ./...` succeeds.

- [ ] **Step 3: Commit**

```bash
git add server/pkg/db/queries/benchmark_suite.sql server/pkg/db/generated/
git commit -m "feat(benchmark): sqlc queries for benchmark_suite"
```

---

## Task 4: sqlc queries — `benchmark_agent_profile`

**Files:**
- Create: `server/pkg/db/queries/benchmark_profile.sql`

- [ ] **Step 1: Write queries**

```sql
-- name: CreateBenchmarkProfile :one
INSERT INTO benchmark_agent_profile (
    workspace_id, slug, display_name, agent_id, agent_name, model,
    prompt_source, prompt_hash, attached_skills, captured_by
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetBenchmarkProfile :one
SELECT * FROM benchmark_agent_profile WHERE id = $1 AND workspace_id = $2;

-- name: GetBenchmarkProfileBySlug :one
SELECT * FROM benchmark_agent_profile WHERE workspace_id = $1 AND slug = $2;

-- name: ListBenchmarkProfiles :many
SELECT * FROM benchmark_agent_profile
WHERE workspace_id = $1
ORDER BY captured_at DESC;

-- name: ListBenchmarkProfilesForAgent :many
SELECT * FROM benchmark_agent_profile
WHERE workspace_id = $1 AND agent_id = $2
ORDER BY captured_at DESC;

-- name: FindProfileByHash :one
SELECT * FROM benchmark_agent_profile
WHERE workspace_id = $1 AND prompt_hash = $2
LIMIT 1;

-- name: DeleteBenchmarkProfile :exec
DELETE FROM benchmark_agent_profile WHERE id = $1 AND workspace_id = $2;
```

- [ ] **Step 2: Generate**

Run: `make sqlc && go build ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add server/pkg/db/queries/benchmark_profile.sql server/pkg/db/generated/
git commit -m "feat(benchmark): sqlc queries for benchmark_agent_profile"
```

---

## Task 5: sqlc shells — run / task / eval / summary

These tables are used in Phase 1 and beyond. Generating empty-but-compiling sqlc bindings now keeps Phase 1 PRs additive.

**Files:**
- Create: `server/pkg/db/queries/benchmark_run.sql`

- [ ] **Step 1: Write minimal queries**

```sql
-- name: CreateBenchmarkRun :one
INSERT INTO benchmark_run (
    workspace_id, suite_id, suite_instance_ids, profile_id, base_run_id,
    display_name, status, evaluator_mode, adapter_version, submission_timeout_seconds, created_by
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetBenchmarkRun :one
SELECT * FROM benchmark_run WHERE id = $1 AND workspace_id = $2;

-- name: ListBenchmarkRuns :many
SELECT * FROM benchmark_run WHERE workspace_id = $1
ORDER BY created_at DESC LIMIT $2;
```

(Phase 1 will extend this file with task/eval/summary queries and lifecycle updates. Keeping the minimal shell now lets Phase 0 service code reference run-counts without leaving holes in the generated package.)

- [ ] **Step 2: Generate and build**

Run: `make sqlc && go build ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add server/pkg/db/queries/benchmark_run.sql server/pkg/db/generated/
git commit -m "feat(benchmark): sqlc shell for benchmark_run (used in phase 1)"
```

---

## Task 6: sqlc queries — `evaluator_pool_token`

**Files:**
- Create: `server/pkg/db/queries/evaluator_pool_token.sql`

- [ ] **Step 1: Write queries**

```sql
-- name: CreateEvaluatorPoolToken :one
INSERT INTO evaluator_pool_token (
    workspace_id, token_prefix, token_hash, display_name, created_by
) VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListEvaluatorPoolTokens :many
SELECT id, workspace_id, token_prefix, display_name, created_at, created_by, last_used_at, revoked_at
FROM evaluator_pool_token
WHERE workspace_id = $1
ORDER BY created_at DESC;

-- name: GetEvaluatorPoolTokenByHash :one
SELECT * FROM evaluator_pool_token
WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: TouchEvaluatorPoolToken :exec
UPDATE evaluator_pool_token SET last_used_at = now() WHERE id = $1;

-- name: RevokeEvaluatorPoolToken :exec
UPDATE evaluator_pool_token SET revoked_at = now()
WHERE id = $1 AND workspace_id = $2 AND revoked_at IS NULL;
```

(Phase 0 only needs these to compile; the create endpoint is added in Phase 1 alongside the evaluator binary.)

- [ ] **Step 2: Generate and build**

Run: `make sqlc && go build ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add server/pkg/db/queries/evaluator_pool_token.sql server/pkg/db/generated/
git commit -m "feat(benchmark): sqlc queries for evaluator_pool_token"
```

---

## Task 7: Profile-hash pure function (TDD)

**Files:**
- Create: `server/internal/service/benchmark/profile_hash.go`
- Test: `server/internal/service/benchmark/profile_hash_test.go`

- [ ] **Step 1: Write the failing test**

```go
package benchmark

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputePromptHash_Deterministic(t *testing.T) {
	in := PromptHashInput{
		AgentName:    "ProgramBenchRunner",
		Model:        "claude-opus-4-7",
		PromptSource: "# system\nyou are a benchmark runner\n",
		AttachedSkills: []SkillRef{
			{Slug: "tar-pack", Version: "1.0.0"},
			{Slug: "verify-executable", Version: "0.3.0"},
		},
	}
	a := ComputePromptHash(in)
	b := ComputePromptHash(in)
	require.Equal(t, a, b)
	require.Len(t, a, 64) // sha256 hex
	require.True(t, strings.IndexFunc(a, func(r rune) bool {
		return !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'))
	}) == -1, "lowercase hex only")
}

func TestComputePromptHash_OrderingOfSkillsDoesNotMatter(t *testing.T) {
	a := ComputePromptHash(PromptHashInput{
		AgentName: "X", Model: "m", PromptSource: "p",
		AttachedSkills: []SkillRef{{Slug: "a", Version: "1"}, {Slug: "b", Version: "2"}},
	})
	b := ComputePromptHash(PromptHashInput{
		AgentName: "X", Model: "m", PromptSource: "p",
		AttachedSkills: []SkillRef{{Slug: "b", Version: "2"}, {Slug: "a", Version: "1"}},
	})
	require.Equal(t, a, b)
}

func TestComputePromptHash_DifferentForDifferentInputs(t *testing.T) {
	base := PromptHashInput{AgentName: "X", Model: "m", PromptSource: "p"}

	cases := []struct {
		name string
		mut  func(p *PromptHashInput)
	}{
		{"different agent name", func(p *PromptHashInput) { p.AgentName = "Y" }},
		{"different model", func(p *PromptHashInput) { p.Model = "claude-opus-4-6" }},
		{"different prompt source - whitespace counts",
			func(p *PromptHashInput) { p.PromptSource = "p " /* trailing space */ }},
		{"added skill", func(p *PromptHashInput) {
			p.AttachedSkills = []SkillRef{{Slug: "s", Version: "1"}}
		}},
		{"different skill version",
			func(p *PromptHashInput) { p.AttachedSkills = []SkillRef{{Slug: "s", Version: "2"}} }},
	}

	baseHash := ComputePromptHash(base)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mut := base
			c.mut(&mut)
			require.NotEqual(t, baseHash, ComputePromptHash(mut))
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./server/internal/service/benchmark/ -run TestComputePromptHash -v`
Expected: FAIL with "undefined: ComputePromptHash" / "undefined: PromptHashInput".

- [ ] **Step 3: Implement**

```go
package benchmark

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// SkillRef is the canonical reference shape used in profile hashing.
// Skill ids are not stable across workspaces, so slug+version is the identity.
type SkillRef struct {
	Slug    string `json:"slug"`
	Version string `json:"version"`
}

// PromptHashInput is the canonical, normalized profile content that goes into
// the hash. Anything not in this struct is metadata and does not affect hash.
type PromptHashInput struct {
	AgentName      string
	Model          string
	PromptSource   string
	AttachedSkills []SkillRef
}

// ComputePromptHash returns sha256(canonical_json(input)) hex-encoded.
// Order of attached skills does not affect the result.
func ComputePromptHash(in PromptHashInput) string {
	skills := append([]SkillRef(nil), in.AttachedSkills...)
	sort.Slice(skills, func(i, j int) bool {
		if skills[i].Slug != skills[j].Slug {
			return skills[i].Slug < skills[j].Slug
		}
		return skills[i].Version < skills[j].Version
	})

	canonical := struct {
		AgentName      string     `json:"agent_name"`
		Model          string     `json:"model"`
		PromptSource   string     `json:"prompt_source"`
		AttachedSkills []SkillRef `json:"attached_skills"`
	}{in.AgentName, in.Model, in.PromptSource, skills}

	// json.Marshal emits map keys sorted; for structs the field order is
	// fixed by the struct definition. Both make output deterministic.
	body, err := json.Marshal(canonical)
	if err != nil {
		// Inputs are plain strings and slices of strings. json.Marshal cannot
		// fail on these. If it ever does, that is a runtime invariant break.
		panic(err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./server/internal/service/benchmark/ -run TestComputePromptHash -v`
Expected: PASS, three subtests green.

- [ ] **Step 5: Commit**

```bash
git add server/internal/service/benchmark/profile_hash.go server/internal/service/benchmark/profile_hash_test.go
git commit -m "feat(benchmark): pure profile prompt-hash function"
```

---

## Task 8: SuiteService CRUD (TDD)

**Files:**
- Create: `server/internal/service/benchmark/suite_service.go`
- Test: `server/internal/service/benchmark/suite_service_test.go`

This service is thin: it validates input, calls sqlc, normalizes errors. Tests use Multica's existing test-DB pattern (`testdb.New(t)`) the same way `service/issue_test.go` does — confirm by reading that file before writing the test if unsure of the helper signature.

- [ ] **Step 1: Write the failing tests**

```go
package benchmark_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/service/benchmark"
	"github.com/multica-ai/multica/server/internal/service/testfixture"
)

func TestSuiteService_Create_StoresAndReturns(t *testing.T) {
	ctx := context.Background()
	tx := testfixture.NewWorkspace(t) // creates workspace + creator user
	s := benchmark.NewSuiteService(tx.Queries)

	got, err := s.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID:  tx.WorkspaceID,
		Slug:         "smoke-cli-v1",
		DisplayName:  "Smoke CLI v1",
		AdapterKind:  "programbench",
		InstanceIDs:  []string{"abishekvashok__cmatrix.5c082c6"},
		CreatedBy:    tx.UserID,
	})
	require.NoError(t, err)
	require.Equal(t, "smoke-cli-v1", got.Slug)
	require.Equal(t, []string{"abishekvashok__cmatrix.5c082c6"}, got.InstanceIDs)
}

func TestSuiteService_Create_RejectsEmptyInstanceList(t *testing.T) {
	ctx := context.Background()
	tx := testfixture.NewWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	_, err := s.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: tx.WorkspaceID, Slug: "empty", DisplayName: "Empty",
		AdapterKind: "programbench", InstanceIDs: nil, CreatedBy: tx.UserID,
	})
	require.ErrorIs(t, err, benchmark.ErrSuiteInstanceListEmpty)
}

func TestSuiteService_Create_RejectsDuplicateSlug(t *testing.T) {
	ctx := context.Background()
	tx := testfixture.NewWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	in := benchmark.CreateSuiteInput{
		WorkspaceID: tx.WorkspaceID, Slug: "dup", DisplayName: "A",
		AdapterKind: "programbench", InstanceIDs: []string{"a"}, CreatedBy: tx.UserID,
	}
	_, err := s.Create(ctx, in)
	require.NoError(t, err)
	_, err = s.Create(ctx, in)
	require.ErrorIs(t, err, benchmark.ErrSuiteSlugTaken)
}

func TestSuiteService_List_ReturnsWorkspaceScoped(t *testing.T) {
	ctx := context.Background()
	tx := testfixture.NewWorkspace(t)
	other := testfixture.NewWorkspace(t)
	s := benchmark.NewSuiteService(tx.Queries)

	_, _ = s.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: tx.WorkspaceID, Slug: "a", DisplayName: "A",
		AdapterKind: "programbench", InstanceIDs: []string{"x"}, CreatedBy: tx.UserID,
	})
	_, _ = s.Create(ctx, benchmark.CreateSuiteInput{
		WorkspaceID: other.WorkspaceID, Slug: "b", DisplayName: "B",
		AdapterKind: "programbench", InstanceIDs: []string{"y"}, CreatedBy: other.UserID,
	})

	got, err := s.List(ctx, tx.WorkspaceID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "a", got[0].Slug)
}
```

- [ ] **Step 2: Confirm the testfixture helper exists**

Run: `find server/internal/service -name 'testfixture*' -o -name 'testdb*' | head`
Expected: at least one helper file. If only `testdb` exists, swap `testfixture.NewWorkspace(t)` for the equivalent in that helper. If neither exists, model the helper on `service/issue_test.go`'s setup pattern and create a small `testfixture/workspace.go` first — but only if it's truly missing.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./server/internal/service/benchmark/ -run TestSuiteService -v`
Expected: FAIL — undefined `NewSuiteService`, `CreateSuiteInput`, `ErrSuite*`.

- [ ] **Step 4: Implement the service**

```go
package benchmark

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrSuiteInstanceListEmpty = errors.New("benchmark: suite instance list cannot be empty")
	ErrSuiteSlugTaken         = errors.New("benchmark: suite slug already used in workspace")
	ErrSuiteNotFound          = errors.New("benchmark: suite not found")
)

type Suite struct {
	ID           uuid.UUID
	WorkspaceID  uuid.UUID
	Slug         string
	DisplayName  string
	AdapterKind  string
	InstanceIDs  []string
	Description  string
	CreatedBy    uuid.UUID
}

type CreateSuiteInput struct {
	WorkspaceID uuid.UUID
	Slug        string
	DisplayName string
	AdapterKind string
	InstanceIDs []string
	Description string
	CreatedBy   uuid.UUID
}

type SuiteService struct {
	q *db.Queries
}

func NewSuiteService(q *db.Queries) *SuiteService { return &SuiteService{q: q} }

func (s *SuiteService) Create(ctx context.Context, in CreateSuiteInput) (Suite, error) {
	if len(in.InstanceIDs) == 0 {
		return Suite{}, ErrSuiteInstanceListEmpty
	}
	in.Slug = strings.TrimSpace(in.Slug)
	row, err := s.q.CreateBenchmarkSuite(ctx, db.CreateBenchmarkSuiteParams{
		WorkspaceID:  in.WorkspaceID,
		Slug:         in.Slug,
		DisplayName:  in.DisplayName,
		AdapterKind:  in.AdapterKind,
		InstanceIds:  in.InstanceIDs,
		Description:  in.Description,
		CreatedBy:    in.CreatedBy,
	})
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return Suite{}, ErrSuiteSlugTaken
		}
		return Suite{}, err
	}
	return rowToSuite(row), nil
}

func (s *SuiteService) Get(ctx context.Context, id, workspaceID uuid.UUID) (Suite, error) {
	row, err := s.q.GetBenchmarkSuite(ctx, db.GetBenchmarkSuiteParams{ID: id, WorkspaceID: workspaceID})
	if errors.Is(err, db.ErrNoRows) {
		return Suite{}, ErrSuiteNotFound
	}
	if err != nil {
		return Suite{}, err
	}
	return rowToSuite(row), nil
}

func (s *SuiteService) List(ctx context.Context, workspaceID uuid.UUID) ([]Suite, error) {
	rows, err := s.q.ListBenchmarkSuites(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]Suite, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToSuite(r))
	}
	return out, nil
}

func (s *SuiteService) Delete(ctx context.Context, id, workspaceID uuid.UUID) error {
	return s.q.DeleteBenchmarkSuite(ctx, db.DeleteBenchmarkSuiteParams{ID: id, WorkspaceID: workspaceID})
}

func rowToSuite(r db.BenchmarkSuite) Suite {
	return Suite{
		ID: r.ID, WorkspaceID: r.WorkspaceID, Slug: r.Slug, DisplayName: r.DisplayName,
		AdapterKind: r.AdapterKind, InstanceIDs: r.InstanceIds,
		Description: r.Description, CreatedBy: r.CreatedBy,
	}
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./server/internal/service/benchmark/ -v`
Expected: all four `TestSuiteService_*` PASS plus the prior `TestComputePromptHash_*`.

- [ ] **Step 6: Commit**

```bash
git add server/internal/service/benchmark/suite_service.go server/internal/service/benchmark/suite_service_test.go
git commit -m "feat(benchmark): SuiteService CRUD with workspace scoping"
```

---

## Task 9: ProfileService Capture (TDD)

**Files:**
- Create: `server/internal/service/benchmark/profile_service.go`
- Test: `server/internal/service/benchmark/profile_service_test.go`

The capture flow is:
1. Read the live `agent` row.
2. Read `agent.prompt_source` and the agent's attached skills (existing `service/skill.go` lookups).
3. Compute `prompt_hash` via `ComputePromptHash`.
4. Insert into `benchmark_agent_profile`.
5. Return a struct, plus `DuplicateOf *uuid.UUID` set when a profile with the same hash already exists in the workspace (warning, not error).

- [ ] **Step 1: Write tests**

```go
package benchmark_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/service/benchmark"
	"github.com/multica-ai/multica/server/internal/service/testfixture"
)

func TestProfileService_Capture_StoresImmutableSnapshot(t *testing.T) {
	ctx := context.Background()
	tx := testfixture.NewWorkspace(t)
	agent := testfixture.NewAgent(t, tx.WorkspaceID, testfixture.AgentSpec{
		Name: "ProgramBenchRunner", Model: "claude-opus-4-7",
		PromptSource: "# system\ndo benchmark things\n",
	})
	s := benchmark.NewProfileService(tx.Queries)

	got, err := s.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: tx.WorkspaceID, AgentID: agent.ID, Slug: "current",
		DisplayName: "Current", CapturedBy: tx.UserID,
	})
	require.NoError(t, err)
	require.Equal(t, "ProgramBenchRunner", got.AgentName)
	require.Equal(t, "claude-opus-4-7", got.Model)
	require.Equal(t, "# system\ndo benchmark things\n", got.PromptSource)
	require.Len(t, got.PromptHash, 64)
	require.Nil(t, got.DuplicateOf)
}

func TestProfileService_Capture_DetectsDuplicateHash(t *testing.T) {
	ctx := context.Background()
	tx := testfixture.NewWorkspace(t)
	agent := testfixture.NewAgent(t, tx.WorkspaceID, testfixture.AgentSpec{
		Name: "X", Model: "m", PromptSource: "p",
	})
	s := benchmark.NewProfileService(tx.Queries)

	first, err := s.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: tx.WorkspaceID, AgentID: agent.ID, Slug: "v1",
		DisplayName: "V1", CapturedBy: tx.UserID,
	})
	require.NoError(t, err)

	second, err := s.Capture(ctx, benchmark.CaptureProfileInput{
		WorkspaceID: tx.WorkspaceID, AgentID: agent.ID, Slug: "v2",
		DisplayName: "V2", CapturedBy: tx.UserID,
	})
	require.NoError(t, err)
	require.NotNil(t, second.DuplicateOf)
	require.Equal(t, first.ID, *second.DuplicateOf)
}

func TestProfileService_Capture_RejectsDuplicateSlug(t *testing.T) {
	ctx := context.Background()
	tx := testfixture.NewWorkspace(t)
	agent := testfixture.NewAgent(t, tx.WorkspaceID, testfixture.AgentSpec{
		Name: "X", Model: "m", PromptSource: "p",
	})
	s := benchmark.NewProfileService(tx.Queries)

	in := benchmark.CaptureProfileInput{
		WorkspaceID: tx.WorkspaceID, AgentID: agent.ID, Slug: "same",
		DisplayName: "Same", CapturedBy: tx.UserID,
	}
	_, err := s.Capture(ctx, in)
	require.NoError(t, err)
	_, err = s.Capture(ctx, in)
	require.ErrorIs(t, err, benchmark.ErrProfileSlugTaken)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/internal/service/benchmark/ -run TestProfileService -v`
Expected: FAIL — undefined `NewProfileService`, etc.

- [ ] **Step 3: Implement**

```go
package benchmark

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	ErrProfileNotFound   = errors.New("benchmark: profile not found")
	ErrProfileSlugTaken  = errors.New("benchmark: profile slug already used in workspace")
	ErrCaptureAgent      = errors.New("benchmark: agent not found or not in workspace")
)

type Profile struct {
	ID             uuid.UUID
	WorkspaceID    uuid.UUID
	Slug           string
	DisplayName    string
	AgentID        uuid.UUID
	AgentName      string
	Model          string
	PromptSource   string
	PromptHash     string
	AttachedSkills []SkillRef
	CapturedBy     uuid.UUID
	DuplicateOf    *uuid.UUID // non-nil when an existing profile has the same prompt_hash
}

type CaptureProfileInput struct {
	WorkspaceID uuid.UUID
	AgentID     uuid.UUID
	Slug        string
	DisplayName string
	CapturedBy  uuid.UUID
}

type ProfileService struct{ q *db.Queries }

func NewProfileService(q *db.Queries) *ProfileService { return &ProfileService{q: q} }

func (s *ProfileService) Capture(ctx context.Context, in CaptureProfileInput) (Profile, error) {
	agent, err := s.q.GetAgentByID(ctx, db.GetAgentByIDParams{
		ID: in.AgentID, WorkspaceID: in.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrCaptureAgent
	}
	if err != nil {
		return Profile{}, err
	}

	skillRefs, err := s.collectAttachedSkills(ctx, agent.ID, in.WorkspaceID)
	if err != nil {
		return Profile{}, err
	}

	hash := ComputePromptHash(PromptHashInput{
		AgentName:      agent.Name,
		Model:          agent.Model,
		PromptSource:   agent.PromptSource,
		AttachedSkills: skillRefs,
	})

	skillsJSON, err := json.Marshal(skillRefs)
	if err != nil {
		return Profile{}, err
	}

	dupID, err := s.findDuplicate(ctx, in.WorkspaceID, hash)
	if err != nil {
		return Profile{}, err
	}

	row, err := s.q.CreateBenchmarkProfile(ctx, db.CreateBenchmarkProfileParams{
		WorkspaceID:    in.WorkspaceID,
		Slug:           in.Slug,
		DisplayName:    in.DisplayName,
		AgentID:        agent.ID,
		AgentName:      agent.Name,
		Model:          agent.Model,
		PromptSource:   agent.PromptSource,
		PromptHash:     hash,
		AttachedSkills: skillsJSON,
		CapturedBy:     in.CapturedBy,
	})
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return Profile{}, ErrProfileSlugTaken
		}
		return Profile{}, err
	}

	return rowToProfile(row, dupID), nil
}

func (s *ProfileService) Get(ctx context.Context, id, workspaceID uuid.UUID) (Profile, error) {
	row, err := s.q.GetBenchmarkProfile(ctx, db.GetBenchmarkProfileParams{ID: id, WorkspaceID: workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrProfileNotFound
	}
	if err != nil {
		return Profile{}, err
	}
	return rowToProfile(row, nil), nil
}

func (s *ProfileService) List(ctx context.Context, workspaceID uuid.UUID) ([]Profile, error) {
	rows, err := s.q.ListBenchmarkProfiles(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]Profile, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToProfile(r, nil))
	}
	return out, nil
}

func (s *ProfileService) Delete(ctx context.Context, id, workspaceID uuid.UUID) error {
	return s.q.DeleteBenchmarkProfile(ctx, db.DeleteBenchmarkProfileParams{ID: id, WorkspaceID: workspaceID})
}

func (s *ProfileService) findDuplicate(ctx context.Context, ws uuid.UUID, hash string) (*uuid.UUID, error) {
	row, err := s.q.FindProfileByHash(ctx, db.FindProfileByHashParams{WorkspaceID: ws, PromptHash: hash})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row.ID, nil
}

func (s *ProfileService) collectAttachedSkills(ctx context.Context, agentID, ws uuid.UUID) ([]SkillRef, error) {
	rows, err := s.q.ListSkillsAttachedToAgent(ctx, db.ListSkillsAttachedToAgentParams{
		AgentID: agentID, WorkspaceID: ws,
	})
	if err != nil {
		return nil, err
	}
	out := make([]SkillRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, SkillRef{Slug: r.Slug, Version: r.Version})
	}
	return out, nil
}

func rowToProfile(r db.BenchmarkAgentProfile, dup *uuid.UUID) Profile {
	var skills []SkillRef
	_ = json.Unmarshal(r.AttachedSkills, &skills)
	return Profile{
		ID: r.ID, WorkspaceID: r.WorkspaceID, Slug: r.Slug, DisplayName: r.DisplayName,
		AgentID: r.AgentID, AgentName: r.AgentName, Model: r.Model,
		PromptSource: r.PromptSource, PromptHash: r.PromptHash,
		AttachedSkills: skills, CapturedBy: r.CapturedBy, DuplicateOf: dup,
	}
}
```

**Note on `ListSkillsAttachedToAgent`:** confirm the exact name in `pkg/db/generated/skill.sql.go` before writing this. If the existing query has a different name (likely something like `ListAgentSkills`), use that. The point is to get `(slug, version)` for skills attached to the agent in this workspace; do not invent a new query if one exists.

- [ ] **Step 4: Run tests**

Run: `go test ./server/internal/service/benchmark/ -run TestProfileService -v`
Expected: all three subtests PASS.

- [ ] **Step 5: Commit**

```bash
git add server/internal/service/benchmark/profile_service.go server/internal/service/benchmark/profile_service_test.go
git commit -m "feat(benchmark): ProfileService capture with hash-based duplicate detection"
```

---

## Task 10: Suite HTTP handler (TDD)

**Files:**
- Create: `server/internal/handler/benchmark.go`
- Test: `server/internal/handler/benchmark_test.go`

Multica's existing handler pattern (see `internal/handler/issue.go`): a struct holds dependencies, methods accept `(w http.ResponseWriter, r *http.Request)`, JSON I/O via `handler.RespondJSON` / `handler.DecodeJSON`. Workspace id comes from middleware (chi `URLParam` after the workspace route). User id comes from `auth.MustUserID(ctx)`.

- [ ] **Step 1: Write tests**

```go
package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/service/benchmark"
	"github.com/multica-ai/multica/server/internal/service/testfixture"
)

func TestBenchmarkHandler_CreateSuite_201(t *testing.T) {
	tx := testfixture.NewWorkspace(t)
	h := handler.NewBenchmarkHandler(handler.BenchmarkDeps{
		Suites:   benchmark.NewSuiteService(tx.Queries),
		Profiles: benchmark.NewProfileService(tx.Queries),
	})

	body := bytes.NewBufferString(`{
		"slug": "smoke-cli-v1",
		"display_name": "Smoke CLI v1",
		"adapter_kind": "programbench",
		"instance_ids": ["abishekvashok__cmatrix.5c082c6"]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/benchmarks/suites", body).WithContext(
		testfixture.AuthCtx(context.Background(), tx.UserID, tx.WorkspaceID),
	)
	rec := httptest.NewRecorder()

	h.CreateSuite(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	var resp struct{ ID string `json:"id"` }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.ID)
}

func TestBenchmarkHandler_CreateSuite_400_OnEmptyInstances(t *testing.T) {
	tx := testfixture.NewWorkspace(t)
	h := handler.NewBenchmarkHandler(handler.BenchmarkDeps{
		Suites:   benchmark.NewSuiteService(tx.Queries),
		Profiles: benchmark.NewProfileService(tx.Queries),
	})

	body := bytes.NewBufferString(`{
		"slug": "x", "display_name": "X",
		"adapter_kind": "programbench", "instance_ids": []
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/benchmarks/suites", body).WithContext(
		testfixture.AuthCtx(context.Background(), tx.UserID, tx.WorkspaceID),
	)
	rec := httptest.NewRecorder()

	h.CreateSuite(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "instance_list_empty")
}

func TestBenchmarkHandler_CreateSuite_409_OnDuplicate(t *testing.T) {
	tx := testfixture.NewWorkspace(t)
	h := handler.NewBenchmarkHandler(handler.BenchmarkDeps{
		Suites:   benchmark.NewSuiteService(tx.Queries),
		Profiles: benchmark.NewProfileService(tx.Queries),
	})

	body := func() *bytes.Buffer {
		return bytes.NewBufferString(`{"slug":"dup","display_name":"X","adapter_kind":"programbench","instance_ids":["x"]}`)
	}
	for i, want := range []int{http.StatusCreated, http.StatusConflict} {
		req := httptest.NewRequest(http.MethodPost, "/api/benchmarks/suites", body()).WithContext(
			testfixture.AuthCtx(context.Background(), tx.UserID, tx.WorkspaceID),
		)
		rec := httptest.NewRecorder()
		h.CreateSuite(rec, req)
		require.Equalf(t, want, rec.Code, "request #%d: body=%s", i+1, rec.Body.String())
	}
}

func TestBenchmarkHandler_ListSuites_200(t *testing.T) {
	tx := testfixture.NewWorkspace(t)
	suites := benchmark.NewSuiteService(tx.Queries)
	_, _ = suites.Create(context.Background(), benchmark.CreateSuiteInput{
		WorkspaceID: tx.WorkspaceID, Slug: "s1", DisplayName: "S1",
		AdapterKind: "programbench", InstanceIDs: []string{"x"}, CreatedBy: tx.UserID,
	})

	h := handler.NewBenchmarkHandler(handler.BenchmarkDeps{Suites: suites, Profiles: benchmark.NewProfileService(tx.Queries)})
	req := httptest.NewRequest(http.MethodGet, "/api/benchmarks/suites", nil).WithContext(
		testfixture.AuthCtx(context.Background(), tx.UserID, tx.WorkspaceID),
	)
	rec := httptest.NewRecorder()
	h.ListSuites(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct{ Items []map[string]any `json:"items"` }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Items, 1)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/internal/handler/ -run TestBenchmarkHandler_Create -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Implement the handler**

```go
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/service/benchmark"
)

type BenchmarkDeps struct {
	Suites   *benchmark.SuiteService
	Profiles *benchmark.ProfileService
}

type BenchmarkHandler struct{ deps BenchmarkDeps }

func NewBenchmarkHandler(deps BenchmarkDeps) *BenchmarkHandler { return &BenchmarkHandler{deps: deps} }

type createSuiteRequest struct {
	Slug        string   `json:"slug"`
	DisplayName string   `json:"display_name"`
	AdapterKind string   `json:"adapter_kind"`
	InstanceIDs []string `json:"instance_ids"`
	Description string   `json:"description"`
}

func (h *BenchmarkHandler) CreateSuite(w http.ResponseWriter, r *http.Request) {
	var req createSuiteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	ws, ok := auth.WorkspaceID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no_workspace", "")
		return
	}
	uid, ok := auth.UserID(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "no_user", "")
		return
	}

	out, err := h.deps.Suites.Create(r.Context(), benchmark.CreateSuiteInput{
		WorkspaceID: ws, Slug: req.Slug, DisplayName: req.DisplayName,
		AdapterKind: req.AdapterKind, InstanceIDs: req.InstanceIDs,
		Description: req.Description, CreatedBy: uid,
	})
	switch {
	case errors.Is(err, benchmark.ErrSuiteInstanceListEmpty):
		writeError(w, http.StatusBadRequest, "instance_list_empty", err.Error())
		return
	case errors.Is(err, benchmark.ErrSuiteSlugTaken):
		writeError(w, http.StatusConflict, "slug_taken", err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, suiteToDTO(out))
}

func (h *BenchmarkHandler) ListSuites(w http.ResponseWriter, r *http.Request) {
	ws, _ := auth.WorkspaceID(r.Context())
	rows, err := h.deps.Suites.List(r.Context(), ws)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, s := range rows {
		out = append(out, suiteToDTO(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *BenchmarkHandler) GetSuite(w http.ResponseWriter, r *http.Request) {
	ws, _ := auth.WorkspaceID(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	row, err := h.deps.Suites.Get(r.Context(), id, ws)
	if errors.Is(err, benchmark.ErrSuiteNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, suiteToDTO(row))
}

func (h *BenchmarkHandler) DeleteSuite(w http.ResponseWriter, r *http.Request) {
	ws, _ := auth.WorkspaceID(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	if err := h.deps.Suites.Delete(r.Context(), id, ws); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func suiteToDTO(s benchmark.Suite) map[string]any {
	return map[string]any{
		"id":            s.ID,
		"workspace_id":  s.WorkspaceID,
		"slug":          s.Slug,
		"display_name":  s.DisplayName,
		"adapter_kind":  s.AdapterKind,
		"instance_ids":  s.InstanceIDs,
		"description":   s.Description,
		"created_by":    s.CreatedBy,
	}
}

// writeJSON / writeError exist already in handler/handler.go; reuse them.
// If they have different names (e.g. respondJSON), search and adjust.
```

- [ ] **Step 4: Run tests**

Run: `go test ./server/internal/handler/ -run TestBenchmarkHandler -v`
Expected: PASS for all four cases.

- [ ] **Step 5: Commit**

```bash
git add server/internal/handler/benchmark.go server/internal/handler/benchmark_test.go
git commit -m "feat(benchmark): HTTP handler for /api/benchmarks/suites"
```

---

## Task 11: Profile HTTP handler (TDD)

**Files:**
- Modify: `server/internal/handler/benchmark.go` (add profile routes)
- Modify: `server/internal/handler/benchmark_test.go` (add profile tests)

- [ ] **Step 1: Write tests**

Add to `benchmark_test.go`:

```go
func TestBenchmarkHandler_CaptureProfile_201(t *testing.T) {
	tx := testfixture.NewWorkspace(t)
	agent := testfixture.NewAgent(t, tx.WorkspaceID, testfixture.AgentSpec{
		Name: "ProgramBenchRunner", Model: "claude-opus-4-7",
		PromptSource: "# bench\n",
	})
	h := handler.NewBenchmarkHandler(handler.BenchmarkDeps{
		Suites:   benchmark.NewSuiteService(tx.Queries),
		Profiles: benchmark.NewProfileService(tx.Queries),
	})

	body := bytes.NewBufferString(`{
		"agent_id": "` + agent.ID.String() + `",
		"slug": "current",
		"display_name": "Current"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/benchmarks/profiles", body).WithContext(
		testfixture.AuthCtx(context.Background(), tx.UserID, tx.WorkspaceID),
	)
	rec := httptest.NewRecorder()
	h.CaptureProfile(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)

	var got struct {
		ID          string `json:"id"`
		PromptHash  string `json:"prompt_hash"`
		DuplicateOf string `json:"duplicate_of,omitempty"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.PromptHash, 64)
	require.Empty(t, got.DuplicateOf)
}

func TestBenchmarkHandler_CaptureProfile_DuplicateHashAllowed(t *testing.T) {
	tx := testfixture.NewWorkspace(t)
	agent := testfixture.NewAgent(t, tx.WorkspaceID, testfixture.AgentSpec{
		Name: "X", Model: "m", PromptSource: "p",
	})
	h := handler.NewBenchmarkHandler(handler.BenchmarkDeps{
		Suites:   benchmark.NewSuiteService(tx.Queries),
		Profiles: benchmark.NewProfileService(tx.Queries),
	})

	mk := func(slug string) *http.Request {
		body := bytes.NewBufferString(`{"agent_id":"` + agent.ID.String() + `","slug":"` + slug + `","display_name":"X"}`)
		return httptest.NewRequest(http.MethodPost, "/api/benchmarks/profiles", body).WithContext(
			testfixture.AuthCtx(context.Background(), tx.UserID, tx.WorkspaceID),
		)
	}

	rec1 := httptest.NewRecorder(); h.CaptureProfile(rec1, mk("v1"))
	rec2 := httptest.NewRecorder(); h.CaptureProfile(rec2, mk("v2"))

	require.Equal(t, http.StatusCreated, rec1.Code)
	require.Equal(t, http.StatusCreated, rec2.Code)

	var got struct{ DuplicateOf string `json:"duplicate_of"` }
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &got))
	require.NotEmpty(t, got.DuplicateOf)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./server/internal/handler/ -run TestBenchmarkHandler_CaptureProfile -v`
Expected: FAIL — `CaptureProfile` undefined.

- [ ] **Step 3: Implement**

Add to `benchmark.go`:

```go
type captureProfileRequest struct {
	AgentID     string `json:"agent_id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

func (h *BenchmarkHandler) CaptureProfile(w http.ResponseWriter, r *http.Request) {
	var req captureProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	agentID, err := uuid.Parse(req.AgentID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_agent_id", err.Error())
		return
	}
	ws, _ := auth.WorkspaceID(r.Context())
	uid, _ := auth.UserID(r.Context())

	out, err := h.deps.Profiles.Capture(r.Context(), benchmark.CaptureProfileInput{
		WorkspaceID: ws, AgentID: agentID, Slug: req.Slug,
		DisplayName: req.DisplayName, CapturedBy: uid,
	})
	switch {
	case errors.Is(err, benchmark.ErrCaptureAgent):
		writeError(w, http.StatusBadRequest, "agent_not_found", err.Error())
		return
	case errors.Is(err, benchmark.ErrProfileSlugTaken):
		writeError(w, http.StatusConflict, "slug_taken", err.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, profileToDTO(out))
}

func (h *BenchmarkHandler) ListProfiles(w http.ResponseWriter, r *http.Request) {
	ws, _ := auth.WorkspaceID(r.Context())
	rows, err := h.deps.Profiles.List(r.Context(), ws)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		out = append(out, profileToDTO(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *BenchmarkHandler) GetProfile(w http.ResponseWriter, r *http.Request) {
	ws, _ := auth.WorkspaceID(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	row, err := h.deps.Profiles.Get(r.Context(), id, ws)
	if errors.Is(err, benchmark.ErrProfileNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, profileToDTO(row))
}

func (h *BenchmarkHandler) DeleteProfile(w http.ResponseWriter, r *http.Request) {
	ws, _ := auth.WorkspaceID(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id", err.Error())
		return
	}
	if err := h.deps.Profiles.Delete(r.Context(), id, ws); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func profileToDTO(p benchmark.Profile) map[string]any {
	dto := map[string]any{
		"id":              p.ID,
		"workspace_id":    p.WorkspaceID,
		"slug":            p.Slug,
		"display_name":    p.DisplayName,
		"agent_id":        p.AgentID,
		"agent_name":      p.AgentName,
		"model":           p.Model,
		"prompt_source":   p.PromptSource,
		"prompt_hash":     p.PromptHash,
		"attached_skills": p.AttachedSkills,
		"captured_by":     p.CapturedBy,
	}
	if p.DuplicateOf != nil {
		dto["duplicate_of"] = p.DuplicateOf.String()
	}
	return dto
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./server/internal/handler/ -run TestBenchmarkHandler -v`
Expected: PASS for all suite + profile cases.

- [ ] **Step 5: Commit**

```bash
git add server/internal/handler/benchmark.go server/internal/handler/benchmark_test.go
git commit -m "feat(benchmark): HTTP handler for /api/benchmarks/profiles"
```

---

## Task 12: Wire benchmark handler into router

**Files:**
- Modify: `server/cmd/server/router.go`
- Modify: `server/cmd/server/main.go` (construct deps)

- [ ] **Step 1: Inspect router structure**

Run: `grep -n 'r.Route("/api/agents"' server/cmd/server/router.go`
Use the surrounding lines as the pattern for adding a new top-level route group.

- [ ] **Step 2: Wire deps in main**

Find the section in `main.go` that constructs handlers (look for `handler.New`) and add:

```go
benchmarkSuites := benchmark.NewSuiteService(queries)
benchmarkProfiles := benchmark.NewProfileService(queries)
benchmarkHandler := handler.NewBenchmarkHandler(handler.BenchmarkDeps{
    Suites: benchmarkSuites, Profiles: benchmarkProfiles,
})
```

Pass `benchmarkHandler` to the router constructor.

- [ ] **Step 3: Add routes**

Inside the existing authenticated route group (the block that contains `/api/issues`, `/api/agents`, etc.), add:

```go
r.Route("/api/benchmarks", func(r chi.Router) {
    r.Route("/suites", func(r chi.Router) {
        r.Get("/", benchmarkHandler.ListSuites)
        r.Post("/", benchmarkHandler.CreateSuite)
        r.Route("/{id}", func(r chi.Router) {
            r.Get("/", benchmarkHandler.GetSuite)
            r.Delete("/", benchmarkHandler.DeleteSuite)
        })
    })
    r.Route("/profiles", func(r chi.Router) {
        r.Get("/", benchmarkHandler.ListProfiles)
        r.Post("/", benchmarkHandler.CaptureProfile)
        r.Route("/{id}", func(r chi.Router) {
            r.Get("/", benchmarkHandler.GetProfile)
            r.Delete("/", benchmarkHandler.DeleteProfile)
        })
    })
})
```

- [ ] **Step 4: Smoke test**

Run server locally and verify:
```bash
make server &
sleep 3
curl -i -H "Authorization: Bearer $TEST_PAT" http://localhost:8080/api/benchmarks/suites
```
Expected: `200` with `{"items":[]}`.

- [ ] **Step 5: Commit**

```bash
git add server/cmd/server/router.go server/cmd/server/main.go
git commit -m "feat(benchmark): mount /api/benchmarks routes"
```

---

## Task 13: Frontend types + API client

**Files:**
- Create: `packages/core/src/benchmarks/types.ts`
- Create: `packages/core/src/benchmarks/api.ts`

- [ ] **Step 1: Write types**

```ts
// packages/core/src/benchmarks/types.ts
export type SkillRef = { slug: string; version: string };

export type BenchmarkSuite = {
  id: string;
  workspace_id: string;
  slug: string;
  display_name: string;
  adapter_kind: string;
  instance_ids: string[];
  description: string;
  created_by: string;
};

export type BenchmarkProfile = {
  id: string;
  workspace_id: string;
  slug: string;
  display_name: string;
  agent_id: string;
  agent_name: string;
  model: string;
  prompt_source: string;
  prompt_hash: string;
  attached_skills: SkillRef[];
  captured_by: string;
  duplicate_of?: string;
};

export type CreateSuiteInput = {
  slug: string;
  display_name: string;
  adapter_kind: string;
  instance_ids: string[];
  description?: string;
};

export type CaptureProfileInput = {
  agent_id: string;
  slug: string;
  display_name: string;
};
```

- [ ] **Step 2: Write API client**

```ts
// packages/core/src/benchmarks/api.ts
import { apiFetch } from "../platform/apiClient";
import type {
  BenchmarkSuite,
  BenchmarkProfile,
  CreateSuiteInput,
  CaptureProfileInput,
} from "./types";

const base = "/api/benchmarks";

export const benchmarksApi = {
  listSuites: () =>
    apiFetch<{ items: BenchmarkSuite[] }>(`${base}/suites`),
  getSuite: (id: string) =>
    apiFetch<BenchmarkSuite>(`${base}/suites/${id}`),
  createSuite: (input: CreateSuiteInput) =>
    apiFetch<BenchmarkSuite>(`${base}/suites`, { method: "POST", body: JSON.stringify(input) }),
  deleteSuite: (id: string) =>
    apiFetch<void>(`${base}/suites/${id}`, { method: "DELETE" }),

  listProfiles: () =>
    apiFetch<{ items: BenchmarkProfile[] }>(`${base}/profiles`),
  getProfile: (id: string) =>
    apiFetch<BenchmarkProfile>(`${base}/profiles/${id}`),
  captureProfile: (input: CaptureProfileInput) =>
    apiFetch<BenchmarkProfile>(`${base}/profiles`, { method: "POST", body: JSON.stringify(input) }),
  deleteProfile: (id: string) =>
    apiFetch<void>(`${base}/profiles/${id}`, { method: "DELETE" }),
};
```

**Note:** `apiFetch` is the existing client used by other features. If the helper has a different name (e.g. `httpClient`, `fetcher`), grep for it under `packages/core/src/platform/` and use that. Do not create a new client.

- [ ] **Step 3: Build the package**

Run: `pnpm -F @multica/core typecheck`
Expected: 0 errors.

- [ ] **Step 4: Commit**

```bash
git add packages/core/src/benchmarks/
git commit -m "feat(benchmark): frontend types and API client"
```

---

## Task 14: TanStack Query hooks + Zustand store

**Files:**
- Create: `packages/core/src/benchmarks/queries.ts`
- Create: `packages/core/src/benchmarks/store.ts`

- [ ] **Step 1: Write the hooks**

```ts
// packages/core/src/benchmarks/queries.ts
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { benchmarksApi } from "./api";
import type { CreateSuiteInput, CaptureProfileInput } from "./types";

const k = {
  suites: (wsId: string) => ["benchmarks", "suites", wsId] as const,
  suite: (wsId: string, id: string) => ["benchmarks", "suite", wsId, id] as const,
  profiles: (wsId: string) => ["benchmarks", "profiles", wsId] as const,
  profile: (wsId: string, id: string) => ["benchmarks", "profile", wsId, id] as const,
};

export function useBenchmarkSuites(wsId: string) {
  return useQuery({
    queryKey: k.suites(wsId),
    queryFn: () => benchmarksApi.listSuites().then(r => r.items),
  });
}

export function useBenchmarkSuite(wsId: string, id: string | undefined) {
  return useQuery({
    queryKey: id ? k.suite(wsId, id) : ["benchmarks", "suite", "noop"],
    queryFn: () => benchmarksApi.getSuite(id!),
    enabled: !!id,
  });
}

export function useCreateSuite(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateSuiteInput) => benchmarksApi.createSuite(input),
    onSuccess: () => qc.invalidateQueries({ queryKey: k.suites(wsId) }),
  });
}

export function useDeleteSuite(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => benchmarksApi.deleteSuite(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: k.suites(wsId) }),
  });
}

export function useBenchmarkProfiles(wsId: string) {
  return useQuery({
    queryKey: k.profiles(wsId),
    queryFn: () => benchmarksApi.listProfiles().then(r => r.items),
  });
}

export function useBenchmarkProfile(wsId: string, id: string | undefined) {
  return useQuery({
    queryKey: id ? k.profile(wsId, id) : ["benchmarks", "profile", "noop"],
    queryFn: () => benchmarksApi.getProfile(id!),
    enabled: !!id,
  });
}

export function useCaptureProfile(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CaptureProfileInput) => benchmarksApi.captureProfile(input),
    onSuccess: () => qc.invalidateQueries({ queryKey: k.profiles(wsId) }),
  });
}

export function useDeleteProfile(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => benchmarksApi.deleteProfile(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: k.profiles(wsId) }),
  });
}
```

- [ ] **Step 2: Write the Zustand store (UI state only)**

```ts
// packages/core/src/benchmarks/store.ts
import { create } from "zustand";

type State = {
  suiteFilter: string;
  profileFilter: string;
  setSuiteFilter: (s: string) => void;
  setProfileFilter: (s: string) => void;
};

export const useBenchmarksUI = create<State>((set) => ({
  suiteFilter: "",
  profileFilter: "",
  setSuiteFilter: (s) => set({ suiteFilter: s }),
  setProfileFilter: (s) => set({ profileFilter: s }),
}));
```

- [ ] **Step 3: Typecheck**

Run: `pnpm -F @multica/core typecheck`
Expected: 0 errors.

- [ ] **Step 4: Commit**

```bash
git add packages/core/src/benchmarks/
git commit -m "feat(benchmark): TanStack Query hooks and UI store"
```

---

## Task 15: Sub-nav + dashboard layout

**Files:**
- Modify: `packages/views/src/sidebar/sidebar-items.tsx` (or wherever sidebar items are configured)
- Create: `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/layout.tsx`

- [ ] **Step 1: Find the sidebar definition**

Run: `grep -rn "Issues\|Skills\|Agents" packages/views/src/sidebar/ apps/web/app/ | grep -i "label\|href" | head`
Expected: a single source of truth for sidebar items.

- [ ] **Step 2: Add Benchmarks entry**

In that file, add after the "Skills" entry (or wherever fits the existing order):

```tsx
{
  key: "benchmarks",
  label: t("sidebar.benchmarks"),
  href: `/${workspaceSlug}/benchmarks/runs`, // routes to runs in phase 1; in phase 0 redirect to /suites
  icon: BeakerIcon,
},
```

For Phase 0, since `/runs` does not exist yet, create the layout.tsx so it redirects to `/suites` if landed on `/benchmarks` directly.

- [ ] **Step 3: Write the layout**

```tsx
// apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/layout.tsx
import Link from "next/link";
import { redirect } from "next/navigation";

export default function BenchmarksLayout({
  children,
  params,
}: {
  children: React.ReactNode;
  params: Promise<{ workspaceSlug: string }>;
}) {
  return (
    <div className="flex flex-col gap-4">
      <SubNav workspaceSlug={params.then(p => p.workspaceSlug)} />
      {children}
    </div>
  );
}

function SubNav({ workspaceSlug }: { workspaceSlug: Promise<string> }) {
  // Resolve via use() in a child client component or pre-resolve in a server component;
  // follow the pattern used by /issues/layout.tsx in this codebase.
  return null; // placeholder — see step 4
}
```

The exact server-component pattern depends on how `[workspaceSlug]/(dashboard)/issues/layout.tsx` is structured today. Open that file and mirror its sub-nav pattern: the only diff is the four tab entries (Runs, Suites, Profiles, Leaderboard) and the active-route highlight.

- [ ] **Step 4: Implement SubNav by mirroring an existing dashboard sub-nav**

Open `apps/web/app/[workspaceSlug]/(dashboard)/issues/layout.tsx` (or the closest analogue) and copy the sub-nav structure. Replace tab entries with:

```tsx
const tabs = [
  { key: "runs", label: t("benchmarks.tabs.runs"), href: "runs" },
  { key: "suites", label: t("benchmarks.tabs.suites"), href: "suites" },
  { key: "profiles", label: t("benchmarks.tabs.profiles"), href: "profiles" },
  { key: "leaderboard", label: t("benchmarks.tabs.leaderboard"), href: "leaderboard" },
];
```

Phase 0 only ships `suites` and `profiles` pages. Render `runs` and `leaderboard` tabs as **disabled** with a tooltip "Available after Phase 1" — this signals roadmap to operators without dead links. Disable via `aria-disabled="true"` and a `cursor-not-allowed opacity-50` class.

- [ ] **Step 5: Add a `/benchmarks` index that redirects to `/suites`**

```tsx
// apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/page.tsx
import { redirect } from "next/navigation";
export default async function BenchmarksIndex({
  params,
}: { params: Promise<{ workspaceSlug: string }> }) {
  const { workspaceSlug } = await params;
  redirect(`/${workspaceSlug}/benchmarks/suites`);
}
```

- [ ] **Step 6: Typecheck and run dev**

Run: `pnpm typecheck && pnpm dev:web` — open the workspace, click Benchmarks in sidebar, see the sub-nav with two enabled and two disabled tabs.

- [ ] **Step 7: Commit**

```bash
git add packages/views/src/sidebar/ apps/web/app/[workspaceSlug]/\(dashboard\)/benchmarks/
git commit -m "feat(benchmark): sidebar entry and dashboard sub-nav"
```

---

## Task 16: Suite list view

**Files:**
- Create: `packages/views/src/benchmarks/SuitesList.tsx`
- Create: `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/suites/page.tsx`

- [ ] **Step 1: Write the list view**

```tsx
// packages/views/src/benchmarks/SuitesList.tsx
import { useBenchmarkSuites, useDeleteSuite } from "@multica/core/benchmarks/queries";
import { useBenchmarksUI } from "@multica/core/benchmarks/store";
import { useTranslation } from "@multica/views/i18n";
import type { BenchmarkSuite } from "@multica/core/benchmarks/types";

export function SuitesList({ wsId, onCreate, onOpen }: {
  wsId: string;
  onCreate: () => void;
  onOpen: (id: string) => void;
}) {
  const { t } = useTranslation();
  const { data: suites = [], isLoading } = useBenchmarkSuites(wsId);
  const del = useDeleteSuite(wsId);
  const { suiteFilter, setSuiteFilter } = useBenchmarksUI();

  const filtered = suites.filter((s: BenchmarkSuite) =>
    s.slug.includes(suiteFilter) || s.display_name.toLowerCase().includes(suiteFilter.toLowerCase()),
  );

  if (isLoading) return <div>{t("common.loading")}</div>;

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <input
          className="input"
          placeholder={t("benchmarks.suites.filter_placeholder")}
          value={suiteFilter}
          onChange={(e) => setSuiteFilter(e.target.value)}
        />
        <button className="btn-primary" onClick={onCreate}>
          {t("benchmarks.suites.create_cta")}
        </button>
      </div>
      <table className="table">
        <thead>
          <tr>
            <th>{t("benchmarks.suites.col_slug")}</th>
            <th>{t("benchmarks.suites.col_display_name")}</th>
            <th>{t("benchmarks.suites.col_adapter")}</th>
            <th>{t("benchmarks.suites.col_instance_count")}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {filtered.map((s) => (
            <tr key={s.id} className="cursor-pointer" onClick={() => onOpen(s.id)}>
              <td>{s.slug}</td>
              <td>{s.display_name}</td>
              <td>{s.adapter_kind}</td>
              <td>{s.instance_ids.length}</td>
              <td>
                <button
                  className="btn-danger"
                  onClick={(e) => { e.stopPropagation(); del.mutate(s.id); }}
                  aria-label={t("benchmarks.suites.delete")}
                >×</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {filtered.length === 0 && <div>{t("benchmarks.suites.empty")}</div>}
    </div>
  );
}
```

- [ ] **Step 2: Wire route**

```tsx
// apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/suites/page.tsx
"use client";
import { useRouter } from "next/navigation";
import { use } from "react";
import { SuitesList } from "@multica/views/benchmarks/SuitesList";
import { useWorkspaceId } from "@multica/core/platform/workspace";

export default function SuitesPage({ params }: { params: Promise<{ workspaceSlug: string }> }) {
  const { workspaceSlug } = use(params);
  const wsId = useWorkspaceId();
  const router = useRouter();
  return (
    <SuitesList
      wsId={wsId}
      onCreate={() => router.push(`/${workspaceSlug}/benchmarks/suites/new`)}
      onOpen={(id) => router.push(`/${workspaceSlug}/benchmarks/suites/${id}`)}
    />
  );
}
```

- [ ] **Step 3: Run dev and click through**

Run: `pnpm dev:web`. Open `/<ws>/benchmarks/suites`. Empty state shows "No suites yet" (i18n key in Task 22).

- [ ] **Step 4: Commit**

```bash
git add packages/views/src/benchmarks/SuitesList.tsx apps/web/app/[workspaceSlug]/\(dashboard\)/benchmarks/suites/page.tsx
git commit -m "feat(benchmark): suites list view"
```

---

## Task 17: Suite create view

**Files:**
- Create: `packages/views/src/benchmarks/SuiteCreate.tsx`
- Create: `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/suites/new/page.tsx`

- [ ] **Step 1: Write the create view**

```tsx
// packages/views/src/benchmarks/SuiteCreate.tsx
import { useState } from "react";
import { useCreateSuite } from "@multica/core/benchmarks/queries";
import { useTranslation } from "@multica/views/i18n";

export function SuiteCreate({ wsId, onCreated, onCancel }: {
  wsId: string;
  onCreated: (id: string) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const [slug, setSlug] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [instanceText, setInstanceText] = useState(""); // newline-separated
  const [description, setDescription] = useState("");
  const create = useCreateSuite(wsId);

  const submit = async () => {
    const ids = instanceText.split(/\r?\n/).map(s => s.trim()).filter(Boolean);
    const out = await create.mutateAsync({
      slug, display_name: displayName, adapter_kind: "programbench",
      instance_ids: ids, description,
    });
    onCreated(out.id);
  };

  return (
    <form className="flex flex-col gap-3 max-w-2xl"
      onSubmit={(e) => { e.preventDefault(); submit(); }}>
      <label>
        <span>{t("benchmarks.suites.create_slug")}</span>
        <input className="input" required value={slug}
          onChange={(e) => setSlug(e.target.value)} />
      </label>
      <label>
        <span>{t("benchmarks.suites.create_display_name")}</span>
        <input className="input" required value={displayName}
          onChange={(e) => setDisplayName(e.target.value)} />
      </label>
      <label>
        <span>{t("benchmarks.suites.create_instance_ids")}</span>
        <textarea className="input min-h-[10rem]" required value={instanceText}
          placeholder="abishekvashok__cmatrix.5c082c6"
          onChange={(e) => setInstanceText(e.target.value)} />
        <small className="text-muted">{t("benchmarks.suites.create_instance_ids_hint")}</small>
      </label>
      <label>
        <span>{t("benchmarks.suites.create_description")}</span>
        <textarea className="input" value={description}
          onChange={(e) => setDescription(e.target.value)} />
      </label>
      {create.isError && (
        <div role="alert" className="text-error">
          {(create.error as Error).message}
        </div>
      )}
      <div className="flex gap-2">
        <button type="submit" className="btn-primary" disabled={create.isPending}>
          {t("benchmarks.suites.create_submit")}
        </button>
        <button type="button" className="btn-secondary" onClick={onCancel}>
          {t("common.cancel")}
        </button>
      </div>
    </form>
  );
}
```

- [ ] **Step 2: Wire route**

```tsx
// apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/suites/new/page.tsx
"use client";
import { useRouter } from "next/navigation";
import { use } from "react";
import { SuiteCreate } from "@multica/views/benchmarks/SuiteCreate";
import { useWorkspaceId } from "@multica/core/platform/workspace";

export default function NewSuitePage({ params }: { params: Promise<{ workspaceSlug: string }> }) {
  const { workspaceSlug } = use(params);
  const wsId = useWorkspaceId();
  const router = useRouter();
  return (
    <SuiteCreate wsId={wsId}
      onCreated={(id) => router.push(`/${workspaceSlug}/benchmarks/suites/${id}`)}
      onCancel={() => router.push(`/${workspaceSlug}/benchmarks/suites`)}
    />
  );
}
```

- [ ] **Step 3: Manual test**

Run: `pnpm dev:web`. Create a suite with one instance id; verify list view shows it after redirect.

- [ ] **Step 4: Commit**

```bash
git add packages/views/src/benchmarks/SuiteCreate.tsx apps/web/app/[workspaceSlug]/\(dashboard\)/benchmarks/suites/new/page.tsx
git commit -m "feat(benchmark): suite create view"
```

---

## Task 18: Suite detail view

**Files:**
- Create: `packages/views/src/benchmarks/SuiteDetail.tsx`
- Create: `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/suites/[suiteId]/page.tsx`

- [ ] **Step 1: Write the detail view**

```tsx
// packages/views/src/benchmarks/SuiteDetail.tsx
import { useBenchmarkSuite } from "@multica/core/benchmarks/queries";
import { useTranslation } from "@multica/views/i18n";

export function SuiteDetail({ wsId, suiteId, onBack }: {
  wsId: string;
  suiteId: string;
  onBack: () => void;
}) {
  const { t } = useTranslation();
  const { data, isLoading, error } = useBenchmarkSuite(wsId, suiteId);

  if (isLoading) return <div>{t("common.loading")}</div>;
  if (error || !data) return <div role="alert">{(error as Error)?.message ?? t("common.not_found")}</div>;

  return (
    <div className="flex flex-col gap-4">
      <button className="btn-link" onClick={onBack}>← {t("common.back")}</button>
      <header>
        <h2>{data.display_name}</h2>
        <code>{data.slug}</code>
        <span className="badge">{data.adapter_kind}</span>
      </header>
      {data.description && <p>{data.description}</p>}
      <section>
        <h3>{t("benchmarks.suites.detail_instances")} ({data.instance_ids.length})</h3>
        <ul className="list-disc pl-6">
          {data.instance_ids.map((id) => <li key={id}><code>{id}</code></li>)}
        </ul>
      </section>
      <p className="text-muted">{t("benchmarks.suites.detail_immutable_note")}</p>
    </div>
  );
}
```

The "immutable note" is the spec's resolved decision: once any run uses the suite, the UI shows "Duplicate" instead of edit. In Phase 0 we don't have runs yet, but the note is correct preview text — it just always says "you can edit until you run this". Phase 1 will add a "has runs" check.

- [ ] **Step 2: Wire route**

```tsx
// apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/suites/[suiteId]/page.tsx
"use client";
import { useRouter } from "next/navigation";
import { use } from "react";
import { SuiteDetail } from "@multica/views/benchmarks/SuiteDetail";
import { useWorkspaceId } from "@multica/core/platform/workspace";

export default function SuiteDetailPage({ params }: {
  params: Promise<{ workspaceSlug: string; suiteId: string }>;
}) {
  const { workspaceSlug, suiteId } = use(params);
  const wsId = useWorkspaceId();
  const router = useRouter();
  return (
    <SuiteDetail wsId={wsId} suiteId={suiteId}
      onBack={() => router.push(`/${workspaceSlug}/benchmarks/suites`)}
    />
  );
}
```

- [ ] **Step 3: Manual test, commit**

```bash
pnpm dev:web   # click into a suite; see slug + instance list
git add packages/views/src/benchmarks/SuiteDetail.tsx apps/web/app/[workspaceSlug]/\(dashboard\)/benchmarks/suites/\[suiteId\]/page.tsx
git commit -m "feat(benchmark): suite detail view"
```

---

## Task 19: Profile list view

**Files:**
- Create: `packages/views/src/benchmarks/ProfilesList.tsx`
- Create: `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/profiles/page.tsx`

- [ ] **Step 1: Write the list view**

```tsx
// packages/views/src/benchmarks/ProfilesList.tsx
import { useBenchmarkProfiles, useDeleteProfile } from "@multica/core/benchmarks/queries";
import { useBenchmarksUI } from "@multica/core/benchmarks/store";
import { useTranslation } from "@multica/views/i18n";

export function ProfilesList({ wsId, onCreate, onOpen }: {
  wsId: string;
  onCreate: () => void;
  onOpen: (id: string) => void;
}) {
  const { t } = useTranslation();
  const { data: profiles = [], isLoading } = useBenchmarkProfiles(wsId);
  const del = useDeleteProfile(wsId);
  const { profileFilter, setProfileFilter } = useBenchmarksUI();

  const filtered = profiles.filter((p) =>
    p.slug.includes(profileFilter) ||
    p.display_name.toLowerCase().includes(profileFilter.toLowerCase()) ||
    p.agent_name.toLowerCase().includes(profileFilter.toLowerCase()),
  );

  if (isLoading) return <div>{t("common.loading")}</div>;

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <input className="input"
          placeholder={t("benchmarks.profiles.filter_placeholder")}
          value={profileFilter}
          onChange={(e) => setProfileFilter(e.target.value)} />
        <button className="btn-primary" onClick={onCreate}>
          {t("benchmarks.profiles.capture_cta")}
        </button>
      </div>
      <table className="table">
        <thead>
          <tr>
            <th>{t("benchmarks.profiles.col_slug")}</th>
            <th>{t("benchmarks.profiles.col_display_name")}</th>
            <th>{t("benchmarks.profiles.col_agent")}</th>
            <th>{t("benchmarks.profiles.col_model")}</th>
            <th>{t("benchmarks.profiles.col_hash")}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {filtered.map((p) => (
            <tr key={p.id} className="cursor-pointer" onClick={() => onOpen(p.id)}>
              <td>{p.slug}</td>
              <td>{p.display_name}</td>
              <td>{p.agent_name}</td>
              <td>{p.model}</td>
              <td><code>{p.prompt_hash.slice(0, 8)}</code></td>
              <td>
                <button className="btn-danger"
                  onClick={(e) => { e.stopPropagation(); del.mutate(p.id); }}
                  aria-label={t("benchmarks.profiles.delete")}
                >×</button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {filtered.length === 0 && <div>{t("benchmarks.profiles.empty")}</div>}
    </div>
  );
}
```

- [ ] **Step 2: Wire route**

```tsx
// apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/profiles/page.tsx
"use client";
import { useRouter } from "next/navigation";
import { use } from "react";
import { ProfilesList } from "@multica/views/benchmarks/ProfilesList";
import { useWorkspaceId } from "@multica/core/platform/workspace";

export default function ProfilesPage({ params }: { params: Promise<{ workspaceSlug: string }> }) {
  const { workspaceSlug } = use(params);
  const wsId = useWorkspaceId();
  const router = useRouter();
  return (
    <ProfilesList wsId={wsId}
      onCreate={() => router.push(`/${workspaceSlug}/benchmarks/profiles/new`)}
      onOpen={(id) => router.push(`/${workspaceSlug}/benchmarks/profiles/${id}`)}
    />
  );
}
```

- [ ] **Step 3: Commit**

```bash
git add packages/views/src/benchmarks/ProfilesList.tsx apps/web/app/[workspaceSlug]/\(dashboard\)/benchmarks/profiles/page.tsx
git commit -m "feat(benchmark): profiles list view"
```

---

## Task 20: Profile capture view

**Files:**
- Create: `packages/views/src/benchmarks/ProfileCapture.tsx`
- Create: `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/profiles/new/page.tsx`

- [ ] **Step 1: Write the capture form**

```tsx
// packages/views/src/benchmarks/ProfileCapture.tsx
import { useState } from "react";
import { useCaptureProfile } from "@multica/core/benchmarks/queries";
import { useAgents } from "@multica/core/agents/queries"; // existing hook in this codebase
import { useTranslation } from "@multica/views/i18n";

export function ProfileCapture({ wsId, onCaptured, onCancel }: {
  wsId: string;
  onCaptured: (id: string) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const { data: agents = [] } = useAgents(wsId);
  const [agentId, setAgentId] = useState("");
  const [slug, setSlug] = useState("");
  const [displayName, setDisplayName] = useState("");
  const capture = useCaptureProfile(wsId);
  const [duplicateOf, setDuplicateOf] = useState<string | null>(null);

  const submit = async () => {
    const out = await capture.mutateAsync({ agent_id: agentId, slug, display_name: displayName });
    if (out.duplicate_of) {
      setDuplicateOf(out.duplicate_of);
      // Profile was still created (server treats hash collisions as a soft warning).
      // Caller can decide: navigate to detail or stay and show a warning.
    }
    onCaptured(out.id);
  };

  return (
    <form className="flex flex-col gap-3 max-w-2xl"
      onSubmit={(e) => { e.preventDefault(); submit(); }}>
      <label>
        <span>{t("benchmarks.profiles.capture_agent")}</span>
        <select className="input" required value={agentId}
          onChange={(e) => setAgentId(e.target.value)}>
          <option value="">{t("benchmarks.profiles.capture_agent_placeholder")}</option>
          {agents.map((a) => (
            <option key={a.id} value={a.id}>{a.name}</option>
          ))}
        </select>
      </label>
      <label>
        <span>{t("benchmarks.profiles.capture_slug")}</span>
        <input className="input" required value={slug}
          onChange={(e) => setSlug(e.target.value)} />
      </label>
      <label>
        <span>{t("benchmarks.profiles.capture_display_name")}</span>
        <input className="input" required value={displayName}
          onChange={(e) => setDisplayName(e.target.value)} />
      </label>
      {duplicateOf && (
        <div role="status" className="text-warning">
          {t("benchmarks.profiles.duplicate_warning", { id: duplicateOf })}
        </div>
      )}
      {capture.isError && (
        <div role="alert" className="text-error">
          {(capture.error as Error).message}
        </div>
      )}
      <div className="flex gap-2">
        <button type="submit" className="btn-primary" disabled={capture.isPending}>
          {t("benchmarks.profiles.capture_submit")}
        </button>
        <button type="button" className="btn-secondary" onClick={onCancel}>
          {t("common.cancel")}
        </button>
      </div>
    </form>
  );
}
```

**Note:** `useAgents` is the existing TanStack hook — confirm exact import path before writing. If the hook signature is different, adapt the agent selector.

- [ ] **Step 2: Wire route**

```tsx
// apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/profiles/new/page.tsx
"use client";
import { useRouter } from "next/navigation";
import { use } from "react";
import { ProfileCapture } from "@multica/views/benchmarks/ProfileCapture";
import { useWorkspaceId } from "@multica/core/platform/workspace";

export default function NewProfilePage({ params }: { params: Promise<{ workspaceSlug: string }> }) {
  const { workspaceSlug } = use(params);
  const wsId = useWorkspaceId();
  const router = useRouter();
  return (
    <ProfileCapture wsId={wsId}
      onCaptured={(id) => router.push(`/${workspaceSlug}/benchmarks/profiles/${id}`)}
      onCancel={() => router.push(`/${workspaceSlug}/benchmarks/profiles`)}
    />
  );
}
```

- [ ] **Step 3: Manual test, commit**

```bash
pnpm dev:web  # capture a profile; verify list shows it; capture same agent again; verify duplicate warning
git add packages/views/src/benchmarks/ProfileCapture.tsx apps/web/app/[workspaceSlug]/\(dashboard\)/benchmarks/profiles/new/page.tsx
git commit -m "feat(benchmark): profile capture view"
```

---

## Task 21: Profile detail view

**Files:**
- Create: `packages/views/src/benchmarks/ProfileDetail.tsx`
- Create: `apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/profiles/[profileId]/page.tsx`

- [ ] **Step 1: Write the detail view**

```tsx
// packages/views/src/benchmarks/ProfileDetail.tsx
import { useState } from "react";
import { useBenchmarkProfile } from "@multica/core/benchmarks/queries";
import { useTranslation } from "@multica/views/i18n";

export function ProfileDetail({ wsId, profileId, onBack }: {
  wsId: string; profileId: string; onBack: () => void;
}) {
  const { t } = useTranslation();
  const { data, isLoading, error } = useBenchmarkProfile(wsId, profileId);
  const [showPrompt, setShowPrompt] = useState(false);

  if (isLoading) return <div>{t("common.loading")}</div>;
  if (error || !data) return <div role="alert">{(error as Error)?.message ?? t("common.not_found")}</div>;

  return (
    <div className="flex flex-col gap-4">
      <button className="btn-link" onClick={onBack}>← {t("common.back")}</button>
      <header>
        <h2>{data.display_name}</h2>
        <code>{data.slug}</code>
        <div className="flex gap-2 text-sm text-muted">
          <span>{data.agent_name}</span>
          <span>·</span>
          <span>{data.model}</span>
          <span>·</span>
          <code title={data.prompt_hash}>{data.prompt_hash.slice(0, 12)}…</code>
        </div>
      </header>
      <section>
        <h3>{t("benchmarks.profiles.detail_skills")}</h3>
        {data.attached_skills.length === 0 ? (
          <p className="text-muted">{t("benchmarks.profiles.detail_no_skills")}</p>
        ) : (
          <ul className="list-disc pl-6">
            {data.attached_skills.map((s) => (
              <li key={`${s.slug}@${s.version}`}><code>{s.slug}@{s.version}</code></li>
            ))}
          </ul>
        )}
      </section>
      <section>
        <button className="btn-link" onClick={() => setShowPrompt((v) => !v)}>
          {showPrompt ? t("benchmarks.profiles.detail_hide_prompt") : t("benchmarks.profiles.detail_show_prompt")}
        </button>
        {showPrompt && <pre className="bg-muted p-3 overflow-auto">{data.prompt_source}</pre>}
      </section>
    </div>
  );
}
```

- [ ] **Step 2: Wire route**

```tsx
// apps/web/app/[workspaceSlug]/(dashboard)/benchmarks/profiles/[profileId]/page.tsx
"use client";
import { useRouter } from "next/navigation";
import { use } from "react";
import { ProfileDetail } from "@multica/views/benchmarks/ProfileDetail";
import { useWorkspaceId } from "@multica/core/platform/workspace";

export default function ProfileDetailPage({ params }: {
  params: Promise<{ workspaceSlug: string; profileId: string }>;
}) {
  const { workspaceSlug, profileId } = use(params);
  const wsId = useWorkspaceId();
  const router = useRouter();
  return (
    <ProfileDetail wsId={wsId} profileId={profileId}
      onBack={() => router.push(`/${workspaceSlug}/benchmarks/profiles`)}
    />
  );
}
```

- [ ] **Step 3: Manual test, commit**

```bash
pnpm dev:web   # open a captured profile; toggle prompt; verify skill list
git add packages/views/src/benchmarks/ProfileDetail.tsx apps/web/app/[workspaceSlug]/\(dashboard\)/benchmarks/profiles/\[profileId\]/page.tsx
git commit -m "feat(benchmark): profile detail view"
```

---

## Task 22: i18n keys (en + zh-Hans)

**Files:**
- Create: `packages/views/locales/en/benchmarks.json`
- Create: `packages/views/locales/zh-Hans/benchmarks.json`

The exact filename convention (`zh-Hans` vs `zh-CN`) varies; check existing locale files under `packages/views/locales/` and match.

- [ ] **Step 1: Write English keys**

```json
{
  "sidebar": { "benchmarks": "Benchmarks" },
  "tabs": {
    "runs": "Runs",
    "suites": "Suites",
    "profiles": "Profiles",
    "leaderboard": "Leaderboard"
  },
  "suites": {
    "create_cta": "Create suite",
    "filter_placeholder": "Filter suites…",
    "col_slug": "Slug",
    "col_display_name": "Name",
    "col_adapter": "Adapter",
    "col_instance_count": "Tasks",
    "delete": "Delete suite",
    "empty": "No suites yet. Create one to define a stable benchmark task set.",
    "create_slug": "Slug (workspace-unique, e.g. smoke-cli-v1)",
    "create_display_name": "Display name",
    "create_instance_ids": "Instance ids",
    "create_instance_ids_hint": "One adapter-defined instance id per line.",
    "create_description": "Description (optional)",
    "create_submit": "Create",
    "detail_instances": "Instances",
    "detail_immutable_note": "Suites become immutable once a run uses them. Edit freely until then."
  },
  "profiles": {
    "capture_cta": "Capture profile",
    "filter_placeholder": "Filter profiles…",
    "col_slug": "Slug",
    "col_display_name": "Name",
    "col_agent": "Agent",
    "col_model": "Model",
    "col_hash": "Hash",
    "delete": "Delete profile",
    "empty": "No profiles yet. Capture one to snapshot an agent's prompt and skills before a run.",
    "capture_agent": "Agent",
    "capture_agent_placeholder": "Select an agent…",
    "capture_slug": "Slug (workspace-unique, e.g. current-v1)",
    "capture_display_name": "Display name",
    "capture_submit": "Capture",
    "duplicate_warning": "An identical profile already exists ({{id}}). This new profile is still saved.",
    "detail_skills": "Attached skills",
    "detail_no_skills": "No attached skills.",
    "detail_show_prompt": "Show prompt source",
    "detail_hide_prompt": "Hide prompt source"
  }
}
```

- [ ] **Step 2: Write Chinese keys**

Use the conventions doc at `apps/docs/content/docs/developers/conventions.zh.mdx` for Chinese voice and term choices. If a term is in the glossary there, use the glossary form; otherwise, follow the established translation style of nearby existing keys.

```json
{
  "sidebar": { "benchmarks": "基准测试" },
  "tabs": {
    "runs": "运行",
    "suites": "套件",
    "profiles": "档案",
    "leaderboard": "排行榜"
  },
  "suites": {
    "create_cta": "创建套件",
    "filter_placeholder": "筛选套件…",
    "col_slug": "标识",
    "col_display_name": "名称",
    "col_adapter": "适配器",
    "col_instance_count": "任务数",
    "delete": "删除套件",
    "empty": "暂无套件。创建一个来固定一组基准任务。",
    "create_slug": "标识（工作区唯一，如 smoke-cli-v1）",
    "create_display_name": "显示名称",
    "create_instance_ids": "实例 ID",
    "create_instance_ids_hint": "每行一个适配器定义的实例 ID。",
    "create_description": "描述（可选）",
    "create_submit": "创建",
    "detail_instances": "实例",
    "detail_immutable_note": "套件被某次运行使用后将变为不可修改。在此之前可自由编辑。"
  },
  "profiles": {
    "capture_cta": "捕获档案",
    "filter_placeholder": "筛选档案…",
    "col_slug": "标识",
    "col_display_name": "名称",
    "col_agent": "智能体",
    "col_model": "模型",
    "col_hash": "哈希",
    "delete": "删除档案",
    "empty": "暂无档案。捕获一个以在运行前固化智能体的提示词与技能。",
    "capture_agent": "智能体",
    "capture_agent_placeholder": "选择智能体…",
    "capture_slug": "标识（工作区唯一，如 current-v1）",
    "capture_display_name": "显示名称",
    "capture_submit": "捕获",
    "duplicate_warning": "已存在相同的档案（{{id}}）。新档案仍已保存。",
    "detail_skills": "附加技能",
    "detail_no_skills": "未附加技能。",
    "detail_show_prompt": "显示提示词内容",
    "detail_hide_prompt": "隐藏提示词内容"
  }
}
```

- [ ] **Step 3: Verify glossary alignment**

Run: `grep -i 'benchmark\|套件\|档案\|排行榜' apps/docs/content/docs/developers/conventions.zh.mdx`
If the glossary disagrees with the words above, adjust to glossary terms.

- [ ] **Step 4: Commit**

```bash
git add packages/views/locales/
git commit -m "feat(benchmark): i18n keys (en + zh)"
```

---

## Task 23: Playwright E2E smoke

**Files:**
- Create: `e2e/benchmarks-foundations.spec.ts`

- [ ] **Step 1: Write the spec**

```ts
// e2e/benchmarks-foundations.spec.ts
import { test, expect } from "@playwright/test";

test.describe("benchmark phase 0 foundations", () => {
  test("create suite, capture profile, navigate detail views", async ({ page }) => {
    await page.goto("/test-workspace/benchmarks");
    await expect(page).toHaveURL(/\/benchmarks\/suites$/);

    // Create suite
    await page.getByRole("button", { name: /create suite/i }).click();
    await page.getByLabel(/slug/i).fill("smoke-cli-v1");
    await page.getByLabel(/display name/i).fill("Smoke CLI v1");
    await page.getByLabel(/instance ids/i).fill("abishekvashok__cmatrix.5c082c6\npsampaz__go-mod-outdated.bb79367");
    await page.getByRole("button", { name: /^create$/i }).click();

    // Land on suite detail
    await expect(page.getByRole("heading", { name: "Smoke CLI v1" })).toBeVisible();
    await expect(page.getByText("abishekvashok__cmatrix.5c082c6")).toBeVisible();

    // Back to list
    await page.getByRole("button", { name: /back/i }).click();
    await expect(page.getByText("smoke-cli-v1")).toBeVisible();

    // Capture profile (depends on a pre-seeded ProgramBenchRunner agent in the e2e fixture)
    await page.getByRole("link", { name: /profiles/i }).click();
    await page.getByRole("button", { name: /capture profile/i }).click();
    await page.selectOption("select", { label: "ProgramBenchRunner" });
    await page.getByLabel(/slug/i).fill("current-v1");
    await page.getByLabel(/display name/i).fill("Current v1");
    await page.getByRole("button", { name: /^capture$/i }).click();

    // Land on profile detail
    await expect(page.getByRole("heading", { name: "Current v1" })).toBeVisible();
    await page.getByRole("button", { name: /show prompt source/i }).click();
    await expect(page.locator("pre")).toBeVisible();
  });

  test("duplicate suite slug returns 409 and displays error", async ({ page }) => {
    await page.goto("/test-workspace/benchmarks/suites");

    const create = async (slug: string) => {
      await page.getByRole("button", { name: /create suite/i }).click();
      await page.getByLabel(/slug/i).fill(slug);
      await page.getByLabel(/display name/i).fill(slug);
      await page.getByLabel(/instance ids/i).fill("x__y.abc");
      await page.getByRole("button", { name: /^create$/i }).click();
    };

    await create("dup-test");
    // Already on detail; go back and try again
    await page.getByRole("button", { name: /back/i }).click();
    await create("dup-test");
    await expect(page.getByRole("alert")).toContainText(/slug/i);
  });
});
```

- [ ] **Step 2: Run**

Run: `pnpm playwright test benchmarks-foundations.spec`
Expected: both cases pass against a freshly migrated dev DB with a seeded `ProgramBenchRunner` agent. If the e2e fixture does not seed an agent, add seeding to the fixture file referenced by `playwright.config.ts`.

- [ ] **Step 3: Commit**

```bash
git add e2e/benchmarks-foundations.spec.ts
git commit -m "test(benchmark): playwright smoke for phase 0 foundations"
```

---

## Task 24: Final pre-PR check

**Files:** none (verification only)

- [ ] **Step 1: Full backend tests**

Run: `make test`
Expected: all green, including the three new test files (`profile_hash_test.go`, `suite_service_test.go`, `profile_service_test.go`, `benchmark_test.go`).

- [ ] **Step 2: Full typecheck and lint**

Run: `pnpm typecheck && pnpm lint`
Expected: 0 errors, 0 new warnings beyond baseline.

- [ ] **Step 3: Migration roundtrip**

Run:
```bash
make migrate-down N=1
make migrate-up
```
Expected: down then up succeeds; running with no pending migrations is a no-op.

- [ ] **Step 4: Manual UI walkthrough**

Run: `pnpm dev:web` and verify:
1. Sidebar Benchmarks entry visible.
2. `/benchmarks` redirects to `/benchmarks/suites`.
3. `Runs` and `Leaderboard` tabs are visible but disabled with tooltip.
4. Create a suite with two instance ids → land on detail → instance list shows two ids.
5. Capture a profile → land on detail → prompt source toggle works.
6. Capture the same agent under a different slug → duplicate warning appears, profile is still saved.
7. Delete a suite → list updates without page reload (TanStack invalidation).

- [ ] **Step 5: Review the pre-PR checklist**

Confirm:
- No TODO/TBD/FIXME left in new files: `grep -RnE 'TODO|TBD|FIXME' server/internal/service/benchmark/ server/internal/handler/benchmark.go packages/core/src/benchmarks/ packages/views/src/benchmarks/`
- All new files end with newline.
- Commit messages follow `feat(benchmark): …` / `test(benchmark): …` convention.

- [ ] **Step 6: Open the PR**

```bash
gh pr create --title "feat(benchmark): phase 0 — suites, profiles, foundations" \
  --body "$(cat <<'EOF'
## Summary
- DB schema for suites, profiles, runs, tasks, eval jobs/results, summaries, and evaluator tokens (migration 070)
- SuiteService and ProfileService with workspace-scoped CRUD and hash-based duplicate detection on profile capture
- HTTP handler at /api/benchmarks/{suites,profiles}
- Next.js /benchmarks route group with sub-nav, list, create, and detail views for suites and profiles
- TanStack Query hooks and Zustand UI store
- en + zh i18n
- Playwright smoke

Phase 1 will build on this with run orchestration, the evaluator binary, and live runs UI.

Spec: docs/superpowers/specs/2026-05-07-multica-programbench-integration-design.md
Plan: docs/superpowers/plans/2026-05-07-multica-programbench-phase-0-foundations.md

## Test plan
- [x] Backend unit + service tests (make test)
- [x] Frontend typecheck and lint (pnpm typecheck && pnpm lint)
- [x] Migration roundtrip
- [x] Playwright smoke (e2e/benchmarks-foundations.spec.ts)
- [x] Manual UI walkthrough per plan Task 24
EOF
)"
```

---

## Self-Review (already applied)

The following inline checks were performed and resolved during plan-writing.

**Spec coverage.** Phase 0 of the spec (Section 10) calls for: migration with all tables, sqlc queries, CRUD service skeleton, handler routes for `/api/benchmarks/{suites,profiles}` (GET/POST/DELETE), UI for Suites and Profiles tabs with capture-from-agent. Tasks 1–6 cover DB; 7–9 cover services; 10–12 cover handlers + router; 13–22 cover frontend; 23 covers e2e; 24 covers verification. The `issue.origin_type` extension to support `benchmark_run` is in Task 1.

**Out of scope, deferred to Phase 1+.** Run lifecycle, task dispatcher, evaluator binary, evaluator pool token endpoints, summaries/comparison/leaderboard, runs/leaderboard UI tabs (these tabs are stubbed disabled in Task 15). The sqlc shells in Tasks 5–6 are intentional: they let Phase 1 land additively without a new migration mid-feature.

**Type consistency.** `BenchmarkSuite`, `BenchmarkProfile`, `SkillRef`, `CreateSuiteInput`, `CaptureProfileInput`, `Suite`, `Profile`, `PromptHashInput` — all introduced in Tasks 7/13 and used consistently in 8/9/10/11/14–21. Field names match between Go DTOs and TS types (`prompt_hash`, `attached_skills`, `instance_ids`, `display_name`, `duplicate_of`).

**Placeholder scan.** No "TBD", "TODO", "implement later". Tasks that reference existing helpers (`apiFetch`, `useAgents`, `writeJSON`, `testfixture.NewWorkspace`) include explicit grep-or-confirm steps so the executing engineer either finds the existing helper or learns it does not exist before writing code that depends on it.

---

## Plan complete.

Saved to `docs/superpowers/plans/2026-05-07-multica-programbench-phase-0-foundations.md`.

Phase 1 (run orchestration + evaluator binary + Runs UI) and Phase 2/3 will get their own plans after Phase 0 is delivered and reviewed in practice — each phase produces a working, testable PR on its own per the spec.

**Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task (T01 → T24), review between tasks, fast iteration through the queue.

**2. Inline Execution** — Execute tasks in this session in batches with checkpoints for review.

Which approach?
