-- Artifact rows are immutable: a version is identified by its sha256 and approvals/exports point at
-- it, so nothing may rewrite or remove one. The only column the app legitimately updates is
-- validation_status (set by the QA stage after the video row exists). Every other column, DELETE and
-- TRUNCATE are refused. A superuser can still disable the trigger on purpose (tests that simulate
-- tampering do so explicitly); the app role cannot do it by accident.
CREATE FUNCTION artifacts_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF (to_jsonb(NEW) - 'validation_status') IS NOT DISTINCT FROM (to_jsonb(OLD) - 'validation_status') THEN
            RETURN NEW;
        END IF;
        RAISE EXCEPTION 'artifacts are immutable (only validation_status may change)' USING ERRCODE = 'restrict_violation';
    END IF;
    RAISE EXCEPTION 'artifacts are immutable (% refused)', TG_OP USING ERRCODE = 'restrict_violation';
END
$$;
CREATE TRIGGER artifacts_immutable_row BEFORE UPDATE OR DELETE ON artifacts
    FOR EACH ROW EXECUTE FUNCTION artifacts_immutable();
CREATE TRIGGER artifacts_immutable_truncate BEFORE TRUNCATE ON artifacts
    FOR EACH STATEMENT EXECUTE FUNCTION artifacts_immutable();
