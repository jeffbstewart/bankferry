package plaid

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeffbstewart/bankferry/civildate"
)

// holdingsFixture is shaped like a real /investments/holdings/get response,
// with the values chosen to catch anything that rounds: a balance with four
// decimal places, a fractional share count with nine, and a cost basis that
// a float64 would not keep.
const holdingsFixture = `{
  "accounts": [{
    "account_id": "acct_roth", "name": "Roth IRA", "official_name": "Vanguard Roth IRA",
    "mask": "4321", "type": "investment", "subtype": "roth",
    "balances": {"current": 23631.9805, "available": 1200.50, "limit": null,
                 "iso_currency_code": "USD", "unofficial_currency_code": null}
  }, {
    "account_id": "acct_brk", "name": "Brokerage", "official_name": null,
    "mask": null, "type": "investment", "subtype": "brokerage",
    "balances": {"current": 100, "available": null, "limit": null,
                 "iso_currency_code": "USD", "unofficial_currency_code": null}
  }],
  "holdings": [{
    "account_id": "acct_roth", "security_id": "sec_vti",
    "quantity": 123.456789123, "cost_basis": 12345678.123456789,
    "institution_price": 250.1234, "institution_price_as_of": "2026-08-20",
    "institution_value": 30873.8771,
    "iso_currency_code": "USD", "unofficial_currency_code": null
  }, {
    "account_id": "acct_brk", "security_id": "sec_cash",
    "quantity": 100, "cost_basis": null,
    "institution_price": 1, "institution_price_as_of": null,
    "institution_value": 100,
    "iso_currency_code": "USD", "unofficial_currency_code": null
  }],
  "securities": [{
    "security_id": "sec_vti", "ticker_symbol": "VTI", "cusip": "922908769", "isin": "US9229087690",
    "name": "Vanguard Total Stock Market ETF", "type": "etf", "is_cash_equivalent": false,
    "iso_currency_code": "USD", "unofficial_currency_code": null
  }, {
    "security_id": "sec_cash", "ticker_symbol": null, "cusip": null, "isin": null,
    "name": "U S Dollar", "type": "cash", "is_cash_equivalent": true,
    "iso_currency_code": "USD", "unofficial_currency_code": null
  }, {
    "security_id": "sec_foreign", "ticker_symbol": "NESN", "cusip": null, "isin": "CH0038863350",
    "name": "Nestle", "type": "equity", "is_cash_equivalent": false,
    "iso_currency_code": "CHF", "unofficial_currency_code": null
  }],
  "item": {"item_id": "item_1", "institution_id": "ins_vanguard", "institution_name": "Vanguard"},
  "request_id": "req_1"
}`

