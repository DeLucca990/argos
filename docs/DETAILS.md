# Argos — the details

A Telegram bot that keeps track of what you spend: send it a receipt — a photo, a PDF, or a line
of text — and it extracts the purchase with Claude and stores it in SQLite.

**Status: phase 5, under way.** It reads receipts, records them as they arrive, imports from
banks through Open Finance, takes corrections in plain words, and answers what was spent — in
commands, by day, week and month, and in questions asked in Portuguese. It backs itself up daily,
reports its health, keeps to a daily budget if given one, and comes with an evaluation to choose
its model by.

## Running it

From a clone of the repository. To install the published image instead, see
[INSTALL.md](INSTALL.md).

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

**Backups.** Every `ARGOS_BACKUP_EVERY` (a day unless set: `6h`, `30m`, `7d`) the bot writes one
with `VACUUM INTO` — a consistent copy even mid-write — to
`/data/backups/argos-YYYY-MM-DD-HHMMSS.db`, keeping the newest `ARGOS_BACKUP_KEEP` (14 unless
set, 0 for none) — backups, not days: every 6 hours, 14 cover three and a half days. When the
next one is due is counted from the time in the newest one's name, so restarting the bot writes
none early; one written as `argos-YYYY-MM-DD.db`, before the interval could be set, counts as
written at midnight. They are in the same volume, so they cover a bad edit or a broken database,
not a lost disk; to take them off the machine:

```sh
make db-backups                    # copies them into ./backups, owned by you
make db-backups BACKUPS=/mnt/nas/argos
```

Copying the volume's files by hand while the bot writes can come out inconsistent, since the
database runs in WAL mode; the backups and `make db-copy` (below) are the safe copies.

**Health.** The container has a healthcheck: every 30 seconds the bot asks Telegram who it is and
the database for a row, and `argos healthcheck` reports it unhealthy once a minute or two pass
without both answering. `docker ps` shows it, and so can an Uptime Kuma Docker monitor. Docker
does not restart a container for being unhealthy — it says so; a crash is what `restart:
unless-stopped` handles.

### Looking inside the database

The bot's image has no shell, and the database is in the volume rather than on the host, so
these run in one-off containers on the volume, as the bot's user. They need nothing installed on
the host and work whether the bot is running or not:

```sh
make db                               # the sqlite3 shell on the live database
make db SQL="SELECT merchant, total_cents FROM expenses WHERE payment_method = 'vale'"
make db-copy                          # a consistent, single-file copy in /tmp/argos-copy.db, for a GUI or scp
make db-copy DB_COPY=$HOME/argos.db   # somewhere else
```

`make db` is the real `sqlite3`, from a small Alpine image the `tools` profile of `compose.yml`
builds the first time and reuses after: `.tables`, `.schema expenses`, `.mode`, and rows drawn as
a table.

```
sqlite> .tables
attachments    categories     expense_items  extractions
cards          digests        expenses       users
sqlite> SELECT id, merchant, total_cents FROM expenses LIMIT 2;
┌────┬────────────────────┬─────────────┐
│ id │      merchant      │ total_cents │
├────┼────────────────────┼─────────────┤
│ 1  │ OXXO CARDOSO DINIZ │ 998         │
│ 2  │ Uber               │ 1894        │
└────┴────────────────────┴─────────────┘
```

It can write — what is typed there changes the data the bot reads — so foreign keys are on, as
they are for the bot. `SQL=` takes quotes inside it as they are, since it reaches the container
as a variable rather than as part of a command line. Both refuse to run before the bot has
created the database, rather than create an empty one. Away from the clone, once the image is
built, the same is

```sh
docker run --rm -it -u 65532:65532 -e HOME=/tmp -v argos-data:/data argos-sqlite sqlite3 -box /data/argos.db
```

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

#### For an agent, through `docker exec`

The bot's container has nothing to exec into, so a tool that only runs commands in running
containers — an MCP server such as Nautilus, with its `docker_container_exec` — cannot read the
database through it. `sqlite-ro` is that place: a container that only waits, with `sqlite3` and
the volume mounted read-only. It is a profile too, started after the bot:

```sh
make db-ro          # argos-sqlite-ro — and again after every reboot, until make db-ro-stop
```

A query is then one command, with no shell in between:

