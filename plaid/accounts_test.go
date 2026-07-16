package plaid

import (
	"context"
	"net/http"
	"testing"

	"github.com/jeffbstewart/bankferry/money"
)

// The 401k balance is the literal Plaid's sandbox returned. Four decimal
// places must survive; a float64 would not hold it exactly.
const accountsGetBody = `{
	"accounts": [
		{
			"account_id": "acc_check", "name": "Plaid Checking",
			"official_name": "Plaid Gold Standard 0%% Interest Checking",
			"mask": "0000", "type": "depository", "subtype": "checking",
			"balances": {"current": 110, "available": 100, "limit": null,
				"iso_currency_code": "USD", "unofficial_currency_code": null}
		},
		{
			"account_id": "acc_cc", "name": "Plaid Credit Card",
			"official_name": null, "mask": "3333",
			"type": "credit", "subtype": "credit card",
			"balances": {"current": 410, "available": null, "limit": 2000,
				"iso_currency_code": "USD", "unofficial_currency_code": null}
		},
		{
			"account_id": "acc_401k", "name": "Plaid 401k",
			"official_name": null, "mask": "6666",
			"type": "investment", "subtype": "401k",
			"balances": {"current": 23631.9805, "available": null, "limit": null,
				"iso_currency_code": "USD", "unofficial_currency_code": null}
		}
	],
	"item": {"item_id": "item_1", "institution_id": "ins_127989", "institution_name": "Bank of America"}
}`

func TestFetchAccounts(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, accountsGetBody)
	})

	accounts, item, err := c.FetchAccounts(context.Background(), "tok")
	if err != nil {
		t.Fatalf("FetchAccounts: %v", err)
	}

	if item.ItemID != "item_1" || item.InstitutionID != "ins_127989" {
		t.Errorf("item = %+v", item)
	}
	if item.InstitutionName != "Bank of America" {
		t.Errorf("institution name = %q", item.InstitutionName)
	}
	if len(accounts) != 3 {
		t.Fatalf("accounts = %d, want 3", len(accounts))
	}

	checking := accounts[0]
	if checking.Type != AccountTypeDepository || checking.Subtype != "checking" {
		t.Errorf("checking = %+v", checking)
	}
	if checking.Mask != "0000" {
		t.Errorf("mask = %q", checking.Mask)
	}
	if checking.Currency != money.USD {
		t.Errorf("currency = %q", checking.Currency)
	}
	if checking.Balance.Current == nil || mustExact(t, *checking.Balance.Current) != "110.00" {
		t.Errorf("current = %v", checking.Balance.Current)
	}
	if !checking.Balance.Current.CurrencyIs(money.USD) {
		t.Error("the balance amount should carry its currency")
	}
	if checking.Balance.Limit != nil {
		t.Error("a null limit must be nil, not zero")
	}

	cc := accounts[1]
	if cc.Type != AccountTypeCredit {
		t.Errorf("cc type = %q", cc.Type)
	}
	if cc.Balance.Available != nil {
		t.Error("a null available balance must be nil, not zero")
	}
	if cc.Balance.Limit == nil || mustExact(t, *cc.Balance.Limit) != "2000.00" {
		t.Errorf("limit = %v", cc.Balance.Limit)
	}
	if cc.OfficialName != "" {
		t.Errorf("official name = %q, want empty", cc.OfficialName)
	}

	// The reason this package exists.
	retirement := accounts[2]
	if retirement.Balance.Current == nil || mustExact(t, *retirement.Balance.Current) != "23631.9805" {
		t.Errorf("401k balance = %v, want 23631.9805 exactly", retirement.Balance.Current)
	}
}

// A currency Plaid does not officially support must stop the fetch rather
// than be folded into a dollar statement.
func TestFetchAccounts_RejectsUnofficialCurrency(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"accounts":[{
			"account_id":"a","name":"Crypto","type":"other","subtype":null,
			"balances":{"current":1.0,"iso_currency_code":null,"unofficial_currency_code":"BTC"}}],
			"item":{"item_id":"i"}}`)
	})

	if _, _, err := c.FetchAccounts(context.Background(), "tok"); err == nil {
		t.Fatal("expected an unofficial currency to be refused")
	}
}

func TestFetchAccounts_RejectsNonUSD(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"accounts":[{
			"account_id":"a","name":"Euro","type":"depository","subtype":"checking",
			"balances":{"current":1.0,"iso_currency_code":"EUR","unofficial_currency_code":null}}],
			"item":{"item_id":"i"}}`)
	})

	if _, _, err := c.FetchAccounts(context.Background(), "tok"); err == nil {
		t.Fatal("expected a euro account to be refused")
	}
}

func TestFetchAccounts_RejectsMissingCurrency(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"accounts":[{
			"account_id":"a","name":"Mystery","type":"depository","subtype":"checking",
			"balances":{"current":1.0,"iso_currency_code":null,"unofficial_currency_code":null}}],
			"item":{"item_id":"i"}}`)
	})

	if _, _, err := c.FetchAccounts(context.Background(), "tok"); err == nil {
		t.Fatal("expected an account with no currency to be refused")
	}
}

// An account with no balances at all is still usable; the balance is simply
// unknown, which is not the same as zero.
func TestFetchAccounts_AllBalancesNull(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"accounts":[{
			"account_id":"a","name":"Sparse","type":"depository","subtype":"checking",
			"balances":{"current":null,"available":null,"limit":null,
				"iso_currency_code":"USD","unofficial_currency_code":null}}],
			"item":{"item_id":"i"}}`)
	})

	accounts, _, err := c.FetchAccounts(context.Background(), "tok")
	if err != nil {
		t.Fatalf("FetchAccounts: %v", err)
	}
	if accounts[0].Balance.Current != nil {
		t.Error("a null current balance must be nil")
	}
	if accounts[0].Currency != money.USD {
		t.Error("currency should still be read from the balances object")
	}
}
