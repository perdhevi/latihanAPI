#!/bin/sh
# Deploys one release on this VM:
#
#   deploy/deploy.sh <commit> <image@sha256:digest>
#
# 1. Refuses images that are not pinned by digest.
# 2. Verifies the image was signed by this repository's release workflow for a
#    v* tag (keyless cosign: the signature is tied to the workflow's identity,
#    not to a key that could leak).
# 3. Checks out exactly <commit>, so compose files, migrations and database
#    init scripts match the image.
# 4. Starts the stack and waits for every health check; Compose runs the
#    migrations before the new API starts.
#
# Rollback: run it again with the previous values from deploy/.previous.
# Migrations are forward-only, so roll back code only to a release that works
# with the current schema.
set -eu

if [ $# -ne 2 ]; then
	echo "usage: $0 <commit> <image@sha256:digest>" >&2
	exit 2
fi
commit=$1
image=$2

case $image in
*@sha256:*) ;;
*)
	echo "refusing $image: deploy an image pinned by digest (name@sha256:...)" >&2
	exit 1
	;;
esac

cd "$(dirname "$0")/.."
repository=${DEPLOY_REPOSITORY:-perdhevi/latihanAPI}

echo "verifying signature of $image"
cosign verify \
	--certificate-oidc-issuer https://token.actions.githubusercontent.com \
	--certificate-identity-regexp "^https://github.com/${repository}/\.github/workflows/release\.yml@refs/tags/v" \
	"$image" >/dev/null

echo "checking out $commit"
git fetch --quiet origin
git checkout --quiet --detach "$commit"

if [ -f deploy/.current ]; then
	cp deploy/.current deploy/.previous
fi

export LATIHAN_IMAGE="$image"
compose="docker compose -f docker-compose.yml -f docker-compose.prod.yml"
$compose pull --quiet latihan-api
$compose up --detach --wait --remove-orphans

printf '%s %s\n' "$commit" "$image" >deploy/.current
echo "deployed $image"
