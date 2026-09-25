FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
# The auth contract is a separate module wired in with a replace directive.
COPY auth ./auth
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/api ./cmd/api

FROM alpine:3.22
# /keys belongs to the app user so a new Docker volume mounted there is writable
# by the keygen job (Docker copies the ownership into empty named volumes).
RUN apk add --no-cache ca-certificates && addgroup -S app && adduser -S -G app app \n    && install -d -o app -g app -m 0700 /keys
COPY --from=build /out/api /usr/local/bin/api
USER app:app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
