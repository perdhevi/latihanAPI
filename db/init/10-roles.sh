#!/bin/sh
# Runs once, when the PostgreSQL data volume is first created
# (docker-entrypoint-initdb.d). Creates the two roles the service uses:
#
#   latihan_migrator  owns the schema; used only by the migration job
#   latihan_app       used by the API: reads and writes rows, never changes the schema
#
# The superuser (POSTGRES_USER) is used for nothing else after this.
set -eu

# secret NAME prints $NAME, or the contents of the file named by $NAME_FILE.
secret() {
	eval "file=\${${1}_FILE:-}"
	if [ -n "$file" ]; then
		cat "$file"
	else
		eval "printf '%s' \"\${$1:?set $1 or ${1}_FILE}\""
	fi
}

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
	-v db="$POSTGRES_DB" \
	-v migrator_password="$(secret LATIHAN_MIGRATOR_PASSWORD)" \
	-v app_password="$(secret LATIHAN_APP_PASSWORD)" <<'SQL'
CREATE ROLE latihan_migrator LOGIN PASSWORD :'migrator_password';
CREATE ROLE latihan_app LOGIN PASSWORD :'app_password';

REVOKE ALL ON DATABASE :"db" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"db" TO latihan_migrator, latihan_app;

ALTER SCHEMA public OWNER TO latihan_migrator;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO latihan_app;

-- Every table the migrator creates is readable and writable by the app, and
-- nothing more: no DDL, no TRUNCATE, no REFERENCES, no TRIGGER.
ALTER DEFAULT PRIVILEGES FOR ROLE latihan_migrator IN SCHEMA public
	GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO latihan_app;
SQL
