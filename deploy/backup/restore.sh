#!/bin/sh
# Restores one backup into the running database, replacing its contents:
#
#   restore.sh /backups/latihan-<time>.dump.age
#
# Needs the age private key at AGE_IDENTITY_FILE (default /identity), mounted
# for this run only. Stop the API first. The restore runs in one transaction:
# it applies completely or not at all.
set -eu -o pipefail
file=${1:?usage: restore.sh /backups/latihan-<time>.dump.age}
identity=${AGE_IDENTITY_FILE:-/identity}
PGPASSWORD=$(cat /run/secrets/migrator_password)
export PGPASSWORD
age -d -i "$identity" "$file" |
	pg_restore -h postgres -U latihan_migrator -d latihan \
		--clean --if-exists --no-owner --single-transaction --exit-on-error
echo "restored $file"
