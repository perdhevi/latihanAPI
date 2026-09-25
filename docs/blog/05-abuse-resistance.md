# Part 5: Surviving abuse

*From CRUD to Hardened, part 5 of 10. Code: `git diff phase-04..phase-05`.*

Everything so far assumed that callers make *reasonable* requests. Attackers don't.
This part is about requests that are each valid but together hurt: too many, too
expensive, or from someone lying about who they are.

## The problem: cheap for them, expensive for us

Denial of service rarely needs a botnet. It needs an **asymmetry**, where one
request costs the attacker nothing and costs us a lot. The API had several:

| Request | Cost to us |
| --- | --- |
| `POST /auth/login`, 1,000 times a second | An argon2id hash each: 19 MiB of memory and real CPU time |
| `GET /sessions?offset=2000000000` | PostgreSQL walks two billion index entries to skip them |
| 10,000 slow concurrent requests | Every connection, goroutine and database connection held until timeout |
| A 1 MB request header | Go's default `MaxHeaderBytes` allows exactly that |
| A query that never finishes | A pooled database connection, held forever |

Then there's **password guessing**. Lockout (part 4) protects one account, but an
attacker trying one common password against thousands of emails never triggers it.

## Rate limiting, three ways

A **token bucket** is the standard tool. Each client has a bucket holding up to
*burst* tokens, which refills at *rate* tokens a second. Each request takes a token;
an empty bucket means `429 Too Many Requests` with `Retry-After`. Policies are one
readable string, such as `50/s:100` (50 per second, bursts of 100) or `off`.

One limit isn't enough, because attacks come in different shapes:

| Limit | Default | Stops |
| --- | --- | --- |
| Per client address | `50/s:100` | Floods from one source |
| Per authenticated user | `10/s:30` | One account hammering the API from many addresses |
| Per address, on auth routes | `10/m:10` | Credential stuffing against login, register and refresh |

The auth-route limit applies to **whatever routes the auth provider mounts**. The
core gives providers their own router, so it knows exactly which routes those are.
That means a third-party provider is protected without writing any rate-limiting
code itself.

Two details make the limiter trustworthy.

**IPv6 is grouped by /64.** One home connection usually gets 2⁶⁴ IPv6 addresses.
Limiting per address would give an attacker 2⁶⁴ buckets. Limiting per /64 gives
them one.

**Eviction must never hand out a free burst.** Idle buckets are removed to save
memory. But a bucket removed while still partly empty gets recreated *full*, so
an attacker could wait for eviction to get a fresh burst. Buckets are therefore
only removed once they'd have refilled anyway:

```go
// idleAfter is how long an unused bucket takes to refill completely.
// Dropping it then is indistinguishable from keeping it, so eviction never
// hands a client extra tokens.
```

At most 100,000 addresses are tracked. When the table is full, *new* addresses get
`429` rather than evicting active ones. Evicting would let an attacker with many
addresses reset everyone else's limits.

## Who is the client, really?

Behind a reverse proxy, every request arrives from the proxy's address. The real
client is in `X-Forwarded-For`, a header **the client can also write**. Trust it
blindly and every limit can be dodged by sending `X-Forwarded-For: 1.2.3.4`.

The rule: trust the header only when the connection comes from a known proxy
(`TRUSTED_PROXIES`, empty by default). Then read it **from the right**, skipping
trusted proxies. The first address that isn't a trusted proxy is the client.
Anything to the left of it was written by the client and is ignored:

```go
hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
for i := len(hops) - 1; i >= 0; i-- {
	hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
	if err != nil {
		return addr // a malformed hop: stop at the last address we can vouch for
	}
	if !l.isTrusted(hop.Unmap()) {
		return hop.Unmap()
	}
	addr = hop.Unmap()
}
```

The resolved address replaces `RemoteAddr`, so everything downstream, auth
providers included, sees the real client.

## Load shedding

