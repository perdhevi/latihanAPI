# Base images are pinned by digest: a tag can be re-pointed, a digest cannot.
# Dependabot proposes digest updates.
FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
WORKDIR /src
COPY go.mod go.sum ./
# The auth contract is a separate module wired in with a replace directive.
COPY auth ./auth
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/api ./cmd/api \
    && mkdir -p /out/keys

# distroless/static: CA certificates and time zones, no shell, no package
# manager. Runs as the unprivileged "nonroot" user (65532).
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/api /usr/local/bin/api
# /keys belongs to nonroot so a new Docker volume mounted there is writable by
# the keygen job (Docker copies the ownership into empty named volumes).
COPY --from=build --chown=nonroot:nonroot --chmod=0700 /out/keys /keys
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
