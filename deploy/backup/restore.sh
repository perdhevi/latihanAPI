#!/bin/sh
# shellcheck shell=busybox
# Runs in the backup image, whose /bin/sh is BusyBox ash: it has pipefail.
# Restores one backup into the running database, replacing its contents:
#
#   restore.sh /backups/latihan-<time>.dump.age
#
# Needs the age private key at AGE_IDENTITY_FILE (default /identity), mounted
# for this run only. Stop the API first. The restore runs in one transaction,
# together with re-applying the audit log's narrower privileges (the restored
# table would otherwise get the default ones): it applies completely or not at all.
set -eu -o pipefail
file=${1:?usage: restore.sh /backups/latihan-<time>.dump.age}
identity=${AGE_IDENTITY_FILE:-/identity}
PGPASSWORD=$(cat /run/secrets/migrator_password)
export PGPASSWORD
{
	age -d -i "$identity" "$file" | pg_restore --clean --if-exists --no-owner -f -
	# pg_dump output clears search_path; the schema is public in deployments.
	echo "SET search_path = public; SELECT enforce_audit_privileges();"
} | psql -h postgres -U latihan_migrator -d latihan --single-transaction -v ON_ERROR_STOP=1 --quiet >/dev/null
echo "restored $file"
