-- The /transactions/sync cursor, one per Plaid Item.
--
-- Plaid never re-delivers a transaction once the cursor has advanced past
-- it, so the cursor must only ever move in the same database transaction
-- that records the transactions it delivered. See db.CommitSync.
--
-- cursor is TEXT, not BLOB. Plaid documents it as "256 characters of
-- base64", so it is ASCII text that we store and return unchanged. Opaque
-- means we never interpret it, not that it is binary.
--
-- The length bound is deliberately not a constraint: 256 is Plaid's
-- documented ceiling, not an invariant this schema owns, and a rejected
-- insert would stall syncing for no safety gain. Emptiness is the hazard
-- worth refusing, because an empty cursor replays the Item's entire
-- history.
--
-- The environment is part of the key. A sandbox cursor is meaningless to
-- production, and applying one to the other would either replay an Item
-- entirely or skip it.
--
-- updated_at exists because a cursor is only guaranteed valid for a year.
CREATE TABLE plaid_sync_state (
    environment TEXT NOT NULL,
    item_id     TEXT NOT NULL,
    cursor      TEXT NOT NULL CHECK (length(cursor) > 0),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (environment, item_id)
);
