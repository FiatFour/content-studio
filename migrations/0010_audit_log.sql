-- Append-only audit log. The app connects as the table owner, so revoking privileges would not stop
-- it; triggers do, for row UPDATE/DELETE and for TRUNCATE. A row is written in the same transaction
-- as the action it records. actor is a column so the single owner ('owner') can become a real user
-- when auth lands (D9). detail is a short sanitized object: no secrets, request bodies or absolute paths.
CREATE TABLE audit_log (
    id           bigserial PRIMARY KEY,
    at           timestamptz NOT NULL DEFAULT now(),
    actor        text NOT NULL CHECK (actor <> ''),
    action       text NOT NULL CHECK (action <> ''),
    target_type  text NOT NULL CHECK (target_type <> ''),
    target_id    bigint,
    detail       jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(detail) = 'object' AND length(detail::text) <= 2000)
);

CREATE FUNCTION audit_log_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only (% refused)', TG_OP USING ERRCODE = 'restrict_violation';
END
$$;
CREATE TRIGGER audit_log_no_update_delete BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_append_only();
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_append_only();
