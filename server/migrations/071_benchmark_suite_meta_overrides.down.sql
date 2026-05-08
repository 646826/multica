-- 071_benchmark_suite_meta_overrides.down.sql

ALTER TABLE benchmark_suite DROP COLUMN IF EXISTS instance_meta_overrides;
