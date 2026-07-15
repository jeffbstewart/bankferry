CREATE TABLE exported_transaction (
    teller_transaction_id TEXT PRIMARY KEY,
    teller_account_id     TEXT NOT NULL,
    exported_at           TEXT NOT NULL DEFAULT (datetime('now'))
);
