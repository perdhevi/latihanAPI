# Part 4: A built-in token issuer

*From CRUD to Hardened, part 4 of 10. Code: `git diff phase-03..phase-04`.*

After part 3 the API is safe, and inconvenient to try: every reader needs a
Firebase project or a Cognito user pool before the first request. This part adds
email-and-password accounts to the service itself, so `docker compose up` works
with nothing else.

That sounds like the easy part. It's the most dangerous one in the series.
**Storing passwords and issuing tokens is where hand-rolled auth usually fails.**
So every decision here names the attack it prevents.

## The shape: our own identity provider

The built-in provider (`AUTH_PROVIDER=jwt`) behaves exactly like an external one.
It has its own accounts, it issues tokens, and the client then calls `POST
/api/v1/users` to create a profile, just as with Firebase. Accounts live in their
own table, separate from profiles. The token's subject is the account ID, linked
through `user_identities` like any other identity.

This symmetry pays off twice. The core code from parts 2 and 3 doesn't change. And
a deployment can later move to Firebase by linking new identities to the same
users.

| Endpoint | Returns |
| --- | --- |
| `POST /api/v1/auth/register` | a token pair, or `409 email_taken` |
| `POST /api/v1/auth/login` | a token pair, or `401 invalid_credentials` |
| `POST /api/v1/auth/refresh` | a *new* token pair |
| `POST /api/v1/auth/logout` | always `204` |
| `GET /.well-known/jwks.json` | our public keys |

## Tokens: short and verifiable

**Access tokens** are JWTs signed with **Ed25519** and valid for 15 minutes.
Ed25519 keys are small, fast, and have no parameter choices to get wrong. Tokens
are verified by **the same verifier** the external providers use, conformance
suite and all. Nothing new gets trusted.

**The key ID** is the key's RFC 7638 thumbprint, a hash of the public key itself:

```go
func keyID(pub ed25519.PublicKey) string {
	canonical := `{"crv":"Ed25519","kty":"OKP","x":"` + base64.RawURLEncoding.EncodeToString(pub) + `"}`
	sum := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
```

No configuration, and two different keys can never share an ID. A unit test checks
it against the example in RFC 8037.

**Key rotation** is built in from day one. You make the new key
`AUTH_JWT_KEY_FILE` and list the old one in `AUTH_JWT_PREVIOUS_KEY_FILES`. Tokens
signed with the old key keep working until they expire, then you remove it. A test
signs with a key, rotates, and checks the token still verifies. Then it retires
the key and checks the token is rejected.

**`api keygen`** creates the key file with mode `0600` using `O_EXCL`, so it
**can never overwrite** an existing key. That makes it safe to run on every
deployment, which is exactly what Compose does.

## Refresh tokens: catching theft

A 15-minute access token needs a longer-lived way to get new ones: the refresh
token. If one leaks, a 30-day credential is gone. The defense is **rotation with
reuse detection**:

- Refresh tokens are 256 random bits, **stored only as SHA-256 hashes**, so a
  database dump yields nothing usable. A fast hash is fine here: with 256 bits of
  randomness there's nothing to brute-force.
- **Every use replaces the token.** The old one is marked used.
- **Presenting a used token revokes the whole session family**, including the
  token that replaced it.

Why the last rule? If a thief copies a refresh token, then either the thief or the
real user refreshes second, and whoever does presents an already-used token. We
can't tell which one is the thief, but we know theft happened, so both are logged
out and the event is logged.

```go
// FOR UPDATE serializes concurrent uses of one token: the second waits,
// then sees used_at set and is treated as reuse.
err = tx.QueryRowContext(ctx, `SELECT ... FROM refresh_tokens WHERE token_hash=$1 FOR UPDATE`, oldHash)
```

That row lock is what makes reuse detection reliable. Without it, two requests
racing with the same token could both succeed. The integration test runs exactly
that race and requires **one success and one reuse**.

