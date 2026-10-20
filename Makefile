.PHONY: build test run up down logs db db-copy db-backups db-migrate db-web db-web-stop eval eval-init

DB_COPY ?= /tmp/argos-copy.db
BACKUPS ?= backups

TOOL = docker compose --profile db-web run --rm --no-deps
SQLITE = docker compose --profile tools run --rm --no-deps -e SQL --entrypoint sh sqlite -c
SQLITE_ARGS = -box -cmd "PRAGMA foreign_keys = ON" /data/argos.db
export SQL
NO_DB = test -f /data/argos.db || { echo 'there is no database yet: start the bot first' >&2; exit 1; }
COPY_DB = import sqlite3, sys; src = sqlite3.connect('/data/argos.db'); dst = sqlite3.connect('/tmp/copy.db'); \
	src.backup(dst); dst.execute('PRAGMA journal_mode = DELETE'); dst.close(); src.close(); \
	sys.stdout.buffer.write(open('/tmp/copy.db', 'rb').read())

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

db:
	@$(SQLITE) 'test -f /data/argos.db || { echo "there is no database yet: start the bot first" >&2; exit 1; }; \
		if [ -n "$$SQL" ]; then exec sqlite3 $(SQLITE_ARGS) "$$SQL"; else exec sqlite3 $(SQLITE_ARGS); fi'

db-copy:
	$(TOOL) -T --entrypoint sh sqlite-web -c "$(NO_DB); exec python3 -c \"$(COPY_DB)\"" > '$(DB_COPY).part'
	@mv '$(DB_COPY).part' '$(DB_COPY)'
	@echo "copied to $(DB_COPY)"

db-backups:
	@mkdir -p '$(BACKUPS)'
	$(TOOL) -T --entrypoint sh sqlite-web -c "test -d /data/backups || { echo 'no backups yet' >&2; exit 1; }; tar -C /data/backups -cf - ." | tar -xf - -C '$(BACKUPS)'
	@echo "copied to $(BACKUPS)/"

db-migrate:
	docker compose run --rm --build argos migrate

db-web:
	@[ -n "$$SQLITE_WEB_PASSWORD" ] || grep -qE '^SQLITE_WEB_PASSWORD=.+' .env 2>/dev/null || \
		{ echo "set SQLITE_WEB_PASSWORD in .env first"; exit 1; }
	docker compose --profile db-web up -d sqlite-web

db-web-stop:
	docker compose --profile db-web stop sqlite-web

eval:
	go run ./cmd/eval $(if $(MODEL),-model $(MODEL)) $(if $(EFFORT),-effort $(EFFORT))

eval-init:
	go run ./cmd/eval -init $(if $(MODEL),-model $(MODEL))