```sh
docker exec argos-sqlite-ro sqlite3 -readonly -box /data/argos.db \
  "SELECT id, merchant, total_cents FROM expenses ORDER BY id DESC LIMIT 20"
```

For Nautilus, `argos-sqlite-ro` goes in `NAUTILUS_ALLOW_CONTAINER_NAMES`, and the command is the
same argument vector: `["sqlite3", "-readonly", "-box", "/data/argos.db", "SELECT ..."]`. Its
output is cut at 16 KB, so a `LIMIT` keeps the answer whole.

The mount is what makes it safe to hand to something that is not you: with the volume read-only,
nothing run there changes the data, `-readonly` or not — an `UPDATE` fails with `attempt to write
a readonly database`. It does mean it reads only while the bot is running. In WAL mode, a reader
that cannot write needs the `-wal` and `-shm` files the bot keeps while it has the database open;
with the bot stopped they are gone, and the answer is `unable to open database file`.

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
📅 19/10/2026 às 10:15 · 💳 Débito · Nubank ···1234
💊 Farmácia
💰 R$ 42,30

• Dipirona 500mg — 2 × R$ 9,90 = R$ 19,80

⚠️ A soma dos itens (R$ 19,80) é diferente do total (R$ 42,30): pode ser desconto, …

✅ Salvo.
leitura: ≈ US$ 0,0400 · claude-opus-5-5
[ 🏷️ Categoria ] [ ❌ Descartar ]
```

It is saved as soon as it is read — there is no confirmation step. **Categoria** swaps the
buttons for the categories — the ones used most in the last 90 days first, *Outros* last;
**Descartar** keeps the record but leaves it out of every total, and frees the receipt to be
sent again.

**Sixteen categories are the same for everyone**, and nobody changes them from the chat. On top
of them each person adds their own with **➕ Nova categoria**, at the bottom of `/categorias` —
the only place one is created. The bot asks, and the answer is an emoji, the name, and — after
a dash — a hint of what goes in it: *🏋️ Academia — mensalidade, personal*. The hint is what
Claude reads to tell when a receipt belongs there, so a category with a name only it could
guess, like *Casa da praia*, needs one. A name a default already has is refused. Thirty at most, counting the
sixteen, with *Outros* always last, where a receipt that fits none of them goes. Nobody sees
anyone else's.

`/categorias` also has a button for each of yours, which opens **Nome**, **Emoji**, **Dica**
and **Remover**; the first three ask too. Removing one with expenses only archives it: it leaves
the buttons and what Claude may choose from, its expenses stay in it and in every report and
answer, and creating it again by the same name brings it back. One with no expenses is deleted.

**To correct one, reply to its card** in your own words — *foi 45,90*, *no crédito*, *era
ontem*, *o nome é Padaria Real*, or all at once. Claude is shown the expense as recorded and the
reply, and answers the same record with only that changed; the card is rewritten in place and
the reply is answered with what moved:

```
✏️ Corrigido:
• total: R$ 42,30 → R$ 45,90
• pagamento: Débito → Crédito
```

That answer counts as the card too, so replying to it corrects again. A reply that asks for
nothing applicable changes nothing and says so. The cost line on the card becomes the sum of
the reading and every correction.

**Corrections are remembered.** Reading a photo or PDF, Claude also describes what kind of
receipt it is — the app or machine it seems to come from, its colours, its section titles, never
its data. When a correction changes something that belongs to that kind of receipt rather than
to this one purchase, the bot keeps it as a lesson:

- the account, the card or the payment method, for receipts that look like this one;
- the name or the category, for payments to the same recipient as read — or, when the receipt
  names nobody, for receipts that look like this one.

The total, the date and the time are never learned. The answer to the correction says what was
kept, with a **🙅 Não lembrar** button for a correction that was about that receipt alone:

```
🧠 Vou lembrar disso nos próximos comprovantes:
• comprovantes como este (comprovante de Pix em tela de app, títulos em roxo): conta Nubank
• pagamentos a MARIA S SOUZA: nome Personal trainer
```

Every reading after that takes your lessons along — the newest 20, a newer one replacing an
older one for the same kind of receipt and field — and Claude uses one only when the receipt
matches it and does not show the field itself. A card filled from a lesson says so:
*🧠 Lembrado das suas correções: conta.* Lessons are learned only from replies to a card, not
from the category buttons, and only from photos and PDFs, never from typed purchases.

**The daily budget.** With `ARGOS_DAILY_BUDGET_USD` set, a receipt or a correction that would
start once today's model calls have reached it is answered with how much the day cost, and not
read; commands keep working, and it lifts at midnight. It is checked before a call, so the call
that crosses the line finishes. `/cost` shows how much of it is used.

What the card warns about is checked in code, not by the model: a CNPJ or CPF whose check
digits do not match (alphanumeric CNPJs included), an NFC-e access key that does not, items that
do not add up to the total, a date in the future or over a year old, a currency other than reais,
and no date at all — then today is used. What the model itself was unsure of comes as 📝.

**Where the money came from.** Besides the payment method, the card shows the account — the
bank, card, wallet or benefits card the receipt names, by its everyday name: *Nubank*, not *Nu
Pagamentos S.A.* — and the card's last four digits when they are printed. Either is empty when
the receipt does not show it, and both can be corrected by replying to the card (*foi no cartão
do Inter, final 9876*). The bank's own CNPJ, printed on a statement screenshot, is never taken
for the merchant's. Expenses read before the field existed have no account.

**The same receipt twice** is caught two ways: the same file by its sha256, before any model is
called, and the same purchase photographed again by the 44-character NFC-e access key printed on
it. Once the first one is discarded, sending it again works.

**How it is read.** One request to Claude per receipt, with the image or PDF and a JSON schema the
answer must follow (structured outputs), so there is no parsing of prose. Images bigger than the
model can see — 2576 px on the long edge — are scaled down first, and a phone photo sent as a file
is turned upright by its EXIF orientation, which the model does not read on its own. Amounts
become integer cents in Go, never floats. Up to two receipts are read at once; the rest wait
their turn, and each photo of an album is a receipt of its own.

**HEIC is not read**, and will not be soon: the API takes JPEG, PNG, GIF and WebP only, and Go
has no HEIC decoder without cgo and libheif, which would end the static, any-architecture image.
Sent as a photo rather than a file, an iPhone picture is converted to JPEG by Telegram itself.

**What it costs** is on every card, and every call — answered, refused, failed or not a receipt
at all — is a row in `extractions` with its tokens and estimated dollars, at list prices. A photo
of a receipt runs around US$ 0,04 on `claude-opus-5-5`; `ARGOS_MODEL=claude-sonnet-5-5` halves
that and `claude-haiku-5-5` costs a fraction of a cent, at some loss of accuracy on hard receipts.

The files themselves stay in `/data/receipts/`, in the volume, named by their sha256 — unless
`ARGOS_DELETE_RECEIPTS=true`. Then no file is ever written: the receipt is read from memory, and
what stays is what was read off it and the file's sha256, which is all that telling a resent
file apart needs, so that still costs nothing. Starting with it on also deletes the files kept
until then, for good — the backups hold the database, not the images. What you lose is
the original to look at again, or to read anew with a better model.

## Importing from the bank

Receipts are one way in; the other is the bank itself, through Open Finance, by
[Pluggy](https://pluggy.ai). Both work at once, and a purchase that arrives both ways is kept once.
The calls to Pluggy themselves — endpoints, payloads, what each field becomes — are in
[PLUGGY.md](PLUGGY.md).

### Setting it up

It runs on **Meu Pluggy**, Pluggy's free personal use: up to five connections, no commercial
use, refreshed by the banks once a day — and only your own accounts, read by an application on
your own Pluggy account. So **each person has an application of their own**, and the bot holds
the credentials of each.

1. At [dashboard.pluggy.ai](https://dashboard.pluggy.ai), create an application. Its row under
   *Applications* shows the **Client ID**; its page generates the **Client Secret**, shown once.
2. Whoever runs the bot puts them in `.env` and restarts it. A pair without a Telegram id is for
   everyone in `TELEGRAM_ALLOWED_USER_IDS` without one of their own — in a bot of one person,
   theirs; a pair ending in a Telegram id is that person's:

   ```sh
   PLUGGY_CLIENT_ID=...                    # yours
   PLUGGY_CLIENT_SECRET=...
   PLUGGY_CLIENT_ID_123456789=...          # the person whose Telegram id is 123456789
   PLUGGY_CLIENT_SECRET_123456789=...
   ```

   The log says `importing expenses from Pluggy every 1h0m0s: 1 user(s) with an application of
   their own, everyone else with PLUGGY_CLIENT_ID` — every hour unless `PLUGGY_SYNC_EVERY` says
   otherwise. `/bancos` and `/conectar` appear in the menu
   of whoever has an application — everyone's, with the pair without an id. Without any pair the
   bot reads receipts only, as before. Never send a secret through the chat: it would stay in
   Telegram's history; `/bancos` from someone without one says which two lines to add, with their id.
3. At [meu.pluggy.ai](https://meu.pluggy.ai), sign up with the same account as the dashboard and
   connect the banks.
4. Back in the dashboard: the application's *Demo* → *Connect Account* → **MeuPluggy**, and sign
   in. Not the bank's own connector — one that asks for the CPF and the bank's approval again is
   the wrong one. Each bank connected in Meu Pluggy becomes an item.
5. Under *Connected Items*, open each and copy its id, then send the bot `/conectar <id>`.

`/conectar` alone sends steps 3 to 5. An item belongs to the Telegram user who connected it,
nobody else can connect it while it is theirs, and it is read with that user's application.
Whoever runs the server can read everyone's data — as with the receipts — and now holds each
person's Pluggy secret too.

Changing someone's credentials to an application that does not have their items makes those
items unreachable: the bot says so once, and `/bancos` shows them as not found. Connect the items
of the new application, then disconnect the old ones; as with a renewal, what already came in
does not come in twice.

### What comes in

On connecting, everything since the 1st of the month. After that, every `PLUGGY_SYNC_EVERY` —
an hour unless set — the bot asks Pluggy whether the item changed, and reads it only when it
did — 30 days back from the last read, so a transaction a bank posts late still comes in. Meu
Pluggy refreshes the banks once a day, so checking more often brings a day's transactions in
sooner, not more of them; checking less often is fewer calls, and the same transactions later.

- **Money out only**: debits from accounts and purchases on cards, pending or posted. Money in,
  refunds, the card's own bill payments, a Pix to the same CPF it came from and anything dated in
  the future — the installments still to come — are left out before any model sees them.
- **Claude files the rest**, up to 40 transactions a call, with your categories and their hints:
  a name people use (*iFood*, not *IFD\*IFOOD COM AGENCIA*), a short description, a category —
  and whether it is spending at all. Paying the card's bill from the account, moving money to
  another account of yours, investing and withdrawing cash are not, and are left out, so a
  purchase on the card counts once.
- **What it already knows** is not asked again: a transaction with a description the bank sent
  before is filed like the last one — its name, description and category, including a category
  you changed with the button or by replying to its card. One left out as not spending is left
  out again.
- The payment method, the account, the card's last four digits and the payee's CNPJ or CPF come
  from the bank's data, not the model: *Crédito* on a card, *Pix*, *Boleto* or *Débito* from an
  account. Through Meu Pluggy the account is the account's own name at the bank, since the
  connector's name — MeuPluggy — says nothing; reply to a card to fix one.

Each import is a message — how many, how much, how many were already there by receipt or had
come through another connection, and how many were left out — followed by the cards of the
newest ten, sent silently:

```
🏦 iFood
📅 19/10/2026 · 💳 Crédito · Nubank Mastercard ···5678
🛵 Delivery
💰 R$ 35,90
Jantar

