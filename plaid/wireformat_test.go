package plaid_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jeffbstewart/bankferry/money"
	"github.com/jeffbstewart/bankferry/plaid"
	"github.com/jeffbstewart/bankferry/secrets"
)

// These tests document how Plaid encodes monetary values on the wire,
// independently of how the generated Go SDK chooses to model them. The
// SDK exposes balances and transaction amounts as float64; that is the
// SDK's decision. What matters for a financial tool is what Plaid
// actually sends, because a JSON number is exact decimal text until
// something parses it into a binary float.
//
// If these ever fail, the money type in the adapter must be revisited.

// itemHealthOnce ensures the Item's health is checked once per test binary
// rather than once per test.
var (
	itemHealthOnce sync.Once
	itemHealthErr  string
)

// requireSandboxItem skips unless credentials and at least one linked
// sandbox Item exist, and fails if that Item needs re-authentication.
//
// The failure is deliberate. A sandbox Item enters ITEM_LOGIN_REQUIRED on
// its own thirty days after creation. Skipping would quietly retire the
// only tests that exercise the live API, and every other test would
// otherwise fail with an opaque ITEM_LOGIN_REQUIRED from whichever call
// happened to run first. Failing once, with the command that fixes it, is
// the honest outcome.
func requireSandboxItem(t *testing.T) plaid.Item {
	t.Helper()
	requireSandboxCredentials(t)

	items, broken, err := plaid.LoadItems(plaid.Sandbox)
	if err != nil {
		t.Fatalf("LoadItems: %v", err)
	}
	for _, b := range broken {
		t.Logf("warning: unreadable keyring entry %s: %v", b.Key, b.Err)
	}
	if len(items) == 0 {
		t.Skip("skipping: no linked sandbox Item (run 'bankferry plaid-link --env sandbox')")
	}

	item := items[0]
	itemHealthOnce.Do(func() { itemHealthErr = checkItemHealth(item) })
	if itemHealthErr != "" {
		t.Fatal(itemHealthErr)
	}
	return item
}

// checkItemHealth returns a message to fail with, or "" when the Item is
// usable.
func checkItemHealth(item plaid.Item) string {
	creds, err := plaid.LoadCredentials(plaid.Sandbox, plaid.KeyringDecrypter{})
	if err != nil {
		return fmt.Sprintf("LoadCredentials: %v", err)
	}

	client, err := plaid.NewDataClient(plaid.Sandbox, creds)
	if err != nil {
		return fmt.Sprintf("NewDataClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	status, err := client.FetchItemStatus(ctx, item.AccessToken)
	if err != nil {
		if plaid.IsLinkRefreshRequired(err) {
			return relinkInstructions(item, "ITEM_LOGIN_REQUIRED")
		}
		return fmt.Sprintf("FetchItemStatus for item %s: %v", item.ItemID, err)
	}
	if status.NeedsLinkRefresh() {
		return relinkInstructions(item, status.ErrorCode)
	}
	return ""
}

func relinkInstructions(item plaid.Item, code string) string {
	return fmt.Sprintf(`sandbox item %s (%s) reports %s and cannot serve these tests.

A sandbox Item enters ITEM_LOGIN_REQUIRED thirty days after it is created,
and /sandbox/item/reset_login puts it there on demand. Repair it in a
browser with Link update mode; the access token does not change and no Item
is consumed:

    go run . plaid-relink --env sandbox --item %s

Then re-run the tests.`, item.ItemID, item.InstitutionName, code, item.ItemID)
}

// rawPost calls a Plaid endpoint directly and returns the response body,
// bypassing the SDK so the JSON is untouched. The access token is sent
// but never returned or logged.
func rawPost(t *testing.T, path string, body map[string]any) []byte {
	t.Helper()

	creds, err := plaid.LoadCredentials(plaid.Sandbox, plaid.KeyringDecrypter{})
	if err != nil {
		if errors.Is(err, secrets.ErrNotFound) {
			t.Skip("skipping: no sandbox credentials")
		}
		t.Fatalf("LoadCredentials: %v", err)
	}

	body["client_id"] = creds.ClientID
	body["secret"] = creds.Secret

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshaling request: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://sandbox.plaid.com"+path, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing response body: %v", cerr)
		}
	}()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s returned %d: %s", path, resp.StatusCode, raw)
	}
	return raw
}

