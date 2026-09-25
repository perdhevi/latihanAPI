# From CRUD to Hardened: a Go API, one threat at a time

A tutorial series that takes a working Go REST API, the backend of a fitness app
that stores workouts and body measurements, and hardens it step by step until it
is fit to hold health data on the internet.

Every article starts from a concrete way the service can be attacked or can fail,
explains why it matters, fixes it, and then **proves** the fix with a test that
fails when the fix is removed. Each step is a git tag, so you can read the diff
next to the article:

```sh
git clone https://github.com/perdhevi/latihanAPI.git && cd latihanAPI
git diff phase-02..phase-03 -- internal/
```

| Part | Article | Tag | The question it answers |
| --- | --- | --- | --- |
| 0 | [The baseline and its threat model](00-baseline-and-threat-model.md) | `phase-00` | What are we protecting, and from whom? |
| 1 | [Guardrails before features](01-guardrails.md) | `phase-01` | How do we stop known mistakes and known vulnerabilities from shipping? |
| 2 | [Pluggable authentication](02-pluggable-authentication.md) | `phase-02` | Who is calling, and how do we let each deployment choose its identity provider? |
| 3 | [Authorization: whose data is this?](03-authorization.md) | `phase-03` | Can a logged-in user reach someone else's records? |
| 4 | [A built-in token issuer](04-built-in-token-issuer.md) | `phase-04` | How do we run with no external accounts, without building a weak login? |
| 5 | [Surviving abuse](05-abuse-resistance.md) | `phase-05` | What happens when someone sends a million requests? |
| 6 | [Safe retries and concurrent edits](06-safe-retries.md) | `phase-06` | What happens when the network drops a response, or two phones edit one plan? |
| 7 | [Seeing inside](07-observability.md) | `phase-07` | How do we know what the service is doing, without leaking what it holds? |
| 8 | [Secrets, TLS and least privilege](08-secrets-tls-least-privilege.md) | `phase-08` | If one part is compromised, how much does the attacker get? |
| 9 | [Shipping signed, scanned images](09-shipping.md) | `phase-09` | How do we know the code running in production is the code we reviewed? |
| 10 | [Proving it, and handling health data responsibly](10-proving-it-and-health-data.md) | `phase-10`, `phase-11` | Does it hold up against inputs we never thought of, and can users take back their data? |

## Who this is for

Developers who can write a Go HTTP handler and want to know what separates it
from a service they would trust with other people's data. No security background
is assumed; every attack is explained before it is defended against.

## Running the code

Every tag from `phase-04` on runs locally with no accounts anywhere:

```sh
cp .env.example .env
docker compose up --build
```

Go 1.27 and Docker are the only requirements.
