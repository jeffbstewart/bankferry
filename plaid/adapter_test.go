package plaid

import (
	"testing"
	"time"

	"github.com/jeffbstewart/bankferry/civildate"
	"github.com/jeffbstewart/bankferry/money"
	"github.com/jeffbstewart/bankferry/source"
)

func amt(s string) money.Amount { return money.MustParse(s, money.USD) }

func amtPtr(s string) *money.Amount {
	a := amt(s)
	return &a
}

func testItemInfo() ItemInfo {
	return ItemInfo{ItemID: "item_1", InstitutionID: "ins_1", InstitutionName: "Bank of America"}
}

func plaidAccount(id, name, typ, subtype, mask string, current *money.Amount) AccountInfo {
	return AccountInfo{
		AccountID: id,
		Name:      name,
		Mask:      mask,
		Type:      typ,
		Subtype:   subtype,
		Currency:  money.USD,
		Balance:   Balance{Current: current},
	}
}

// The sandbox Item's real shape: checking, savings, credit card, 401k.
func sandboxAccounts() []AccountInfo {
	return []AccountInfo{
		plaidAccount("acc_check", "Plaid Checking", "depository", "checking", "0000", amtPtr("110.00")),
		plaidAccount("acc_save", "Plaid Saving", "depository", "savings", "1111", amtPtr("210.00")),
		plaidAccount("acc_cc", "Plaid Credit Card", "credit", "credit card", "3333", amtPtr("410.00")),
		plaidAccount("acc_401k", "Plaid 401k", "investment", "401k", "6666", amtPtr("23631.9805")),
	}
}

// ---------------------------------------------------------------------------
// Accounts
// ---------------------------------------------------------------------------

// An investment account has no place in a bank OFX statement.
func TestSourceAccounts_DropsNonBankAccounts(t *testing.T) {
	got := SourceAccounts(sandboxAccounts(), testItemInfo())

	if len(got) != 3 {
		t.Fatalf("accounts = %d, want 3 (the 401k is dropped)", len(got))
	}
	for _, a := range got {
		if a.ID == "acc_401k" {
			t.Fatal("the 401k must not be exported")
		}
	}
}

func TestSkippedAccounts_NamesWhatWasDropped(t *testing.T) {
	skipped := SkippedAccounts(sandboxAccounts())

	if len(skipped) != 1 || skipped[0].AccountID != "acc_401k" {
		t.Fatalf("skipped = %+v, want the 401k", skipped)
	}
}

func TestSourceAccounts_MapsTypesAndSubtypes(t *testing.T) {
	got := SourceAccounts(sandboxAccounts(), testItemInfo())

	want := []struct {
		id      string
		typ     source.AccountType
		subtype source.AccountSubtype
	}{
		{"acc_check", source.Depository, source.Checking},
		{"acc_save", source.Depository, source.Savings},
		{"acc_cc", source.Credit, source.CreditCard},
	}
	for i, w := range want {
		if got[i].ID != w.id || got[i].Type != w.typ || got[i].Subtype != w.subtype {
			t.Errorf("account %d = %+v, want %s/%s/%s", i, got[i], w.id, w.typ, w.subtype)
		}
	}
}