// decodeNumbers walks the JSON with json.Number so every numeric literal
// is preserved exactly as Plaid wrote it.
func decodeNumbers(t *testing.T, raw []byte, into any) {
	t.Helper()

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(into); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
}

// expectedCurrency is the only currency this tool is built for. OFX
// carries one CURDEF per statement, and the payee/amount pipeline does no
// conversion, so an account in another currency must be noticed rather
// than quietly folded into a dollar statement.
const expectedCurrency = "USD"

type wireAccounts struct {
	Accounts []struct {
		AccountID string `json:"account_id"`
		Name      string `json:"name"`
		Mask      string `json:"mask"`
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		Balances  struct {
			Current   *json.Number `json:"current"`
			Available *json.Number `json:"available"`
			Limit     *json.Number `json:"limit"`

			// Exactly one of these is non-null. Plaid sets
			// unofficial_currency_code for currencies it does not
			// officially support.
			IsoCurrencyCode        *string `json:"iso_currency_code"`
			UnofficialCurrencyCode *string `json:"unofficial_currency_code"`
		} `json:"balances"`
	} `json:"accounts"`
	Item struct {
		ItemID        string `json:"item_id"`
		InstitutionID string `json:"institution_id"`
	} `json:"item"`
}

// Plaid sends balances as JSON numbers, not strings. This test records
// which, and prints the exact literals so the encoding is visible.
func TestWire_AccountBalancesAreJSONNumbers(t *testing.T) {
	item := requireSandboxItem(t)

	raw := rawPost(t, "/accounts/get", map[string]any{"access_token": item.AccessToken})

	// A string balance would be quoted. Assert on the raw bytes before
	// any decoding can normalize it away.
	if bytes.Contains(raw, []byte(`"current":"`)) {
		t.Error(`balances.current is encoded as a JSON string; a fixed-decimal type is warranted`)
	}

	var got wireAccounts
	decodeNumbers(t, raw, &got)

	if len(got.Accounts) == 0 {
		t.Fatal("no accounts returned")
	}

	t.Logf("institution_id=%s item_id=%s accounts=%d",
		got.Item.InstitutionID, got.Item.ItemID, len(got.Accounts))

	for _, a := range got.Accounts {
		cur := numOrNull(a.Balances.Current)
		avail := numOrNull(a.Balances.Available)
		limit := numOrNull(a.Balances.Limit)
		ccy := strOrNull(a.Balances.IsoCurrencyCode)
		unofficial := strOrNull(a.Balances.UnofficialCurrencyCode)

		t.Logf("mask=%-4s type=%-10s subtype=%-14s current=%-12s available=%-12s limit=%-10s iso=%-5s unofficial=%s  name=%q",
			a.Mask, a.Type, a.Subtype, cur, avail, limit, ccy, unofficial, a.Name)

		if a.Type == "" {
			t.Errorf("account %s has no type", a.AccountID)
		}

		// A non-USD account cannot be written into a USD OFX statement.
		if a.Balances.UnofficialCurrencyCode != nil {
			t.Errorf("account %s (%s) reports unofficial currency %q; this tool assumes %s",
				a.AccountID, a.Name, *a.Balances.UnofficialCurrencyCode, expectedCurrency)
		}
		if a.Balances.IsoCurrencyCode == nil {
			t.Errorf("account %s (%s) reports no iso_currency_code", a.AccountID, a.Name)
			continue
		}
		if *a.Balances.IsoCurrencyCode != expectedCurrency {
			t.Errorf("account %s (%s) is denominated in %s, not %s; the OFX statement "+
				"carries a single CURDEF and this pipeline does no conversion",
				a.AccountID, a.Name, *a.Balances.IsoCurrencyCode, expectedCurrency)
		}
	}
}

