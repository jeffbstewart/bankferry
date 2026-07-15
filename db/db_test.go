package db

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestOpen_CreatesDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file not created: %v", err)
	}
}

func TestOpen_RunsMigrations(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	for _, table := range []string{"migrations_applied", "exported_transaction"} {
		var count int
		err := d.conn.QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&count)
		if err != nil {
			t.Fatalf("query sqlite_master for %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s not found", table)
		}
	}
}

func TestOpen_MigrationsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	d1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := d1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() {
		if err := d2.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
	}()

	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		t.Fatalf("read migration dir: %v", err)
	}
	expectedCount := 0
	for _, e := range entries {
		if !e.IsDir() {
			expectedCount++
		}
	}

	var count int
	err = d2.conn.QueryRow("SELECT COUNT(*) FROM migrations_applied").Scan(&count)
	if err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != expectedCount {
		t.Errorf("expected %d applied migrations, got %d", expectedCount, count)
	}
}

func TestIsExported_NotFound(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	exported, err := d.IsExported("txn_nonexistent")
	if err != nil {
		t.Fatalf("IsExported: %v", err)
	}
	if exported {
		t.Error("expected false for unknown transaction ID")
	}
}

func TestMarkExported_ThenIsExported(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	ids := []string{"txn_001", "txn_002"}
	if err := d.MarkExported("acc_1", ids); err != nil {
		t.Fatalf("MarkExported: %v", err)
	}

	for _, id := range ids {
		exported, err := d.IsExported(id)
		if err != nil {
			t.Fatalf("IsExported(%s): %v", id, err)
		}
		if !exported {
			t.Errorf("expected %s to be exported", id)
		}
	}
}

func TestMarkExported_Idempotent(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	if err := d.MarkExported("acc_1", []string{"txn_001"}); err != nil {
		t.Fatalf("first MarkExported: %v", err)
	}
	if err := d.MarkExported("acc_1", []string{"txn_001"}); err != nil {
		t.Fatalf("second MarkExported: %v", err)
	}
}

func TestMarkExported_EmptySlice(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	if err := d.MarkExported("acc_1", []string{}); err != nil {
		t.Fatalf("MarkExported with empty slice: %v", err)
	}
}

func TestMarkExported_MultipleIDs(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	ids := []string{"txn_a", "txn_b", "txn_c", "txn_d", "txn_e"}
	if err := d.MarkExported("acc_1", ids); err != nil {
		t.Fatalf("MarkExported: %v", err)
	}

	for _, id := range ids {
		exported, err := d.IsExported(id)
		if err != nil {
			t.Fatalf("IsExported(%s): %v", id, err)
		}
		if !exported {
			t.Errorf("expected %s to be exported", id)
		}
	}
}

func TestIsExported_DifferentAccount(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	if err := d.MarkExported("acc_1", []string{"txn_shared"}); err != nil {
		t.Fatalf("MarkExported: %v", err)
	}

	// Transaction ID is global, not per-account — querying without
	// specifying an account should still find it.
	exported, err := d.IsExported("txn_shared")
	if err != nil {
		t.Fatalf("IsExported: %v", err)
	}
	if !exported {
		t.Error("expected txn_shared to be exported regardless of account")
	}
}

// --- Payee tests ---

func TestUpsertPayee_Insert(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	id, err := d.UpsertPayee("Amazon")
	if err != nil {
		t.Fatalf("UpsertPayee: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero ID")
	}

	payees, err := d.LoadPayees()
	if err != nil {
		t.Fatalf("LoadPayees: %v", err)
	}
	if len(payees) != 1 {
		t.Fatalf("expected 1 payee, got %d", len(payees))
	}
	if payees[0].Name != "Amazon" {
		t.Errorf("unexpected payee: %+v", payees[0])
	}
}

// Re-inserting an existing payee returns the same ID and creates no
// second row, so learn can be re-run against a grown GnuCash file.
func TestUpsertPayee_Idempotent(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	id1, err := d.UpsertPayee("Amazon")
	if err != nil {
		t.Fatalf("first UpsertPayee: %v", err)
	}

	id2, err := d.UpsertPayee("Amazon")
	if err != nil {
		t.Fatalf("second UpsertPayee: %v", err)
	}

	if id1 != id2 {
		t.Errorf("expected same ID, got %d and %d", id1, id2)
	}

	payees, err := d.LoadPayees()
	if err != nil {
		t.Fatalf("LoadPayees: %v", err)
	}
	if len(payees) != 1 {
		t.Fatalf("expected 1 payee, got %d", len(payees))
	}
}

// --- Rule tests ---

func TestInsertRuleIfNew(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	id, err := d.UpsertPayee("Amazon")
	if err != nil {
		t.Fatalf("UpsertPayee: %v", err)
	}

	if err := d.InsertRuleIfNew(id, "AMAZON.COM", MatchRaw, 0); err != nil {
		t.Fatalf("InsertRuleIfNew: %v", err)
	}

	rules, err := d.LoadRules()
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Pattern != "AMAZON.COM" || rules[0].PayeeID != id {
		t.Errorf("unexpected rule: %+v", rules[0])
	}
	if rules[0].MatchField != MatchRaw {
		t.Errorf("MatchField = %q, want raw", rules[0].MatchField)
	}
}

