# Part 3: Authorization: whose data is this?

*From CRUD to Hardened, part 3 of 10. Code: `git diff phase-02..phase-03`.*

After part 2 every request carries a verified identity. That feels like security.
But run this against the `phase-02` tag with a perfectly valid token for user A:

```http
GET /api/v1/sessions?user_id=<user B's id>
Authorization: Bearer <user A's valid token>
```

User A gets user B's workout history. Authentication worked flawlessly and
protected nothing, because **the owner of the data came from the request**.

## The problem

This is broken object-level authorization, the flaw at the top of the OWASP API
Security Top 10 year after year. It takes several forms, and the API had all of
them:

- **Owner in the query string:** `GET /sessions?user_id=B`.
- **Owner in the body:** `POST /plans` with `"user_id": "B"` creates a plan *in
  B's account*.
- **Owner in the path:** `GET /users/B/measurements`.
- **No owner at all:** `GET /plans/{id}` ran `SELECT ... WHERE id = $1`. Knowing
  a plan's ID was enough to read, change or delete it.

UUIDs don't help here. They leak through logs, URLs, shared links, support tickets
and screenshots. **Treat every ID as known to an attacker.**

## The rule

One sentence fixes all four forms:

> **The owner of every record comes from the verified token, never from the
> request.**

Following it through the code takes three steps.

### 1. Remove the owner from the input

`user_id` is deleted from `PlanInput` and `SessionInput`, and the list endpoints no
longer accept `?user_id=`. Because the API already rejects unknown fields and
parameters, old clients that still send `user_id` get a clear `400` rather than
having it silently ignored. That's a breaking change, documented in the README.
Silently ignoring it would be worse: a client that *thinks* it's choosing an owner
is a bug waiting to happen.

Responses still include `user_id`. Reading your own owner is fine; *choosing* it
isn't.

### 2. Put the owner in every query

Every repository method now takes the owner explicitly, and every SQL statement
filters by it:

```go
// Repository methods take the owner explicitly and must scope every query by
// it: a record that belongs to someone else is indistinguishable from one that
// does not exist.
type Repository interface {
	GetPlan(ctx context.Context, owner, id uuid.UUID) (Plan, error)
	DeletePlan(ctx context.Context, owner, id uuid.UUID) error
	// ...
}
```

```sql
SELECT ... FROM plans WHERE id=$1 AND user_id=$2
DELETE FROM sessions WHERE id=$1 AND user_id=$2
```

This puts the check in the **data layer**, not just the handler. A handler written
next year that forgets to check ownership still can't read someone else's row,
because the query it calls won't return one.

The handler side is a single helper, used everywhere a handler needs an owner:

```go
// owner is the authenticated caller's user ID. Handlers use it, never a client
// supplied ID, to decide whose records a request touches.
func owner(r *http.Request) uuid.UUID { return principalFrom(r.Context()).userID }
```

And the service refuses to run without one. If a wiring bug ever reaches it with
no owner, the result is a loud 500, never an unscoped query:

```go
if owner == uuid.Nil {
	return Plan{}, errNoOwner
}
```

### 3. `me`, and 404 for everyone else

Paths like `/users/{userID}/measurements` accept `me` or the caller's own ID. Any
other ID gets **404, not 403**:

```go
if id != caller {
	writeError(w, http.StatusNotFound, "user_not_found", "user not found")
	return uuid.Nil, false
}
```

Why 404? A 403 says "this exists, but you can't have it", which lets an attacker
map which IDs are real. Someone else's record should look exactly like a record that
doesn't exist.

## How we prove it

Authorization bugs hide in the endpoints nobody thought to check, so the test
doesn't check a few. It checks **every route**, from the attacker's side.

`TestCrossUserAccessIntegration` creates a victim with a profile, a measurement, a
plan and a session linked to that plan. Then an intruder with a valid account of
their own is given **every ID** and tries everything:

```go
for _, tc := range []struct{ method, path string; body any }{
	{"GET", userURL, nil},
	{"PUT", userURL, profile.UserInput{DisplayName: "Hacked"}},
	{"DELETE", userURL, nil},
	{"GET", userURL + "/measurements", nil},
	{"POST", userURL + "/measurements", measurementInput},
	{"GET", measurementURL, nil},
	// ... PUT/DELETE measurement, GET/PUT/DELETE plan, comparison, GET/PUT/DELETE session
} {
	callAs(t, h, "intruder", tc.method, tc.path, tc.body, 404, nil)
}
```

That's 15 routes, all 404. The test also checks that:

- the intruder's own lists contain nothing of the victim's;
- the intruder can't attach the victim's plan to their own session, or link to its
  exercise entries;
- afterwards, the victim's data is **exactly** as it was, including its
  `updated_at`.

**The break-it check.** I reintroduced the original bug in three places: dropped
the owner filter from `GetPlan` and `DeleteSession`, and let any user ID through
the path check. Ten subtests failed, each naming the exact route that leaked. Then
the fix was restored.

## Trade-offs

- **It's a breaking API change.** Clients that sent `user_id` must stop. For a
  pre-1.0 API that's the right trade. Carrying a field the server must never trust
  is a permanent hazard.
- **Profile routes rely on one layer.** Profiles and measurements are protected by
  the path check, with SQL filtered by the ID the handler passes in. Plans and
  sessions pass the owner explicitly through the service as well. PostgreSQL
  row-level security would add a layer *below* the SQL. We discuss why it's
  deferred in part 8.

## Try it

```sh
git checkout phase-03
go test -tags=integration -run CrossUser ./internal/httpapi/   # needs TEST_DATABASE_URL
```

**Next: [Part 4, A built-in token issuer](04-built-in-token-issuer.md).** So far
you need a Firebase or Cognito account to run the tutorial. Next we build a login
that needs nothing, without building a weak one.
