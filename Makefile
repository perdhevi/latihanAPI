-include .env
export

GO ?= go
MIGRATE ?= go run -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@v4.18.3
GOLANGCI_LINT ?= $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
GOVULNCHECK ?= $(GO) run golang.org/x/vuln/cmd/govulncheck@v1.8.0

.PHONY: run build test fmt vet lint vuln check keygen migrate-up migrate-down docker-up docker-down test-integration seed
# keygen creates the local signing key for AUTH_PROVIDER=jwt (never overwrites).
keygen:
	mkdir -p .keys
	$(GO) run ./cmd/api keygen -out .keys/signing.pem
run:
	$(GO) run ./cmd/api
build:
	$(GO) build -o bin/api ./cmd/api
test:
	$(GO) test ./...
	cd auth && $(GO) test ./...
fmt:
	$(GOLANGCI_LINT) fmt ./...
	cd auth && $(GOLANGCI_LINT) fmt --config ../.golangci.yml ./...
lint:
	$(GOLANGCI_LINT) run ./...
	cd auth && $(GOLANGCI_LINT) run --config ../.golangci.yml ./...
vuln:
	$(GOVULNCHECK) ./...
# check runs every CI gate that needs no database.
check: lint vuln test
vet:
	$(GO) vet ./...
migrate-up:
	$(MIGRATE) -path migrations -database "$(DATABASE_URL)" up
migrate-down:
	$(MIGRATE) -path migrations -database "$(DATABASE_URL)" down 1
docker-up:
	docker compose up --build
docker-down:
	docker compose down
test-integration:
	$(GO) test -count=1 -tags=integration ./...
seed:
	psql "$(DATABASE_URL)" -v ON_ERROR_STOP=1 -f seeds/development.sql
