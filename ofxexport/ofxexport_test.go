package ofxexport

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jeffbstewart/bankferry/civildate"
	"github.com/jeffbstewart/bankferry/money"
	"github.com/jeffbstewart/bankferry/ofx"
	"github.com/jeffbstewart/bankferry/source"
)

// usd builds a source amount. Positive means money left the account, so a
// $25 purchase is usd("25.00").
func usd(s string) money.Amount { return money.MustParse(s, money.USD) }

func usdPtr(s string) *money.Amount {
	a := usd(s)
	return &a
}

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type mockStore struct {
	exported      map[string]bool
	isExportedErr error
}

func (m *mockStore) IsExported(id string) (bool, error) {
	if m.isExportedErr != nil {
		return false, m.isExportedErr
	}
	return m.exported[id], nil
}

type mockWriteCloser struct {
	buf      strings.Builder
	closeErr error
	writeErr error
}

func (m *mockWriteCloser) Write(p []byte) (int, error) {
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	return m.buf.Write(p)
}

func (m *mockWriteCloser) Close() error { return m.closeErr }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestAccount(typ source.AccountType, subtype source.AccountSubtype) source.Account {
	return source.Account{
		ID:          "acc_1",
		Name:        "Test Account",
		Type:        typ,
		Subtype:     subtype,
		Currency:    money.USD,
		LastFour:    "1234",
		Institution: source.Institution{ID: "chase", Name: "Chase"},
		Balance:     usdPtr("500.00"),
	}
}

// amount is positive for money leaving the account, so a purchase is
// newPostedTxn(..., "25.00", ...).
func newPostedTxn(id, amount, desc string, y int, m time.Month, d int) source.Transaction {
	return source.Transaction{
		ID:          id,
		AccountID:   "acc_1",
		Amount:      usd(amount),
		Date:        civildate.MustNew(y, m, d),
		Description: desc,
	}
}

func newPendingTxn(id string) source.Transaction {
	return source.Transaction{
		ID:        id,
		AccountID: "acc_1",
		Amount:    usd("5.00"),
		Date:      civildate.MustNew(2025, time.June, 1),
		Pending:   true,
	}
}

func newExporter(store ExportStore, dryRun bool, createFile FileCreator) *Exporter {
	return &Exporter{
		Store:      store,
		OutputDir:  "/tmp/ofx",
		DryRun:     dryRun,
		CreateFile: createFile,
	}
}

func successFileCreator() (FileCreator, *mockWriteCloser) {
	wc := &mockWriteCloser{}
	return func(_ string) (io.WriteCloser, error) { return wc, nil }, wc
}

func emptyStore() *mockStore { return &mockStore{exported: map[string]bool{}} }

// ---------------------------------------------------------------------------
// Statement selection
// ---------------------------------------------------------------------------

func TestExportAccount_Depository_Checking(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)
	txn := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)
	fc, wc := successFileCreator()

	e := newExporter(emptyStore(), false, fc)
	result := e.ExportAccount(acct, []source.Transaction{txn})

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if result.Skipped {
		t.Fatal("expected not skipped")
	}
	if result.NewTransactions != 1 {
		t.Errorf("NewTransactions = %d, want 1", result.NewTransactions)
	}
	if result.FilePath == "" {
		t.Error("expected non-empty FilePath")
	}
	if len(result.ExportedIDs) != 1 || result.ExportedIDs[0] != "txn_1" {
		t.Errorf("ExportedIDs = %v, want [txn_1]", result.ExportedIDs)
	}
	out := wc.buf.String()
	if !strings.Contains(out, "BANKMSGSRSV1") {
		t.Error("expected BANKMSGSRSV1 in OFX output")
	}
	if !strings.Contains(out, "CHECKING") {
		t.Error("expected CHECKING in OFX output")
	}
}

func TestExportAccount_Credit_Card(t *testing.T) {
	acct := newTestAccount(source.Credit, source.CreditCard)
	txn := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)
	fc, wc := successFileCreator()

	e := newExporter(emptyStore(), false, fc)
	if r := e.ExportAccount(acct, []source.Transaction{txn}); r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}
	if !strings.Contains(wc.buf.String(), "CREDITCARDMSGSRSV1") {
		t.Error("expected CREDITCARDMSGSRSV1 in OFX output")
	}
}

