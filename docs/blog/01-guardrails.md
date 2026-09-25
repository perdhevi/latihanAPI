# Part 1: Guardrails before features

*From CRUD to Hardened, part 1 of 10. Code: `git diff phase-00..phase-01`.*

It's tempting to start hardening with the exciting parts: authentication, rate
limiting, encryption. We start with something duller that pays off in every later
part: **making it hard to ship a known mistake.**

## The problem

Two kinds of problems reach production without anyone deciding to ship them.

**Known bug patterns.** An HTTP response body that's never closed leaks a
connection. An error compared with `==` instead of `errors.Is` silently stops
matching once someone wraps it. A database query without a context can't be
cancelled. None of these is exotic, and all of them slip through code review
because reviewers are human.

**Known vulnerabilities in dependencies.** Our code imports a PostgreSQL driver,
which imports text-processing libraries, which import more. A vulnerability anywhere
in that tree is our vulnerability. Checking by hand doesn't scale.

There's a third, quieter problem: **the build pipeline itself**. A GitHub Action
referenced as `actions/checkout@v4` runs whatever code the tag `v4` points to
*today*. Tags can be moved. In 2025 a popular action's tags were rewritten to
point at code that stole secrets from the CI runs of thousands of repositories.

## What we build

### 1. A linter configured for security, not style

[golangci-lint](https://golangci-lint.run) runs dozens of analyzers in one pass.
Its value is in choosing them. [`.golangci.yml`](../../.golangci.yml) enables the
standard set plus analyzers that catch real bugs:

```yaml
linters:
  default: standard # errcheck, govet, ineffassign, staticcheck, unused
  enable:
    - bodyclose     # unclosed HTTP response bodies leak connections
    - errorlint     # wrapped errors must be compared with errors.Is/As
    - gosec         # security-focused checks
    - noctx         # outbound requests and queries must carry a context
    - rowserrcheck  # rows.Err() must be checked after iteration
    - sqlclosecheck # rows and statements must be closed
    - usestdlibvars # http.StatusBadRequest instead of 400
```

It also enforces formatting (`gofmt`, `goimports`), so formatting never comes up
in code review again.

Its first run found 11 issues in code that had passed review:

- Status codes written as `204` instead of `http.StatusNoContent`.
- A transaction rollback whose error was silently discarded, now written
  explicitly as `defer func() { _ = tx.Rollback(ctx) }()` so the intent is visible.
- Test requests created without a context.
- Imports in the wrong groups.
- One false positive from `gosec`: reading migration files from a fixed glob. It's
  suppressed with a comment that says *why*:

```go
sql, err := os.ReadFile(path) //nolint:gosec // G304: path comes from a fixed glob of this repository's migrations
```

A bare `//nolint` teaches nothing. A `//nolint` with a reason lets the next reviewer
check the reasoning.

### 2. Vulnerability scanning that reports what matters

[`govulncheck`](https://go.dev/blog/govulncheck) is Go's official vulnerability
scanner, and it does something most scanners don't: it analyzes **which vulnerable
functions our code actually calls**. The first run printed this:

```text
Vulnerability #1: GO-2026-5970
    Infinite loop on invalid input in golang.org/x/text
    Found in: golang.org/x/text@v0.24.0   Fixed in: v0.39.0
      database.Open calls pgxpool.NewWithConfig, which eventually calls norm.Form.Transform

Vulnerability #2: GO-2026-5004
    SQL Injection via placeholder confusion with dollar quoted string literals
    Found in: github.com/jackc/pgx/v5@v5.7.6   Fixed in: v5.9.2
      training.PostgresRepository.ListSessions calls pgxpool.Pool.Query, which eventually calls sanitize.SanitizeSQL

Your code is affected by 2 vulnerabilities from 2 modules.
This scan also found ... 20 vulnerabilities in modules you require,
but your code doesn't appear to call these vulnerabilities.
```

Read the last lines closely. Twenty more vulnerabilities existed in our dependency
tree, **but our code never reaches them**. A scanner that failed the build on all
22 would train us to ignore it. `govulncheck` fails only on the two that are real,
and both **were** real: an SQL injection path in our database driver, reached from
our list endpoint. Upgrading `pgx` to 5.11.0 and `x/text` to 0.42.0 fixed both.

This choice has a cost, and part 9 shows it: a scanner that only reports reachable
code can miss a vulnerability reached through a code path its analysis doesn't
see. We'll add a second, broader scanner there.

### 3. A pipeline that can't be swapped out under us

In [the CI workflow](../../.github/workflows/ci.yml) every third-party action is
pinned to a full commit hash, with the human-readable version as a comment:

```yaml
- uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
  with:
    persist-credentials: false
```

A commit hash can't be moved. `persist-credentials: false` stops the checkout
step from leaving a GitHub token on disk for later steps (or a compromised
dependency) to find.

Pinning creates a new problem: pinned versions go stale. That's what
[Dependabot](../../.github/dependabot.yml) is for. It proposes updates to Go
modules, actions and base images every week, and it updates the hash and the
comment together.

CI now runs three jobs in parallel: **lint** (including a check that `go.mod` is
tidy), **vulnerabilities**, and **test**. The `test` job runs the race detector
and the integration tests against PostgreSQL.

### When should CI run?

A guardrail only works if it runs before a change lands. The strongest setup runs
CI **on every pull request**, and makes a green run a condition for merging:

```yaml
on:
  push:
    branches: [master]
  pull_request:
  workflow_dispatch: # also allow a manual run
```

This tutorial repository ships with only `workflow_dispatch`: you start CI yourself
from the Actions tab. That's deliberate for a repository people fork and experiment
with, because it stops every push in every fork from using Actions minutes. It
also moves responsibility to you: **run the workflow on a branch before you merge
it**, Dependabot's branches included. In your own project, switch to the automatic
triggers above. The later parts of this series add fuzzing, an end-to-end smoke
test, a disaster-recovery drill and an image scan to this workflow, and all of them
are only as useful as how often they run.

### 4. A small thing for Windows readers

`.gitattributes` forces LF line endings. Without it, a reader on Windows checks
out CRLF files, `gofmt` sees every line as changed, and the formatting check fails
on code that's perfectly fine.

## How we know it works

The linter and scanner prove themselves on their first run: 11 findings, and two
real, reachable vulnerabilities. All are fixed, and `golangci-lint`, `govulncheck`
and the race-enabled tests are green.

## What this doesn't do

- **Linters catch patterns, not logic.** None of these tools notices that any caller
  can read any record. That takes a threat model (part 0) and targeted tests
  (part 3).
- **`govulncheck` only sees Go code.** The container's base image has its own
  packages; part 9 scans those.
- Pinned hashes protect against a tag being moved later. They don't vet the code
  you pinned in the first place.

## Try it

```sh
git checkout phase-01
make check   # golangci-lint, govulncheck, unit tests
```

**Next: [Part 2, Pluggable authentication](02-pluggable-authentication.md).** We
finally answer the question from part 0: who is calling?
