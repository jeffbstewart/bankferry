-- A payee rule now records which field its pattern is matched against: the
-- raw bank descriptor, or Plaid's normalized merchant_name. Raw is immutable
-- ground truth; merchant is Plaid's inference and can be confidently wrong
-- (a new local shop "Crack'd" is returned as "Cracker Barrel"). Raw-keyed
-- rules therefore outrank merchant-keyed ones at match time. See payee.md.
--
-- This is a NON-destructive migration: existing rules are preserved and
-- tagged match_field='raw', so behavior is unchanged until new merchant-keyed
-- rules are added. The clean-slate purge the rework calls for is a separate,
-- explicit operator action ('learn --reset'), not a silent data loss baked
-- into an upgrade.
--
-- SQLite cannot alter a column's UNIQUE constraint in place, and the old
-- table declared `pattern TEXT NOT NULL UNIQUE`. The new uniqueness is on
-- (pattern, match_field), so the same pattern may key both a raw and a
-- merchant rule. That requires the table-recreation dance below. Nothing
-- references payee_rule, so dropping and renaming it is safe with foreign
-- keys enabled.

CREATE TABLE payee_rule_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    payee_id    INTEGER NOT NULL REFERENCES payee(id),
    pattern     TEXT NOT NULL,
    match_field TEXT NOT NULL DEFAULT 'raw' CHECK (match_field IN ('raw', 'merchant')),
    priority    INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (pattern, match_field)
);

INSERT INTO payee_rule_new (id, payee_id, pattern, match_field, priority, created_at)
    SELECT id, payee_id, pattern, 'raw', priority, created_at FROM payee_rule;

DROP TABLE payee_rule;

ALTER TABLE payee_rule_new RENAME TO payee_rule;