func TestExportAccount_SubtypeSavings(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Savings)
	txn := newPostedTxn("txn_1", "25.00", "Transfer", 2025, time.June, 3)
	fc, wc := successFileCreator()

	e := newExporter(emptyStore(), false, fc)
	if r := e.ExportAccount(acct, []source.Transaction{txn}); r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}
	if !strings.Contains(wc.buf.String(), "SAVINGS") {
		t.Error("expected SAVINGS in OFX output")
	}
}

func TestExportAccount_SubtypeMoneyMarket(t *testing.T) {
	acct := newTestAccount(source.Depository, source.MoneyMarket)
	txn := newPostedTxn("txn_1", "25.00", "Transfer", 2025, time.June, 3)
	fc, wc := successFileCreator()

	e := newExporter(emptyStore(), false, fc)
	if r := e.ExportAccount(acct, []source.Transaction{txn}); r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}
	if !strings.Contains(wc.buf.String(), "MONEYMRKT") {
		t.Error("expected MONEYMRKT in OFX output")
	}
}

// ---------------------------------------------------------------------------
// Filtering
// ---------------------------------------------------------------------------

func TestExportAccount_AllAlreadyExported(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)
	txn := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)
	store := &mockStore{exported: map[string]bool{"txn_1": true}}

	result := newExporter(store, false, nil).ExportAccount(acct, []source.Transaction{txn})

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if !result.Skipped {
		t.Error("expected Skipped = true")
	}
	if len(result.ExportedIDs) != 0 {
		t.Errorf("ExportedIDs = %v, want none", result.ExportedIDs)
	}
}

func TestExportAccount_MixedNewAndExported(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)
	txn1 := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)
	txn2 := newPostedTxn("txn_2", "10.00", "Lunch", 2025, time.June, 4)
	store := &mockStore{exported: map[string]bool{"txn_1": true}}
	fc, _ := successFileCreator()

	result := newExporter(store, false, fc).ExportAccount(acct, []source.Transaction{txn1, txn2})

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if result.NewTransactions != 1 {
		t.Errorf("NewTransactions = %d, want 1", result.NewTransactions)
	}
	if len(result.ExportedIDs) != 1 || result.ExportedIDs[0] != "txn_2" {
		t.Errorf("ExportedIDs = %v, want [txn_2]", result.ExportedIDs)
	}
}

// Pending transactions must never reach OFX. Plaid gives a posted
// transaction a different ID than the pending one it replaced, so exporting
// the pending one imports the purchase twice.
func TestExportAccount_PendingFiltered(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)

	result := newExporter(emptyStore(), false, nil).
		ExportAccount(acct, []source.Transaction{newPendingTxn("txn_p")})

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if !result.Skipped {
		t.Error("expected Skipped = true for pending-only transactions")
	}
}

func TestExportAccount_PendingFilteredInDryRun(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)
	txns := []source.Transaction{
		newPendingTxn("txn_p"),
		newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3),
	}

	result := newExporter(emptyStore(), true, nil).ExportAccount(acct, txns)

	if result.NewTransactions != 1 {
		t.Errorf("NewTransactions = %d, want 1: pending must not be counted", result.NewTransactions)
	}
}

func TestExportAccount_NoTransactions(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)

	result := newExporter(emptyStore(), false, nil).ExportAccount(acct, nil)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if !result.Skipped {
		t.Error("expected Skipped = true for no transactions")
	}
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

func TestExportAccount_IsExportedError(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)
	txn := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)
	store := &mockStore{exported: map[string]bool{}, isExportedErr: errors.New("db read error")}

	result := newExporter(store, false, nil).ExportAccount(acct, []source.Transaction{txn})

	if result.Err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(result.Err.Error(), "db read error") {
		t.Errorf("error = %q", result.Err)
	}
}

func TestExportAccount_FileCreateError(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)
	txn := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)
	fc := func(_ string) (io.WriteCloser, error) { return nil, errors.New("permission denied") }

	result := newExporter(emptyStore(), false, fc).ExportAccount(acct, []source.Transaction{txn})

	if result.Err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(result.Err.Error(), "permission denied") {
		t.Errorf("error = %q", result.Err)
	}
}

