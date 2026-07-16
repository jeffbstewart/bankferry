package plaid

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jeffbstewart/bankferry/civildate"
	"github.com/jeffbstewart/bankferry/money"
)

// syncPageSize is the transactions requested per page. 500 is Plaid's
// maximum and keeps a weekly fetch to a single page in practice.
const syncPageSize = 500

// syncPageLimit bounds the drain loop. A first sync of two years of
// history is a few pages; hundreds would mean the cursor is not advancing,
// which is a bug worth failing on rather than looping forever.
const syncPageLimit = 200

// Transaction is one transaction as Plaid describes it.
//
// Amount follows Plaid's convention: money leaving the account is
// positive. source.Transaction carries the same convention, so the adapter
// copies it through without negation.
type Transaction struct {
	ID        string
	AccountID string
	Amount    money.Amount
	Date      civildate.ISO8601Date

	// Name is the raw descriptor, e.g. "Uber 072515 SF**POOL**".
	// MerchantName is Plaid's normalized payee, e.g. "Uber". It is often
	// empty.
	Name         string
	MerchantName string

	Pending bool

	// PendingTransactionID links a posted transaction back to the pending
	// one it superseded, which carried a different ID. Empty otherwise.
	PendingTransactionID string
}

// RemovedTransaction identifies a transaction Plaid has deleted. Plaid
// sends only the identifiers, not the full object.
type RemovedTransaction struct {
	ID        string
	AccountID string
}

// SyncResult is the accumulated delta since a cursor.
//
// The three arrays mean different things, and conflating them corrupts a
// GnuCash book:
//
//   - Added: transactions Plaid has not sent before.
//   - Modified: transactions already sent whose fields changed. The ID is
//     unchanged. A posted transaction is not immutable; a refund or an
//     institution recategorization can surface it here.
//   - Removed: transactions to delete. This fires both when a pending
//     transaction posts, superseded by a new transaction with a *different*
//     ID, and when a transaction is genuinely reversed.
//
// Because a pending transaction posts under a new ID, pending transactions
// must never be exported to OFX. GnuCash deduplicates on FITID alone, so
// exporting the pending one and later the posted one would import the same
// purchase twice.
type SyncResult struct {
	Added    []Transaction
	Modified []Transaction
	Removed  []RemovedTransaction

	// NextCursor is only meaningful when the whole delta drained cleanly.
	NextCursor string
}

type wireTransaction struct {
	TransactionID        string       `json:"transaction_id"`
	AccountID            string       `json:"account_id"`
	Amount               *json.Number `json:"amount"`
	Date                 string       `json:"date"`
	Name                 string       `json:"name"`
	MerchantName         *string      `json:"merchant_name"`
	Pending              bool         `json:"pending"`
	PendingTransactionID *string      `json:"pending_transaction_id"`

	IsoCurrencyCode        *string `json:"iso_currency_code"`
	UnofficialCurrencyCode *string `json:"unofficial_currency_code"`
}

type wireRemoved struct {
	TransactionID string `json:"transaction_id"`
	AccountID     string `json:"account_id"`
}

type wireSync struct {
	Added      []wireTransaction `json:"added"`
	Modified   []wireTransaction `json:"modified"`
	Removed    []wireRemoved     `json:"removed"`
	NextCursor string            `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
}

// SyncTransactions drains the delta since cursor and returns it whole.
//
// Pass an empty cursor on the very first sync to receive the Item's full
// history. Store NextCursor and pass it next time.
//
// The cursor is opaque and immutable: calling again with the same cursor
// returns the same delta, so a failed call is safe to retry. That is why a
// failure part-way through the drain discards every page fetched so far
// and returns no cursor. The caller keeps the cursor it already had and
// re-runs, rather than advancing past transactions it never saw.
func (c *DataClient) SyncTransactions(ctx context.Context, accessToken, cursor string) (SyncResult, error) {
	var result SyncResult

	for page := 0; ; page++ {
		if page >= syncPageLimit {
			return SyncResult{}, fmt.Errorf(
				"plaid: transactions sync did not finish after %d pages; the cursor is not advancing",
				syncPageLimit)
		}

		body := map[string]any{
			"access_token": accessToken,
			"count":        syncPageSize,
		}
		// Omit the cursor entirely on a first sync; Plaid rejects null.
		if cursor != "" {
			body["cursor"] = cursor
		}

		var resp wireSync
		if err := c.post(ctx, "transactions sync", "/transactions/sync", body, &resp); err != nil {
			return SyncResult{}, err
		}

		added, err := convertTransactions(resp.Added)
		if err != nil {
			return SyncResult{}, err
		}
		modified, err := convertTransactions(resp.Modified)
		if err != nil {
			return SyncResult{}, err
		}

		result.Added = append(result.Added, added...)
		result.Modified = append(result.Modified, modified...)
		for _, r := range resp.Removed {
			result.Removed = append(result.Removed, RemovedTransaction{
				ID:        r.TransactionID,
				AccountID: r.AccountID,
			})
		}

		if resp.NextCursor == "" {
			return SyncResult{}, fmt.Errorf("plaid: transactions sync returned an empty cursor")
		}
		cursor = resp.NextCursor

		if !resp.HasMore {
			result.NextCursor = cursor
			return result, nil
		}
	}
}

func convertTransactions(raw []wireTransaction) ([]Transaction, error) {
	out := make([]Transaction, 0, len(raw))
	for _, t := range raw {
		txn, err := convertTransaction(t)
		if err != nil {
			return nil, err
		}
		out = append(out, txn)
	}
	return out, nil
}

func convertTransaction(t wireTransaction) (Transaction, error) {
	what := fmt.Sprintf("transaction %s", t.TransactionID)

	amount, err := parseAmount(what, t.Amount, t.IsoCurrencyCode, t.UnofficialCurrencyCode)
	if err != nil {
		return Transaction{}, err
	}

	date, err := civildate.Parse("2006-01-02", t.Date)
	if err != nil {
		return Transaction{}, fmt.Errorf("plaid: %s has an unparseable date %q: %w", what, t.Date, err)
	}

	txn := Transaction{
		ID:        t.TransactionID,
		AccountID: t.AccountID,
		Amount:    amount,
		Date:      date,
		Name:      t.Name,
		Pending:   t.Pending,
	}
	if t.MerchantName != nil {
		txn.MerchantName = *t.MerchantName
	}
	if t.PendingTransactionID != nil {
		txn.PendingTransactionID = *t.PendingTransactionID
	}
	return txn, nil
}
