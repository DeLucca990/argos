# Argos

A Telegram bot that keeps track of what you spend: send it a receipt — a photo, a PDF, or a line
of text — and it extracts the purchase with Claude and stores it in SQLite.

**Status: phase 2.** It reads receipts and records them as they arrive. Reports — `/hoje`,
`/mes`, `/exportar` and the rest — come in phase 3.

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
cp .env.example .env && chmod 600 .env    # fill in the three variables
docker compose up -d --build
docker compose logs -f argos
# [argos] telegram bot running for 1 user(s), data in /data, reading receipts with claude-opus-5-5 at effort low
```

Send it `/start`. A user not in the allowlist gets no reply at all — a reply would confirm the
bot exists — and the log names who was ignored.

The container runs read-only, as the image's unprivileged user (65532), and its only writable
path is `/data`: the **`argos-data` Docker volume**, which holds the database and the receipts
and is what to back up. Nothing on the host needs creating or `chown`ing — the image ships
`/data` owned by its user, and Docker gives that ownership to the volume the first time the bot
mounts it.

It is a volume, not a folder of the clone, on purpose. SQLite needs file locks that hold between
every process that opens the database, and a folder shared from a Mac into Docker Desktop does
not keep them between two containers: with the bot and sqlite-web both on `./data`, sqlite-web
closing the database deleted the bot's `-wal` and, with it, the expenses not yet copied into
`argos.db`. Inside a volume, every container is on the same Linux filesystem, on a Mac as on the
server.

To update: `git pull && docker compose up -d --build`. The schema migrates itself on startup,
and a database written by a newer build is refused rather than opened. `make db-migrate` does
only that step — builds the image, migrates `argos.db` in a one-off container as the bot's
user, and says what it applied — so you can migrate before the bot starts, or just check the
schema version:

```
[argos] database /data/argos.db: applied 0001_init, now at schema version 1
[argos] database /data/argos.db: at schema version 1, nothing to apply
```

Outside Docker, the same is `ARGOS_DATA_DIR=./data go run ./cmd/argos migrate`.

To back it up while the bot runs, `make db-copy` (below): the database runs in WAL mode, and
copying its files by hand while it writes can come out inconsistent.

### Looking inside the database

The bot's image has no shell, and the database is in the volume rather than on the host, so
both targets run in a one-off container of the sqlite-web image, which has Python's SQLite — on
the volume, as the bot's user. They need nothing installed on the host and work whether the bot
is running or not:

```sh
make db                               # an SQL prompt on the live database: SELECT …, .tables
make db-copy                          # a consistent, single-file copy in /tmp/argos-copy.db, for a GUI or scp
make db-copy DB_COPY=$HOME/argos.db   # somewhere else
```

`make db` is Python's `python3 -m sqlite3`, which prints rows as tuples, and it can write: what
is typed there changes the data the bot reads. Both refuse to run before the bot has created
the database, rather than create an empty one.

#### In a browser

[sqlite-web](https://github.com/coleifer/sqlite-web) is in `compose.yml` as an optional service
on the same `argos-data` volume: tables, schema, a SQL editor and row editing, behind a password.
It is a profile, so `docker compose up` leaves it alone and only this starts it, after the bot:

```sh
echo "SQLITE_WEB_PASSWORD=$(openssl rand -hex 16)" >> .env   # the password is then in .env
make db-web          # http://127.0.0.1:8081 — and again after every reboot, until make db-web-stop
```

It listens on `127.0.0.1` only. From another machine, either tunnel to it —
`ssh -L 8081:127.0.0.1:8081 server`, then `http://127.0.0.1:8081` there — or set
`SQLITE_WEB_ADDR` to this host's Tailscale address. Never `0.0.0.0`: that is every expense,
on every network the host is on, behind one password.

Without a password it refuses to start rather than open unprotected, which is why `make db-web`
checks first. It runs as the bot's user, 65532, because it creates `-wal` and `-shm` when they
are missing, and files of another owner would lock the bot out of its own database.

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

## Sending it receipts

Three things count as a receipt, in a private chat with the bot:

