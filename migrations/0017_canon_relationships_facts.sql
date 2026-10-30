-- M3 canon: relationships and established facts. These tables have no owner-edit path: a row exists
-- only because an approved proposal committed it. The trigger refuses an INSERT whose proposal_id has
-- no 'approved' decision (so a direct write from a stage fails inside the database), and proposal_id is
-- UNIQUE so one approval commits one row. Rows are append-only (record_immutable); ending a
-- relationship or retracting a fact is not designed yet.
CREATE FUNCTION canon_require_approved() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM canon_proposal_decisions d JOIN canon_proposals p ON p.id = d.proposal_id
                   WHERE d.proposal_id = NEW.proposal_id AND d.decision = 'approved' AND p.project_id = NEW.project_id) THEN
        RAISE EXCEPTION 'canon write to % refused: proposal % is not approved', TG_TABLE_NAME, NEW.proposal_id
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TABLE character_relationships (
    id               bigserial PRIMARY KEY,
    project_id       bigint NOT NULL REFERENCES projects(id),
    from_character   bigint NOT NULL REFERENCES characters(id),
    to_character     bigint NOT NULL REFERENCES characters(id),
    rel_type         text NOT NULL CHECK (rel_type IN ('parent', 'child', 'sibling', 'spouse', 'partner', 'friend', 'rival', 'coworker', 'neighbor')),
    starts_on        date,
    ends_on          date,
    proposal_id      bigint NOT NULL UNIQUE REFERENCES canon_proposals(id),
    created_at       timestamptz NOT NULL DEFAULT now(),
    CHECK (from_character <> to_character),
    CHECK (ends_on IS NULL OR starts_on IS NULL OR ends_on >= starts_on)
);

CREATE TABLE established_facts (
    id                 bigserial PRIMARY KEY,
    project_id         bigint NOT NULL REFERENCES projects(id),
    statement          text NOT NULL CHECK (char_length(statement) BETWEEN 5 AND 300),
    source_episode_id  bigint REFERENCES episodes(id),
    proposal_id        bigint NOT NULL UNIQUE REFERENCES canon_proposals(id),
    created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER character_relationships_guard BEFORE INSERT ON character_relationships
    FOR EACH ROW EXECUTE FUNCTION canon_require_approved();
CREATE TRIGGER established_facts_guard BEFORE INSERT ON established_facts
    FOR EACH ROW EXECUTE FUNCTION canon_require_approved();

CREATE TRIGGER character_relationships_immutable_row BEFORE UPDATE OR DELETE ON character_relationships
    FOR EACH ROW EXECUTE FUNCTION record_immutable();
CREATE TRIGGER character_relationships_immutable_truncate BEFORE TRUNCATE ON character_relationships
    FOR EACH STATEMENT EXECUTE FUNCTION record_immutable();
CREATE TRIGGER established_facts_immutable_row BEFORE UPDATE OR DELETE ON established_facts
    FOR EACH ROW EXECUTE FUNCTION record_immutable();
CREATE TRIGGER established_facts_immutable_truncate BEFORE TRUNCATE ON established_facts
    FOR EACH STATEMENT EXECUTE FUNCTION record_immutable();
