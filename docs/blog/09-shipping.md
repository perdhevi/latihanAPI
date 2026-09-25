# Part 9: Shipping signed, scanned images

*From CRUD to Hardened, part 9 of 10. Code: `git diff phase-08..phase-09`.*

Eight parts hardened the code. Now look at the path from a commit to the server:
build, push, pull, run. Each step is a place to swap in something else. Attackers
increasingly target that path, the **software supply chain**, because
compromising one build reaches every deployment.

The question for this part: **how do we know the image running in production is
the code we reviewed, built by our pipeline, and free of known vulnerabilities?**

## Problem 1: the image carries more than the app

The old runtime image was Alpine Linux: a shell, a package manager, and dozens of
utilities. The app needs none of them. An attacker who gets code execution in the
container needs all of them, because they're how you download tools and pivot.

**Fix: a distroless image.** `gcr.io/distroless/static` contains CA certificates,
time zone data and our binary. No shell, no package manager. It runs as an
unprivileged user (UID 65532) and weighs about 35 MB.

No shell broke one thing: the Compose health check ran `wget`. Instead of adding
`wget` back, the binary checks itself:

```go
// commands are one-off tasks run with the same binary, since the runtime image
// has no shell: api keygen, api healthcheck.
var commands = map[string]func(args []string) error{
	"keygen":      keygen,
	"healthcheck": func([]string) error { return healthcheck(os.Getenv("HTTP_ADDR")) },
}
```

**Every image is pinned by digest.** That covers the Go build image, the runtime
image, PostgreSQL, Caddy and the rest:

```dockerfile
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
```

A tag like `:nonroot` can be re-pointed at a new image tomorrow; a digest can't.
Dependabot proposes digest updates, so pinning doesn't mean going stale.

## Problem 2: vulnerabilities in the whole image

Part 1's `govulncheck` looks at Go code and reports only what our code calls. That
focus is its strength and its blind spot.

**Fix: scan the built image with [Trivy](https://trivy.dev)**, which checks every
module in the binary and every OS package. CI scans every build, and the release
fails on any **fixable** HIGH or CRITICAL vulnerability.

On its very first run it found a real one:

```text
google.golang.org/grpc  CVE-2026-84445  HIGH  fixed  v1.83.1  → 1.82.2, 1.83.2, 1.85.0-dev
gRPC-Go: Denial of Service
```

`grpc` wasn't something we chose. It came in indirectly with the OpenTelemetry
exporter from part 7. `govulncheck` didn't flag it because its analysis didn't see
a path from our code. Trivy did. Two scanners, two different blind spots.

The fix had a twist. Upgrading to the newest release, 1.84.0, **didn't fix it**:
that release predates the fix on its own line. The fixed version is 1.83.2, so we
pinned exactly that. If Dependabot later proposes 1.84.0, the same Trivy gate will
block it. "Upgrade to latest" isn't the same as "upgrade to fixed".

## Problem 3: proving where an image came from

A registry holds images. It doesn't tell you *who built them*. If an attacker can
push to the registry, or trick a server into pulling a look-alike, the server runs
their code.

**Fix: sign every release image, and refuse unsigned images at deploy time.**

The [Release workflow](../../.github/workflows/release.yml) runs on `v*` tags:

1. **Build and push to GHCR**, with an SBOM (a list of everything inside the image)
   and SLSA provenance (a signed record of how it was built) attached.
2. **Scan with Trivy.** Stop here on a fixable HIGH or CRITICAL.
3. **Sign with [cosign](https://docs.sigstore.dev), keyless.** There's no private
   key to steal. GitHub vouches for the workflow's identity through OIDC, and the
   signature records exactly which repository, workflow and tag produced the image.

The order matters. An image that fails the scan is pushed but **never signed**. On
the server, [`deploy/deploy.sh`](../../deploy/deploy.sh) only runs images that pass
this check:

```sh
cosign verify \
	--certificate-oidc-issuer https://token.actions.githubusercontent.com \
	--certificate-identity-regexp "^https://github.com/${repository}/\.github/workflows/release\.yml@refs/tags/v" \
	"$image" >/dev/null
```

It also refuses anything not pinned by digest. Then it checks out **the exact commit
the image was built from**, so the Compose files, migrations and database init
scripts match the image. It runs migrations, waits for every health check, and
records the previous release for rollback.

The workflow itself is hardened too:

- actions are pinned by commit;
- permissions are empty by default and granted per job;
- deployment waits for manual approval through a GitHub environment;
- the server's SSH host key is pinned, never trusted on first use.

Two linters check the workflows: `actionlint`, and `zizmor` for security issues.
`zizmor` found two real problems. The CI PostgreSQL image wasn't pinned by digest,
and one step expanded a `${{ }}` template straight into shell. That's harmless with
our constant, but it's the pattern behind a whole class of CI injection attacks.

## Problem 4: the server

The [deployment guide](../DEPLOY.md) covers the VM itself:

- SSH keys only, with no root login;
- a firewall allowing 22, 80 and 443;
- automatic security updates;
- Docker log rotation.

It also covers one trap: **Docker's published ports bypass UFW**. That's why only
Caddy publishes ports; there's nothing else for the firewall to miss.

In production the containers also get memory and PID limits, rotated logs,
`restart: unless-stopped`, and **no build step**: production runs only the signed
image named by digest.

## How we prove it

- **The image:** 35 MB, UID 65532, `docker run --entrypoint /bin/sh` fails (no
  shell), and the full stack passes the smoke test.
- **The health check:** healthy with PostgreSQL up; exit 1 with it stopped.
- **The build:** it produces both attestations, an SPDX SBOM and SLSA v1
  provenance.
- **The production overlay** ran with the distroless image, with the memory, PID
  and log limits all in effect.
- **`deploy.sh`** refuses unpinned images and unsigned images ("no signatures
  found").

**What can't be proven locally:** the Release workflow running for real, and
`deploy.sh` *accepting* a correctly signed image. Both need GitHub and a server, so
the first real test is the first `v*` release. Saying so plainly beats implying a
test that never ran.

## Try it

```sh
git checkout phase-09
docker compose build && docker image inspect latihan-api:local --format '{{.Config.User}} {{.Size}}'
docker save latihan-api:local -o image.tar
docker run --rm -v "$PWD/image.tar:/image.tar:ro" aquasec/trivy:0.74.0 \
  image --input /image.tar --ignore-unfixed --severity HIGH,CRITICAL
```

**Next: [Part 10, Proving it, and handling health data responsibly](10-proving-it-and-health-data.md).**
We test the system against inputs we never imagined, and give users control over
their data.
