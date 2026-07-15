package plaid

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jeffbstewart/bankferry/civildate"
)

// syncStub serves a scripted sequence of /transactions/sync responses and
// records the cursor each request carried.
type syncStub struct {
	pages   []string
	cursors []string
	calls   int
}

func (s *syncStub) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		cursor, _ := body["cursor"].(string)
		s.cursors = append(s.cursors, cursor)

		if s.calls >= len(s.pages) {
			t.Errorf("unexpected call %d", s.calls)
			writeJSON(t, w, http.StatusInternalServerError, `{}`)
			return
		}
		page := s.pages[s.calls]
		s.calls++

		if page == "FAIL" {
			writeJSON(t, w, http.StatusBadRequest, `{
				"error_type":"API_ERROR","error_code":"INTERNAL_SERVER_ERROR",
				"error_message":"boom","request_id":"req_1"}`)
			return
		}
		writeJSON(t, w, http.StatusOK, page)
	}
}

func txnJSON(id, amount, date string, pending bool) string {
	return `{
		"transaction_id":"` + id + `",
		"account_id":"acc_1",
		"amount":` + amount + `,
		"date":"` + date + `",
		"name":"Uber 072515 SF",
		"merchant_name":"Uber",
		"pending":` + boolStr(pending) + `,
		"pending_transaction_id":null,
		"iso_currency_code":"USD",
		"unofficial_currency_code":null
	}`
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// ---------------------------------------------------------------------------
// Decoding
// ---------------------------------------------------------------------------

func TestSyncTransactions_DecodesExactAmounts(t *testing.T) {
	stub := &syncStub{pages: []string{`{
		"added":[` + txnJSON("txn_1", "89.4", "2026-07-09", false) + `],
		"modified":[],"removed":[],
		"next_cursor":"cur_1","has_more":false}`}}
	c := newTestClient(t, stub.handler(t))

	got, err := c.SyncTransactions(context.Background(), "tok", "")
	if err != nil {
		t.Fatalf("SyncTransactions: %v", err)
	}
	if len(got.Added) != 1 {
		t.Fatalf("added = %d, want 1", len(got.Added))
	}

	txn := got.Added[0]
	if amt := mustExact(t, txn.Amount); amt != "89.40" {
		t.Errorf("Amount.Exact() = %q, want 89.40", amt)
	}
	// Plaid writes money leaving the account as positive, and source keeps
	// that convention, so a purchase must not be negative here.
	if txn.Amount.IsNegative() {
		t.Error("a purchase must be positive in Plaid's convention")
	}
	if txn.Date.Compare(civildate.MustNew(2026, time.July, 9)) != 0 {
		t.Errorf("Date = %v", txn.Date)
	}
	if txn.MerchantName != "Uber" || txn.Name != "Uber 072515 SF" {
		t.Errorf("name=%q merchant=%q", txn.Name, txn.MerchantName)
	}
	if got.NextCursor != "cur_1" {
		t.Errorf("NextCursor = %q", got.NextCursor)
	}
}

// A first sync sends no cursor at all; Plaid rejects an explicit null.
func TestSyncTransactions_FirstSyncOmitsCursor(t *testing.T) {
	stub := &syncStub{pages: []string{`{"added":[],"modified":[],"removed":[],"next_cursor":"cur_1","has_more":false}`}}
	c := newTestClient(t, stub.handler(t))

	if _, err := c.SyncTransactions(context.Background(), "tok", ""); err != nil {
		t.Fatalf("SyncTransactions: %v", err)
	}
	if stub.cursors[0] != "" {
		t.Errorf("first sync sent cursor %q, want none", stub.cursors[0])
	}
}

// ---------------------------------------------------------------------------
// Pagination
// ---------------------------------------------------------------------------

func TestSyncTransactions_DrainsPages(t *testing.T) {
	stub := &syncStub{pages: []string{
		`{"added":[` + txnJSON("txn_1", "1.00", "2026-07-01", false) + `],
		  "modified":[],"removed":[],"next_cursor":"cur_1","has_more":true}`,
		`{"added":[` + txnJSON("txn_2", "2.00", "2026-07-02", false) + `],
		  "modified":[],"removed":[{"transaction_id":"txn_0","account_id":"acc_1"}],
		  "next_cursor":"cur_2","has_more":false}`,
	}}
	c := newTestClient(t, stub.handler(t))

	got, err := c.SyncTransactions(context.Background(), "tok", "cur_0")
	if err != nil {
		t.Fatalf("SyncTransactions: %v", err)
	}
	if len(got.Added) != 2 {
		t.Errorf("added = %d, want 2", len(got.Added))
	}
	if len(got.Removed) != 1 || got.Removed[0].ID != "txn_0" {
		t.Errorf("removed = %+v", got.Removed)
	}
	if got.NextCursor != "cur_2" {
		t.Errorf("NextCursor = %q, want cur_2", got.NextCursor)
	}

	// Each page must be requested with the cursor the previous one returned.
	want := []string{"cur_0", "cur_1"}
	for i, w := range want {
		if stub.cursors[i] != w {
			t.Errorf("page %d sent cursor %q, want %q", i, stub.cursors[i], w)
		}
	}
}

// A failure part-way through the drain must return no cursor and no
// transactions. The caller keeps its old cursor and retries, rather than
// advancing past transactions it never saw. The cursor is immutable, so the
// retry returns the same delta.
func TestSyncTransactions_MidDrainFailureAdvancesNothing(t *testing.T) {
	stub := &syncStub{pages: []string{
		`{"added":[` + txnJSON("txn_1", "1.00", "2026-07-01", false) + `],
		  "modified":[],"removed":[],"next_cursor":"cur_1","has_more":true}`,
		"FAIL",
	}}
	c := newTestClient(t, stub.handler(t))

	got, err := c.SyncTransactions(context.Background(), "tok", "cur_0")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got.NextCursor != "" {
		t.Errorf("NextCursor = %q, want empty so the caller does not advance", got.NextCursor)
	}
	if len(got.Added) != 0 {
		t.Errorf("added = %d, want none: a partial delta must not be applied", len(got.Added))
	}
}

// An empty next_cursor would silently reset the Item to its full history on
// the next run. Refuse it.
func TestSyncTransactions_EmptyCursorIsAnError(t *testing.T) {
	stub := &syncStub{pages: []string{`{"added":[],"modified":[],"removed":[],"next_cursor":"","has_more":false}`}}
	c := newTestClient(t, stub.handler(t))

	if _, err := c.SyncTransactions(context.Background(), "tok", "cur_0"); err == nil {
		t.Fatal("expected an error for an empty next_cursor")
	}
}

// ---------------------------------------------------------------------------
// pending -> posted
// ---------------------------------------------------------------------------

// When a pending transaction posts, Plaid removes the pending id and adds a
// new transaction with a different id, linked by pending_transaction_id.
// Exporting the pending one would therefore import the purchase twice,
// because GnuCash deduplicates on FITID alone.
func TestSyncTransactions_PendingPostsUnderANewID(t *testing.T) {
	page := `{
		"added":[{
			"transaction_id":"txn_posted",
			"account_id":"acc_1",
			"amount":25.00,
			"date":"2026-07-09",
			"name":"Coffee",
			"merchant_name":null,
			"pending":false,
			"pending_transaction_id":"txn_pending",
			"iso_currency_code":"USD",
			"unofficial_currency_code":null
		}],
		"modified":[],
		"removed":[{"transaction_id":"txn_pending","account_id":"acc_1"}],
		"next_cursor":"cur_1","has_more":false}`

	stub := &syncStub{pages: []string{page}}
	c := newTestClient(t, stub.handler(t))

	got, err := c.SyncTransactions(context.Background(), "tok", "cur_0")
	if err != nil {
		t.Fatalf("SyncTransactions: %v", err)
	}

	if len(got.Added) != 1 || got.Added[0].ID != "txn_posted" {
		t.Fatalf("added = %+v", got.Added)
	}
	if got.Added[0].PendingTransactionID != "txn_pending" {
		t.Error("the posted transaction must link back to the pending one")
	}
	if got.Added[0].Pending {
		t.Error("the posted transaction must not be pending")
	}
	if len(got.Removed) != 1 || got.Removed[0].ID != "txn_pending" {
		t.Errorf("removed = %+v, want the pending id", got.Removed)
	}
	if got.Added[0].ID == got.Removed[0].ID {
		t.Error("the posted id must differ from the pending id")
	}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func TestSyncTransactions_RejectsNonUSD(t *testing.T) {
	page := `{"added":[{
		"transaction_id":"txn_1","account_id":"acc_1","amount":1.00,
		"date":"2026-07-09","name":"x","pending":false,
		"iso_currency_code":"EUR","unofficial_currency_code":null}],
		"modified":[],"removed":[],"next_cursor":"c","has_more":false}`

	stub := &syncStub{pages: []string{page}}
	c := newTestClient(t, stub.handler(t))

	if _, err := c.SyncTransactions(context.Background(), "tok", "c0"); err == nil {
		t.Fatal("expected a euro transaction to be refused")
	}
}

func TestSyncTransactions_RejectsBadDate(t *testing.T) {
	page := `{"added":[{
		"transaction_id":"txn_1","account_id":"acc_1","amount":1.00,
		"date":"09/07/2026","name":"x","pending":false,
		"iso_currency_code":"USD","unofficial_currency_code":null}],
		"modified":[],"removed":[],"next_cursor":"c","has_more":false}`

	stub := &syncStub{pages: []string{page}}
	c := newTestClient(t, stub.handler(t))

	if _, err := c.SyncTransactions(context.Background(), "tok", "c0"); err == nil {
		t.Fatal("expected an unparseable date to be refused")
	}
}
