.PHONY: run build test test-db fmt vet tidy docker up down logs
#
# Running from source and testing, plus shortcuts for the docker compose deploy
# (`make up`). See the README.

# --- Running from source ------------------------------------------------------

ENV_FILE ?= .env

# Reads $(ENV_FILE) exactly as `docker run --env-file` does, so the same file
# behaves the same way in both: one KEY=VALUE per line, value taken literally —
# no quoting, no expansion, and only a whole-line # is a comment.
run:
	@test -f $(ENV_FILE) || { echo "$(ENV_FILE) not found: copy .env.example to $(ENV_FILE) and fill it in"; exit 1; }
	@set -a; while IFS= read -r line; do \
		case "$$line" in ''|\#*) continue;; *=*) export "$$line";; esac; \
	done < $(ENV_FILE); set +a; go run ./cmd/bot

build:
	go build -o bin/bot ./cmd/bot

# --- Tests --------------------------------------------------------------------

test:
	go test ./...

# Integration tests. Point this at a scratch database, not your working one.
# make test-db TEST_DB='postgres://user:pass@localhost:5432/aksha-test'
TEST_DB ?= postgres://postgres:postgres@localhost:5432/aksha-test

test-db:
	TEST_DATABASE_URL="$(TEST_DB)" go test ./internal/storage/ ./internal/scheduler/ ./internal/bot/

fmt:
	gofmt -w .

vet:
	go vet ./...

tidy:
	go mod tidy

# --- The image ----------------------------------------------------------------

IMAGE ?= aksha-bitpesin

docker:
	docker build -t $(IMAGE) .

# --- Deploy (docker compose, settings from .env at run time) -------------------

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f bot