## Passwords: slow on purpose

Passwords are hashed with **argon2id**, a memory-hard algorithm designed so that
guessing on GPUs costs memory as well as compute. The parameters are OWASP's
minimum (19 MiB, 2 passes). Stored hashes use the standard PHC format, which
records the parameters used, so we can raise them later and upgrade each hash the
next time its user logs in.

Memory-hard hashing creates its own denial-of-service risk: 100 simultaneous
logins would need about 1.9 GB of RAM. So hashing runs through a semaphore of four
slots. Excess logins wait in line instead of exhausting memory.

The password rules follow NIST SP 800-63B: **length, not composition**. 12 to 128
characters, no "must contain a symbol". The upper bound caps the hashing work per
request.

## Not helping attackers find accounts

Two small leaks let attackers build a list of valid emails.

**Different answers.** "No such user" versus "wrong password". So an unknown email
and a wrong password get the same response.

**Different timing.** An unknown email returns instantly; a wrong password costs
tens of milliseconds of hashing. So unknown emails hash a dummy password anyway:

```go
if errors.Is(err, errNoCredential) || locked {
	// Same work and same answer as a wrong password: no account enumeration,
	// and a locked account does not confirm that it exists.
	_, _, _ = p.hasher.verify(r.Context(), in.Password, p.hasher.dummy)
	invalidCredentials(w)
	return
}
```

**Lockout.** Five consecutive failures lock the account for 15 minutes. While it's
locked, even the right password gets the same generic 401, because "account
locked" would confirm the account exists.

One leak we **accept and document**: registering an email that's already taken
returns `409`. Hiding that requires an email-verification round trip, and this
provider doesn't send email. That's also why email verification, password reset
and account deletion are listed as reasons to choose an external provider instead.

## A zero-account quickstart

Compose gets a one-shot job that creates the signing key in a volume on first start
and leaves it alone afterwards. The API mounts that volume read-only:

```yaml
jwt-keys:
  image: latihan-api:local
  command: ["keygen", "-out", "/keys/signing.pem"]
  volumes: [jwt-keys:/keys]
```

`.env.example` now selects `AUTH_PROVIDER=jwt`. So the whole tutorial runs with:

```sh
cp .env.example .env && docker compose up --build
```

## How we prove it

- **Unit tests** use an in-memory store and a controllable clock:
  - registration, duplicates and invalid input;
  - lockout, including that a locked account answers exactly like a wrong password;
  - refresh rotation, reuse revoking the family, other sessions surviving;
  - expiry and logout;
  - the published keys matching the token's `kid`;
  - key rotation.
- **Integration tests against PostgreSQL:** the SQL store, including the concurrent
  refresh race.
- **An end-to-end test through the real router:** register, get 403 before a
  profile exists, create a profile, create data, rotate, replay, log out.
- **A live run of the real stack with curl:** after a restart, the key was kept
  and old tokens still worked.

The tests also caught one of *my* mistakes. The settings-validation test passed
every case, but for the wrong reason: each case failed on "no database handle"
before reaching the setting it was meant to test. A test that can't fail proves
nothing. It now uses a real (lazily connected) handle and includes a case that
must *pass*.

## Also in this part: several providers at once

`AUTH_PROVIDER` accepts a list, such as `firebase,jwt`. Each token goes to the
provider whose issuer matches its `iss` claim. That claim is read *unverified*, but
only to pick a verifier, which then checks everything including that same claim.
A forged `iss` only changes which verifier rejects the token. This is how a
deployment migrates between providers without logging everyone out at once.

## Try it

```sh
git checkout phase-04
cp .env.example .env && docker compose up --build
curl -s -X POST localhost:8080/api/v1/auth/register -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"correct horse battery staple"}'
```

**Next: [Part 5, Surviving abuse](05-abuse-resistance.md).** The login endpoint
now does deliberately expensive work on every request. What stops someone from
sending a thousand a second?
