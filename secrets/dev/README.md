Development-only credentials, committed on purpose so `docker compose up` works
out of the box. Never deploy them: run `scripts/init-secrets.sh`, which creates
random secrets in `./secrets/` (not committed), and set `SECRETS_DIR=./secrets`.
