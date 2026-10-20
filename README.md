# Argos

A Telegram bot that keeps track of what you spend. Send it a receipt — a photo, a PDF or a line
of text — and Claude reads it into a SQLite database. Everything runs in Docker.

## Setup

1. Create a bot with [@BotFather](https://t.me/BotFather) — one of its own, not the Nautilus
   bot's token — and get your numeric user id from [@userinfobot](https://t.me/userinfobot).
2. Configure and start it:

```sh
cp .env.example .env && chmod 600 .env   # TELEGRAM_BOT_TOKEN, TELEGRAM_ALLOWED_USER_IDS, ANTHROPIC_API_KEY
make up
make logs
```

It answers only private chats from the users in the allowlist. To update:
`git pull && make up` — the database migrates itself on startup.

### Without the source

The image is on Docker Hub as [`pedrodl/argos-bot`](https://hub.docker.com/r/pedrodl/argos-bot),
for amd64 and arm64, with its healthcheck. In an empty folder:

```sh
mkdir -p config/argos/data
curl -fsSL -o config/argos/.env https://raw.githubusercontent.com/DeLucca990/argos-finance-bot/main/.env.example
chmod 600 config/argos/.env   # fill it in as above
```

and a `compose.yml` beside `config/`. On a Linux server the data can be a plain folder. `user` is
the one that owns it (`id -u`:`id -g`, 1000 for Ubuntu's first user): the image runs as 65532,
and cannot write to a folder that Docker made for root.

```yaml
services:
  argos:
    image: pedrodl/argos-bot:latest
    container_name: argos
    user: "1000:1000"
    env_file: ./config/argos/.env
    volumes:
      - ./config/argos/data:/data
    restart: unless-stopped
```

The database, the receipts and the daily backups are then files in `config/argos/data` — copy
`backups/`, not the live `argos.db`. A bot already running on the `argos-data` volume moves there,
stopped, with:

```sh
docker run --rm -v argos-data:/from -v "$PWD/config/argos/data":/to alpine \
  sh -c "cp -a /from/. /to/ && chown -R $(id -u):$(id -g) /to"
```

On a Mac or Windows a shared folder does not keep SQLite's file locks ([Data](#data)): leave out
`user` and use a volume.

```yaml
    volumes:
      - argos-data:/data

volumes:
  argos-data:
    name: argos-data
```

`docker compose up -d` starts it and `docker compose pull && docker compose up -d` updates it.
Without a `compose.yml`, the same with a volume is
`docker run -d --name argos --env-file config/argos/.env -v argos-data:/data --restart unless-stopped pedrodl/argos-bot:latest`.
`:1`
follows every release of 1.x; `:1.2.3` pins one, `:latest` is the newest release and `:edge`
the newest commit on `main`.

## Using it

**Send a receipt** — a photo, an image or PDF file, or text with a number in it (*almoço 35,90 no
pix*). It is saved right away and answered with a card:

```
🧾 Drogaria Boa Saúde
📅 19/10/2026 às 10:15 · 💳 Débito · Nubank ···1234
💊 Farmácia
💰 R$ 42,30
✅ Salvo.
[ 🏷️ Categoria ] [ ❌ Descartar ]
```

- **Correct it** by replying to the card: *foi 45,90 no crédito*.
- **Ask** in plain words: *quanto gastei com mercado em setembro?* A `?` or a question word
  (quanto, qual, quando…) makes it a question rather than a purchase.
- The same file, or the same NFC-e, sent twice is recognised. Doubts — a CNPJ that does not
  check, items that do not add up to the total, an odd date — show on the card.

| Command | |
| --- | --- |
| `/hoje`, `/semana`, `/mes [mês]` | totals for the period, by category |
| `/ultimos [n]` | the latest expenses |
| `/resumo [mês]` | the monthly digest — also sent on the 1st |
| `/exportar [mês \| tudo]` | a CSV for spreadsheets |
| `/desfazer` | discards the expense saved last |
| `/categorias`, `/cost`, `/help` | the categories, what Claude has cost, the help |

A month is `2026-10`, `10/2026` or `outubro`.

## Configuration

`.env`, read at startup:

| Variable | Default | |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | — | **required** |
| `TELEGRAM_ALLOWED_USER_IDS` | — | **required**, comma-separated |
| `ANTHROPIC_API_KEY` | — | **required** |
| `ARGOS_MODEL` / `ARGOS_EFFORT` | `claude-opus-5-5` / `low` | the model that reads and answers |
| `ARGOS_DAILY_BUDGET_USD` | — | the most a day of Claude may cost |
| `ARGOS_DIGEST_AT` | `09:00` | when the monthly digest goes out; `off` for never |
| `ARGOS_BACKUP_KEEP` | `14` | daily backups to keep; `0` for none |
| `ARGOS_DELETE_RECEIPTS` | `false` | `true` keeps no receipt files, and deletes the ones kept before |
| `TZ` | `America/Sao_Paulo` | the time zone of dates, `/hoje` and the digest |
| `SQLITE_WEB_PASSWORD` | — | required by `make db-web` |

## Data

Everything is in the Docker volume `argos-data`, mounted at `/data`: the database `argos.db`,
the receipt files and the daily backups. It is a volume rather than a folder of the clone
because SQLite needs file locks that a folder shared from a Mac into Docker does not keep.

```sh
make db                # the sqlite3 shell on the live database
make db SQL="SELECT …" # one query
make db-copy           # a consistent copy in /tmp/argos-copy.db
make db-backups        # the daily backups, copied into ./backups
make db-web            # sqlite-web at http://127.0.0.1:8081
make down              # stops everything and keeps the volume
```

## Cost

Every reading, correction and question is one call to Claude, recorded with its cost; `/cost`
adds them up. A receipt costs around two cents on Opus 5.5. To see whether a cheaper model reads
your own receipts as well, put some in `testdata/receipts/` (kept out of git) and:

```sh
make eval-init                       # writes the answer key — correct it by hand
make eval MODEL=claude-sonnet-5-5    # scores each field
```

How each part works, and why: [docs/DETAILS.md](docs/DETAILS.md).
