-- One row per completed stage execution, keyed by run ID + stage + input hash. A job that is queued
-- again with an unchanged input finds its key here and reuses the artifacts its earlier execution
-- stored (artifacts.job_id = job_id) instead of calling providers and storing new versions.
CREATE TABLE stage_executions (
    idempotency_key  text PRIMARY KEY,
    run_id           bigint NOT NULL REFERENCES workflow_runs(id),
    stage            text NOT NULL,
    input_hash       text NOT NULL,
    job_id           bigint NOT NULL REFERENCES jobs(id),
    attempt          int NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX stage_executions_job_idx ON stage_executions (job_id);
