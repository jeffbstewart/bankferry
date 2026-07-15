CREATE TABLE payee_rule (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    payee_id   INTEGER NOT NULL REFERENCES payee(id),
    pattern    TEXT NOT NULL UNIQUE,
    priority   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
