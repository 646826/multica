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
    created_by      UUID NOT NULL REFERENCES "user"(id),
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
    captured_by     UUID NOT NULL REFERENCES "user"(id),
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
    created_by                  UUID NOT NULL REFERENCES "user"(id),
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
    created_by      UUID NOT NULL REFERENCES "user"(id),
    last_used_at    TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ
);
CREATE INDEX idx_evaluator_token_workspace ON evaluator_pool_token(workspace_id) WHERE revoked_at IS NULL;
