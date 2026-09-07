.PHONY: help build test lint run up down logs verify-sso fmt tidy

help:
	@grep -E '^[a-zA-Z-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

env: ## Create .env from the example without ever clobbering an existing one
	@cp -n .env.example .env && echo "created .env" || echo ".env already exists — left untouched"

build: ## Compile the binary
	go build -o bin/isntustup ./cmd/isntustup

test: ## Run the test suite
	go test ./... -count=1

lint: ## Vet and check formatting
	go vet ./...
	@test -z "$$(gofmt -l cmd internal)" || (echo "unformatted files:"; gofmt -l cmd internal; exit 1)

fmt: ## Format
	gofmt -w cmd internal

tidy: ## Tidy modules
	go mod tidy

run: ## Run locally against a local Postgres
	go run ./cmd/isntustup

up: ## Start the stack
	docker compose up -d --build

down: ## Stop the stack
	docker compose down

logs: ## Follow the app logs
	docker compose logs -f app

verify-sso: ## One deliberate SSO login per service, redacted output
	./scripts/verify-sso.sh
