DROP TRIGGER sessions_audit ON sessions;
DROP TRIGGER plans_audit ON plans;
DROP TRIGGER measurements_audit ON measurements;
DROP TRIGGER user_profiles_audit ON user_profiles;
DROP FUNCTION enforce_audit_privileges();
DROP FUNCTION purge_audit_events(interval);
DROP FUNCTION audit_row_change();
DROP TABLE audit_events;