🏦 No extrato: IFD*IFOOD

✅ Salvo.
[ 🏷️ Categoria ] [ ❌ Descartar ]
```

They are expenses like any other: in every report, corrected by replying, discarded with the
button. A discarded one never comes back from the bank.

### Receipts and the bank together

The same purchase both ways is kept once, whichever comes first:

- a transaction of the same amount as an expense you sent, within two days of it, is linked to
  that expense instead of becoming another — unless the receipt says it was paid in cash;
- a receipt sent after the bank's transaction replaces it: the imported expense is discarded and
  the transaction moves to the receipt's, which has the items and the store's own name.

Either way that card says *🏦 No extrato*. What this gets wrong is two purchases of the same
amount within two days, one by receipt and one only at the bank: the second is taken for the
first. Discarding the receipt's expense, should it be the wrong one, takes the bank's with it.

Pluggy's ids for a transaction are not forever. A bank sometimes deletes one and creates it again
under a new id — when a pending purchase changes too much as it posts — and connecting a bank
again makes a new item, whose accounts and transactions all have new ids. What does not change
is the id the bank gives the transaction in Open Finance, which Pluggy passes on: a transaction
whose Open Finance id came in before, through any connection of the same person, keeps the
expense it had — discarded, if it was — and is not filed again. A transaction without one, as
some banks send, is recognised only within its account: the same amount, within a week, as one
that vanished from the listing.

### Keeping it working

`/bancos` lists the connections: whether each is up to date, since when it imports and when it
last checked, with 🔄 to read Pluggy now and 🔌 to disconnect — the imports stop, and what came
in stays. Connecting the item again later imports nothing twice.

**The authorisation lasts a year** from step 4. `/bancos` warns in its last 30 days, and when
Pluggy reports an item in error the bot says so, once. Renewing is step 4 again; when the item
id changes, `/conectar` the new one and disconnect the old — what had come through the old one is
recognised by its Open Finance id, and not imported again.

**Filing costs model calls**, text only, one per 40 transactions, shown apart in `/cost`. The
daily budget holds them too: past it, transactions wait, and are filed on the first check after
midnight. A failed call waits the same way.

**Not followed**: a purchase cancelled while pending, a refund, an amount the bank changes after
the transaction came in — fix those by hand. Nor does the bot listen for Pluggy's webhooks: it
asks, Pluggy never calls it, so it needs no open port.

## Asking what was spent

| Command | Answers |
| --- | --- |
| `/hoje` | today's expenses, one per line, with the time when the receipt had one |
| `/semana` | this calendar week, Monday to Sunday, by category |
| `/mes [mês]` | a month by category, with each one's share; the current month unless named |
| `/ultimos [n]` | the latest expenses, 10 unless a number up to 50 follows |
| `/desfazer` | discards the expense saved last — the same as its card's Descartar |
| `/exportar [mês \| tudo]` | a month, or everything, as a CSV file |
| `/resumo [mês]` | the monthly digest below, for any month; the last closed one unless named |
| `/categorias` | the default categories, then yours with their hints; ➕ to create one, and a button to edit or remove each |
| `/bancos` | the banks connected through Open Finance, with buttons to read now and to disconnect — [above](#importing-from-the-bank) |
| `/conectar [id]` | connects a bank by its Pluggy item id; alone, how to find one |
| `/cost` | what the model calls cost today, in 7 days and in 30 — readings, corrections and questions apart — and the model making them |

A month is `2026-10`, `10/2026`, `10`, `outubro` or `out`, with a year after the name if it is
not this one — `março 2025`. A month later in the year than the current one means last year's:
in October, `/mes dezembro` is December 2025.

```
📊 Outubro de 2026
💰 R$ 312,38 em 4 gastos