func numOrNull(n *json.Number) string {
	if n == nil {
		return "null"
	}
	return n.String()
}

func strOrNull(s *string) string {
	if s == nil {
		return "null"
	}
	return *s
}

// Transaction amounts: same question, and the one that matters most,
// because it feeds the OFX TRNAMT field.
func TestWire_TransactionAmountsAreJSONNumbers(t *testing.T) {
	item := requireSandboxItem(t)

	raw := rawPost(t, "/transactions/sync", map[string]any{
		"access_token": item.AccessToken,
		"count":        100,
	})

	if bytes.Contains(raw, []byte(`"amount":"`)) {
		t.Error(`transaction amount is encoded as a JSON string; a fixed-decimal type is warranted`)
	}

	var got struct {
		Added []struct {
			TransactionID          string       `json:"transaction_id"`
			PendingTransactionID   *string      `json:"pending_transaction_id"`
			Pending                bool         `json:"pending"`
			Amount                 *json.Number `json:"amount"`
			Date                   string       `json:"date"`
			Name                   string       `json:"name"`
			MerchantName           *string      `json:"merchant_name"`
			IsoCurrencyCode        *string      `json:"iso_currency_code"`
			UnofficialCurrencyCode *string      `json:"unofficial_currency_code"`
		} `json:"added"`
		HasMore bool `json:"has_more"`
	}
	decodeNumbers(t, raw, &got)

	if len(got.Added) == 0 {
		t.Skip("sandbox Item has no transactions yet; the historical pull may still be running")
	}

	maxDecimals := 0
	for _, txn := range got.Added {
		if txn.Amount == nil {
			t.Errorf("transaction %s has a null amount", txn.TransactionID)
			continue
		}
		if d := decimalPlaces(txn.Amount.String()); d > maxDecimals {
			maxDecimals = d
		}

		if txn.UnofficialCurrencyCode != nil {
			t.Errorf("transaction %s reports unofficial currency %q; this tool assumes %s",
				txn.TransactionID, *txn.UnofficialCurrencyCode, expectedCurrency)
		}
		if txn.IsoCurrencyCode == nil {
			t.Errorf("transaction %s reports no iso_currency_code", txn.TransactionID)
			continue
		}
		if *txn.IsoCurrencyCode != expectedCurrency {
			t.Errorf("transaction %s is denominated in %s, not %s",
				txn.TransactionID, *txn.IsoCurrencyCode, expectedCurrency)
		}
	}

	t.Logf("added=%d has_more=%v max_decimal_places_seen=%d",
		len(got.Added), got.HasMore, maxDecimals)

	for i, txn := range got.Added {
		if i >= 8 {
			break
		}
		t.Logf("amount=%-10s ccy=%-4s pending=%-5v date=%s name=%q merchant=%q",
			txn.Amount.String(), strOrNull(txn.IsoCurrencyCode), txn.Pending,
			txn.Date, txn.Name, strOrNull(txn.MerchantName))
	}
}

// decimalPlaces counts digits after the decimal point in a JSON numeric
// literal, exactly as Plaid wrote it.
func decimalPlaces(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return len(s) - i - 1
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// DataClient against the live sandbox
// ---------------------------------------------------------------------------

func newSandboxDataClient(t *testing.T) *plaid.DataClient {
	t.Helper()

	creds, err := plaid.LoadCredentials(plaid.Sandbox, plaid.KeyringDecrypter{})
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}

	c, err := plaid.NewDataClient(plaid.Sandbox, creds)
	if err != nil {
		t.Fatalf("NewDataClient: %v", err)
	}
	return c
}

