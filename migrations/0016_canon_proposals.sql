-- M3 canon proposals. A stage or agent can only INSERT a proposal; it changes canon only when the
-- owner approves it (internal/canon/proposals.go commits the change and the audit row in the decision's
-- transaction). Both tables are append-only (record_immutable, migration 0014): a proposal is never
-- edited, and its decision is a separate row, so history is never rewritten. Status is derived:
-- no decision = proposed. At most one decision per proposal, so a double approve is refused by the DB.
CREATE TABLE canon_proposals (
    id           bigserial PRIMARY KEY,
    project_id   bigint NOT NULL REFERENCES projects(id),
    kind         text NOT NULL CHECK (kind IN ('character.create', 'character.update', 'relationship.add', 'fact.add')),
    payload      jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object' AND length(payload::text) <= 4000),
    rationale    text NOT NULL DEFAULT '' CHECK (char_length(rationale) <= 600),
    proposed_by  text NOT NULL CHECK (proposed_by <> ''),
    episode_id   bigint REFERENCES episodes(id),
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE canon_proposal_decisions (
    id           bigserial PRIMARY KEY,
    proposal_id  bigint NOT NULL UNIQUE REFERENCES canon_proposals(id),
    decision     text NOT NULL CHECK (decision IN ('approved', 'rejected')),
    decided_by   text NOT NULL CHECK (decided_by <> ''),
    reason       text NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 500),
    decided_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER canon_proposals_immutable_row BEFORE UPDATE OR DELETE ON canon_proposals
    FOR EACH ROW EXECUTE FUNCTION record_immutable();
CREATE TRIGGER canon_proposals_immutable_truncate BEFORE TRUNCATE ON canon_proposals
    FOR EACH STATEMENT EXECUTE FUNCTION record_immutable();
CREATE TRIGGER canon_proposal_decisions_immutable_row BEFORE UPDATE OR DELETE ON canon_proposal_decisions
    FOR EACH ROW EXECUTE FUNCTION record_immutable();
CREATE TRIGGER canon_proposal_decisions_immutable_truncate BEFORE TRUNCATE ON canon_proposal_decisions
    FOR EACH STATEMENT EXECUTE FUNCTION record_immutable();
