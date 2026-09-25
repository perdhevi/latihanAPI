# Part 8: Secrets, TLS and least privilege

*From CRUD to Hardened, part 8 of 10. Code: `git diff phase-07..phase-08`.*

Every part so far tried to stop an attack. This one assumes an attack
**succeeds**, and asks how much it's worth. A secret leaks, a dependency is
backdoored, an SQL injection slips through. If any single failure gives away
everything, the design has no depth.

## Problem 1: secrets in the wrong places

The database password lived in `DATABASE_URL`, an environment variable. Environment
variables leak more than people expect. They show up in:

- `docker inspect`;
- `/proc/<pid>/environ`;
- crash reports and debugging sessions;
- an `.env` file accidentally committed to git.

**Fix: secrets as files.** Any setting `X` can instead be given as `X_FILE`, a path
to a file holding the value. That's exactly how Docker and Compose mount secrets,
at `/run/secrets/<name>`.

Two details matter:

- **Setting both `X` and `X_FILE` is a startup error.** Silently picking one would
  hide a misconfiguration.
- **`X_FILE` is resolved only when `X` is asked for.** `AUTH_JWT_KEY_FILE` already
  ends in `_FILE` but is a path, not an indirection. Resolving every `*_FILE`
  variable eagerly would try to load a PEM file into a setting that doesn't exist.

Compose now mounts every credential as a secret from `SECRETS_DIR`. Development
values are committed in `secrets/dev/` (so the quickstart still works), and
`scripts/init-secrets.sh` generates random ones for a real deployment. Error
messages name the setting, never its value. A test checks that a bad secret file
never echoes its contents.

## Problem 2: the API was the database superuser

The API connected as the same PostgreSQL role that created the database. So **any**
SQL injection, or any compromised dependency in the process, could:

- `DROP TABLE sessions`;
- `ALTER` a table to plant a trigger;
- read other databases on the server.

**Fix: three roles, each with only what it needs.** They're created when the
database volume is first initialized:

| Role | Used by | Can |
| --- | --- | --- |
| `latihan` | Nothing, after setup | Everything (superuser) |
| `latihan_migrator` | The migration job | Own and change the schema |
| `latihan_app` | The API | `SELECT`, `INSERT`, `UPDATE`, `DELETE` on rows. No DDL, no `TRUNCATE`. |

```sql
ALTER SCHEMA public OWNER TO latihan_migrator;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO latihan_app;
-- Every table the migrator creates is readable and writable by the app, and
-- nothing more: no DDL, no TRUNCATE, no REFERENCES, no TRIGGER.
ALTER DEFAULT PRIVILEGES FOR ROLE latihan_migrator IN SCHEMA public
	GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO latihan_app;
```

An SQL injection in the API can still read and change rows (parts 3 and 5 make that
hard to reach). But it **cannot destroy or reshape the database**.

A subtle bug turned up while wiring this in. The PostgreSQL health check used the
Unix socket, and during first-time initialization the server listens *only* on
that socket. So the migrations could start before the roles existed. The check now
uses TCP, which isn't available until initialization finishes.

That last `ALTER DEFAULT PRIVILEGES` line will come back to bite us in part 10.
Keep it in mind.

## Problem 3: plain HTTP

Tokens, passwords and health data were crossing the network in the clear.

**Fix: Caddy in front**, added by a production overlay, `docker-compose.prod.yml`.
Caddy obtains and renews Let's Encrypt certificates automatically, redirects HTTP
to HTTPS, and adds security headers:

```text
Strict-Transport-Security "max-age=31536000; includeSubDomains"
Content-Security-Policy "default-src 'none'; frame-ancestors 'none'"
X-Frame-Options DENY
Referrer-Policy no-referrer
```

The overlay also makes Caddy the **only** thing with published ports. PostgreSQL,
the API and its admin port are reachable only on the internal Compose network.

Remember `TRUSTED_PROXIES` from part 5? Behind Caddy, every request arrives from
Caddy. So Caddy gets a **fixed address**, `172.30.0.10`, and that single address
is the API's only trusted proxy. Caddy replaces any `X-Forwarded-For` a client
sends, so the API always sees the real client.

**The database connection** gets a warning rather than a rule. On the same host, or
on a private Compose network, unencrypted traffic never crosses a shared link. For
any other database the API logs a warning unless the URL uses
`sslmode=verify-full`. Note that `require` encrypts but **accepts any
certificate**, so a machine in the middle can still read everything.

## Deferred: row-level security

PostgreSQL row-level security (RLS) would add ownership checks *below* the SQL:
even a query that forgot `WHERE user_id = $2` would return only the caller's rows.
It's a real defense, and we don't build it here. It needs every query to run inside
a transaction that first sets the current user (`SET LOCAL app.user_id`). That means
reworking every repository, and paying the cost on every request. Ownership is
already enforced in every query and covered by the attack tests from part 3, so RLS
is documented as the next layer for anyone who needs it.

## How we prove it

**A CI smoke test** starts the real Compose stack, runs the quickstart, then
connects **as the app role** and tries to break things:

```sh
for ddl in "DROP TABLE sessions" "TRUNCATE sessions" "ALTER TABLE sessions ADD COLUMN x int" "CREATE TABLE smoke (id int)"; do
	if out=$(as_app "$ddl"); then
		fail "app role was allowed to run: $ddl"
	fi
done
```

**Break-it check:** I granted the app role `TRUNCATE` by hand, and the smoke test
failed immediately. The check isn't just passing because nothing is checked.

**The production overlay, run locally** with `DOMAIN=localhost` (Caddy then uses
its internal certificate authority):

- HTTP was redirected to HTTPS with `308`, and all the security headers were
  present;
- ports 8080 and 5432 were unreachable from the host;
- register and login worked over HTTPS;
- a spoofed `X-Forwarded-For: 6.6.6.6` was ignored.

## Try it

```sh
git checkout phase-08
scripts/init-secrets.sh
# .env: SECRETS_DIR=./secrets, DOMAIN=localhost, ACME_EMAIL=you@example.com
docker compose -f docker-compose.yml -f docker-compose.prod.yml up --build -d
curl -sk -D - -o /dev/null https://localhost/health
```

**Next: [Part 9, Shipping signed, scanned images](09-shipping.md).** The code is
hardened. How do we know the image running in production *is* that code?