⛽ Combustível — R$ 200,00 (64%) · 1
🛒 Mercado — R$ 97,38 (31%) · 2
🍽️ Restaurante — R$ 15,00 (5%) · 1
```

Every report counts saved expenses only — never a discarded one — and only the sender's: each
person in `TELEGRAM_ALLOWED_USER_IDS` sees what they sent.

**The CSV** is made for a spreadsheet in Portuguese: `;` between columns, a decimal comma
(`87,40`), dates as `20/10/2026`, and a byte-order mark so Excel reads the accents. One row per
expense — date, time, merchant, CNPJ or CPF, category, payment method, account, the card's last
four digits, amount, currency, description, where it came from and its id. Merchant names and descriptions were read off
receipts, so one starting with `=`, `+`, `-` or `@` is prefixed with `'`: a planted
`=HYPERLINK(…)` arrives as text instead of running as a formula when the file opens.

### The monthly digest

On the 1st, at `ARGOS_DIGEST_AT` (09:00 unless set; `off` for never), everyone in the allowlist
gets the month that just closed — their own expenses only:

```
📅 Resumo de setembro de 2026

💰 R$ 388,00 em 3 gastos · média de R$ 12,93 por dia
↗️ R$ 288,00 a mais que em agosto (+288%)

Por categoria
🛒 Mercado — R$ 370,00 (95%) · ↗️ +R$ 270,00
🍽️ Restaurante — R$ 18,00 (5%) · novo

Os maiores
• 02/09 🛒 Supermercado Extra — R$ 250,00
• 29/09 🛒 Extra Hiper — R$ 120,00
• 15/09 🍽️ Padaria — R$ 18,00

Pagamento
Crédito R$ 250,00 · Débito R$ 120,00 · Pix R$ 18,00
```