- **a photo** — a cupom fiscal, a card slip, a Pix confirmation, a screenshot from an app;
- **a file** — an image (JPEG, PNG, WebP, GIF) or a PDF, such as a nota fiscal or a bank statement page;
- **a line of text** with a number in it — *almoço 35,90 no pix ontem*. Text with no number gets
  the help instead, so a stray "oi" costs nothing.

A caption or the text itself can steer it: *farmácia*, *no crédito*, *foi ontem* win over what the
model would infer. The bot answers "🔎 Lendo…" and turns that message into a card:

```
🧾 Drogaria Boa Saúde
CNPJ 11.222.333/0001-81
📅 19/10/2026 às 10:15 · 💳 Débito
💊 Farmácia
💰 R$ 42,30

• Dipirona 500mg — 2 × R$ 9,90 = R$ 19,80

⚠️ A soma dos itens (R$ 19,80) é diferente do total (R$ 42,30): pode ser desconto, …

✅ Salvo.
leitura: ≈ US$ 0,0400 · claude-opus-5-5
[ 🏷️ Categoria ] [ ❌ Descartar ]
```

It is saved as soon as it is read — there is no confirmation step. **Categoria** swaps the
buttons for the categories; **Descartar** keeps the record but leaves it out of every total,
and frees the receipt to be sent again, which is the way to fix a misread one for now.

What the card warns about is checked in code, not by the model: a CNPJ or CPF whose check
digits do not match (alphanumeric CNPJs included), an NFC-e access key that does not, items that
do not add up to the total, a date in the future or over a year old, a currency other than reais,
and no date at all — then today is used. What the model itself was unsure of comes as 📝.

**The same receipt twice** is caught two ways: the same file by its sha256, before any model is
called, and the same purchase photographed again by the 44-character NFC-e access key printed on
it. Once the first one is discarded, sending it again works.

**How it is read.** One request to Claude per receipt, with the image or PDF and a JSON schema the
answer must follow (structured outputs), so there is no parsing of prose. Images bigger than the
model can see — 2576 px on the long edge — are scaled down first, and a phone photo sent as a file
is turned upright by its EXIF orientation, which the model does not read on its own. HEIC is not
accepted yet: send it as a photo, which Telegram converts. Amounts become integer cents in Go,
never floats. Up to two receipts are read at once; the rest wait their turn.

**What it costs** is on every card, and every call — answered, refused, failed or not a receipt
at all — is a row in `extractions` with its tokens and estimated dollars, at list prices. A photo
of a receipt runs around US$ 0,04 on `claude-opus-5-5`; `ARGOS_MODEL=claude-sonnet-5-5` halves
that and `claude-haiku-5-5` costs a fraction of a cent, at some loss of accuracy on hard receipts.

The files themselves stay in `/data/receipts/`, in the volume, named by their sha256.

## Configuration

| Variable | Default | |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | — | **required**, a bot of its own |
| `TELEGRAM_ALLOWED_USER_IDS` | — | **required**, numeric ids, comma-separated |
| `ANTHROPIC_API_KEY` | — | **required**: receipts are read by Claude (`ANTHROPIC_AUTH_TOKEN` also works) |
| `ARGOS_MODEL` | `claude-opus-5-5` | the model that reads them |
| `ARGOS_EFFORT` | `low` | `low`, `medium`, `high`, `xhigh` or `max`: how much it thinks before answering |
| `ARGOS_DATA_DIR` | `/data` | where the `argos-data` volume is mounted; `make run`, outside Docker, sets `./data` |
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
| `expenses` | one purchase: when, where, category, payment method, total in cents, and its status — `confirmed` once read, `discarded` when you discard it; `pending` is for an expense still awaiting a decision, which the bot does not create today |
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
internal/receipt/    what a receipt is: amounts in cents, CNPJ/CPF/NFC-e checks, the warnings; imports nothing
internal/extract/    Claude: the schema, the request, images made readable, what each call cost
internal/store/      SQLite — migrations, expenses, reports; knows nothing of Telegram or Claude
internal/telegram/   the bot — configuration, authorisation, the card and its buttons, commands
```

```sh
make test
```