`MAX_IN_FLIGHT` (default 256) caps concurrent requests. Request 257 gets an
immediate `503` with `Retry-After: 1`. That sounds harsh, but the alternative is
worse: without a cap, overload queues work until *every* request times out. Shedding
some requests keeps the rest fast. Health checks are exempt, so the orchestrator can
still see the service is alive.

## Pagination that can't be weaponized

`?offset=N` makes the database skip N rows by walking past them. It's slow for large
N, and it also skips or repeats items when rows are inserted between pages. The
lists switch to **keyset (cursor) pagination**: the next page starts *after* the last
item's `(timestamp, id)`:

```sql
WHERE user_id=$1 AND (performed_at, id) < ($2, $3)
ORDER BY performed_at DESC, id DESC LIMIT 21
```

We ask for one extra row. If it comes back, there's another page, and the response
carries `next_cursor`. Page 1,000 costs the same as page 1. That's a breaking
change, so `offset` now returns `400`.

The cursor is an opaque token, and it isn't signed. That's deliberate: a forged
cursor can only choose a position inside the caller's own list, which is already
filtered by owner.

## Limits the database enforces itself

The Go code already sets a 10-second deadline per request. PostgreSQL gets its own
limits too, set on every connection, so a runaway query stops even if the client
side never cancels it:

- `statement_timeout = 5s` for any single statement;
- `lock_timeout = 3s` for any row-lock wait;
- `idle_in_transaction_session_timeout = 10s` for a transaction that was abandoned.

And `MaxHeaderBytes` drops from Go's default of 1 MB to 16 KiB. Every response
also gets `Cache-Control: no-store`, because these responses are personal health
data that no shared cache should keep.

## How we prove it

**Unit tests** cover:

- the limiter, including that a busy bucket is never evicted early;
- 429s carrying `Retry-After`, and health checks staying exempt;
- IPv6 addresses within one /64 sharing a bucket;
- the auth limit applying only to provider routes;
- the per-user limit following a user across addresses;
- load shedding under a held request;
- a table of `X-Forwarded-For` forgeries.

**Against real PostgreSQL:**

- a 2-second `pg_sleep` is cancelled at a 200 ms limit **with no client deadline
  set**;
- `EXPLAIN` shows the keyset query walks the index without sorting;
- a pagination walk, with ties on timestamps and a new record inserted mid-walk,
  returns every item exactly once.

**Break-it checks:** trusting every proxy made the forgery tests fail, and removing
the /64 grouping made the IPv6 test fail.

**A live run against the real stack:** twelve failed logins gave `401` ten times,
then `429` with `Retry-After`. A spoofed `X-Forwarded-For` didn't help.

One live probe *seemed* to fail: a 20 KiB header was accepted. That wasn't a bug.
Go adds 4 KiB of slack to `MaxHeaderBytes`, so the real ceiling is about 20 KiB. A
test now pins the behaviour: 8 KiB is accepted and 32 KiB gets `431`.

## Trade-offs

- **Limits live in memory.** Right for one instance; several instances would each
  allow the full rate, and need a shared store like Redis.
- **Carrier NAT** can put many phones behind one address. If real users hit the
  per-address limit, raise it; the per-user limit still bounds each account.
- **Behind a proxy you must set `TRUSTED_PROXIES`.** Otherwise every user shares the
  proxy's limit. Part 8 does exactly that.

## Try it

```sh
git checkout phase-05
cp .env.example .env && docker compose up --build -d
for i in $(seq 12); do curl -s -o /dev/null -w '%{http_code} ' -X POST localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' -d '{"email":"x@example.com","password":"wrong password here"}'; done
# 401 401 401 401 401 401 401 401 401 401 429 429
```

**Next: [Part 6, Safe retries and concurrent edits](06-safe-retries.md).** Not
every problem is an attacker. Sometimes it's just a phone losing signal at the
wrong moment.