It is a template over the database, with no model in it, so it costs nothing. What was sent is a
row in `digests`, per person and month, which makes a restart safe: nobody gets the same month
twice, and a bot that was down on the 1st sends it when it comes back, through the 7th — after
that it waits for the next month. A month with no expenses sends nothing. A person who blocked
the bot is recorded as done rather than retried for a week.

`/resumo` shows the same at any time. For the month under way it reads *Outubro de 2026, até o
dia 20*, and compares with the same days of the month before rather than with the whole of it.

### In plain words

Anything the commands do not cover can be asked: *quanto gastei com mercado em setembro?*,
*qual foi meu maior gasto da semana?*, *quanto foi de cerveja este ano?*, *compara setembro e
outubro*. The bot answers "🤔 Pensando…" and then the answer, with what it cost:

```
Em setembro você gastou R$ 388,00, em 3 gastos:

• Mercado: R$ 370,00 (95%)
• Restaurante: R$ 18,00 (5%)

≈ US$ 0,0300
```

**What counts as a question.** A message with a `?`; one that starts with a question word or a
request — quanto, qual, quando, onde, como, quem, o que, por que, mostra, lista, compara, resume,
faça, me dá, gere, calcule and a few more; or any text of more than one word without a number,
since that cannot be a purchase — *faça um resumo de todos os gastos*. Anything else with a
number in it is still a purchase — *almoço 35,90 no pix* — which is why *quanto gastei em 2026?*
needs its question word or its `?` to be read as one. A single word with no number, *oi*, gets
the help.

