# Part 6: Safe retries and concurrent edits

*From CRUD to Hardened, part 6 of 10. Code: `git diff phase-05..phase-06`.*

Not every data-integrity problem involves an attacker. This part fixes two that
every mobile app hits eventually, caused by nothing worse than a lift, a tunnel
and a second device.

## Problem 1: the response that never arrived

A user finishes a workout in a gym basement and taps "Save":

1. The phone sends `POST /api/v1/sessions`.
2. The server saves the session and sends `201 Created`.
3. The response is lost on the way back. The signal dropped.
4. The app sees a timeout and does the sensible thing: it retries.
5. The server saves the session **again**.

The user now has two identical workouts, and their weekly volume is doubled. From
the server's side the retry is indistinguishable from a new workout, because
nothing in the request says "this is the same one again".

## Problem 2: the edit that silently vanished

The same user edits a training plan on their phone, and a minute later on their
tablet, which still shows the old version:

1. Phone: `PUT /plans/42` with "squats 5×5". Saved.
2. Tablet: `PUT /plans/42` with the old list plus a renamed plan. Saved,
   **overwriting the phone's change**.

Nobody sees an error. The phone's edit simply never happened. This is the **lost
update** problem. The README until now even documented it: "concurrent replacements
use the last successful write".

## Fix 1: `Idempotency-Key`

The client generates a random key for each intended action and sends it with the
request. **It reuses the same key when retrying:**

```http
POST /api/v1/sessions
Idempotency-Key: 6f1c2a9e-2b54-4f0e-9d3a-7c1e5b0a8d42
```

The server remembers the first response under that key and replays it for every
retry:

| Retry with the same key | Answer |
| --- | --- |
| Same body, first request finished | The stored status, body, `Location` and `ETag`, plus `Idempotent-Replayed: true`. Nothing is created. |
| First request still running | `409` with `Retry-After`. Never run it twice in parallel. |
| Different body or endpoint | `422`. That's a client bug, and it shouldn't be hidden. |
| First request got `5xx` or `429` | Not stored, so the retry runs for real. |

Several details carry the weight:

- **Keys are private to the caller's identity.** Otherwise one user could replay
  another's response, and with it another user's health data. The key is stored
  per `(issuer, subject, key)`.
- **"Same request" means a fingerprint** of method, path, content type and body.
  The same key with a different body is a mistake worth reporting.
- **The claim is atomic.** `INSERT ... ON CONFLICT DO NOTHING` means exactly one
  of several parallel requests wins. The others see "in progress".
- **Crashes don't wedge a key forever.** A claim that never completed is taken over
  after a minute. The request deadline is 10 seconds, so a claim that old is dead.
- **Keys expire after 24 hours** and are purged hourly.

The handler is a wrapper around the existing create handlers. It runs the real
handler against a recorder, stores what it produced, then sends it:

```go
rec := &recorder{header: http.Header{}}
next(rec, r)
resp := rec.response()
// Server errors and rate limits are not the request's answer; the
// client should be able to try again for real.
if resp.Status >= http.StatusInternalServerError || resp.Status == http.StatusTooManyRequests {
	err = h.idempotency.Release(cleanup, scope)
} else {
	err = h.idempotency.Complete(cleanup, scope, resp)
}
```

The bookkeeping uses `context.WithoutCancel`, so a client that disconnects
mid-request can't leave a half-finished claim behind.

**The gap we accept.** The new session and the stored response are written in two
separate transactions. If the process dies *exactly* between them, a retry runs the
request again. Closing that window would mean every create handler writing the key
inside its own transaction. That's a real design, and a lot of plumbing for a
millisecond-wide window. We document it instead of pretending it isn't there.

## Fix 2: `ETag` and `If-Match`

HTTP already has the answer to lost updates: **optimistic concurrency**. Every
single-record response carries a version tag:

```http
ETag: "hmm8f7ojel"
```

A client that wants to be safe sends it back:

```http
PUT /api/v1/plans/42
If-Match: "hmm8f7ojel"
```

If the record changed since that read, the answer is `412 Precondition Failed`
and **nothing is written**. The client re-fetches, shows the user both versions,
and retries.

The version needed no new column. Every write already moves `updated_at` forward
by at least a microsecond, so the ETag is `updated_at` in base 36.

The crucial detail is *where* the check happens. Reading the version, comparing it,
then writing is a race: two writers can both pass the check. So the condition goes
**inside the write itself**:

```sql
UPDATE plans SET ... WHERE id=$1 AND user_id=$2 AND updated_at = ANY($6)
```

PostgreSQL row locking does the rest. When two writers hold the same ETag, the
second `UPDATE` waits for the first, re-checks its `WHERE` clause against the new
row, and matches nothing. Sessions, whose update already runs under
`SELECT ... FOR UPDATE`, compare the version under that lock.

If the `UPDATE` matched nothing, was the record missing or just changed? A second
query decides between `404` and `412`. And that query is owner-scoped, because
otherwise `If-Match` would let an attacker probe whether *someone else's* record
exists.

**Optional by default.** Requests without `If-Match` behave as before, so existing
clients keep working. `REQUIRE_IF_MATCH=true` makes the header mandatory, with
`428 Precondition Required` when it's missing.

## How we prove it

The concurrency claims get concurrency tests, against real PostgreSQL:

- **Five parallel requests with one `Idempotency-Key` → exactly one session.** The
  others get a replay or `409`.
- **Two writers with the same ETag, ten rounds → exactly one `200` and one `412`
  every round.**
- Replays are byte-for-byte identical, including `Location` and `ETag`.
- Another user's identical key doesn't collide. A reused key gets `422`; an
  in-progress key gets `409`; an abandoned claim is taken over.
- A stale edit leaves the plan untouched. Weak ETags never match. `*` matches any
  version.
- A missing record, or another user's record, stays `404` even with a valid-looking
  `If-Match`.

**The break-it checks:**

- With the version condition removed from SQL, the stale edit **overwrote** the
  plan, and in the parallel test **both** writers got `200`. That's the lost update,
  reproduced.
- With the claim check removed, **five parallel requests created five sessions.**

## Trade-offs

- **Stored responses hold health data** for up to 24 hours. That counts toward the
  retention story in part 10, where erasure also deletes them.
- Optional `If-Match` means clients must opt in to safety. That's the price of not
  breaking existing clients a third time.

## Try it

```sh
git checkout phase-06
# Send the same POST twice with one Idempotency-Key: one session, the second answer replayed.
# Then PUT a plan twice with the same If-Match: 200, then 412.
```

**Next: [Part 7, Seeing inside](07-observability.md).** We've built a lot of
defenses. How would we know if they're firing, or failing?
