-- One typed origin per run. A run is fresh, a re-render of its parent's inputs, or a rewrite of its
-- parent's script with feedback. The old columns (source_run_id, rewrite_of, auto_rewrites) allowed
-- contradictory combinations; the rewrite count is now derived from the chain instead of stored.
ALTER TABLE workflow_runs DROP CONSTRAINT workflow_runs_kind_check;
ALTER TABLE workflow_runs RENAME COLUMN source_run_id TO parent_run_id;
UPDATE workflow_runs SET parent_run_id = rewrite_of, kind = 'rewrite' WHERE rewrite_of IS NOT NULL;
ALTER TABLE workflow_runs DROP COLUMN rewrite_of;
ALTER TABLE workflow_runs DROP COLUMN auto_rewrites;
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_kind_check CHECK (kind IN ('full', 'rerender', 'rewrite'));
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_parent_matches_kind CHECK ((kind = 'full') = (parent_run_id IS NULL));
ALTER TABLE workflow_runs ADD CONSTRAINT workflow_runs_feedback_matches_kind CHECK ((kind = 'rewrite') = (feedback IS NOT NULL));
