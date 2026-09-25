# Part 2: Pluggable authentication

*From CRUD to Hardened, part 2 of 10. Code: `git diff phase-01..phase-02`.*

Until now, anyone who could reach the API could be anyone. This part adds
**authentication**, which answers "who is calling?". Deciding *what they may touch*
is authorization, and that's the next part. Keeping the two separate is the first
design lesson here.

## The problem

Authentication means verifying a credential. For a mobile app today that credential
is almost always a **JWT**: a signed token issued by an identity provider such as
Firebase, Amazon Cognito, Auth0 or Keycloak. The Flutter app signs the user in with
the provider's SDK and sends the token with every request:

```http
Authorization: Bearer eyJhbGciOiJSUzI1NiIsImtpZCI6...
```

The API has to check that token. This is where many services go wrong, because a
JWT carries its own instructions for how to verify it, and trusting those
instructions is the classic mistake:

- **`alg: none`.** The token declares itself unsigned, and a careless library
  accepts it.
- **Key confusion.** The token claims `HS256` (a shared-secret algorithm), and the
  verifier uses the provider's *public* key as the secret. Since the attacker
  also has the public key, they can forge any token.
- **Missing audience check.** A valid token that the same provider issued for *a
  different app* gets accepted.
- **Expiry ignored or clocks wrong.** Stolen tokens work forever.

Then there's a product problem. This repository is a tutorial that people deploy
themselves. One reader uses Firebase, another Cognito, a third runs Keycloak.
**The authentication method has to be a setting, not a code change.**

## The design

Three decisions shape everything:

1. **Providers only answer "who is this?"** Authorization stays in the core, so a
   buggy provider can at worst misidentify someone. It can never grant access to
   another user's data.
2. **Selecting a provider is configuration.** `AUTH_PROVIDER=firebase` plus a few
   settings. There is **no default**: if `AUTH_PROVIDER` is missing or wrong, the
   service refuses to start. A forgotten setting must never mean "no
   authentication".
3. **Unusual providers are compile-time plugins.** LDAP, SAML bridges and
   in-house identity systems are added with an import and a rebuild, not with Go's
   fragile runtime `plugin` package.

### The contract

The contract is a separate Go module that uses only the standard library, so a
provider written in another repository doesn't inherit our dependencies:

```go
// Identity is a caller vouched for by a provider. It says nothing about permissions.
type Identity struct {
	Issuer  string // who vouched for the caller
	Subject string // the issuer's stable ID for the caller
	Email   string
	EmailVerified bool
}

var ErrUnauthenticated = errors.New("auth: missing or invalid credentials")

type Authenticator interface {
	Authenticate(r *http.Request) (Identity, error)
}

type Factory func(ctx context.Context, deps Deps) (Authenticator, error)

func Register(name string, f Factory) // panics on duplicates, like database/sql
```

`ErrUnauthenticated` becomes a 401. **Any other error becomes a 503.** The
difference matters: "your token is bad" and "we can't reach Google to fetch keys
right now" deserve different answers, and only the first should make a client log
the user out.

Registration is the `database/sql` driver pattern. A provider registers itself in
an `init` function, and one file lists what's compiled in:

```go
// cmd/api/plugins.go
import (
	_ "github.com/perdhevi/latihanAPI/internal/authn/cognito"
	_ "github.com/perdhevi/latihanAPI/internal/authn/firebase"
	_ "github.com/perdhevi/latihanAPI/internal/authn/oidc"
)
```

To add a provider from another repository: `go get` it, add one import line,
rebuild the image.

### One verifier, three presets

Firebase, Cognito and generic OpenID Connect (OIDC) all issue standard JWTs signed
with keys published as a **JWKS** (a JSON document of public keys). So there is
**one** verifier, and each provider is a small preset that fills in its settings:

```go
func Config(project string) jwks.Config { // Firebase
	return jwks.Config{
		Issuer:     "https://securetoken.google.com/" + project,
		Audience:   project,
		JWKSURL:    "https://www.googleapis.com/service_accounts/v1/jwk/securetoken@system.gserviceaccount.com",
		Algorithms: []string{"RS256"},
	}
}
```

