# Part 0: The baseline and its threat model

*From CRUD to Hardened, part 0 of 10. Code: tag `phase-00`.*

Most "secure your API" articles start with a checklist. This series starts with
a question instead: **what are we protecting, and from whom?** Every change in the
following parts answers a specific threat identified here. If a change can't be
traced back to a threat, it probably doesn't belong.

## The service

`latihanAPI` is the backend of a fitness app. A Flutter client talks to it over
REST, and it keeps everything in PostgreSQL:

```text
latihan_mobile -- REST --> latihanAPI (Go) --> PostgreSQL
```

Users record **workout sessions**, build reusable **training plans**, compare what
they planned with what they actually did, and keep a history of **body
measurements**: weight, body fat, waist, chest and hip.

That last list is the reason for this whole series. Body measurements are
**health data**. Under laws like the GDPR it is a special category with stricter
rules. Leaking it is embarrassing, and it can also be harmful to real people.

## A good starting point

The code we start from is not a toy. It's an idiomatic Go modular monolith, and
it already does a lot right:

- **Strict input handling.** JSON bodies are capped at 1 MiB. Unknown fields,
  trailing data and unknown query parameters are rejected rather than ignored.
- **Timeouts everywhere.** It has HTTP read, write and idle timeouts, and a
  10-second deadline on every database call.
- **Graceful shutdown.** On `SIGTERM` it drains in-flight requests, then closes
  the connection pool.
- **Privacy-aware errors.** Unexpected failures return a generic 500; SQL errors
  never reach the client.
- **A non-root, read-only container** with all Linux capabilities dropped.
- **Real integration tests**, which run every workflow against a real PostgreSQL.

If you handed this to a reviewer as "a REST API", they'd be happy. And yet its own
README contains this sentence:

> **User IDs identify record ownership; they do not authenticate callers.**
> Anyone with network access can read or change records.

That's the lesson of part 0. **Well-written code and a trustworthy service are
different things.** The code does what it says. What it says just isn't enough
for health data on the internet.

## Assets and adversaries

What we protect:

1. **Users' health data.** Its confidentiality and its integrity: nobody reads it,
   nobody silently changes it.
2. **Users' accounts**, once they exist.
3. **Availability.** An API that one person can take down fails every user.
4. **The operator's infrastructure.** The database, the server and the signing keys.

Who we defend against:

| Adversary | What they have |
| --- | --- |
| Anonymous internet | The URL. Unlimited requests. |
| A curious or malicious user | A valid account, and patience to try other people's IDs. |
| A thief of one credential | One user's token, or one leaked backup file. |
| A compromised dependency or build | Code running inside our pipeline or our process. |
| Ourselves | Typos in config, forgotten flags, untested restores. |

The last row matters as much as the others. Many real incidents are
misconfigurations, not attacks.

## STRIDE, applied

STRIDE is a checklist of six ways systems fail. Walking the current service through
it gives us the work for the rest of the series:

| Threat | What it looks like here | Fixed in |
| --- | --- | --- |
| **S**poofing | There is no authentication at all. Anyone can claim to be any user by putting their ID in a request. | Parts 2, 4 |
| **T**ampering | Anyone can change or delete any record. A retried request creates duplicates. Two devices editing one plan silently overwrite each other. | Parts 3, 6 |
| **R**epudiation | Nothing records who changed what. | Part 10 |
| **I**nformation disclosure | Records are fetched by ID alone (an insecure direct object reference, or IDOR). Backups, logs and exports can leak too. | Parts 3, 7, 8, 10 |
| **D**enial of service | No rate limits. `?offset=2000000000` makes PostgreSQL walk two billion rows. No cap on concurrent requests. | Part 5 |
| **E**levation of privilege | The API connects as the database superuser. A known-vulnerable dependency or an unsigned image could run as us. | Parts 1, 8, 9 |

Two items in that table deserve a closer look, because they recur through the
series.

**IDOR** means fetching a record by an ID the client supplies without checking
who owns it. `GET /plans/{id}` running `SELECT ... WHERE id = $1` is the textbook
case. UUIDs make IDs hard to guess, but they leak through URLs, logs, shared
screenshots and support tickets. **Unguessable is not the same as authorized.**

**Cheap requests that cost us a lot** are the core of denial of service. An
attacker doesn't need a botnet if one request makes our database do a billion
units of work.

## The rules for this series

Three rules keep the series honest:

1. **Every change answers a threat from the table above.**
2. **Every fix is proven by a test that fails without it.** Several times in this
   series a test passed for the wrong reason: it never exercised the code at all.
   We catch those by deliberately breaking the fix and watching the test fail. You'll
   see this "break-it check" in every part.
3. **Every part is a git tag.** `git diff phase-02..phase-03` shows exactly what one
   article changed.

## The first commit

Part 0's code change is tiny, and it looks ahead. The Go module was called
`latihanApi`, a name nothing outside the repository can import. It becomes
`github.com/perdhevi/latihanAPI`:

```diff
-module latihanApi
+module github.com/perdhevi/latihanAPI
```

Why bother now? In part 2, authentication providers will live in *other*
repositories and plug into this one. They can only do that if this module has a
real, importable path. Renaming a module path later breaks every import in every
dependent repository, so we do it before anyone depends on us.

## Try it

```sh
git clone https://github.com/perdhevi/latihanAPI.git && cd latihanAPI
git checkout phase-00
cp .env.example .env && docker compose up --build
curl -s http://localhost:8080/api/v1/sessions?user_id=10000000-0000-4000-8000-000000000001
```

Notice that the last request needs no credentials at all. Keep that in mind: by
part 3, the same request without a token gets `401`, and with someone else's
user ID it gets `400`.

**Next: [Part 1, Guardrails before features](01-guardrails.md).** Before we write
a single security feature, we make sure known mistakes and known-vulnerable
dependencies can't ship.