// Plaid spells two subtypes with a space. Getting either wrong silently
// falls back to checking.
func TestSourceSubtype_SpellingsWithSpaces(t *testing.T) {
	cases := []struct {
		in   string
		want source.AccountSubtype
	}{
		{"checking", source.Checking},
		{"savings", source.Savings},
		{"money market", source.MoneyMarket},
		{"credit card", source.CreditCard},
		{"cd", source.Checking},           // unrecognized depository subtype
		{"money_market", source.Checking}, // not how Plaid spells it
	}
	for _, tc := range cases {
		if got := sourceSubtype(tc.in); got != tc.want {
			t.Errorf("sourceSubtype(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSourceAccounts_CarriesInstitutionAndBalance(t *testing.T) {
	got := SourceAccounts(sandboxAccounts(), testItemInfo())

	checking := got[0]
	if checking.Institution.ID != "ins_1" || checking.Institution.Name != "Bank of America" {
		t.Errorf("institution = %+v", checking.Institution)
	}
	if checking.LastFour != "0000" {
		t.Errorf("LastFour = %q", checking.LastFour)
	}
	if checking.Currency != money.USD {
		t.Errorf("currency = %q", checking.Currency)
	}
	if checking.Balance == nil || mustExact(t, *checking.Balance) != "110.00" {
		t.Errorf("balance = %v", checking.Balance)
	}
}

// A mask may be shorter than four characters. It is used verbatim.
func TestSourceAccounts_ShortMask(t *testing.T) {
	accounts := []AccountInfo{
		plaidAccount("acc_1", "Short", "depository", "checking", "12", amtPtr("1.00")),
	}
	got := SourceAccounts(accounts, testItemInfo())
	if got[0].LastFour != "12" {
		t.Errorf("LastFour = %q, want the mask verbatim", got[0].LastFour)
	}
}

// An unknown balance stays unknown; it is not a balance of zero.
func TestSourceAccounts_NilBalanceStaysNil(t *testing.T) {
	accounts := []AccountInfo{
		plaidAccount("acc_1", "Sparse", "depository", "checking", "0000", nil),
	}
	got := SourceAccounts(accounts, testItemInfo())
	if got[0].Balance != nil {
		t.Error("a missing balance must not become zero")
	}
}

// The converted balance must not alias the Plaid struct's pointer.
func TestSourceAccounts_BalanceIsCopied(t *testing.T) {
	original := amt("110.00")
	accounts := []AccountInfo{
		plaidAccount("acc_1", "Checking", "depository", "checking", "0000", &original),
	}

	got := SourceAccounts(accounts, testItemInfo())
	if got[0].Balance == &original {
		t.Error("the source account aliases Plaid's balance pointer")
	}
	if bal := mustExact(t, *got[0].Balance); bal != "110.00" {
		t.Errorf("balance = %s", bal)
	}
}

// ---------------------------------------------------------------------------
// Transactions
// ---------------------------------------------------------------------------

func plaidTxn(id, account, amount string, pending bool) Transaction {
	return Transaction{
		ID:           id,
		AccountID:    account,
		Amount:       amt(amount),
		Date:         civildate.MustNew(2026, time.July, 9),
		Name:         "Uber 072515 SF**POOL**",
		MerchantName: "Uber",
		Pending:      pending,
	}
}

// The amount passes through with no negation at all. Plaid signs money
// leaving the account as positive, which is exactly what source documents.
func TestSourceTransaction_AmountIsNotNegated(t *testing.T) {
	purchase := plaidTxn("txn_1", "acc_1", "89.40", false)

	got := SourceTransaction(purchase)

	if got.Amount.IsNegative() {
		t.Fatal("a purchase must stay positive: money leaving the account")
	}
	if amt := mustExact(t, got.Amount); amt != "89.40" {
		t.Errorf("amount = %s, want 89.40 unchanged", amt)
	}
}

func TestSourceTransaction_RefundStaysNegative(t *testing.T) {
	refund := plaidTxn("txn_1", "acc_1", "-500.00", false)

	if got := SourceTransaction(refund); !got.Amount.IsNegative() {
		t.Error("a refund must stay negative: money entering the account")
	}
}

// The raw descriptor feeds the OFX NAME field, because the learned payee
// rules were built against raw bank descriptors.
func TestSourceTransaction_UsesRawNameNotMerchantName(t *testing.T) {
	got := SourceTransaction(plaidTxn("txn_1", "acc_1", "6.33", false))

	if got.Description != "Uber 072515 SF**POOL**" {
		t.Errorf("description = %q, want the raw name", got.Description)
	}
	if got.Description == "Uber" {
		t.Error("merchant_name must not silently replace name")
	}
}

func TestSourceTransaction_CarriesIDsDateAndPending(t *testing.T) {
	got := SourceTransaction(plaidTxn("txn_1", "acc_1", "1.00", true))

	if got.ID != "txn_1" || got.AccountID != "acc_1" {
		t.Errorf("ids = %s/%s", got.ID, got.AccountID)
	}
	if got.Date.Compare(civildate.MustNew(2026, time.July, 9)) != 0 {
		t.Errorf("date = %v", got.Date)
	}
	if !got.Pending {
		t.Error("pending was lost")
	}
}

// ---------------------------------------------------------------------------
// Grouping
// ---------------------------------------------------------------------------

func TestSourceTransactionsByAccount_Groups(t *testing.T) {
	accounts := SourceAccounts(sandboxAccounts(), testItemInfo())
	txns := []Transaction{
		plaidTxn("txn_1", "acc_check", "1.00", false),
		plaidTxn("txn_2", "acc_check", "2.00", false),
		plaidTxn("txn_3", "acc_cc", "3.00", false),
	}

	got := SourceTransactionsByAccount(txns, accounts)

	if len(got["acc_check"]) != 2 {
		t.Errorf("acc_check = %d transactions, want 2", len(got["acc_check"]))
	}
	if len(got["acc_cc"]) != 1 {
		t.Errorf("acc_cc = %d transactions, want 1", len(got["acc_cc"]))
	}
}

// Transactions belonging to a dropped account are dropped too. No statement
// will be written for the 401k, so its transactions have nowhere to go.
func TestSourceTransactionsByAccount_DropsFilteredAccounts(t *testing.T) {
	accounts := SourceAccounts(sandboxAccounts(), testItemInfo())
	txns := []Transaction{
		plaidTxn("txn_1", "acc_check", "1.00", false),
		plaidTxn("txn_2", "acc_401k", "500.00", false),
	}

	got := SourceTransactionsByAccount(txns, accounts)

	if _, present := got["acc_401k"]; present {
		t.Error("transactions for a dropped account must not be grouped")
	}
	if len(got) != 1 {
		t.Errorf("groups = %d, want 1", len(got))
	}
}

// Pending transactions survive the adapter; the exporter drops them. The
// adapter's job is translation, not policy.
func TestSourceTransactionsByAccount_KeepsPendingForTheExporter(t *testing.T) {
	accounts := SourceAccounts(sandboxAccounts(), testItemInfo())
	txns := []Transaction{plaidTxn("txn_1", "acc_check", "1.00", true)}

	got := SourceTransactionsByAccount(txns, accounts)

	if len(got["acc_check"]) != 1 || !got["acc_check"][0].Pending {
		t.Error("the adapter must not silently drop pending transactions")
	}
}
