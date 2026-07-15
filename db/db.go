// Package db provides a SQLite-backed store for tracking which
// transactions have been exported as OFX files, and for caching the
// GnuCash accounts and payee rules used by the learn and map commands.
//
// The exported_transaction columns are still named teller_*: migration
// 002 has already been applied to live databases, so renaming them
// requires a new migration rather than an edit to the old one.
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"sort"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite connection with migration support and
// exported-transaction tracking.
type DB struct {
	conn *sql.DB
}

// Payee represents a known clean payee name.
type Payee struct {
	ID   int64
	Name string
}

// MatchField names which transaction field a payee rule's pattern is tested
// against. It is a string so it stores and scans as the TEXT column directly.
type MatchField string

const (
	// MatchRaw matches the pattern against the raw bank descriptor. It is the
	// default and the higher-authority field: raw is what the bank actually
	// charged and never changes.
	MatchRaw MatchField = "raw"
	// MatchMerchant matches against Plaid's normalized merchant_name. Handy
	// for catching many raw variants at once, but merchant can be wrong, so
	// merchant-keyed rules yield to raw-keyed ones.
	MatchMerchant MatchField = "merchant"
)

// Valid reports whether f is a known match field.
func (f MatchField) Valid() bool { return f == MatchRaw || f == MatchMerchant }

// PayeeRule maps a case-insensitive substring pattern to a Payee. MatchField
// selects which transaction field the pattern is tested against.
type PayeeRule struct {
	ID         int64
	PayeeID    int64
	Pattern    string
	MatchField MatchField
	Priority   int
}