The verifier refuses to let the token pick its own verification rules:

```go
opts := []jwt.ParserOption{
	jwt.WithValidMethods(cfg.Algorithms), // an explicit list; the token cannot widen it
	jwt.WithIssuer(cfg.Issuer),
	jwt.WithExpirationRequired(),
	jwt.WithIssuedAt(),
	jwt.WithLeeway(30 * time.Second),
}
```

It also checks, **independently of the JWT library**, that the key type matches
the algorithm. An RS256 token must be verified with an RSA key, never with bytes
that happen to be an RSA key's text:

```go
switch {
case strings.HasPrefix(alg, "RS"), strings.HasPrefix(alg, "PS"):
	if _, ok := k.key.(*rsa.PublicKey); ok {
		return k.key, nil
	}
// ... ES*, EdDSA
}
return nil, errors.New("key type does not match algorithm")
```

A few more details, each closing a specific hole:

- **Only HTTPS key URLs.** An attacker in the network path could otherwise swap
  the keys we fetch.
- **An 8 KiB token cap**, so no parsing work happens on huge inputs.
- **Key caching with limits.** Keys are refreshed hourly. A token with an unknown
  key ID triggers a refetch **at most once a minute**, otherwise every garbage
  token would make us call Google. If the key server is down, the last good keys
  keep working.
- **A Cognito trap.** Cognito *access* tokens have no `aud` claim. They name the
  app in `client_id`, and `token_use` says whether it's an access or an ID token.
  The preset checks both, so an ID token can't be replayed where an access token
  is expected. Many tutorials get this wrong.
- **OIDC discovery** must return exactly the configured issuer, so a misconfigured
  or spoofed discovery endpoint can't redirect where we fetch keys from.

### Linking identities to users

Providers identify users with their own IDs: a Firebase UID, a Cognito `sub`. Our
data uses our own UUIDs. A new table links them:

```sql
CREATE TABLE user_identities (
    issuer TEXT NOT NULL, subject TEXT NOT NULL,
    user_id UUID NOT NULL REFERENCES user_profiles(id) ON DELETE CASCADE,
    PRIMARY KEY (issuer, subject)
);
```

`POST /api/v1/users` creates a profile and links the caller's identity **in one
transaction**. Every other route requires a linked profile and answers `403
profile_required` without one. A user can have several identities, so switching
providers later means linking a new identity to the same user, not migrating
data.

## How we prove it

A provider that accepts a valid token is only half tested. What matters is that it
rejects every *invalid* one. So the contract module ships a **conformance suite**,
`auth/authtest`. It runs a fake identity provider over TLS and throws the attacks
above at the provider under test:

```text
no credentials · wrong scheme · malformed · expired · not yet valid
issued in the future · missing expiry · wrong issuer · wrong audience
missing audience · missing subject · alg none · HS256 signed with the public key
another key with a trusted kid · unknown kid · tampered payload
```

Every built-in provider runs it, and an external provider's author can run it in
their own CI with a few lines.

**The break-it check.** I removed the audience check from the verifier and ran the
suite. `wrong audience` and `missing audience` failed, as they should. Then I also
let `HS256` and `none` through the algorithm allow-list. The suite **still
rejected** those tokens, because the second, independent layer (key type must
match algorithm) caught them. That's defense in depth, observed rather than
assumed.

Finally, the Firebase preset downloaded and parsed Google's real production keys,
and the Cognito preset reached AWS and correctly rejected a made-up user pool.

## What this doesn't do yet

A logged-in user can now be identified, but **can still read other users' data**
if they know the IDs, because routes still accept a `user_id` from the request. The
README says so explicitly at this tag. That's deliberate: the next part fixes it,
and adds an attack test that proves it.

## Try it

```sh
git checkout phase-02
go test ./auth/... ./internal/authn/...    # the conformance suite, per provider
DATABASE_URL=postgres://localhost/x AUTH_PROVIDER=nonsense go run ./cmd/api
# auth: unknown provider "nonsense" (available: cognito, firebase, oidc)
```

**Next: [Part 3, Authorization: whose data is this?](03-authorization.md)**
