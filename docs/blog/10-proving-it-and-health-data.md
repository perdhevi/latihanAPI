# Part 10: Proving it, and handling health data responsibly

*From CRUD to Hardened, part 10 of 10. Code: `git diff phase-09..phase-10` (proving it)
and `git diff phase-10..phase-11` (health data).*

This final part has two halves that share one idea. The first half asks whether
our defenses survive inputs and situations **we didn't imagine**. The second asks
whether the people whose data this is **can take it back**. Both find real bugs.

---

## Half 1: Proving it

Every earlier test checks a case someone thought of. Attackers specialize in the
cases nobody thought of. So we add three kinds of tests that generate cases for us.

### Fuzzing: millions of inputs we didn't write

Go's built-in fuzzer mutates inputs and watches for failures. Twelve fuzz targets
cover every parser of untrusted input:

- JSON bodies;
- bearer tokens and multi-provider routing;
- `X-Forwarded-For`;
- cursors, `If-Match` and rate-limit policies;
- emails and stored password hashes;
- JWKs, text validation, and the plan comparison maths.

A fuzz test that only checks "no panic" wastes most of its power. Each of these
asserts an **invariant**, a property that must hold for every input:

```go
// The resolved client is the direct peer or an address listed in
// X-Forwarded-For, and X-Forwarded-For only matters when the peer is trusted.
if !l.isTrusted(peer.Addr().Unmap()) {
	if got != peer.Addr().Unmap() {
		t.Fatalf("untrusted peer %s, but client resolved to %s from %q", peer, got, forwardedFor)
	}
}
```

Others: a cursor that decodes must re-encode to the same position; accepted text is
trimmed, valid UTF-8 and within its limit; comparison totals are never NaN or
infinite.

Between 400,000 and 7.5 million executions per target. Two real findings:

1. **`RATE_LIMIT_IP=NaN/s:1` was accepted.** The check was `n <= 0`, and every
   comparison with NaN is false. The fix is the positive form, `!(n > 0)`, which
   also rejects NaN.
2. **`BearerToken` accepted a carriage return as a token.** Go's HTTP server
   rejects raw CR in real headers, but `BearerToken` is a public function that
   third-party providers call, and it shouldn't rely on its caller. It now enforces
   the RFC 6750 token grammar exactly.

Failing inputs are saved under `testdata/fuzz/` and replayed by plain `go test`
forever after. The CI `fuzz` job gives each target 15 seconds whenever the
workflow runs.

### Contract tests: does the documentation tell the truth?

`api/openapi.yaml` had been edited by hand in every part. Clients (the Flutter app,
or code generated from the spec) trust it. So every response in the integration
tests is now validated against it. The status must be documented for that
operation, and the body must match its schema exactly.

The first version of this check had a bug of its own: **it matched zero responses
and passed**. Route lookup needed an absolute URL, while test requests carry only a
path. It only surfaced because the check counts what it validated and fails on
zero:

```go
// A contract check that silently matches nothing proves nothing.
t.Cleanup(func() {
	if checked == 0 {
		t.Error("contract: no response was matched to an operation in api/openapi.yaml")
	}
})
```

Once it worked (200 responses validated), it found three kinds of drift:

- an **invalid schema**: an array declared without saying what it holds;
- **14 error codes** the API returns but the spec didn't list, which would make a
  generated client choke on real errors;
- an **undocumented 404**.

A unit test now scans the source for every error code and fails if one is missing
from the spec.

### The attacker's view

Part 3's cross-user test predates idempotency keys, ETags and cursors. A new suite
attacks through those features:

- replaying a victim's `Idempotency-Key`;
- sending a victim's **correct** ETag;
- forging a cursor into a victim's history;
- comparing the answers for real and made-up IDs byte for byte;
- injecting the victim's ID in paths, bodies and queries.

Break-it checks: making idempotency keys global handed the victim's stored response
to the attacker, and an owner-blind existence check answered `412`, confirming the
record existed. Both fail the suite.

### Load tests: what does it actually take?