// A failed write reports no exported IDs, so the caller cannot record
// transactions that were never persisted.
func TestExportAccount_WriteFailureReportsNothingExported(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)
	txn := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)

	wc := &mockWriteCloser{writeErr: errors.New("disk full")}
	fc := func(_ string) (io.WriteCloser, error) { return wc, nil }

	result := newExporter(emptyStore(), false, fc).ExportAccount(acct, []source.Transaction{txn})

	if result.Err == nil {
		t.Fatal("expected error")
	}
	if len(result.ExportedIDs) != 0 {
		t.Errorf("ExportedIDs = %v, want none after a failed write", result.ExportedIDs)
	}
	if result.FilePath != "" {
		t.Errorf("FilePath = %q, want empty after a failed write", result.FilePath)
	}
}

func TestExportAccount_CloseFailureReportsNothingExported(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)
	txn := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)

	wc := &mockWriteCloser{closeErr: errors.New("disk full")}
	fc := func(_ string) (io.WriteCloser, error) { return wc, nil }

	result := newExporter(emptyStore(), false, fc).ExportAccount(acct, []source.Transaction{txn})

	if result.Err == nil {
		t.Fatal("expected error")
	}
	if len(result.ExportedIDs) != 0 {
		t.Errorf("ExportedIDs = %v, want none", result.ExportedIDs)
	}
}

// ---------------------------------------------------------------------------
// Dry run
// ---------------------------------------------------------------------------

func TestExportAccount_DryRunWritesNothing(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)
	txn := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)

	// A nil FileCreator would panic if the exporter tried to write.
	result := newExporter(emptyStore(), true, nil).ExportAccount(acct, []source.Transaction{txn})

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if result.FilePath != "" {
		t.Errorf("FilePath = %q, want empty in dry run", result.FilePath)
	}
	if len(result.ExportedIDs) != 0 {
		t.Errorf("ExportedIDs = %v, want none in dry run", result.ExportedIDs)
	}
	if len(result.Transactions) != 1 {
		t.Errorf("Transactions = %d, want 1", len(result.Transactions))
	}
}

// Dry run shows everything posted, including what was already exported, so
// the operator sees the whole picture.
func TestExportAccount_DryRunShowsAlreadyExported(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)
	txn1 := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)
	txn2 := newPostedTxn("txn_2", "10.00", "Lunch", 2025, time.June, 4)
	store := &mockStore{exported: map[string]bool{"txn_1": true}}

	result := newExporter(store, true, nil).ExportAccount(acct, []source.Transaction{txn1, txn2})

	if result.NewTransactions != 2 {
		t.Errorf("NewTransactions = %d, want 2", result.NewTransactions)
	}
}

// ---------------------------------------------------------------------------
// ExportAll
// ---------------------------------------------------------------------------

func TestExportAll_RoutesTransactionsByAccount(t *testing.T) {
	acct1 := newTestAccount(source.Depository, source.Checking)
	acct1.ID, acct1.Name = "acc_1", "Checking"
	acct2 := newTestAccount(source.Credit, source.CreditCard)
	acct2.ID, acct2.Name = "acc_2", "Credit Card"

	txn1 := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)
	txn2 := newPostedTxn("txn_2", "50.00", "Dinner", 2025, time.June, 4)
	txn2.AccountID = "acc_2"

	fc, _ := successFileCreator()
	results := newExporter(emptyStore(), false, fc).ExportAll(
		[]source.Account{acct1, acct2},
		map[string][]source.Transaction{
			"acc_1": {txn1},
			"acc_2": {txn2},
		})

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for i, r := range results {
		if r.Err != nil {
			t.Errorf("result[%d] unexpected error: %v", i, r.Err)
		}
		if len(r.ExportedIDs) != 1 {
			t.Errorf("result[%d] ExportedIDs = %v", i, r.ExportedIDs)
		}
	}
	if results[0].ExportedIDs[0] != "txn_1" || results[1].ExportedIDs[0] != "txn_2" {
		t.Error("transactions were routed to the wrong accounts")
	}
}

