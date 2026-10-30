# Installing from Docker Hub

The image is [`pedrodl/argos-bot`](https://hub.docker.com/r/pedrodl/argos-bot), for amd64 and
arm64. It needs no source: a `.env` with the settings, and a place for the data.

## Before you start

1. [@BotFather](https://t.me/BotFather): `/newbot`, and the token it answers with is
   `TELEGRAM_BOT_TOKEN`. Each install needs a bot of its own — two processes on one token fail
   with `409 Conflict`. `/setjoingroups` → **Disable**: the bot answers private chats only.
2. [@userinfobot](https://t.me/userinfobot): your numeric id is `TELEGRAM_ALLOWED_USER_IDS`.
3. [platform.claude.com](https://platform.claude.com) → API keys: `ANTHROPIC_API_KEY`. Every
   receipt read is a billed call, around two cents on Opus 5.5.

In the folder the `compose.yml` will go in:

```sh
curl -fsSL -o .env https://raw.githubusercontent.com/DeLucca990/argos-finance-bot/main/.env.example
chmod 600 .env   # fill in the three above; the rest is optional
```

The bot fetches its messages from Telegram, so no port needs opening.

## On a Linux server: a folder

```sh
mkdir data
```

```yaml
services:
  argos:
    image: pedrodl/argos-bot:latest
    container_name: argos
    user: "1000:1000"
    env_file: .env
    volumes:
      - ./data:/data
    restart: unless-stopped
```

The image runs as 65532 unless told otherwise. `user` makes it the folder's owner — `id -u` and
`id -g`, 1000 for Ubuntu's first user — so the files it writes are yours. Create the folder
before the first start: one that Docker creates belongs to root, and the bot refuses it with
`ARGOS_DATA_DIR=/data is not writable`.

The block works on its own or beside the services of a compose you already have.

## On a Mac or Windows: a volume

A folder shared into Docker Desktop does not keep the file locks SQLite needs
([why](DETAILS.md#2-docker)), so the data goes in a volume, and `user` is left out:

```yaml
services:
  argos:
    image: pedrodl/argos-bot:latest
    container_name: argos
    env_file: .env
    volumes:
      - argos-data:/data
    restart: unless-stopped

volumes:
  argos-data:
    name: argos-data
```

## Starting and updating

```sh
docker compose up -d
docker compose logs -f argos
# [argos] telegram bot running for 1 user(s), data in /data, reading receipts with claude-opus-5-5 at effort low
```

Send it `/start`. Someone not in `TELEGRAM_ALLOWED_USER_IDS` gets no reply, and the log names
who was ignored. `docker ps` shows the container `healthy` a minute after it starts.

To update, `docker compose pull && docker compose up -d`. The database migrates itself on
startup, and a database written by a newer version is refused rather than opened — going back to
an older tag after an update needs a backup from before it.

| Tag | |
| --- | --- |
| `latest` | the newest release |
| `1` | every 1.x release, never a 2.0 that may break a `.env` that works |
| `1.2.3` | that release, for good |
| `edge` | the newest commit on `main`, not yet released |

The image sets `TZ=America/Sao_Paulo`; a `TZ` in the `.env` replaces it. Every other setting is
in [DETAILS.md](DETAILS.md#configuration).

## Without compose

```sh
docker run -d --name argos --env-file .env -v argos-data:/data --restart unless-stopped pedrodl/argos-bot:latest
```

or, with a folder, `--user "$(id -u):$(id -g)" -v "$PWD/data":/data`. A container
keeps the options it was created with, so updating is creating it again with the same ones:

```sh
docker pull pedrodl/argos-bot:latest && docker rm -f argos && docker run -d --name argos …
```

## The data

`/data` holds the database `argos.db`, the receipt files and `backups/`, where the bot writes a
consistent copy of the database every day and keeps the newest 14 — `ARGOS_BACKUP_EVERY` and
`ARGOS_BACKUP_KEEP` change both. Copy those rather than the
live `argos.db`, which can come out inconsistent while the bot writes. In a folder they are plain
files; out of a volume:

```sh
mkdir -p backups
docker run --rm -v argos-data:/data alpine tar -C /data/backups -cf - . | tar -xf - -C backups
```

`/exportar tudo`, in Telegram, sends every expense as a CSV.

To move from the volume to a folder, stop the bot, then:

```sh
mkdir -p data
docker run --rm -v argos-data:/from -v "$PWD/data":/to alpine \
  sh -c "cp -a /from/. /to/ && chown -R $(id -u):$(id -g) /to"
```
