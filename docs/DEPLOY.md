# Deploying to a VM

One Linux VM running Docker Compose, with images built, scanned and signed by
GitHub Actions and deployed only after the signature checks out.

```text
git tag v1.2.3 ─► Release workflow: build ─► push to GHCR (SBOM + provenance)
                                     ─► Trivy scan ─► cosign sign (keyless)
                 ─► approve "production" ─► ssh deploy@vm deploy/deploy.sh <commit> <image@digest>
VM: verify signature ─► checkout commit ─► migrate ─► start ─► wait for health checks
```

These steps were written for Ubuntu 24.04 LTS. Commands marked `#` run as root.

## 1. Baseline the VM

```sh
# adduser --disabled-password deploy
# install -d -m 700 -o deploy -g deploy /home/deploy/.ssh
# nano /home/deploy/.ssh/authorized_keys   # your key, and the CI deploy key (step 4)
```

SSH: keys only, no root login. Create `/etc/ssh/sshd_config.d/10-hardening.conf`:

```text
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
AllowUsers deploy
```

then `# systemctl reload ssh`, and confirm you can still log in from a second terminal.

Firewall and automatic security updates:

```sh
# ufw allow OpenSSH && ufw allow 80/tcp && ufw allow 443/tcp && ufw allow 443/udp && ufw enable
# apt install unattended-upgrades && dpkg-reconfigure -plow unattended-upgrades
```

Docker publishes ports by writing its own firewall rules, which bypass UFW. That
is why only Caddy publishes ports in `docker-compose.prod.yml`: PostgreSQL, the
API and its admin port are never published, so there is nothing for UFW to miss.

Install Docker Engine from Docker's apt repository
(https://docs.docker.com/engine/install/ubuntu/), then set daemon defaults in
`/etc/docker/daemon.json`:

```json
{
  "log-driver": "json-file",
  "log-opts": { "max-size": "10m", "max-file": "5" },
  "live-restore": true,
  "no-new-privileges": true
}
```

`# systemctl restart docker && usermod -aG docker deploy`. Membership of the
`docker` group is equivalent to root on this machine, so give it only to the
deploy user.

Install cosign from its GitHub release, checking the published checksum:

```sh
# V=v2.4.1; cd /tmp
# curl -fsSLO https://github.com/sigstore/cosign/releases/download/$V/cosign-linux-amd64
# curl -fsSLO https://github.com/sigstore/cosign/releases/download/$V/cosign_checksums.txt
# grep ' cosign-linux-amd64$' cosign_checksums.txt | sha256sum -c -
# install -m 755 cosign-linux-amd64 /usr/local/bin/cosign
```

## 2. Prepare the application (as `deploy`)

```sh
git clone https://github.com/perdhevi/latihanAPI.git && cd latihanAPI
scripts/init-secrets.sh
cp .env.example .env
```

In `.env`: set `SECRETS_DIR=./secrets`, `DOMAIN` (its DNS A/AAAA records must
point at the VM), `ACME_EMAIL`, and your `AUTH_*` settings (with the built-in
provider, `AUTH_JWT_ISSUER=https://<DOMAIN>`). Back up `./secrets` somewhere
safe: losing it means resetting database passwords.

If the GHCR package is private, let the VM pull it with a classic personal
access token that has only `read:packages`:

```sh
docker login ghcr.io -u <github-user>   # paste the token
```

## 3. Configure GitHub

In the repository settings:

- **Environments → production**: add required reviewers, so each deployment
  waits for approval.
- **Environment secrets**:
  - `DEPLOY_HOST`: the VM's address.
  - `DEPLOY_USER`: `deploy`.
  - `DEPLOY_PATH`: `/home/deploy/latihanAPI`.
  - `DEPLOY_SSH_KEY`: a private key made only for CI (`ssh-keygen -t ed25519 -f ci_deploy`).
  - `DEPLOY_KNOWN_HOSTS`: the VM's host key line. Get it with `ssh-keyscan <host>`
    and compare its fingerprint with `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`
    on the VM; never accept a host key unchecked.
- **Variables**: `DEPLOY_ENABLED=true`.

On the VM, add `ci_deploy.pub` to `~deploy/.ssh/authorized_keys` prefixed with
`restrict ` (no port forwarding, agent forwarding or terminal for that key).

## 4. Release

```sh
git tag v0.1.0 && git push origin v0.1.0
```

The Release workflow builds and pushes the image, attaches its SBOM and
provenance, fails on fixable HIGH or CRITICAL vulnerabilities, signs the image,
then waits for approval and runs `deploy/deploy.sh` on the VM. That script
refuses anything not pinned by digest or not signed by this repository's
release workflow for a `v*` tag, so neither a pushed-but-unscanned image nor
an image from another source can be deployed.

## Rollback

`deploy/.previous` holds the commit and image of the release before the current
one:

```sh
deploy/deploy.sh $(cat deploy/.previous)
```

Migrations only move forward, so roll back only to a release that works with
the current schema, or restore a backup.
