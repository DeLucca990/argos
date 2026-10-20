package store

type migration struct {
	name string
	sql  string
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
}
