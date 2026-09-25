#!/bin/sh
# Disaster-recovery drill for a running stack with data in it (for example
# after scripts/smoke-test.sh): take an encrypted backup, destroy every volume,
# start from nothing, restore, and check that data and privileges came back.
# DESTROYS the stack's volumes. Used by CI.
set -eu
# The drill's throwaway key lives in the mounted backups directory for the
# restore run only; a real private key never goes on the server.
mkdir -p backups
key=backups/.drill-identity.txt
trap 'rm -f "$key"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
psql_super() { docker compose exec -T postgres psql -U latihan -d latihan -tAc "$1"; }

# A throwaway key pair; in production the private key never touches the server.
docker compose --profile backup build --quiet backup
docker compose --profile backup run --rm --no-deps --entrypoint age-keygen backup >"$key" 2>/dev/null
recipient=$(grep -o 'age1[0-9a-z]*' "$key")
export BACKUP_AGE_RECIPIENT="$recipient"

before=$(psql_super "SELECT (SELECT count(*) FROM sessions) || '/' || (SELECT count(*) FROM user_profiles) || '/' || (SELECT count(*) FROM audit_events)")
[ "$before" != "0/0/0" ] || fail "no data to back up; run scripts/smoke-test.sh first"

docker compose --profile backup run --rm backup backup.sh --once
# Names are UTC timestamps, so the last in glob (lexical) order is the newest.
for file in backups/latihan-*.dump.age; do :; done
[ "$(head -c 21 "$file")" = "age-encryption.org/v1" ] || fail "backup is not age-encrypted"
if grep -qa 'latihan_app' "$file"; then fail "backup contains plaintext"; fi
echo "ok: encrypted backup $file"

docker compose down --volumes
docker compose up --detach --wait
[ "$(psql_super 'SELECT count(*) FROM sessions')" = 0 ] || fail "fresh stack is not empty"
docker compose stop latihan-api
# MSYS_NO_PATHCONV stops Git Bash on Windows rewriting the container path; Linux ignores it.
MSYS_NO_PATHCONV=1 docker compose --profile backup run --rm -e AGE_IDENTITY_FILE=/backups/.drill-identity.txt backup restore.sh "/backups/$(basename "$file")"
docker compose start latihan-api

after=$(psql_super "SELECT (SELECT count(*) FROM sessions) || '/' || (SELECT count(*) FROM user_profiles) || '/' || (SELECT count(*) FROM audit_events)")
[ "$after" = "$before" ] || fail "restored sessions/profiles/audit events $after, backed up $before"
echo "ok: restored $after sessions/profiles/audit events"

# Privileges come back with the data: the API role can still write rows but
# cannot read or rewrite the audit log.
as_app() {
	docker compose exec -T -e PGPASSWORD="$(cat "${SECRETS_DIR:-./secrets/dev}/app_password")" postgres \
		psql -h 127.0.0.1 -U latihan_app -d latihan -v ON_ERROR_STOP=1 -tAc "$1" 2>&1
}
as_app "SELECT count(*) FROM sessions" >/dev/null || fail "app role lost access to its data"
for sql in "SELECT count(*) FROM audit_events" "UPDATE audit_events SET action='insert'" "DELETE FROM audit_events"; do
	if as_app "$sql" >/dev/null; then fail "app role may run after restore: $sql"; fi
done
as_app "SELECT purge_audit_events(interval '1 day')" | grep -q 'at least 30 days' || fail "audit retention floor lost"
echo "ok: privileges and audit protections restored"
