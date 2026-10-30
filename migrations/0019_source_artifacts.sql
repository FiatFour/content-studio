-- Sources (Knowledge Shorts, D15) are owner-pasted text stored as artifacts. They belong to an episode
-- but to no run or job, so those two columns may now be NULL, and only for type 'source'. Every other
-- type keeps the old guarantee. The immutability triggers (0011) are untouched.
ALTER TABLE artifacts ALTER COLUMN run_id DROP NOT NULL;
ALTER TABLE artifacts ALTER COLUMN job_id DROP NOT NULL;
ALTER TABLE artifacts ADD CONSTRAINT artifacts_run_job_required
    CHECK (type = 'source' OR (run_id IS NOT NULL AND job_id IS NOT NULL));