**How it is answered.** Claude gets three tools and nothing else, all reading, none writing,
each scoped to the asker's saved expenses:

| Tool | Answers |
| --- | --- |
| `spending_totals` | sums, overall or by category, merchant, payment method, day, week or month, with each group's share |
| `list_expenses` | the expenses themselves, newest or largest first, up to 50 |
| `search_items` | the item lines of receipts that listed them — *cerveja*, *gasolina* — with their sum |

Each takes the same filters: a date range, a category, part of a merchant's name, a payment
method, an account — *quanto gastei no Nubank em outubro?* — a minimum and a maximum. There is no free-form SQL: what Claude can ask the database is
exactly these three questions, and someone else's expenses are not among the answers. Every
figure in a reply comes from a tool — the prompt forbids estimating one — and the merchant names
and item lines it reads are treated as data, since they came off receipts.

**A conversation.** Follow-ups work — *e em agosto?*, *e no crédito?* — for 30 minutes after the
last question; after that, or after a long one, it starts over. The history is kept whole, never
trimmed, so the prompt cache holds and the earlier turns stay valid. One question per person at
a time.

Questions count toward `ARGOS_DAILY_BUDGET_USD` like receipts, and `/cost` tells them apart:
*Hoje: US$ 0,0854 · 2 leituras · 1 correção · 3 perguntas*. On `claude-opus-5-5` a question runs
around two or three cents; `claude-haiku-5-5` answers the same for a fraction of a cent, and
reads ambiguous follow-ups less well.

## Choosing the model by your own receipts

The model that reads best is a matter of your receipts, not of a benchmark, so the repo carries
an evaluation to run on them. Put photos and PDFs in `testdata/receipts/` — ignored by git and
by the Docker build, since they are yours — then:

```sh
make eval-init                       # writes a .json beside each with what the model read
# correct every .json by hand: it is the answer key, and the model wrote it
make eval                            # the configured model
make eval MODEL=claude-sonnet-5-5    # or any other, and EFFORT=medium
```

```
receipt   total  date  payment  category  merchant  cnpj  nfce  US$
oxxo.jpg  ✓      ✓     ✗        ✓         ✓         ·     ·     0.0180

oxxo.jpg: payment read "debito", expected "credito"

claude-opus-5-5 at effort low: total 1/1 · date 1/1 · payment 0/1 · category 1/1 · merchant 1/1 — US$ 0.0180 for 1 receipt
```

The total is compared in cents, the merchant loosely — either name inside the other, ignoring
case and punctuation — and the CNPJ and the NFC-e key only where the answer key has them (`·`
otherwise). It runs on your machine with the `ANTHROPIC_API_KEY` in `.env`, and every receipt is
a real, billed call: ten receipts on Opus 5.5 cost around US$ 0,40.

## Configuration

