-- approvals, exports, qa_results and artifact_parents are append-only records: an approval is bound
-- to an artifact hash, an export to an approval, and QA results and lineage explain why a version was
-- allowed to be reviewed. Code only INSERTs into these tables (checked: no UPDATE or DELETE exists in
-- the app, and nothing cascades into them), so no column is exempt. UPDATE, DELETE and TRUNCATE are
-- refused. A superuser can still disable a trigger on purpose; the app role cannot do it by accident.
CREATE FUNCTION record_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% records are immutable (% refused)', TG_TABLE_NAME, TG_OP USING ERRCODE = 'restrict_violation';
END
$$;

CREATE TRIGGER approvals_immutable_row BEFORE UPDATE OR DELETE ON approvals
    FOR EACH ROW EXECUTE FUNCTION record_immutable();
CREATE TRIGGER approvals_immutable_truncate BEFORE TRUNCATE ON approvals
    FOR EACH STATEMENT EXECUTE FUNCTION record_immutable();

CREATE TRIGGER exports_immutable_row BEFORE UPDATE OR DELETE ON exports
    FOR EACH ROW EXECUTE FUNCTION record_immutable();
CREATE TRIGGER exports_immutable_truncate BEFORE TRUNCATE ON exports
    FOR EACH STATEMENT EXECUTE FUNCTION record_immutable();

CREATE TRIGGER qa_results_immutable_row BEFORE UPDATE OR DELETE ON qa_results
    FOR EACH ROW EXECUTE FUNCTION record_immutable();
CREATE TRIGGER qa_results_immutable_truncate BEFORE TRUNCATE ON qa_results
    FOR EACH STATEMENT EXECUTE FUNCTION record_immutable();

CREATE TRIGGER artifact_parents_immutable_row BEFORE UPDATE OR DELETE ON artifact_parents
    FOR EACH ROW EXECUTE FUNCTION record_immutable();
CREATE TRIGGER artifact_parents_immutable_truncate BEFORE TRUNCATE ON artifact_parents
    FOR EACH STATEMENT EXECUTE FUNCTION record_immutable();