// The parsed accounts must agree with what the raw JSON said, and every
// balance must arrive as an exact Amount.
func TestIntegration_FetchAccounts(t *testing.T) {
	item := requireSandboxItem(t)
	c := newSandboxDataClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	accounts, info, err := c.FetchAccounts(ctx, item.AccessToken)
	if err != nil {
		t.Fatalf("FetchAccounts: %v", err)
	}
	if len(accounts) == 0 {
		t.Fatal("no accounts returned")
	}

	t.Logf("item=%s institution=%s (%s)", info.ItemID, info.InstitutionName, info.InstitutionID)

	sawDepository, sawCredit := false, false
	for _, a := range accounts {
		current := "null"
		if a.Balance.Current != nil {
			c, err := a.Balance.Current.Exact()
			if err != nil {
				t.Fatalf("Exact(): %v", err)
			}
			current = c
		}
		t.Logf("type=%-11s subtype=%-13s mask=%-4s currency=%s current=%s  %q",
			a.Type, a.Subtype, a.Mask, a.Currency, current, a.Name)

		if a.Currency != money.USD {
			t.Errorf("account %s is not USD", a.AccountID)
		}
		switch a.Type {
		case plaid.AccountTypeDepository:
			sawDepository = true
		case plaid.AccountTypeCredit:
			sawCredit = true
		}
	}

	if !sawDepository || !sawCredit {
		t.Logf("note: sandbox item lacks a depository or credit account (dep=%v credit=%v)",
			sawDepository, sawCredit)
	}
}

// A full sync from an empty cursor, then an immediate re-sync from the
// cursor it returned. The second must be empty: nothing changed in between.
// That is the property the whole incremental design rests on.
func TestIntegration_SyncTransactions_CursorAdvances(t *testing.T) {
	item := requireSandboxItem(t)
	c := newSandboxDataClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	first, err := c.SyncTransactions(ctx, item.AccessToken, "")
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if first.NextCursor == "" {
		t.Fatal("first sync returned no cursor")
	}
	t.Logf("first sync: added=%d modified=%d removed=%d",
		len(first.Added), len(first.Modified), len(first.Removed))

	if len(first.Added) == 0 {
		t.Skip("sandbox item has no transactions yet; the historical pull may still be running")
	}

	// Every amount is exact and in USD, and pending transactions are
	// identifiable so the exporter can drop them.
	pending := 0
	for _, txn := range first.Added {
		if !txn.Amount.CurrencyIs(money.USD) {
			t.Errorf("transaction %s is not USD", txn.ID)
		}
		if txn.Pending {
			pending++
		}
	}
	t.Logf("pending in first sync: %d of %d", pending, len(first.Added))

	second, err := c.SyncTransactions(ctx, item.AccessToken, first.NextCursor)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if n := len(second.Added) + len(second.Modified) + len(second.Removed); n != 0 {
		t.Errorf("re-syncing from the returned cursor produced %d changes, want 0", n)
	}
	if second.NextCursor == "" {
		t.Error("second sync returned no cursor")
	}
}

// The same cursor must always return the same delta, which is what makes a
// failed sync safe to retry.
func TestIntegration_SyncTransactions_CursorIsImmutable(t *testing.T) {
	item := requireSandboxItem(t)
	c := newSandboxDataClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	a, err := c.SyncTransactions(ctx, item.AccessToken, "")
	if err != nil {
		t.Fatalf("sync a: %v", err)
	}
	b, err := c.SyncTransactions(ctx, item.AccessToken, "")
	if err != nil {
		t.Fatalf("sync b: %v", err)
	}

	if len(a.Added) != len(b.Added) {
		t.Fatalf("same cursor returned %d then %d added transactions", len(a.Added), len(b.Added))
	}
	for i := range a.Added {
		if a.Added[i].ID != b.Added[i].ID {
			t.Errorf("added[%d] id differs between identical syncs: %s vs %s",
				i, a.Added[i].ID, b.Added[i].ID)
		}
		if !a.Added[i].Amount.Equal(b.Added[i].Amount) {
			t.Errorf("added[%d] amount differs between identical syncs", i)
		}
	}
}

