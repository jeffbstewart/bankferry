CREATE TABLE payee (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    default_account TEXT,
    created_at      TEXT NOT NULL DEFAULT (datetime('now'))
);
