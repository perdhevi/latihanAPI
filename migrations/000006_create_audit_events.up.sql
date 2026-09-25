-- An append-only record of every change to user data, and of exports and
-- erasures. Rows say who, what kind of record and which one, never the values:
-- the audit log must not become a second copy of the health data.
CREATE TABLE audit_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    -- The user whose data it is. After erasure nothing links this UUID to a
    -- person any more, so the record of what happened can stay.
    user_id UUID NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('insert', 'update', 'delete', 'export', 'erase')),
    resource_type TEXT NOT NULL CHECK (resource_type IN ('user_profiles', 'measurements', 'plans', 'sessions')),
    resource_id UUID NOT NULL
);
CREATE INDEX audit_events_user_idx ON audit_events (user_id, occurred_at DESC);
CREATE INDEX audit_events_occurred_idx ON audit_events (occurred_at);

-- Triggers write the audit rows in the same transaction as the change, so a
-- change and its record commit or roll back together, and no code path can
-- forget to audit. TG_ARGV[0] names the column that holds the owner.
CREATE FUNCTION audit_row_change() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    row_data jsonb := to_jsonb(CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END);
BEGIN
    INSERT INTO audit_events (user_id, action, resource_type, resource_id)
    VALUES ((row_data ->> TG_ARGV[0])::uuid, lower(TG_OP), TG_TABLE_NAME, (row_data ->> 'id')::uuid);
    RETURN NULL;
END;
$$;

CREATE TRIGGER user_profiles_audit AFTER INSERT OR UPDATE OR DELETE ON user_profiles
    FOR EACH ROW EXECUTE FUNCTION audit_row_change('id');
CREATE TRIGGER measurements_audit AFTER INSERT OR UPDATE OR DELETE ON measurements
    FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id');
CREATE TRIGGER plans_audit AFTER INSERT OR UPDATE OR DELETE ON plans
    FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id');
CREATE TRIGGER sessions_audit AFTER INSERT OR UPDATE OR DELETE ON sessions
    FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id');

-- The only way to delete audit rows: those older than keep, which may not be
-- shorter than 30 days. SECURITY DEFINER runs it with the owner's rights, so
-- the API role needs no DELETE on the table itself.
CREATE FUNCTION purge_audit_events(keep interval) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path FROM CURRENT AS $$
DECLARE
    removed bigint;
BEGIN
    IF keep < interval '30 days' THEN
        RAISE EXCEPTION 'audit retention must be at least 30 days, got %', keep;
    END IF;
    DELETE FROM audit_events WHERE occurred_at < clock_timestamp() - keep;
    GET DIAGNOSTICS removed = ROW_COUNT;
    RETURN removed;
END;
$$;
REVOKE ALL ON FUNCTION purge_audit_events(interval) FROM PUBLIC;

-- The API may append audit rows and nothing else: it cannot read, change or
-- delete them, even if compromised. These privileges differ from the default
-- privileges every other table gets, so they live in one function that both
-- this migration and deploy/backup/restore.sh apply: a restore recreates the
-- table with the defaults, and the dump alone does not take them away. (The
-- role exists in deployments created by db/init/10-roles.sh; test databases
-- may not have it.)
CREATE FUNCTION enforce_audit_privileges() RETURNS void
LANGUAGE plpgsql SET search_path FROM CURRENT AS $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'latihan_app') THEN
        REVOKE ALL ON audit_events FROM latihan_app;
        GRANT INSERT ON audit_events TO latihan_app;
        GRANT EXECUTE ON FUNCTION purge_audit_events(interval) TO latihan_app;
    END IF;
END;
$$;
REVOKE ALL ON FUNCTION enforce_audit_privileges() FROM PUBLIC;
SELECT enforce_audit_privileges();
