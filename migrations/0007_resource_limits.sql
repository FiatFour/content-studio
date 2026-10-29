-- Per-resource concurrency limits. A job names the resource it holds while running; Claim keeps
-- the number of running jobs per resource at or below max_concurrent and parks the rest in
-- waiting_for_resource with a visible reason. A resource with no row here gets one slot.
CREATE TABLE resource_limits (
    resource        text PRIMARY KEY,
    max_concurrent  int NOT NULL CHECK (max_concurrent >= 1)
);
INSERT INTO resource_limits (resource, max_concurrent) VALUES ('gpu', 1);

ALTER TABLE jobs ADD COLUMN resource text;
ALTER TABLE jobs ADD COLUMN wait_reason text;
-- Jobs already in flight when this applies: the GPU stages of the fictional_series profile.
UPDATE jobs SET resource = 'gpu' WHERE stage IN ('script', 'tts', 'images') AND state IN ('queued', 'running', 'retry_scheduled');
CREATE INDEX jobs_resource_running_idx ON jobs (resource) WHERE state = 'running';
