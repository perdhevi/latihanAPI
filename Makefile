-include .env
export

GO ?= go
MIGRATE ?= go run -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@v4.18.3

.PHONY: run build test fmt vet migrate-up migrate-down docker-up docker-down test-integration seed
run:
	$(GO) run ./cmd/api
build:
	$(GO) build -o bin/api ./cmd/api
test:
	$(GO) test ./...
fmt:
	gofmt -w cmd internal
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