func TestFetchHoldings_KeepsEveryLiteralVerbatim(t *testing.T) {
	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		writeJSON(t, w, http.StatusOK, holdingsFixture)
	})

	res, err := c.FetchHoldings(context.Background(), "tok")
	if err != nil {
		t.Fatalf("FetchHoldings: %v", err)
	}
	if gotPath != "/investments/holdings/get" {
		t.Errorf("path = %q", gotPath)
	}
	if res.Item != (ItemInfo{ItemID: "item_1", InstitutionID: "ins_vanguard", InstitutionName: "Vanguard"}) {
		t.Errorf("item = %+v", res.Item)
	}

	if len(res.Accounts) != 2 {
		t.Fatalf("accounts = %d", len(res.Accounts))
	}
	roth := res.Accounts[0]
	if roth.Current == nil || *roth.Current != (DecimalAmount{"23631.9805", "USD"}) {
		t.Errorf("roth current = %+v", roth.Current)
	}
	if roth.Available == nil || roth.Available.Value != "1200.50" {
		t.Errorf("roth available = %+v; trailing zero must survive", roth.Available)
	}
	if roth.OfficialName != "Vanguard Roth IRA" || roth.Mask != "4321" || roth.Subtype != "roth" {
		t.Errorf("roth = %+v", roth)
	}
	brk := res.Accounts[1]
	if brk.Available != nil {
		t.Errorf("a null available balance came back as %+v, want nil", brk.Available)
	}
	if brk.OfficialName != "" || brk.Mask != "" {
		t.Errorf("null strings came back as %+v", brk)
	}

	if len(res.Holdings) != 2 {
		t.Fatalf("holdings = %d", len(res.Holdings))
	}
	vti := res.Holdings[0]
	want := Holding{
		AccountID: "acct_roth", SecurityID: "sec_vti", Quantity: "123.456789123",
		CostBasis:        &DecimalAmount{"12345678.123456789", "USD"},
		InstitutionPrice: &DecimalAmount{"250.1234", "USD"},
		InstitutionValue: &DecimalAmount{"30873.8771", "USD"},
		PriceAsOf:        date(t, 2026, 8, 20),
	}
	if vti.AccountID != want.AccountID || vti.SecurityID != want.SecurityID || vti.Quantity != want.Quantity ||
		*vti.CostBasis != *want.CostBasis || *vti.InstitutionPrice != *want.InstitutionPrice ||
		*vti.InstitutionValue != *want.InstitutionValue || vti.PriceAsOf.Compare(want.PriceAsOf) != 0 {
		t.Errorf("vti holding = %+v, want %+v", vti, want)
	}
	cash := res.Holdings[1]
	if cash.CostBasis != nil || !cash.PriceAsOf.IsZero() {
		t.Errorf("absent cost basis / price date came back as %+v", cash)
	}
	if cash.Quantity != "100" {
		t.Errorf("cash quantity = %q", cash.Quantity)
	}

	if len(res.Securities) != 3 {
		t.Fatalf("securities = %d", len(res.Securities))
	}
	if s := res.Securities[0]; s != (Security{ID: "sec_vti", Ticker: "VTI", CUSIP: "922908769", ISIN: "US9229087690",
		Name: "Vanguard Total Stock Market ETF", Type: "etf", Currency: "USD"}) {
		t.Errorf("vti security = %+v", s)
	}
	if s := res.Securities[1]; !s.IsCashEquivalent || s.Ticker != "" {
		t.Errorf("cash security = %+v", s)
	}
	if s := res.Securities[2]; s.Currency != "CHF" {
		t.Errorf("foreign security currency = %q; a non-USD code is data here, not an error", s.Currency)
	}
}

// Nothing is parsed into a float on the way through. The proof is a literal
// no float64 can represent: it must come out as the same bytes.
func TestFetchHoldings_DoesNotRoundThroughFloat(t *testing.T) {
	const precise = "0.1000000000000000055511151231257827"
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"accounts":[],"securities":[],"item":{"item_id":"i"},
		  "holdings":[{"account_id":"a","security_id":"s","quantity":`+precise+`,
		               "institution_value":`+precise+`,"iso_currency_code":"USD"}]}`)
	})
	res, err := c.FetchHoldings(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Holdings[0].Quantity; got != precise {
		t.Errorf("quantity = %q, want %q", got, precise)
	}
	if got := res.Holdings[0].InstitutionValue.Value; got != precise {
		t.Errorf("value = %q, want %q", got, precise)
	}
}

func TestFetchHoldings_Refusals(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"unofficial currency on a holding": {
			`{"accounts":[],"securities":[],"item":{"item_id":"i"},
			  "holdings":[{"account_id":"a","security_id":"s","quantity":1,"institution_value":1,
			               "iso_currency_code":null,"unofficial_currency_code":"BTC"}]}`,
			"unofficial currency",
		},
		"value without a currency": {
			`{"accounts":[],"securities":[],"item":{"item_id":"i"},
			  "holdings":[{"account_id":"a","security_id":"s","quantity":1,"institution_value":1}]}`,
			"has no currency",
		},
		"malformed currency code": {
			`{"accounts":[],"securities":[],"item":{"item_id":"i"},
			  "holdings":[{"account_id":"a","security_id":"s","quantity":1,"institution_value":1,"iso_currency_code":"usd"}]}`,
			"malformed currency",
		},
		"missing quantity": {
			`{"accounts":[],"securities":[],"item":{"item_id":"i"},
			  "holdings":[{"account_id":"a","security_id":"s","iso_currency_code":"USD"}]}`,
			"quantity is missing",
		},
		"security without an id": {
			`{"accounts":[],"holdings":[],"item":{"item_id":"i"},"securities":[{"name":"x"}]}`,
			"no security_id",
		},
		"unparseable price date": {
			`{"accounts":[],"securities":[],"item":{"item_id":"i"},
			  "holdings":[{"account_id":"a","security_id":"s","quantity":1,"institution_price_as_of":"yesterday","iso_currency_code":"USD"}]}`,
			"unparseable price date",
		},
		"account balance in unofficial currency": {
			`{"holdings":[],"securities":[],"item":{"item_id":"i"},
			  "accounts":[{"account_id":"a","name":"n","type":"investment",
			               "balances":{"current":1,"unofficial_currency_code":"BTC"}}]}`,
			"unofficial currency",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, http.StatusOK, tc.body)
			})
			_, err := c.FetchHoldings(context.Background(), "tok")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// Plaid omits a security's currency for some types; that is tolerated. It
// never omits one on an amount; that is not.
func TestFetchHoldings_SecurityMayLackCurrency(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"accounts":[],"holdings":[],"item":{"item_id":"i"},
		  "securities":[{"security_id":"s","type":"derivative"}]}`)
	})
	res, err := c.FetchHoldings(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if res.Securities[0].Currency != "" {
		t.Errorf("currency = %q", res.Securities[0].Currency)
	}
}

