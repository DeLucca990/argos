package store

type migration struct {
	name    string
	sql     string
	rebuild bool
}

var migrations = []migration{
	{name: "0001_init", sql: `
CREATE TABLE categories (
    slug     TEXT PRIMARY KEY,
    label    TEXT NOT NULL,
    emoji    TEXT NOT NULL,
    position INTEGER NOT NULL
);

INSERT INTO categories (slug, label, emoji, position) VALUES
    ('mercado',     'Mercado',     '🛒', 1),
    ('restaurante', 'Restaurante', '🍽️', 2),
    ('delivery',    'Delivery',    '🛵', 3),
    ('transporte',  'Transporte',  '🚕', 4),
    ('combustivel', 'Combustível', '⛽', 5),
    ('saude',       'Saúde',       '🩺', 6),
    ('farmacia',    'Farmácia',    '💊', 7),
    ('moradia',     'Moradia',     '🏠', 8),
    ('contas',      'Contas',      '💡', 9),
    ('lazer',       'Lazer',       '🎉', 10),
    ('educacao',    'Educação',    '📚', 11),
    ('assinaturas', 'Assinaturas', '📺', 12),
    ('vestuario',   'Vestuário',   '👕', 13),
    ('pets',        'Pets',        '🐾', 14),
    ('viagem',      'Viagem',      '✈️', 15),
    ('outros',      'Outros',      '📦', 16);

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    telegram_id   INTEGER NOT NULL UNIQUE,
    first_name    TEXT NOT NULL DEFAULT '',
    last_name     TEXT NOT NULL DEFAULT '',
    username      TEXT NOT NULL DEFAULT '',
    language_code TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    last_seen_at  TEXT NOT NULL
);

CREATE TABLE attachments (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    sha256     TEXT NOT NULL UNIQUE,
    path       TEXT NOT NULL,
    mime       TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE expenses (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         INTEGER NOT NULL REFERENCES users (id),
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'confirmed', 'discarded')),
    spent_on        TEXT NOT NULL CHECK (spent_on GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    spent_time      TEXT CHECK (spent_time GLOB '[0-2][0-9]:[0-5][0-9]'),
    merchant        TEXT NOT NULL DEFAULT '',
    merchant_tax_id TEXT NOT NULL DEFAULT '',
    category        TEXT NOT NULL REFERENCES categories (slug) ON UPDATE CASCADE,
    payment_method  TEXT CHECK (payment_method IN ('pix', 'credito', 'debito', 'dinheiro', 'boleto', 'vale', 'outro')),
    total_cents     INTEGER NOT NULL CHECK (total_cents > 0),
    currency        TEXT NOT NULL DEFAULT 'BRL',
    description     TEXT NOT NULL DEFAULT '',
    nfce_key        TEXT CHECK (length(nfce_key) = 44),
    source          TEXT NOT NULL CHECK (source IN ('photo', 'pdf', 'text')),
    attachment_id   INTEGER REFERENCES attachments (id),
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    confirmed_at    TEXT
);

CREATE INDEX expenses_by_user_and_day ON expenses (user_id, status, spent_on);

CREATE UNIQUE INDEX expenses_one_per_attachment ON expenses (attachment_id)
    WHERE attachment_id IS NOT NULL AND status != 'discarded';

CREATE UNIQUE INDEX expenses_one_per_nfce_key ON expenses (nfce_key)
    WHERE nfce_key IS NOT NULL AND status != 'discarded';

CREATE TABLE expense_items (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    expense_id  INTEGER NOT NULL REFERENCES expenses (id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    description TEXT NOT NULL,
    quantity    REAL NOT NULL DEFAULT 1,
    unit_cents  INTEGER,
    total_cents INTEGER NOT NULL,
    UNIQUE (expense_id, position)
);

CREATE TABLE extractions (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id            INTEGER NOT NULL REFERENCES users (id),
    attachment_id      INTEGER REFERENCES attachments (id),
    expense_id         INTEGER REFERENCES expenses (id) ON DELETE SET NULL,
    model              TEXT NOT NULL,
    raw_json           TEXT NOT NULL DEFAULT '',
    failed             INTEGER NOT NULL DEFAULT 0,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    usd                REAL NOT NULL DEFAULT 0,
    created_at         TEXT NOT NULL
);

CREATE INDEX extractions_by_time ON extractions (created_at);
`},
	{name: "0002_cards", sql: `
CREATE TABLE cards (
    chat_id    INTEGER NOT NULL,
    message_id INTEGER NOT NULL,
    expense_id INTEGER NOT NULL REFERENCES expenses (id),
    PRIMARY KEY (chat_id, message_id)
);
`},
	{name: "0003_extraction_kinds", sql: `
ALTER TABLE extractions ADD COLUMN kind TEXT NOT NULL DEFAULT 'receipt'
    CHECK (kind IN ('receipt', 'correction', 'question'));
`},
	{name: "0004_digests", sql: `
CREATE TABLE digests (
    user_id INTEGER NOT NULL REFERENCES users (id),
    month   TEXT NOT NULL CHECK (month GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]'),
    sent_at TEXT NOT NULL,
    PRIMARY KEY (user_id, month)
);
`},
	{name: "0005_payment_account", sql: `
ALTER TABLE expenses ADD COLUMN account TEXT NOT NULL DEFAULT '';
ALTER TABLE expenses ADD COLUMN card_last4 TEXT CHECK (card_last4 GLOB '[0-9][0-9][0-9][0-9]');
`},
	{name: "0006_custom_categories", rebuild: true, sql: `
CREATE TABLE new_categories (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER REFERENCES users (id),
    slug        TEXT NOT NULL,
    label       TEXT NOT NULL,
    emoji       TEXT NOT NULL,
    hint        TEXT NOT NULL DEFAULT '',
    position    INTEGER NOT NULL,
    archived_at TEXT
);

INSERT INTO new_categories (slug, label, emoji, position)
    SELECT slug, label, emoji, position FROM categories ORDER BY position;

CREATE TABLE new_expenses (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         INTEGER NOT NULL REFERENCES users (id),
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'confirmed', 'discarded')),
    spent_on        TEXT NOT NULL CHECK (spent_on GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    spent_time      TEXT CHECK (spent_time GLOB '[0-2][0-9]:[0-5][0-9]'),
    merchant        TEXT NOT NULL DEFAULT '',
    merchant_tax_id TEXT NOT NULL DEFAULT '',
    category_id     INTEGER NOT NULL REFERENCES categories (id),
    payment_method  TEXT CHECK (payment_method IN ('pix', 'credito', 'debito', 'dinheiro', 'boleto', 'vale', 'outro')),
    account         TEXT NOT NULL DEFAULT '',
    card_last4      TEXT CHECK (card_last4 GLOB '[0-9][0-9][0-9][0-9]'),
    total_cents     INTEGER NOT NULL CHECK (total_cents > 0),
    currency        TEXT NOT NULL DEFAULT 'BRL',
    description     TEXT NOT NULL DEFAULT '',
    nfce_key        TEXT CHECK (length(nfce_key) = 44),
    source          TEXT NOT NULL CHECK (source IN ('photo', 'pdf', 'text')),
    attachment_id   INTEGER REFERENCES attachments (id),
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    confirmed_at    TEXT
);

INSERT INTO new_expenses (id, user_id, status, spent_on, spent_time, merchant, merchant_tax_id, category_id,
    payment_method, account, card_last4, total_cents, currency, description, nfce_key, source, attachment_id,
    created_at, updated_at, confirmed_at)
    SELECT e.id, e.user_id, e.status, e.spent_on, e.spent_time, e.merchant, e.merchant_tax_id, c.id,
        e.payment_method, e.account, e.card_last4, e.total_cents, e.currency, e.description, e.nfce_key, e.source,
        e.attachment_id, e.created_at, e.updated_at, e.confirmed_at
    FROM expenses e LEFT JOIN new_categories c ON c.user_id IS NULL AND c.slug = e.category
    ORDER BY e.id;

DELETE FROM sqlite_sequence WHERE name = 'new_expenses';
INSERT INTO sqlite_sequence (name, seq) SELECT 'new_expenses', seq FROM sqlite_sequence WHERE name = 'expenses';

DROP TABLE expenses;
DROP TABLE categories;
ALTER TABLE new_categories RENAME TO categories;
ALTER TABLE new_expenses RENAME TO expenses;

CREATE UNIQUE INDEX categories_shared ON categories (slug) WHERE user_id IS NULL;
CREATE UNIQUE INDEX categories_own ON categories (user_id, slug) WHERE user_id IS NOT NULL;

CREATE INDEX expenses_by_user_and_day ON expenses (user_id, status, spent_on);

CREATE UNIQUE INDEX expenses_one_per_attachment ON expenses (attachment_id)
    WHERE attachment_id IS NOT NULL AND status != 'discarded';

CREATE UNIQUE INDEX expenses_one_per_nfce_key ON expenses (nfce_key)
    WHERE nfce_key IS NOT NULL AND status != 'discarded';
`},
	{name: "0007_lessons", sql: `
CREATE TABLE lessons (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users (id),
    expense_id INTEGER REFERENCES expenses (id) ON DELETE SET NULL,
    field      TEXT NOT NULL CHECK (field IN ('merchant', 'payment_method', 'payment_account', 'card_last4', 'category')),
    value      TEXT NOT NULL,
    layout     TEXT NOT NULL DEFAULT '',
    payee      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE INDEX lessons_by_user ON lessons (user_id, id);
`},
}