// An account with no transactions is skipped, not an error.
func TestExportAll_AccountWithNoTransactions(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)

	results := newExporter(emptyStore(), false, nil).
		ExportAll([]source.Account{acct}, map[string][]source.Transaction{})

	if len(results) != 1 {
		t.Fatalf("got %d results", len(results))
	}
	if results[0].Err != nil || !results[0].Skipped {
		t.Errorf("result = %+v, want skipped without error", results[0])
	}
}

func TestExportAll_Empty(t *testing.T) {
	results := newExporter(emptyStore(), false, nil).ExportAll(nil, nil)
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

// ---------------------------------------------------------------------------
// Sign convention
// ---------------------------------------------------------------------------

// TRNTYPE depends only on the direction of the money, never on the
// statement type.
func TestOFXTransactionType(t *testing.T) {
	cases := []struct {
		input string
		want  ofx.TransactionType
	}{
		{"25.00", ofx.TransactionDebit},   // money out
		{"0.01", ofx.TransactionDebit},    // money out
		{"-25.00", ofx.TransactionCredit}, // money in
		{"0.00", ofx.TransactionDebit},    // zero is not money in
	}
	for _, tc := range cases {
		if got := ofxTransactionType(usd(tc.input)); got != tc.want {
			t.Errorf("ofxTransactionType(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TRNAMT, unlike TRNTYPE, does depend on the statement type. A charge on a
// credit card is written positive; a withdrawal from a bank account is
// written negative. Both are the same source amount and both are DEBIT.
//
// The code once negated unconditionally, which produced correct credit card
// statements and turned every bank withdrawal into a deposit. This test
// exists so that cannot come back.
func TestOFXAmount_SignDependsOnStatementType(t *testing.T) {
	moneyOut := usd("25.00")
	moneyIn := usd("-25.00")

	cases := []struct {
		name     string
		acctType source.AccountType
		amount   money.Amount
		want     string
	}{
		{"bank withdrawal is negative", source.Depository, moneyOut, "-25.00"},
		{"bank deposit is positive", source.Depository, moneyIn, "25.00"},
		{"credit card charge is positive", source.Credit, moneyOut, "25.00"},
		{"credit card payment is negative", source.Credit, moneyIn, "-25.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ofxAmount(tc.acctType, tc.amount)
			if err != nil {
				t.Fatalf("ofxAmount: %v", err)
			}
			if got != tc.want {
				t.Errorf("ofxAmount(%s, %s) = %q, want %q",
					tc.acctType, tc.amount, got, tc.want)
			}
		})
	}
}

func TestExportAccount_CreditCardChargeIsPositiveDebit(t *testing.T) {
	acct := newTestAccount(source.Credit, source.CreditCard)
	txn := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)
	fc, wc := successFileCreator()

	if r := newExporter(emptyStore(), false, fc).ExportAccount(acct, []source.Transaction{txn}); r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}

	out := wc.buf.String()
	if !strings.Contains(out, "<TRNTYPE>DEBIT</TRNTYPE>") {
		t.Error("expected TRNTYPE DEBIT for money leaving the account")
	}
	if !strings.Contains(out, "<TRNAMT>25.00</TRNAMT>") {
		t.Error("expected TRNAMT 25.00 for a credit card charge")
	}
}

func TestExportAccount_BankWithdrawalIsNegativeDebit(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)
	txn := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)
	fc, wc := successFileCreator()

	if r := newExporter(emptyStore(), false, fc).ExportAccount(acct, []source.Transaction{txn}); r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}

	out := wc.buf.String()
	if !strings.Contains(out, "<TRNTYPE>DEBIT</TRNTYPE>") {
		t.Error("expected TRNTYPE DEBIT for money leaving the account")
	}
	if !strings.Contains(out, "<TRNAMT>-25.00</TRNAMT>") {
		t.Error("expected TRNAMT -25.00 for a bank withdrawal")
	}
}