// Transaction IDs must be stable across separate fetches. If they were not,
// they could not be used as the OFX FITID, and GnuCash would duplicate
// every transaction on every import.
func TestIntegration_TransactionIDsAreStableAcrossFetches(t *testing.T) {
	item := requireSandboxItem(t)
	c := newSandboxDataClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	first, err := c.SyncTransactions(ctx, item.AccessToken, "")
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if len(first.Added) == 0 {
		t.Skip("sandbox item has no transactions yet")
	}

	seen := make(map[string]string, len(first.Added))
	for _, txn := range first.Added {
		if prev, dup := seen[txn.ID]; dup {
			t.Errorf("transaction id %s appears twice in one sync (%q and %q)", txn.ID, prev, txn.Name)
		}
		seen[txn.ID] = txn.Name
	}

	again, err := c.SyncTransactions(ctx, item.AccessToken, "")
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	for _, txn := range again.Added {
		if _, ok := seen[txn.ID]; !ok {
			t.Errorf("transaction %s (%q) appeared with a new id on a repeated sync", txn.ID, txn.Name)
		}
	}
}

// Plaid's own sandbox pair, checked against the live API. If these ever
// disagree, the OAuth flag is not what this tool believes it is.
func TestIntegration_SandboxOAuthInstitutions(t *testing.T) {
	requireSandboxCredentials(t)
	c := newSandboxDataClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oauth, err := c.FetchInstitution(ctx, plaid.SandboxOAuthInstitution)
	if err != nil {
		t.Fatalf("FetchInstitution(%s): %v", plaid.SandboxOAuthInstitution, err)
	}
	t.Logf("%s = %q, oauth=%v", oauth.ID, oauth.Name, oauth.OAuth)
	if !oauth.OAuth {
		t.Errorf("%s (%s) should report oauth=true", oauth.Name, oauth.ID)
	}

	plain, err := c.FetchInstitution(ctx, plaid.SandboxNonOAuthInstitution)
	if err != nil {
		t.Fatalf("FetchInstitution(%s): %v", plaid.SandboxNonOAuthInstitution, err)
	}
	t.Logf("%s = %q, oauth=%v", plain.ID, plain.Name, plain.OAuth)
	if plain.OAuth {
		t.Errorf("%s (%s) should report oauth=false", plain.Name, plain.ID)
	}
}

// Whatever is linked in sandbox, report whether its institution runs OAuth.
// This is the question "did my Chase link use OAuth" answered from the API
// rather than from the look of the pane.
func TestIntegration_LinkedInstitutionOAuthFlag(t *testing.T) {
	item := requireSandboxItem(t)
	c := newSandboxDataClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if item.InstitutionID == "" {
		t.Skip("this item recorded no institution ID")
	}

	info, err := c.FetchInstitution(ctx, item.InstitutionID)
	if err != nil {
		t.Fatalf("FetchInstitution: %v", err)
	}
	t.Logf("linked institution %s (%s): oauth=%v", info.Name, info.ID, info.OAuth)
}

func TestIntegration_FetchItemStatus(t *testing.T) {
	item := requireSandboxItem(t)
	c := newSandboxDataClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	status, err := c.FetchItemStatus(ctx, item.AccessToken)
	if err != nil {
		t.Fatalf("FetchItemStatus: %v", err)
	}
	if status.ItemID != item.ItemID {
		t.Errorf("item_id = %q, want %q", status.ItemID, item.ItemID)
	}

	expiry := "none"
	if status.ConsentExpiresAt != nil {
		expiry = status.ConsentExpiresAt.String()
	}
	t.Logf("item=%s institution=%s consent_expires=%s error=%q",
		status.ItemID, status.InstitutionID, expiry, status.ErrorCode)

	if status.NeedsLinkRefresh() {
		t.Log("note: this sandbox item needs a link refresh; " +
			"sandbox items enter ITEM_LOGIN_REQUIRED 30 days after creation")
	}
}
