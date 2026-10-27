-- A run whose voice came out the wrong length for its target is failed with a measured reason, and the
-- queue starts one new full run for the episode with that reason as feedback for the scriptwriter.
-- auto_rewrites counts how many such restarts led to this run, so the loop is bounded.
ALTER TABLE workflow_runs ADD COLUMN feedback text;
ALTER TABLE workflow_runs ADD COLUMN rewrite_of bigint REFERENCES workflow_runs(id);
ALTER TABLE workflow_runs ADD COLUMN auto_rewrites int NOT NULL DEFAULT 0;
