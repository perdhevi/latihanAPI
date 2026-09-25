#!/bin/sh
# Encrypted database backups: every BACKUP_INTERVAL_SECONDS (default a day),
# or once with --once.
#
# Each backup is a pg_dump (custom format) encrypted to BACKUP_AGE_RECIPIENT,
# an age public key. The server can write backups but not read them: only the
# holder of the private key, kept off the server, can restore. Backups older
# than BACKUP_RETENTION_DAYS (default 30) are deleted, which is also how long
# erased data can survive in backups.
set -eu -o pipefail
: "${BACKUP_AGE_RECIPIENT:?set BACKUP_AGE_RECIPIENT to an age public key (age1...)}"
retention=${BACKUP_RETENTION_DAYS:-30}
interval=${BACKUP_INTERVAL_SECONDS:-86400}
dir=/backups
PGPASSWORD=$(cat /run/secrets/migrator_password)
export PGPASSWORD

backup() {
	name="latihan-$(date -u +%Y%m%dT%H%M%SZ).dump.age"
	partial="$dir/.$name.partial"
	# pipefail: a failed pg_dump fails the backup instead of leaving a
	# valid-looking but empty file.
	if ! pg_dump -h postgres -U latihan_migrator -d latihan --format=custom | age -r "$BACKUP_AGE_RECIPIENT" >"$partial"; then
		rm -f "$partial"
		echo "backup failed" >&2
		return 1
	fi
	mv "$partial" "$dir/$name"
	date +%s >"$dir/.last-success"
	find "$dir" -name 'latihan-*.dump.age' -mtime +"$retention" -delete
	echo "backup written: $name ($(wc -c <"$dir/$name") bytes)"
}

if [ "${1:-}" = "--once" ]; then
	backup
	exit
fi
while :; do
	backup || true
	sleep "$interval"
done