| Variable | Default | |
| --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | — | **required**, a bot of its own |
| `TELEGRAM_ALLOWED_USER_IDS` | — | **required**, numeric ids, comma-separated |
| `ANTHROPIC_API_KEY` | — | **required**: receipts are read by Claude (`ANTHROPIC_AUTH_TOKEN` also works) |
| `ARGOS_MODEL` | `claude-opus-5-5` | the model that reads them |
| `ARGOS_EFFORT` | `low` | `low`, `medium`, `high`, `xhigh` or `max`: how much it thinks before answering |
| `ARGOS_DAILY_BUDGET_USD` | — | the most a day of model calls may cost before reading pauses until midnight; `1.50` or `1,50` |
| `ARGOS_BACKUP_KEEP` | `14` | how many backups to keep in `/data/backups`; `0` turns them off |
| `ARGOS_BACKUP_EVERY` | `1d` | how often to write one: whole minutes, at least `1m` — `30m`, `6h`, `1d`, `7d` |
| `ARGOS_DIGEST_AT` | `09:00` | when the monthly digest goes out on the 1st, local time; `off` for never |
| `ARGOS_DELETE_RECEIPTS` | `false` | `true` keeps no receipt files: each is read from memory, and the ones kept before are deleted at startup |
| `PLUGGY_CLIENT_ID` / `PLUGGY_CLIENT_SECRET` | — | a Pluggy application, to [import from banks](#importing-from-the-bank), for everyone without one of their own; neither for receipts only |
| `PLUGGY_CLIENT_ID_<id>` / `PLUGGY_CLIENT_SECRET_<id>` | — | the Pluggy application of the person with that Telegram id, who must be in the allowlist |
| `PLUGGY_SYNC_EVERY` | `1h` | how often to ask Pluggy whether a connection changed: whole minutes, at least `1m` — `30m`, `6h`, `1d` |
| `TZ` | `America/Sao_Paulo` | the time zone of dates, `/hoje` and the digest; set in the image |
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
| `expenses` | one purchase: when, where, category, payment method, the account paid from and the card's last four digits, total in cents, and its status — `confirmed` once read, `discarded` when you discard it; `pending` is for an expense still awaiting a decision, which the bot does not create today |
| `expense_items` | the lines of a receipt, in order |
| `attachments` | the files received, by sha256, so the same file is kept once |
| `extractions` | every call to the model: its raw answer, tokens and estimated cost |
| `categories` | the sixteen defaults, kept once with no `user_id`, and each user's own — name, emoji, a hint of what goes in it, and when it was archived |
| `lessons` | what each user taught by correcting: the field, the right value, and the kind of receipt or the recipient it applies to |
| `bank_items` | each bank connection: its Pluggy item id, whose it is, its state, and since when it imports |
| `bank_transactions` | every transaction taken from a bank, by its Pluggy id, and the expense it became or was matched to — none when it was not spending — so none comes in twice |

Who may use the bot is still `TELEGRAM_ALLOWED_USER_IDS`; `users` records who did, and every
expense and extraction belongs to one of them. Removing someone from the list stops them without
touching what they recorded.

Every table has an `id` that SQLite hands out in sequence, with
`AUTOINCREMENT`, so no id is ever handed out twice — not even after a delete, which matters
because ids end up in the buttons of old messages. The Telegram id is a column of `users`, not
its key.

Money is integer cents, never a float. The day of a purchase is the local date printed on the
receipt (`spent_on`), with its time separately when it has one. The same file, or the same
NFC-e access key, cannot be recorded twice unless the first record was discarded.

## Project layout

```
cmd/argos/           entrypoint: .env loading, the database, signal handling, backups; argos migrate and argos healthcheck
cmd/eval/            make eval: reads your own receipts with a model and scores each field against what they say
internal/dotenv/     reads a .env into the environment before the configuration is checked
internal/receipt/    what a receipt is: amounts in cents, CNPJ/CPF/NFC-e checks, the warnings; imports nothing
internal/extract/    Claude: the schema, the request, images made readable, corrections, filing bank transactions
internal/ask/        Claude answering questions: the conversation, and the three read-only tools it may call
internal/cost/       what each model call cost, at list prices, fallbacks included
internal/pluggy/     the Pluggy API: signing in, items, accounts and transactions
internal/store/      SQLite — migrations, expenses, reports; knows nothing of Telegram or Claude
internal/telegram/   the bot — configuration, authorisation, the card and its buttons, commands
```

```sh
make test
```