// Open opens (or creates) the SQLite database at the given path,
// enables WAL mode and foreign keys, and runs any unapplied
// migrations.
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if _, err := conn.Exec("PRAGMA journal_mode=WAL"); err != nil {
		if cerr := conn.Close(); cerr != nil {
			log.Printf("db: close after WAL error: %v", cerr)
		}
		return nil, fmt.Errorf("enable WAL mode: %w", err)
	}
	if _, err := conn.Exec("PRAGMA foreign_keys=ON"); err != nil {
		if cerr := conn.Close(); cerr != nil {
			log.Printf("db: close after foreign keys error: %v", cerr)
		}
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	d := &DB{conn: conn}
	if err := d.runMigrations(); err != nil {
		if cerr := conn.Close(); cerr != nil {
			log.Printf("db: close after migration error: %v", cerr)
		}
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	return d, nil
}

// Close closes the database connection.
func (d *DB) Close() error {
	return d.conn.Close()
}

// IsExported reports whether the given transaction ID has already been
// exported.
func (d *DB) IsExported(transactionID string) (bool, error) {
	var count int
	err := d.conn.QueryRow(
		"SELECT COUNT(*) FROM exported_transaction WHERE teller_transaction_id = ?",
		transactionID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check exported: %w", err)
	}
	return count > 0, nil
}

// MarkExported records that the given transaction IDs have been
// exported, associated with the given account ID. It is idempotent —
// re-marking an already-exported ID is not an error.
func (d *DB) MarkExported(accountID string, transactionIDs []string) error {
	if len(transactionIDs) == 0 {
		return nil
	}

	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && rerr != sql.ErrTxDone {
			log.Printf("db: rollback: %v", rerr)
		}
	}()

	stmt, err := tx.Prepare(
		"INSERT OR IGNORE INTO exported_transaction (teller_transaction_id, teller_account_id) VALUES (?, ?)",
	)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer func() {
		if cerr := stmt.Close(); cerr != nil {
			log.Printf("db: close prepared statement: %v", cerr)
		}
	}()

	for _, id := range transactionIDs {
		if _, err := stmt.Exec(id, accountID); err != nil {
			return fmt.Errorf("insert transaction %s: %w", id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// LoadSyncCursor returns the stored /transactions/sync cursor for an Item.
// The boolean is false when no cursor exists, which means the next sync
// must start from the beginning of the Item's history.
//
// An empty string is never stored, so "" and "absent" cannot be confused.
func (d *DB) LoadSyncCursor(environment, itemID string) (string, bool, error) {
	var cursor string
	err := d.conn.QueryRow(
		"SELECT cursor FROM plaid_sync_state WHERE environment = ? AND item_id = ?",
		environment, itemID,
	).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("load sync cursor for %s/%s: %w", environment, itemID, err)
	}
	return cursor, true, nil
}

// CommitSync records exported transactions and advances the sync cursor in
// a single database transaction.
//
// These must not be separated. Plaid never re-delivers a transaction once
// the cursor has moved past it, so a cursor that advanced without the
// transactions being recorded loses them permanently.
//
// The opposite order is safe. If the process dies after the OFX files are
// written but before this commits, the cursor has not moved, the next run
// receives the same transactions, and the files are rewritten. GnuCash
// deduplicates them on FITID, which is stable.
//
// exported maps an account ID to the transaction IDs written for it. It may
// be empty: a sync that produced nothing new still advances the cursor.
func (d *DB) CommitSync(environment, itemID string, exported map[string][]string, cursor string) error {
	if environment == "" || itemID == "" {
		return errors.New("db: environment and item ID are required")
	}
	if cursor == "" {
		return errors.New("db: refusing to store an empty sync cursor, " +
			"which would replay the item's entire history")
	}

	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if rerr := tx.Rollback(); rerr != nil && rerr != sql.ErrTxDone {
			log.Printf("db: rollback commit sync: %v", rerr)
		}
	}()

	stmt, err := tx.Prepare(
		"INSERT OR IGNORE INTO exported_transaction (teller_transaction_id, teller_account_id) VALUES (?, ?)",
	)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer func() {
		if cerr := stmt.Close(); cerr != nil {
			log.Printf("db: close prepared statement: %v", cerr)
		}
	}()

	for accountID, transactionIDs := range exported {
		for _, id := range transactionIDs {
			if _, err := stmt.Exec(id, accountID); err != nil {
				return fmt.Errorf("insert transaction %s: %w", id, err)
			}
		}
	}

	_, err = tx.Exec(
		`INSERT INTO plaid_sync_state (environment, item_id, cursor) VALUES (?, ?, ?)
		 ON CONFLICT(environment, item_id) DO UPDATE SET
		   cursor = excluded.cursor,
		   updated_at = datetime('now')`,
		environment, itemID, cursor,
	)
	if err != nil {
		return fmt.Errorf("advance sync cursor for %s/%s: %w", environment, itemID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// ResetSyncCursor forgets an Item's cursor, so the next sync replays its
// entire history. Already-exported transactions are still filtered out by
// exported_transaction, so this is recovery, not duplication.
func (d *DB) ResetSyncCursor(environment, itemID string) error {
	_, err := d.conn.Exec(
		"DELETE FROM plaid_sync_state WHERE environment = ? AND item_id = ?",
		environment, itemID,
	)
	if err != nil {
		return fmt.Errorf("reset sync cursor for %s/%s: %w", environment, itemID, err)
	}
	return nil
}

// SaveWrappedAPIKey stores the hardware-wrapped API secret for an
// environment, replacing any prior value.
//
// The blob is opaque here: db does not interpret it, exactly as it does not
// interpret a sync cursor. Its structure and every safety property live in
// package plaid, which owns the crypto.
func (d *DB) SaveWrappedAPIKey(environment string, blob []byte) error {
	if len(blob) == 0 {
		return fmt.Errorf("save wrapped api key for %s: blob is empty", environment)
	}
	_, err := d.conn.Exec(
		`INSERT INTO plaid_wrapped_api_key (environment, blob, updated_at)
		 VALUES (?, ?, datetime('now'))
		 ON CONFLICT(environment) DO UPDATE SET blob = excluded.blob, updated_at = excluded.updated_at`,
		environment, blob,
	)
	if err != nil {
		return fmt.Errorf("save wrapped api key for %s: %w", environment, err)
	}
	return nil
}

// LoadWrappedAPIKey returns the wrapped API secret for an environment.
// found is false when none is enrolled, which is distinct from an error.
func (d *DB) LoadWrappedAPIKey(environment string) (blob []byte, found bool, err error) {
	err = d.conn.QueryRow(
		"SELECT blob FROM plaid_wrapped_api_key WHERE environment = ?",
		environment,
	).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load wrapped api key for %s: %w", environment, err)
	}
	return blob, true, nil
}

// DeleteWrappedAPIKey removes the wrapped API secret for an environment.
func (d *DB) DeleteWrappedAPIKey(environment string) error {
	_, err := d.conn.Exec(
		"DELETE FROM plaid_wrapped_api_key WHERE environment = ?", environment,
	)
	if err != nil {
		return fmt.Errorf("delete wrapped api key for %s: %w", environment, err)
	}
	return nil
}

// UpsertPayee inserts a payee if it does not already exist and returns
// its ID.
func (d *DB) UpsertPayee(name string) (int64, error) {
	_, err := d.conn.Exec("INSERT OR IGNORE INTO payee (name) VALUES (?)", name)
	if err != nil {
		return 0, fmt.Errorf("upsert payee %q: %w", name, err)
	}

	// INSERT OR IGNORE does not report a last insert ID when the row
	// already existed, so query for it explicitly.
	var id int64
	err = d.conn.QueryRow("SELECT id FROM payee WHERE name = ?", name).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("get payee id %q: %w", name, err)
	}
	return id, nil
}

// LoadPayees returns all payee records.
func (d *DB) LoadPayees() ([]Payee, error) {
	rows, err := d.conn.Query("SELECT id, name FROM payee ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("query payees: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			log.Printf("db: close payee rows: %v", cerr)
		}
	}()

	var payees []Payee
	for rows.Next() {
		var p Payee
		if err := rows.Scan(&p.ID, &p.Name); err != nil {
			return nil, fmt.Errorf("scan payee: %w", err)
		}
		payees = append(payees, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate payees: %w", err)
	}
	return payees, nil
}

// InsertRuleIfNew inserts a payee rule if the (pattern, matchField) pair does
// not already exist. It is a no-op if that pair is already present. An empty
// matchField defaults to MatchRaw.
func (d *DB) InsertRuleIfNew(payeeID int64, pattern string, matchField MatchField, priority int) error {
	if matchField == "" {
		matchField = MatchRaw
	}
	if !matchField.Valid() {
		return fmt.Errorf("insert rule %q: invalid match field %q", pattern, matchField)
	}
	_, err := d.conn.Exec(
		"INSERT OR IGNORE INTO payee_rule (payee_id, pattern, match_field, priority) VALUES (?, ?, ?, ?)",
		payeeID, pattern, string(matchField), priority,
	)
	if err != nil {
		return fmt.Errorf("insert rule %q: %w", pattern, err)
	}
	return nil
}

// LoadRules returns all payee rules.
func (d *DB) LoadRules() ([]PayeeRule, error) {
	rows, err := d.conn.Query("SELECT id, payee_id, pattern, match_field, priority FROM payee_rule ORDER BY priority DESC, length(pattern) DESC")
	if err != nil {
		return nil, fmt.Errorf("query rules: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			log.Printf("db: close rule rows: %v", cerr)
		}
	}()

	var rules []PayeeRule
	for rows.Next() {
		var r PayeeRule
		if err := rows.Scan(&r.ID, &r.PayeeID, &r.Pattern, &r.MatchField, &r.Priority); err != nil {
			return nil, fmt.Errorf("scan rule: %w", err)
		}
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rules: %w", err)
	}
	return rules, nil
}

// ResetPayeeData deletes every payee and payee rule, returning the counts
// removed. It is the clean-slate purge for the payee rework: run it, then
// re-learn from GnuCash and rebuild curations through map review. Auto-learned
// (priority 0) rules regenerate identically; hand-created ones are lost, which
// is the point.
func (d *DB) ResetPayeeData() (rules, payees int, err error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("begin reset: %w", err)
	}
	defer func() {
		if err != nil {
			if rberr := tx.Rollback(); rberr != nil {
				log.Printf("db: rollback reset: %v", rberr)
			}
		}
	}()

	if err = tx.QueryRow("SELECT count(*) FROM payee_rule").Scan(&rules); err != nil {
		return 0, 0, fmt.Errorf("count rules: %w", err)
	}
	if err = tx.QueryRow("SELECT count(*) FROM payee").Scan(&payees); err != nil {
		return 0, 0, fmt.Errorf("count payees: %w", err)
	}
	// Rules reference payees, so delete rules first.
	if _, err = tx.Exec("DELETE FROM payee_rule"); err != nil {
		return 0, 0, fmt.Errorf("delete rules: %w", err)
	}
	if _, err = tx.Exec("DELETE FROM payee"); err != nil {
		return 0, 0, fmt.Errorf("delete payees: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit reset: %w", err)
	}
	return rules, payees, nil
}

func (d *DB) runMigrations() error {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("read migration dir: %w", err)
	}

	var filenames []string
	for _, e := range entries {
		if !e.IsDir() {
			filenames = append(filenames, e.Name())
		}
	}
	sort.Strings(filenames)

	// Check whether migrations_applied table exists yet.
	var tableExists bool
	err = d.conn.QueryRow(
		"SELECT COUNT(*) > 0 FROM sqlite_master WHERE type='table' AND name='migrations_applied'",
	).Scan(&tableExists)
	if err != nil {
		return fmt.Errorf("check migrations_applied table: %w", err)
	}

	// The first migration (lexicographically) must create the migrations_applied
	// table, since the tracking INSERT below depends on it existing.
	for _, name := range filenames {
		if tableExists {
			var applied bool
			err := d.conn.QueryRow(
				"SELECT COUNT(*) > 0 FROM migrations_applied WHERE filename = ?", name,
			).Scan(&applied)
			if err != nil {
				return fmt.Errorf("check migration %s: %w", name, err)
			}
			if applied {
				continue
			}
		}

		sqlBytes, err := fs.ReadFile(migrationFiles, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		tx, err := d.conn.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}

		if _, err := tx.Exec(string(sqlBytes)); err != nil {
			if rerr := tx.Rollback(); rerr != nil {
				log.Printf("db: rollback migration %s: %v", name, rerr)
			}
			return fmt.Errorf("execute migration %s: %w", name, err)
		}

		// After executing the first migration, the table now exists.
		if _, err := tx.Exec(
			"INSERT INTO migrations_applied (filename) VALUES (?)", name,
		); err != nil {
			if rerr := tx.Rollback(); rerr != nil {
				log.Printf("db: rollback migration %s: %v", name, rerr)
			}
			return fmt.Errorf("record migration %s: %w", name, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
		tableExists = true
	}

	return nil
}
