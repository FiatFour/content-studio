-- A running job holds a lease that its worker renews by heartbeat. A running job whose lease has
-- expired belongs to a dead worker and is recovered by any other worker (docs/spec/jobs.md).
ALTER TABLE jobs ADD COLUMN lease_owner text;
ALTER TABLE jobs ADD COLUMN lease_expires_at timestamptz;
ALTER TABLE jobs ADD COLUMN heartbeat_at timestamptz;
ALTER TABLE jobs ADD CONSTRAINT jobs_lease_only_when_running CHECK (state = 'running' OR (lease_owner IS NULL AND lease_expires_at IS NULL));
-- A job running when this migration applies has no lease: give it one that is already expired so
-- the next recovery pass handles it exactly like a crashed worker's job.
UPDATE jobs SET lease_owner = 'pre-lease', lease_expires_at = now() WHERE state = 'running';
CREATE INDEX jobs_lease_idx ON jobs (lease_expires_at) WHERE state = 'running';
