CREATE TABLE gnucash_account (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,
    parent_id  TEXT,
    full_path  TEXT NOT NULL,
    learned_at TEXT NOT NULL DEFAULT (datetime('now'))
);
