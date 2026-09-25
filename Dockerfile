FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
# The auth contract is a separate module wired in with a replace directive.
COPY auth ./auth
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -S app && adduser -S -G app app
COPY --from=build /out/api /usr/local/bin/api
USER app:app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
