package plaid_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jeffbstewart/bankferry/plaid"
)

// This is the harness for gate P2: do posted transaction IDs survive a
// re-authentication unchanged?
//
// It matters more than any other property here. The OFX FITID is the
// transaction ID, and GnuCash deduplicates imports on FITID alone, never on
// content. If an ID changes when an Item is repaired, every transaction
// re-imports as a duplicate. That is precisely what happened with Teller:
// re-enrolling shifted the IDs and the book gained a second copy of
// everything.
//
// The check spans a manual step, so it runs in two phases:
//
//  1. With no baseline on disk, it records the current posted transactions
//     and skips, printing what to do next.
//  2. With a baseline, it re-syncs and fails on any drift.
//
// Between the two, break the Item and repair it:
//
//	go run . plaid-reset-login --env sandbox
//	go run . plaid-relink --env sandbox      (completes in a browser)

// snapshotTxn is the identity of one posted transaction, as it must survive
// a re-authentication.
type snapshotTxn struct {
	AccountID string `json:"account_id"`
	Date      string `json:"date"`
	Amount    string `json:"amount"`
	Name      string `json:"name"`
}

type idSnapshot struct {
	ItemID   string                 `json:"item_id"`
	TakenAt  string                 `json:"taken_at"`
	Postures map[string]snapshotTxn `json:"transactions"`
}

// snapshotPath is stable across runs so the baseline survives between the
// two phases. PLAID_ID_SNAPSHOT overrides it.
func snapshotPath(t *testing.T, itemID string) string {
	t.Helper()

	if override := os.Getenv("PLAID_ID_SNAPSHOT"); override != "" {
		return override
	}

	dir, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("locating a cache directory: %v", err)
	}
	dir = filepath.Join(dir, "bankferry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	return filepath.Join(dir, fmt.Sprintf("plaid-ids-sandbox-%s.json", itemID))
}

// postedTransactions syncs the Item's whole history and keys the posted
// transactions by ID.
//
// Pending transactions are excluded on purpose. Plaid models a pending
// transaction that posts as a *different* transaction with a new ID, linked
// by pending_transaction_id, so a pending ID legitimately disappears. Only
// posted IDs are promised to be stable, and only posted transactions are
// ever written to OFX.
func postedTransactions(t *testing.T, c *plaid.DataClient, accessToken string) map[string]snapshotTxn {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	result, err := c.SyncTransactions(ctx, accessToken, "")
	if err != nil {
		t.Fatalf("SyncTransactions: %v", err)
	}

	posted := make(map[string]snapshotTxn, len(result.Added))
	for _, txn := range result.Added {
		if txn.Pending {
			continue
		}
		amount, err := txn.Amount.Exact()
		if err != nil {
			t.Fatalf("Exact() for %s: %v", txn.ID, err)
		}
		posted[txn.ID] = snapshotTxn{
			AccountID: txn.AccountID,
			Date:      txn.Date.String(),
			Amount:    amount,
			Name:      txn.Name,
		}
	}
	return posted
}

func loadSnapshot(t *testing.T, path string) (idSnapshot, bool) {
	t.Helper()

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return idSnapshot{}, false
	}
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var snap idSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("parsing %s: %v (delete it to start over)", path, err)
	}
	return snap, true
}

func writeSnapshot(t *testing.T, path string, snap idSnapshot) {
	t.Helper()

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		t.Fatalf("encoding snapshot: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestIntegration_TransactionIDsSurviveReauth is gate P2.
func TestIntegration_TransactionIDsSurviveReauth(t *testing.T) {
	item := requireSandboxItem(t)
	c := newSandboxDataClient(t)

	path := snapshotPath(t, item.ItemID)
	baseline, found := loadSnapshot(t, path)

	current := postedTransactions(t, c, item.AccessToken)
	if len(current) == 0 {
		t.Skip("sandbox item has no posted transactions yet")
	}

	if !found {
		writeSnapshot(t, path, idSnapshot{
			ItemID:   item.ItemID,
			TakenAt:  time.Now().UTC().Format(time.RFC3339),
			Postures: current,
		})
		t.Skipf(`baseline of %d posted transaction IDs written to
    %s

Now break and repair the Item, then re-run this test:

    go run . plaid-reset-login --env sandbox
    go run . plaid-relink --env sandbox      (completes in a browser)
    go test ./plaid/... -run TransactionIDsSurviveReauth -v`,
			len(current), path)
	}

	if baseline.ItemID != item.ItemID {
		t.Fatalf("baseline is for item %s, not %s; delete %s",
			baseline.ItemID, item.ItemID, path)
	}

	t.Logf("comparing %d posted transactions against a baseline taken %s",
		len(baseline.Postures), baseline.TakenAt)

	// Update mode can add or drop accounts. A transaction whose account is
	// no longer linked is absent for that reason, not because Plaid reissued
	// its ID. Conflating the two would raise a false alarm on this gate.
	linked := linkedAccountIDs(t, c, item.AccessToken)

	// Every transaction present before must still be present, under the
	// same ID, with the same content.
	var missing, unlinked []string
	changed := 0
	for id, want := range baseline.Postures {
		got, ok := current[id]
		if !ok {
			if !linked[want.AccountID] {
				unlinked = append(unlinked, id)
			} else {
				missing = append(missing, id)
			}
			continue
		}
		if got != want {
			changed++
			t.Errorf("transaction %s changed across re-authentication:\n  before: %+v\n  after:  %+v",
				id, want, got)
		}
	}

	if len(unlinked) > 0 {
		t.Logf("note: %d baseline transactions belong to accounts that are no longer "+
			"linked; account selection changed during update mode, which is not an ID reissue",
			len(unlinked))
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		show := missing
		if len(show) > 10 {
			show = show[:10]
		}
		t.Errorf(`%d of %d posted transaction IDs vanished after re-authentication,
from accounts that are still linked.

Plaid reissued them, so they cannot serve as the OFX FITID: GnuCash
deduplicates on FITID alone, and every transaction would import a second
time. This is gate P2 and it has failed. Examples: %v`,
			len(missing), len(baseline.Postures), show)
	}

	// Transactions appearing that were not in the baseline are expected only
	// if the sandbox generated new ones, or a pending transaction posted.
	// Report them rather than fail: they are not evidence of reissued IDs.
	appeared := 0
	for id := range current {
		if _, ok := baseline.Postures[id]; !ok {
			appeared++
		}
	}
	if appeared > 0 {
		t.Logf("note: %d posted transactions appeared since the baseline "+
			"(new sandbox activity, or pending transactions that posted)", appeared)
	}

	if len(missing) == 0 && changed == 0 {
		t.Logf("gate P2 holds: %d of %d posted transaction IDs survived re-authentication "+
			"unchanged (%d belonged to accounts no longer linked)",
			len(baseline.Postures)-len(unlinked), len(baseline.Postures), len(unlinked))
	}
}

// linkedAccountIDs returns the account IDs currently reachable under the
// Item.
func linkedAccountIDs(t *testing.T, c *plaid.DataClient, accessToken string) map[string]bool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	accounts, _, err := c.FetchAccounts(ctx, accessToken)
	if err != nil {
		t.Fatalf("FetchAccounts: %v", err)
	}

	linked := make(map[string]bool, len(accounts))
	for _, a := range accounts {
		linked[a.AccountID] = true
	}
	return linked
}
