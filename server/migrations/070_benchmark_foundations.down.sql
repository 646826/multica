-- 070_benchmark_foundations.down.sql
-- Reverses 070_benchmark_foundations.up.sql. Drops phase-0 benchmark tables in
-- reverse FK-dependency order, then restores the issue.origin_type CHECK to
-- the pre-070 state defined by 060_issue_origin_quick_create.up.sql.

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