Two [k6](https://k6.io) scripts run against the Compose stack. On a development
machine, so the numbers are only indicative:

- **Capacity**, with limits off, 50 users: about **4,000 requests/s**, p95 of 10 ms
  for reads and 23 ms for writes. The metrics from part 7 named the bottleneck: the
  10-connection pool, with 323,501 waits for a free connection. At 30 connections
  the stack reached **about 7,100 requests/s**. The default stays at 10 for small
  servers. Now you know which metric to watch before changing it.
- **Pressure**, with default limits, at 600 requests/s of password guessing and
  anonymous traffic from one address:
  - 10,911 answered `429` with `Retry-After`;
  - **only 10 login attempts reached password hashing**;
  - zero server errors;
  - p99 under 1 ms, because refusing is cheap.

---

## Half 2: Health data, responsibly

Body measurements are health data. Keeping them *secure* isn't enough. Users should
be able to **take all of it** and **remove all of it**. Every change should be
**accountable**. And losing a server shouldn't mean losing everyone's history.

### Export

`GET /api/v1/account/export` returns one JSON document: the profile, linked
identities, and every measurement, plan and session. It's **streamed** page by
page, so memory stays flat however much history there is, and it's recorded in the
audit log and rate limited.

Streaming raises a question: what if the database fails halfway through? The status
is already `200` and half a JSON document has been sent. The answer reuses part 7:
`panic(http.ErrAbortHandler)` cuts the connection, so the client sees a failed
download, never a valid-looking truncated file.

### Erasure

`DELETE /api/v1/account` removes everything in **one transaction**: sessions,
plans, measurements, the profile and its identity links. It also removes
**stored idempotency responses**, which are easy to forget: part 6 stored full
responses for 24 hours, and those responses contain health data.

The provider contract gains an optional `AccountEraser`, so the built-in provider
deletes the login too. Firebase and Cognito accounts must be deleted by the app with
the provider, and the docs say so.

Erasure needs only a valid token, not a profile, so an erasure interrupted halfway
can be finished, and repeating it is harmless. A plain `DELETE /users/me` still
refuses (`409`) while history exists, so nobody erases their data by accident.

### An audit log the API can't rewrite

Every insert, update and delete of user data is recorded by **database triggers**,
in the same transaction as the change:

```sql
CREATE TRIGGER sessions_audit AFTER INSERT OR UPDATE OR DELETE ON sessions
    FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id');
```

Triggers mean a change and its audit row commit or roll back together, and no
endpoint added later can forget to audit. The rows hold **only identifiers**: who,
what action, which record, when. The audit log must not become a second copy of the
health data.

The app role may only **append**. It can't read, update or delete audit rows, except
through a purge function that refuses to touch anything newer than 30 days. A
compromised API can't cover its tracks. After an erasure, the audit rows stay under
a UUID that no longer leads to anyone.

### Encrypted backups, and a drill that found a bug

A nightly `pg_dump` is encrypted to an [age](https://age-encryption.org) **public
key**. The server can write backups but **can't read them**; restoring needs the
private key, kept off the server. Other properties:

- Backups are deleted after 30 days, which also makes 30 days the deadline for
  erased data to leave backups.
- The container turns unhealthy if no backup succeeds for 26 hours.
- Production refuses to start without a backup key.

An untested backup is a hope, not a backup. So CI runs a **disaster-recovery
drill**:

1. Take a backup.
2. **Destroy every volume.**
3. Start a new, empty stack.
4. Restore into it.
5. Check that the data and the privileges came back.

The drill's first run found a real bug. **After restoring, the app role could read
the audit log again.** The restore recreates the `audit_events` table, and the new
table picks up the migrator's *default privileges* from part 8, which include read
and write for the app role. The dump's own permission statements only *add* the
narrow `INSERT` grant; they never revoke the defaults. The append-only guarantee
would have quietly vanished on the one day you most need it: after a disaster.

The fix makes the privileges one source of truth: a database function,
`enforce_audit_privileges()`. The migration calls it, and the restore calls it
**in the same transaction** as the restored data:

```sh
{
	age -d -i "$identity" "$file" | pg_restore --clean --if-exists --no-owner -f -
	# pg_dump output clears search_path; the schema is public in deployments.
	echo "SET search_path = public; SELECT enforce_audit_privileges();"
} | psql -h postgres -U latihan_migrator -d latihan --single-transaction -v ON_ERROR_STOP=1
```

The drill now passes end to end, and the CI `smoke` job repeats it on every run.

### How long each kind of data lives

| Data | Kept for |
| --- | --- |
| Profile, measurements, plans, sessions | Until the user deletes or erases them |
| The same data in backups | Up to 30 days |
| Stored idempotency responses | 24 hours |
| Audit events (identifiers only) | 1 year by default, never less than 30 days |
| Application logs | Log rotation; `LOG_CLIENT_IP=truncated` logs /24 or /48 networks instead of addresses |

This is engineering, not legal advice. A privacy policy and a legal basis for
processing health data are still up to you.

---

## Looking back

Across twelve tags, most bugs weren't found by writing code. They were found by
**checks that could fail**:

- The linter found 11 issues in reviewed code, and `govulncheck` a reachable SQL
  injection path.
- The authentication conformance suite showed defense in depth catching what one
  layer missed.
- Trivy found a CVE that `govulncheck` couldn't see.
- Fuzzing found NaN and carriage-return bugs.
- Contract tests found 14 undocumented error codes, and first had to be caught being
  vacuous themselves.
- The disaster-recovery drill found a privilege leak that only happens during a
  restore.

The habit that tied it together was the break-it check: deliberately reintroduce
each bug and watch the test fail. A green test that can't go red is decoration.

## Try it

```sh
git checkout phase-11
cp .env.example .env && docker compose up --build -d --wait
sh scripts/smoke-test.sh && sh scripts/backup-restore-test.sh   # destroys and restores the stack
sh scripts/fuzz.sh                                               # FUZZTIME=20s per target by default
```

*That's the series. The full diff from where we started:* `git diff phase-00..phase-11 --stat`.
