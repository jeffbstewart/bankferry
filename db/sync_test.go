package db

import (
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()

	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return d
}

// No cursor means the next sync starts from the beginning of history.
// Absent must be distinguishable from empty.
func TestLoadSyncCursor_Absent(t *testing.T) {
	d := openTestDB(t)

	cursor, found, err := d.LoadSyncCursor("sandbox", "item_1")
	if err != nil {
		t.Fatalf("LoadSyncCursor: %v", err)
	}
	if found {
		t.Error("expected no cursor")
	}
	if cursor != "" {
		t.Errorf("cursor = %q", cursor)
	}
}

func TestCommitSync_StoresCursorAndTransactions(t *testing.T) {
	d := openTestDB(t)

	exported := map[string][]string{
		"acc_1": {"txn_1", "txn_2"},
		"acc_2": {"txn_3"},
	}
	if err := d.CommitSync("sandbox", "item_1", exported, "cur_1"); err != nil {
		t.Fatalf("CommitSync: %v", err)
	}

	cursor, found, err := d.LoadSyncCursor("sandbox", "item_1")
	if err != nil {
		t.Fatal(err)
	}
	if !found || cursor != "cur_1" {
		t.Errorf("cursor = %q found = %v", cursor, found)
	}

	for _, id := range []string{"txn_1", "txn_2", "txn_3"} {
		exported, err := d.IsExported(id)
		if err != nil {
			t.Fatal(err)
		}
		if !exported {
			t.Errorf("%s should be marked exported", id)
		}
	}
}

func TestCommitSync_AdvancesCursor(t *testing.T) {
	d := openTestDB(t)

	if err := d.CommitSync("sandbox", "item_1", nil, "cur_1"); err != nil {
		t.Fatal(err)
	}
	if err := d.CommitSync("sandbox", "item_1", nil, "cur_2"); err != nil {
		t.Fatal(err)
	}

	cursor, _, err := d.LoadSyncCursor("sandbox", "item_1")
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "cur_2" {
		t.Errorf("cursor = %q, want cur_2", cursor)
	}
}

// A sync that produced nothing new still advances the cursor.
func TestCommitSync_EmptyExportedIsFine(t *testing.T) {
	d := openTestDB(t)

	if err := d.CommitSync("sandbox", "item_1", map[string][]string{}, "cur_1"); err != nil {
		t.Fatalf("CommitSync: %v", err)
	}
	if _, found, _ := d.LoadSyncCursor("sandbox", "item_1"); !found {
		t.Error("cursor should have been stored")
	}
}

// An empty cursor would replay the Item's entire history on the next run.
func TestCommitSync_RefusesEmptyCursor(t *testing.T) {
	d := openTestDB(t)

	if err := d.CommitSync("sandbox", "item_1", nil, ""); err == nil {
		t.Fatal("expected an empty cursor to be refused")
	}
	if _, found, _ := d.LoadSyncCursor("sandbox", "item_1"); found {
		t.Error("nothing should have been stored")
	}
}

func TestCommitSync_RequiresEnvironmentAndItem(t *testing.T) {
	d := openTestDB(t)

	if err := d.CommitSync("", "item_1", nil, "cur"); err == nil {
		t.Error("expected an error for an empty environment")
	}
	if err := d.CommitSync("sandbox", "", nil, "cur"); err == nil {
		t.Error("expected an error for an empty item ID")
	}
}

// Sandbox and production cursors are independent. Applying one to the
// other would either replay an Item entirely or skip it.
func TestCommitSync_EnvironmentsAreIsolated(t *testing.T) {
	d := openTestDB(t)

	if err := d.CommitSync("sandbox", "item_1", nil, "cur_sandbox"); err != nil {
		t.Fatal(err)
	}
	if err := d.CommitSync("production", "item_1", nil, "cur_production"); err != nil {
		t.Fatal(err)
	}

	cursor, _, err := d.LoadSyncCursor("sandbox", "item_1")
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "cur_sandbox" {
		t.Errorf("sandbox cursor = %q", cursor)
	}
}

// The cursor and the exported transactions move together or not at all.
// If the cursor advanced without the transactions being recorded, Plaid
// would never re-deliver them and they would be lost.
//
// The failure is forced after the transaction rows are inserted: SQLite's
// length() counts characters "prior to the first NUL character", so a
// cursor of "\x00" clears the Go guard against emptiness and then fails
// the CHECK constraint, rolling the whole transaction back. A real base64
// cursor never contains a NUL; this is simply the only way to fail the
// second statement of the pair.
func TestCommitSync_RollsBackTransactionsWhenTheCursorWriteFails(t *testing.T) {
	d := openTestDB(t)

	if err := d.CommitSync("sandbox", "item_1", nil, "cur_1"); err != nil {
		t.Fatal(err)
	}

	err := d.CommitSync("sandbox", "item_1", map[string][]string{"acc_1": {"txn_1"}}, "\x00")
	if err == nil {
		t.Fatal("expected the commit to fail on the cursor constraint")
	}

	// Neither the cursor nor the transaction may have moved.
	cursor, _, lerr := d.LoadSyncCursor("sandbox", "item_1")
	if lerr != nil {
		t.Fatal(lerr)
	}
	if cursor != "cur_1" {
		t.Errorf("cursor = %q, want the previous cur_1", cursor)
	}
	exported, lerr := d.IsExported("txn_1")
	if lerr != nil {
		t.Fatal(lerr)
	}
	if exported {
		t.Error("txn_1 must not be recorded when the cursor write failed")
	}
}

// Re-committing the same transaction IDs is idempotent, which is what makes
// a re-run after a crash safe.
func TestCommitSync_IsIdempotent(t *testing.T) {
	d := openTestDB(t)

	exported := map[string][]string{"acc_1": {"txn_1"}}
	if err := d.CommitSync("sandbox", "item_1", exported, "cur_1"); err != nil {
		t.Fatal(err)
	}
	if err := d.CommitSync("sandbox", "item_1", exported, "cur_2"); err != nil {
		t.Fatalf("re-committing the same transactions must succeed: %v", err)
	}

	cursor, _, err := d.LoadSyncCursor("sandbox", "item_1")
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "cur_2" {
		t.Errorf("cursor = %q, want cur_2", cursor)
	}
}

func TestResetSyncCursor(t *testing.T) {
	d := openTestDB(t)

	if err := d.CommitSync("sandbox", "item_1", map[string][]string{"acc_1": {"txn_1"}}, "cur_1"); err != nil {
		t.Fatal(err)
	}
	if err := d.ResetSyncCursor("sandbox", "item_1"); err != nil {
		t.Fatalf("ResetSyncCursor: %v", err)
	}

	if _, found, err := d.LoadSyncCursor("sandbox", "item_1"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Error("cursor should be gone")
	}

	// Recovery, not duplication: the export record survives, so a replayed
	// history still filters out what was already written.
	exported, err := d.IsExported("txn_1")
	if err != nil {
		t.Fatal(err)
	}
	if !exported {
		t.Error("resetting the cursor must not forget what was exported")
	}
}

func TestResetSyncCursor_AbsentIsNotAnError(t *testing.T) {
	d := openTestDB(t)

	if err := d.ResetSyncCursor("sandbox", "nope"); err != nil {
		t.Errorf("resetting a missing cursor should be a no-op: %v", err)
	}
}
