#!/bin/sh
# End-to-end check of a running stack (docker compose up --wait): the quickstart
# works, and the database roles are as narrow as intended. Used by CI.
set -eu
base=${BASE_URL:-http://localhost:8080}
secrets=${SECRETS_DIR:-./secrets/dev}
json='Content-Type: application/json'
fail() { echo "FAIL: $*" >&2; exit 1; }

code=$(curl -s -o /dev/null -w '%{http_code}' "$base/ready")
[ "$code" = 200 ] || fail "ready returned $code"

tokens=$(curl -sf -X POST "$base/api/v1/auth/register" -H "$json" \
	-d "{\"email\":\"smoke-$(date +%s)@example.com\",\"password\":\"correct horse battery staple\"}")
token=$(printf '%s' "$tokens" | sed -E 's/.*"access_token":"([^"]+)".*/\1/')
auth="Authorization: Bearer $token"
curl -sf -o /dev/null -X POST "$base/api/v1/users" -H "$auth" -H "$json" -d '{"display_name":"Smoke"}' || fail "create profile"
curl -sf -o /dev/null -X POST "$base/api/v1/sessions" -H "$auth" -H "$json" \
	-d '{"name":"Walk","performed_at":"2026-09-25T07:00:00Z","exercises":[{"name":"Walk","kind":"cardio","minutes":20}]}' || fail "create session"
curl -sf "$base/api/v1/sessions" -H "$auth" | grep -q '"name":"Walk"' || fail "list sessions"
echo "ok: API quickstart"

# as_app runs SQL as latihan_app, the role the API uses.
as_app() {
	docker compose exec -T -e PGPASSWORD="$(cat "$secrets/app_password")" postgres \
		psql -h 127.0.0.1 -U latihan_app -d latihan -v ON_ERROR_STOP=1 -tAc "$1" 2>&1
}
as_app "SELECT count(*) FROM sessions" >/dev/null || fail "app role cannot read"
for ddl in "DROP TABLE sessions" "TRUNCATE sessions" "ALTER TABLE sessions ADD COLUMN x int" "CREATE TABLE smoke (id int)"; do
	if out=$(as_app "$ddl"); then
		fail "app role was allowed to run: $ddl"
	fi
	printf '%s' "$out" | grep -qE 'must be owner|permission denied' || fail "unexpected error for $ddl: $out"
done
echo "ok: app role cannot change the schema"
