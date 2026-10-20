# Argos

A Telegram bot that keeps track of what you spend: send it a receipt — a photo, a PDF, or a line
of text — and it extracts the purchase with Claude and stores it in SQLite.

**Status: phase 1.** It runs in Docker, answers the users in its allowlist, and keeps its
database in `data/argos.db` — `/categorias` reads it. Reading receipts and recording expenses
come in the next phases.

## Running it

### 1. @BotFather

1. `/newbot` → a name and a username. The token it answers with is `TELEGRAM_BOT_TOKEN`.
   **It must be a bot of its own**: the Nautilus bot's token will not do, since two processes
   long-polling one token fail with `409 Conflict`.
2. `/setjoingroups` → **Disable**. Expenses are personal, so the bot answers only private chats.
3. Your numeric user id — [@userinfobot](https://t.me/userinfobot) tells you — is
   `TELEGRAM_ALLOWED_USER_IDS`.

### 2. Docker

```sh
git clone <this repo> argos && cd argos
cp .env.example .env && chmod 600 .env    # fill in the two variables
mkdir -p data && sudo chown 1000:1000 data
docker compose up -d --build
docker compose logs -f argos
# [argos] telegram bot running for 1 user(s), data in /data
```

Send it `/start`. A user not in the allowlist gets no reply at all — a reply would confirm the
bot exists — and the log names who was ignored.

The container runs as `1000:1000`, read-only, with `./data` as its only writable path; that is
where the database, the receipts and the backups will live, and what to back up. If `./data`
on the host belongs to another user, set `PUID`/`PGID` in `.env`. Docker creates a missing
`./data` as root, which the bot cannot write to — it says so on startup instead of failing later.

To update: `git pull && docker compose up -d --build`. The schema migrates itself on startup,
and a database written by a newer build is refused rather than opened. `make db-migrate` does
only that step — builds the image, migrates `data/argos.db` in a one-off container as the bot's
user, and says what it applied — so you can migrate before the bot starts, or just check the
schema version:

```
[argos] database /data/argos.db: applied 0001_init, now at schema version 1
[argos] database /data/argos.db: at schema version 1, nothing to apply
```

Outside Docker, the same is `ARGOS_DATA_DIR=./data go run ./cmd/argos migrate`.

Until backups arrive, stop the container before copying `data/`: the database runs in WAL mode,
and a copy taken while it writes can come out inconsistent. `make db-copy` is the exception.

### Looking inside the database

The image has no shell, so `docker exec` will not reach it; the file is on the host, in
`data/argos.db`. Both targets need `sqlite3` on the host and work whether the bot is running or not:

```sh
make db                          # sqlite3 refusing writes: .tables, .schema expenses, SELECT …
make db-copy                     # a consistent, single-file copy in /tmp/argos-copy.db, for a GUI or scp
make db-copy DB_COPY=~/argos.db  # somewhere else
```

Run them as the owner of `data/`: a root `sqlite3` on a stopped bot can leave `-wal`/`-shm`
files the container can no longer open.

#### In a browser

[sqlite-web](https://github.com/coleifer/sqlite-web) is in `compose.yml` as an optional service:
tables, schema, a SQL editor and row editing, behind a password. It is a profile, so
`docker compose up` leaves it alone and only this starts it:

```sh
echo "SQLITE_WEB_PASSWORD=$(openssl rand -hex 16)" >> .env   # the password is then in .env
make db-web          # http://127.0.0.1:8081 — and again after every reboot, until make db-web-stop
```

It listens on `127.0.0.1` only. From another machine, either tunnel to it —
`ssh -L 8081:127.0.0.1:8081 server`, then `http://127.0.0.1:8081` there — or set
`SQLITE_WEB_ADDR` to this host's Tailscale address. Never `0.0.0.0`: that is every expense,
on every network the host is on, behind one password.

Without a password it refuses to start rather than open unprotected, which is why `make db-web`
checks first. It runs as the bot's user for the same reason as above: it creates `-wal` and
`-shm` when they are missing.

It writes: an edit there is a change to the data the bot reads, made without the bot. The schema
still holds — foreign keys are on, so deleting a user who has expenses fails, and a status or
payment method outside the allowed values is refused — but nothing the bot does on its own
happens, such as `updated_at` or `confirmed_at` being set. `make db-copy` first is cheap.

### Without Docker

```sh
cp .env.example .env    # fill it in
make run                # builds bin/argos and keeps its data in ./data
```

The `.env` is looked for beside the executable, one directory above it, then in the working
directory. A variable already set in the environment always wins over the file.

## Configuration

| Variable | Default | |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | — | **required**, a bot of its own |
| `TELEGRAM_ALLOWED_USER_IDS` | — | **required**, numeric ids, comma-separated |
| `ARGOS_DATA_DIR` | `/data` | the volume in `compose.yml`; `make run` sets `./data` |
| `PUID` / `PGID` | `1000` | Docker only: the owner of `./data` on the host |
| `SQLITE_WEB_PASSWORD` | — | **required** by `make db-web` |
| `SQLITE_WEB_ADDR` / `SQLITE_WEB_PORT` | `127.0.0.1` / `8081` | where `make db-web` listens on the host |

Every problem is reported at once, on startup:

```
[argos] configuration:
  - TELEGRAM_BOT_TOKEN is not set: ...
  - ARGOS_DATA_DIR=/data is not writable (permission denied): ...
```

## The database

SQLite, through a pure Go driver, so the binary needs no cgo and the image builds for any
architecture. The schema is the list in `internal/store/migrations.go`, applied in order and
tracked in `PRAGMA user_version`. A change to it is a new entry at the end — `0002_…`, then
`0003_…` — never an edit to one already released: a database that has applied `0001_init` will
not apply it again. The numbers are checked on startup, so a gap or a reordering stops the bot
instead of skipping a step.

| Table | Holds |
| --- | --- |
| `users` | everyone who has used the bot: their Telegram id, and the name and username their latest message carried |
| `expenses` | one purchase: when, where, category, payment method, total in cents, and its status — `pending` until you confirm it, then `confirmed` or `discarded` |
| `expense_items` | the lines of a receipt, in order |
| `attachments` | the files received, by sha256, so the same file is kept once |
| `extractions` | every call to the model: its raw answer, tokens and estimated cost |
| `categories` | the categories offered, seeded by the first migration |

Who may use the bot is still `TELEGRAM_ALLOWED_USER_IDS`; `users` records who did, and every
expense and extraction belongs to one of them. Removing someone from the list stops them without
touching what they recorded.

Every table but `categories` has an `id` that SQLite hands out in sequence, with
`AUTOINCREMENT`, so no id is ever handed out twice — not even after a delete, which matters
because ids end up in the buttons of old messages. The Telegram id is a column of `users`, not
its key.

Money is integer cents, never a float. The day of a purchase is the local date printed on the
receipt (`spent_on`), with its time separately when it has one. The same file, or the same
NFC-e access key, cannot be recorded twice unless the first record was discarded.

## Project layout

```
cmd/argos/           entrypoint: .env loading, the database, signal handling; argos migrate
internal/dotenv/     reads a .env into the environment before the configuration is checked
internal/store/      SQLite — migrations, expenses, reports; knows nothing of Telegram
internal/telegram/   the bot — configuration, authorisation, commands
```

```sh
make test
```