// ---------------------------------------------------------------------------
// Transactions
// ---------------------------------------------------------------------------

func txnPage(ids []string, total int) string {
	rows := make([]string, len(ids))
	for i, id := range ids {
		rows[i] = `{"investment_transaction_id":"` + id + `","account_id":"acct","security_id":"sec_vti",
		  "date":"2026-03-0` + string(rune('1'+i%9)) + `","name":"BUY VTI","quantity":1.5,"amount":-375.1850,
		  "price":250.1233,"fees":0,"type":"buy","subtype":"buy","iso_currency_code":"USD"}`
	}
	return `{"investment_transactions":[` + strings.Join(rows, ",") + `],
	  "securities":[{"security_id":"sec_vti","ticker_symbol":"VTI","iso_currency_code":"USD"}],
	  "total_investment_transactions":` + itoa(total) + `,"request_id":"r"}`
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestFetchInvestmentTransactions_PagesToTheTotal(t *testing.T) {
	var requests []map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		requests = append(requests, body)
		offset := int(body["options"].(map[string]any)["offset"].(float64))
		switch offset {
		case 0:
			writeJSON(t, w, http.StatusOK, txnPage([]string{"t1", "t2"}, 3))
		case 2:
			writeJSON(t, w, http.StatusOK, txnPage([]string{"t3"}, 3))
		default:
			t.Errorf("unexpected offset %d", offset)
		}
	})

	start, end := date(t, 2024, 9, 1), date(t, 2026, 8, 21)
	res, err := c.FetchInvestmentTransactions(context.Background(), "tok", start, end)
	if err != nil {
		t.Fatalf("FetchInvestmentTransactions: %v", err)
	}

	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	first := requests[0]
	if first["start_date"] != "2024-09-01" || first["end_date"] != "2026-08-21" {
		t.Errorf("window = %v..%v", first["start_date"], first["end_date"])
	}
	if opts := first["options"].(map[string]any); opts["count"] != float64(investmentTxnPageSize) {
		t.Errorf("count = %v", opts["count"])
	}

	if len(res.Transactions) != 3 {
		t.Fatalf("transactions = %d", len(res.Transactions))
	}
	tx := res.Transactions[0]
	if tx.ID != "t1" || tx.SecurityID != "sec_vti" || tx.Type != "buy" || tx.Quantity != "1.5" ||
		tx.Amount != (DecimalAmount{"-375.1850", "USD"}) || tx.Price.Value != "250.1233" || tx.Fees.Value != "0" ||
		tx.Date.Compare(date(t, 2026, 3, 1)) != 0 {
		t.Errorf("transaction = %+v", tx)
	}
	// The securities table repeats on every page; it is reported once.
	if len(res.Securities) != 1 || res.Securities[0].Ticker != "VTI" {
		t.Errorf("securities = %+v", res.Securities)
	}
}