// The same pattern may key both a raw and a merchant rule; they are distinct.
func TestInsertRuleIfNew_SamePatternDifferentField(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	id, err := d.UpsertPayee("Amazon")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.InsertRuleIfNew(id, "AMAZON", MatchRaw, 0); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertRuleIfNew(id, "AMAZON", MatchMerchant, 0); err != nil {
		t.Fatal(err)
	}

	rules, err := d.LoadRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules (raw + merchant), got %d", len(rules))
	}
}

func TestInsertRuleIfNew_RejectsInvalidField(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	id, err := d.UpsertPayee("Amazon")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.InsertRuleIfNew(id, "AMAZON", MatchField("bogus"), 0); err == nil {
		t.Error("expected an error for an invalid match field")
	}
}

func TestResetPayeeData(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	id, err := d.UpsertPayee("Amazon")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.InsertRuleIfNew(id, "AMAZON", MatchRaw, 0); err != nil {
		t.Fatal(err)
	}

	rules, payees, err := d.ResetPayeeData()
	if err != nil {
		t.Fatalf("ResetPayeeData: %v", err)
	}
	if rules != 1 || payees != 1 {
		t.Errorf("reset removed %d rules, %d payees; want 1, 1", rules, payees)
	}

	got, err := d.LoadRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected no rules after reset, got %d", len(got))
	}
	gotP, err := d.LoadPayees()
	if err != nil {
		t.Fatal(err)
	}
	if len(gotP) != 0 {
		t.Errorf("expected no payees after reset, got %d", len(gotP))
	}
}

func TestInsertRuleIfNew_Duplicate(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	id, err := d.UpsertPayee("Amazon")
	if err != nil {
		t.Fatalf("UpsertPayee: %v", err)
	}

	if err := d.InsertRuleIfNew(id, "AMAZON", MatchRaw, 0); err != nil {
		t.Fatalf("first InsertRuleIfNew: %v", err)
	}
	if err := d.InsertRuleIfNew(id, "AMAZON", MatchRaw, 10); err != nil {
		t.Fatalf("second InsertRuleIfNew: %v", err)
	}

	rules, err := d.LoadRules()
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule (duplicate ignored), got %d", len(rules))
	}
}

func TestLoadRules_OrderByPriorityAndLength(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	id1, err := d.UpsertPayee("Amazon")
	if err != nil {
		t.Fatalf("UpsertPayee: %v", err)
	}
	id2, err := d.UpsertPayee("Whole Foods")
	if err != nil {
		t.Fatalf("UpsertPayee: %v", err)
	}

	// Insert rules in non-sorted order.
	if err := d.InsertRuleIfNew(id1, "AMZ", MatchRaw, 0); err != nil {
		t.Fatalf("InsertRuleIfNew: %v", err)
	}
	if err := d.InsertRuleIfNew(id2, "WHOLEFDS", MatchRaw, 10); err != nil {
		t.Fatalf("InsertRuleIfNew: %v", err)
	}
	if err := d.InsertRuleIfNew(id1, "AMAZON.COM", MatchRaw, 0); err != nil {
		t.Fatalf("InsertRuleIfNew: %v", err)
	}

	rules, err := d.LoadRules()
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rules))
	}

	// Priority 10 first, then priority 0 sorted by length desc.
	if rules[0].Pattern != "WHOLEFDS" {
		t.Errorf("expected WHOLEFDS first (highest priority), got %s", rules[0].Pattern)
	}
	if rules[1].Pattern != "AMAZON.COM" {
		t.Errorf("expected AMAZON.COM second (longer), got %s", rules[1].Pattern)
	}
	if rules[2].Pattern != "AMZ" {
		t.Errorf("expected AMZ third (shorter), got %s", rules[2].Pattern)
	}
}

func TestWrappedAPIKey_RoundTrip(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	if _, found, err := d.LoadWrappedAPIKey("production"); err != nil || found {
		t.Fatalf("empty store: found=%v err=%v", found, err)
	}

	blob := []byte(`{"format":1,"environment":"production"}`)
	if err := d.SaveWrappedAPIKey("production", blob); err != nil {
		t.Fatalf("SaveWrappedAPIKey: %v", err)
	}

	got, found, err := d.LoadWrappedAPIKey("production")
	if err != nil || !found {
		t.Fatalf("after save: found=%v err=%v", found, err)
	}
	if string(got) != string(blob) {
		t.Errorf("blob = %q, want %q", got, blob)
	}

	// Overwrite in place.
	blob2 := []byte(`{"format":1,"environment":"production","v":2}`)
	if err := d.SaveWrappedAPIKey("production", blob2); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := d.LoadWrappedAPIKey("production"); string(got) != string(blob2) {
		t.Errorf("after overwrite = %q, want %q", got, blob2)
	}

	if err := d.DeleteWrappedAPIKey("production"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := d.LoadWrappedAPIKey("production"); found {
		t.Error("blob still present after delete")
	}
}

func TestWrappedAPIKey_RejectsEmptyBlob(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	if err := d.SaveWrappedAPIKey("production", nil); err == nil {
		t.Error("an empty blob should be rejected")
	}
}
