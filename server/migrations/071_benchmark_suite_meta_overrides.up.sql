-- 071_benchmark_suite_meta_overrides.up.sql
-- Phase 4 of the ProgramBench integration. Adds an opaque JSONB blob to
-- benchmark_suite so the multica_replay adapter (and any future adapter
-- that needs per-instance meta captured at suite-creation time) can store
-- the full instance meta keyed by instance_id, without each adapter
-- needing its own table. Default '{}' keeps existing rows valid.

ALTER TABLE benchmark_suite
ADD COLUMN instance_meta_overrides JSONB NOT NULL DEFAULT '{}'::jsonb;
