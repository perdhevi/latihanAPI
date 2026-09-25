#!/bin/sh
# Creates random secrets for a real deployment in ./secrets (or the directory
# given as $1). Existing files are kept, so running it again is safe.
#
# The directory is private to you (0700); the files inside are world-readable
# (0644) because Compose bind-mounts each file into containers that run as
# other users. Passwords are 32 letters and digits, so they are safe in URLs.
#
# Passwords take effect when the database volume is first created. To change
# them later, use ALTER ROLE ... PASSWORD and update the files.
set -eu
dir=${1:-secrets}
umask 077
mkdir -p "$dir"
chmod 700 "$dir"

random() { LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 32; }

write() {
	if [ -e "$dir/$1" ]; then
		echo "keeping $dir/$1"
		return
	fi
	printf '%s' "$2" >"$dir/$1"
	chmod 644 "$dir/$1"
	echo "created $dir/$1"
}

write postgres_password "$(random)"
write migrator_password "$(random)"
write app_password "$(random)"
write app_database_url "postgres://latihan_app:$(cat "$dir/app_password")@postgres:5432/latihan?sslmode=disable"