func TestExportAccount_AmountIsNotRounded(t *testing.T) {
	acct := newTestAccount(source.Credit, source.CreditCard)
	txn := newPostedTxn("txn_1", "23631.9805", "Rebalance", 2025, time.June, 3)
	fc, wc := successFileCreator()

	if r := newExporter(emptyStore(), false, fc).ExportAccount(acct, []source.Transaction{txn}); r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}
	if !strings.Contains(wc.buf.String(), "<TRNAMT>23631.9805</TRNAMT>") {
		t.Error("expected the exact amount, unrounded, in TRNAMT")
	}
}

// ---------------------------------------------------------------------------
// Statement period and balance
// ---------------------------------------------------------------------------

// The statement period is derived by scanning, because providers make no
// promise about transaction order.
func TestExportAccount_StatementPeriodIsOrderIndependent(t *testing.T) {
	oldest := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)
	newest := newPostedTxn("txn_2", "10.00", "Lunch", 2025, time.June, 17)
	middle := newPostedTxn("txn_3", "15.00", "Books", 2025, time.June, 9)

	orders := map[string][]source.Transaction{
		"ascending":  {oldest, middle, newest},
		"descending": {newest, middle, oldest},
		"shuffled":   {middle, oldest, newest},
	}

	for name, txns := range orders {
		t.Run(name, func(t *testing.T) {
			acct := newTestAccount(source.Depository, source.Checking)
			fc, wc := successFileCreator()

			if r := newExporter(emptyStore(), false, fc).ExportAccount(acct, txns); r.Err != nil {
				t.Fatalf("unexpected error: %v", r.Err)
			}

			out := wc.buf.String()
			if !strings.Contains(out, "<DTSTART>20250603</DTSTART>") {
				t.Errorf("expected DTSTART 20250603 for %s order", name)
			}
			if !strings.Contains(out, "<DTEND>20250617</DTEND>") {
				t.Errorf("expected DTEND 20250617 for %s order", name)
			}
		})
	}
}

func TestExportAccount_LedgerBalanceFromAccount(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)
	acct.Balance = usdPtr("1234.56")
	acct.BalanceDate = civildate.MustNew(2025, time.June, 17)
	txn := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)
	fc, wc := successFileCreator()

	if r := newExporter(emptyStore(), false, fc).ExportAccount(acct, []source.Transaction{txn}); r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}

	out := wc.buf.String()
	if !strings.Contains(out, "<BALAMT>1234.56</BALAMT>") {
		t.Error("expected BALAMT from the account balance")
	}
	if !strings.Contains(out, "<DTASOF>20250617</DTASOF>") {
		t.Error("expected DTASOF from the account balance date")
	}
}

// An unknown balance is not a balance of zero, but OFX needs a number.
func TestExportAccount_MissingBalanceDefaultsToZero(t *testing.T) {
	acct := newTestAccount(source.Depository, source.Checking)
	acct.Balance = nil
	txn := newPostedTxn("txn_1", "25.00", "Coffee", 2025, time.June, 3)
	fc, wc := successFileCreator()

	if r := newExporter(emptyStore(), false, fc).ExportAccount(acct, []source.Transaction{txn}); r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}
	if !strings.Contains(wc.buf.String(), "<BALAMT>0.00</BALAMT>") {
		t.Error("expected a well-formed zero BALAMT when the account has no balance")
	}
}

// ---------------------------------------------------------------------------
// Misc
// ---------------------------------------------------------------------------

func TestMapAccountSubtype_AllTypes(t *testing.T) {
	cases := []struct {
		input source.AccountSubtype
		want  ofx.AccountType
	}{
		{source.Checking, ofx.Checking},
		{source.Savings, ofx.Savings},
		{source.MoneyMarket, ofx.MoneyMarket},
		{source.CreditCard, ofx.Checking},
		{"unrecognized", ofx.Checking},
	}
	for _, tc := range cases {
		if got := mapAccountSubtype(tc.input); got != tc.want {
			t.Errorf("mapAccountSubtype(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := []struct{ input, want string }{
		{"Chase", "Chase"},
		{"Bank of America", "Bank_of_America"},
		{"Wells Fargo & Co.", "Wells_Fargo_Co_"},
		{"US$Bank!", "US_Bank_"},
		{"simple", "simple"},
		{"a--b  c", "a_b_c"},
	}
	for _, tc := range cases {
		if got := sanitizeFilename(tc.input); got != tc.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