func TestFetchInvestmentTransactions_CashRowHasNoSecurity(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"investment_transactions":[
		  {"investment_transaction_id":"t","account_id":"a","security_id":null,"date":"2026-01-02",
		   "name":"FEE","quantity":0,"amount":12.34,"price":null,"fees":null,"type":"fee","subtype":"management fee",
		   "iso_currency_code":"USD"}],"securities":[],"total_investment_transactions":1}`)
	})
	res, err := c.FetchInvestmentTransactions(context.Background(), "tok", date(t, 2026, 1, 1), date(t, 2026, 1, 31))
	if err != nil {
		t.Fatal(err)
	}
	tx := res.Transactions[0]
	if tx.SecurityID != "" || tx.Price != nil || tx.Fees != nil || tx.Amount.Value != "12.34" {
		t.Errorf("fee row = %+v", tx)
	}
}

func TestFetchInvestmentTransactions_PartialFailureDiscardsEverything(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			writeJSON(t, w, http.StatusOK, txnPage([]string{"t1"}, 2))
			return
		}
		writeJSON(t, w, http.StatusBadRequest, `{"error_type":"API_ERROR","error_code":"INTERNAL_SERVER_ERROR","error_message":"boom"}`)
	})
	res, err := c.FetchInvestmentTransactions(context.Background(), "tok", date(t, 2026, 1, 1), date(t, 2026, 1, 31))
	if err == nil {
		t.Fatal("a failed second page returned a result")
	}
	if len(res.Transactions) != 0 {
		t.Errorf("a partial result leaked: %+v", res.Transactions)
	}
}

// A server that keeps reporting a total it never delivers must not spin.
func TestFetchInvestmentTransactions_EmptyPageBeforeTotalIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, txnPage(nil, 5))
	})
	_, err := c.FetchInvestmentTransactions(context.Background(), "tok", date(t, 2026, 1, 1), date(t, 2026, 1, 31))
	if err == nil || !strings.Contains(err.Error(), "page was empty") {
		t.Errorf("error = %v", err)
	}
}

func TestFetchInvestmentTransactions_RefusesABadWindow(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the request reached the server")
	})
	ctx := context.Background()
	if _, err := c.FetchInvestmentTransactions(ctx, "tok", date(t, 2026, 2, 1), date(t, 2026, 1, 1)); err == nil {
		t.Error("inverted window accepted")
	}
	if _, err := c.FetchInvestmentTransactions(ctx, "tok", civildate.ISO8601Date{}, date(t, 2026, 1, 1)); err == nil {
		t.Error("zero start accepted")
	}
}

func TestFetchInvestmentTransactions_RefusesAMissingAmount(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"investment_transactions":[
		  {"investment_transaction_id":"t","account_id":"a","date":"2026-01-02","name":"x","quantity":0,
		   "type":"cash","subtype":"x","iso_currency_code":"USD"}],"securities":[],"total_investment_transactions":1}`)
	})
	_, err := c.FetchInvestmentTransactions(context.Background(), "tok", date(t, 2026, 1, 1), date(t, 2026, 1, 31))
	if err == nil || !strings.Contains(err.Error(), "has no amount") {
		t.Errorf("error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Error classification
// ---------------------------------------------------------------------------

func TestInvestmentsErrorClassification(t *testing.T) {
	api := func(code string) error {
		return &APIError{Op: "x", ErrorCode: code}
	}
	for _, code := range []string{ErrorCodeProductsNotSupported, ErrorCodeInvalidProduct, ErrorCodeNoInvestmentAccounts} {
		if !IsInvestmentsUnavailable(api(code)) {
			t.Errorf("%s not classified as unavailable", code)
		}
		if IsProductNotReady(api(code)) {
			t.Errorf("%s classified as not-ready", code)
		}
	}
	if !IsProductNotReady(api(ErrorCodeProductNotReady)) || IsInvestmentsUnavailable(api(ErrorCodeProductNotReady)) {
		t.Error("PRODUCT_NOT_READY misclassified")
	}
	if IsInvestmentsUnavailable(api(ErrorCodeItemLoginRequired)) {
		t.Error("ITEM_LOGIN_REQUIRED classified as unavailable; it is repairable")
	}
	if IsInvestmentsUnavailable(errors.New("plain")) || IsProductNotReady(errors.New("plain")) {
		t.Error("a non-API error was classified")
	}
}

func date(t *testing.T, y, m, d int) civildate.ISO8601Date {
	t.Helper()
	v, err := civildate.New(y, time.Month(m), d)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
