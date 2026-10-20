.PHONY: build test run up down logs db db-copy db-check db-migrate db-web db-web-stop

DB ?= data/argos.db
DB_COPY ?= /tmp/argos-copy.db

build:
	go build -o bin/argos ./cmd/argos

test:
	go test ./...

run: build
	ARGOS_DATA_DIR=$(CURDIR)/data ./bin/argos

up:
	docker compose up -d --build

down:
	docker compose --profile db-web down

logs:
	docker compose logs -f argos

db: db-check
	sqlite3 -header -column -cmd "PRAGMA query_only = ON" $(DB)

db-copy: db-check
	sqlite3 -cmd "PRAGMA query_only = ON" $(DB) ".backup '$(DB_COPY)'"
	sqlite3 $(DB_COPY) "PRAGMA journal_mode = DELETE" >/dev/null
	@rm -f '$(DB_COPY)-wal' '$(DB_COPY)-shm'
	@echo "copied to $(DB_COPY)"

db-check:
	@command -v sqlite3 >/dev/null || { echo "sqlite3 is not installed (sudo apt install sqlite3)"; exit 1; }
	@test -f $(DB) || { echo "$(DB) does not exist yet: start the bot first"; exit 1; }

db-migrate:
	docker compose run --rm --build argos migrate

db-web:
	@[ -n "$$SQLITE_WEB_PASSWORD" ] || grep -qE '^SQLITE_WEB_PASSWORD=.+' .env 2>/dev/null || \
		{ echo "set SQLITE_WEB_PASSWORD in .env first"; exit 1; }
	docker compose --profile db-web up -d sqlite-web

db-web-stop:
	docker compose --profile db-web stop sqlite-web
