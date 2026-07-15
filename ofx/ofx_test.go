package ofx

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jeffbstewart/bankferry/civildate"
)

// validBankStatement returns a minimal Statement that Write accepts. Tests
// break one field of it to exercise a single failure mode.
func validBankStatement() Statement {
	return Statement{
		ServerDate: civildate.MustNew(2025, time.June, 15),
		Org:        "Test Bank",
		FID:        "1234",
		Bank: &BankStatement{
			Account:   BankAccount{BankID: "111", AccountID: "222", Type: Checking},
			Currency:  "USD",
			StartDate: civildate.MustNew(2025, time.June, 1),
			EndDate:   civildate.MustNew(2025, time.June, 15),
			LedgerBal: Balance{Amount: "100.00", AsOf: civildate.MustNew(2025, time.June, 15)},
			Transactions: []Transaction{
				{Type: TransactionDebit, DatePosted: civildate.MustNew(2025, time.June, 3), Amount: "-45.00", ID: "T1", Name: "Store"},
			},
		},
	}
}

// A Statement with both Bank and CreditCard set must be refused, not have one
// silently dropped.
func TestWrite_RejectsBothSet(t *testing.T) {
	s := validBankStatement()
	s.CreditCard = &CreditCardStatement{
		Account:   CreditCardAccount{AccountID: "999"},
		Currency:  "USD",
		StartDate: civildate.MustNew(2025, time.June, 1),
		EndDate:   civildate.MustNew(2025, time.June, 15),
		LedgerBal: Balance{Amount: "1.00", AsOf: civildate.MustNew(2025, time.June, 15)},
	}
	if err := Write(io.Discard, s); err == nil {
		t.Error("expected an error when both Bank and CreditCard are set")
	}
}

func TestWrite_RejectsNeither(t *testing.T) {
	s := validBankStatement()
	s.Bank = nil
	if err := Write(io.Discard, s); err == nil {
		t.Error("expected an error when neither Bank nor CreditCard is set")
	}
}

// An unset required date would render as the nonsense "-00011130"; Write must
// reject it rather than emit a document that cannot be read back.
func TestWrite_RejectsZeroRequiredDates(t *testing.T) {
	cases := map[string]func(*Statement){
		"server date":  func(s *Statement) { s.ServerDate = civildate.ISO8601Date{} },
		"end date":     func(s *Statement) { s.Bank.EndDate = civildate.ISO8601Date{} },
		"start date":   func(s *Statement) { s.Bank.StartDate = civildate.ISO8601Date{} },
		"balance date": func(s *Statement) { s.Bank.LedgerBal.AsOf = civildate.ISO8601Date{} },
		"txn date":     func(s *Statement) { s.Bank.Transactions[0].DatePosted = civildate.ISO8601Date{} },
	}
	for name, breakField := range cases {
		t.Run(name, func(t *testing.T) {
			s := validBankStatement()
			breakField(&s)
			if err := Write(io.Discard, s); err == nil {
				t.Errorf("expected an error for a zero %s", name)
			}
		})
	}
}

func TestWrite_RejectsEmptyRequiredAmounts(t *testing.T) {
	cases := map[string]func(*Statement){
		"balance amount": func(s *Statement) { s.Bank.LedgerBal.Amount = "" },
		"txn amount":     func(s *Statement) { s.Bank.Transactions[0].Amount = "" },
	}
	for name, breakField := range cases {
		t.Run(name, func(t *testing.T) {
			s := validBankStatement()
			breakField(&s)
			if err := Write(io.Discard, s); err == nil {
				t.Errorf("expected an error for an empty %s", name)
			}
		})
	}
}

// Control characters in bank-controlled text must be dropped, so the document
// stays valid XML and round-trips.
func TestWrite_StripsIllegalXMLChars(t *testing.T) {
	s := validBankStatement()
	s.Bank.Transactions[0].Name = "AC\x00ME\x07 STORE"
	var buf bytes.Buffer
	if err := Write(&buf, s); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(&buf)
	if err != nil {
		t.Fatalf("Read (illegal chars leaked into the XML): %v", err)
	}
	if name := got.Bank.Transactions[0].Name; name != "ACME STORE" {
		t.Errorf("Name = %q, want %q", name, "ACME STORE")
	}
}

// Read must tolerate the OFX datetime/timezone date form a real bank export
// uses, keeping only the calendar date.
func TestRead_ToleratesDatetimeDates(t *testing.T) {
	s := validBankStatement()
	var buf bytes.Buffer
	if err := Write(&buf, s); err != nil {
		t.Fatalf("Write: %v", err)
	}
	doc := strings.Replace(buf.String(),
		"<DTSERVER>20250615</DTSERVER>",
		"<DTSERVER>20250615120000.000[-5:EST]</DTSERVER>", 1)
	got, err := Read(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.ServerDate.String() != "2025-06-15" {
		t.Errorf("ServerDate = %v, want 2025-06-15", got.ServerDate)
	}
}

func TestOfxDate(t *testing.T) {
	tests := []struct {
		date civildate.ISO8601Date
		want string
	}{
		{civildate.MustNew(2025, time.January, 5), "20250105"},
		{civildate.MustNew(2025, time.December, 31), "20251231"},
		{civildate.MustNew(2000, time.February, 1), "20000201"},
	}
	for _, tc := range tests {
		got := ofxDate(tc.date)
		if got != tc.want {
			t.Errorf("ofxDate(%v) = %q, want %q", tc.date, got, tc.want)
		}
	}
}

func TestWriteBankStatement(t *testing.T) {
	stmt := Statement{
		ServerDate: civildate.MustNew(2025, time.June, 15),
		Language:   "ENG",
		Org:        "Test Bank",
		FID:        "1234",
		Bank: &BankStatement{
			Account: BankAccount{
				BankID:    "011000015",
				AccountID: "999888777",
				Type:      Checking,
			},
			Currency:  "USD",
			StartDate: civildate.MustNew(2025, time.June, 1),
			EndDate:   civildate.MustNew(2025, time.June, 15),
			Transactions: []Transaction{
				{
					Type:       TransactionDebit,
					DatePosted: civildate.MustNew(2025, time.June, 3),
					Amount:     "-45.00",
					ID:         "TXN001",
					Name:       "ACME Store",
				},
				{
					Type:       TransactionDirectDep,
					DatePosted: civildate.MustNew(2025, time.June, 5),
					Amount:     "2500.00",
					ID:         "TXN002",
					Name:       "Employer Inc",
					Memo:       "Payroll deposit",
				},
			},
			LedgerBal: Balance{
				Amount: "5432.10",
				AsOf:   civildate.MustNew(2025, time.June, 15),
			},
		},
	}

	var buf bytes.Buffer
	err := Write(&buf, stmt)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	out := buf.String()

	// Verify XML header
	if !strings.Contains(out, `<?xml version="1.0" encoding="UTF-8"?>`) {
		t.Error("missing XML declaration")
	}
	if !strings.Contains(out, `<?OFX OFXHEADER="200" VERSION="220"`) {
		t.Error("missing OFX processing instruction")
	}

	// Verify bank account
	if !strings.Contains(out, "<BANKACCTFROM>") {
		t.Error("missing BANKACCTFROM")
	}
	if !strings.Contains(out, "<BANKID>011000015</BANKID>") {
		t.Error("missing BANKID")
	}
	if !strings.Contains(out, "<ACCTID>999888777</ACCTID>") {
		t.Error("missing ACCTID")
	}
	if !strings.Contains(out, "<ACCTTYPE>CHECKING</ACCTTYPE>") {
		t.Error("missing ACCTTYPE")
	}

	// Verify transactions
	if !strings.Contains(out, "<TRNTYPE>DEBIT</TRNTYPE>") {
		t.Error("missing DEBIT transaction")
	}
	if !strings.Contains(out, "<TRNAMT>-45.00</TRNAMT>") {
		t.Error("missing transaction amount")
	}
	if !strings.Contains(out, "<FITID>TXN001</FITID>") {
		t.Error("missing FITID")
	}
	if !strings.Contains(out, "<NAME>ACME Store</NAME>") {
		t.Error("missing NAME")
	}
	if !strings.Contains(out, "<TRNTYPE>DIRECTDEP</TRNTYPE>") {
		t.Error("missing DIRECTDEP transaction")
	}
	if !strings.Contains(out, "<MEMO>Payroll deposit</MEMO>") {
		t.Error("missing MEMO")
	}

	// Verify balance
	if !strings.Contains(out, "<LEDGERBAL>") {
		t.Error("missing LEDGERBAL")
	}
	if !strings.Contains(out, "<BALAMT>5432.10</BALAMT>") {
		t.Error("missing BALAMT")
	}

	// Verify wrapping elements
	if !strings.Contains(out, "<BANKMSGSRSV1>") {
		t.Error("missing BANKMSGSRSV1")
	}
	if !strings.Contains(out, "<STMTRS>") {
		t.Error("missing STMTRS")
	}
}

func TestWriteCreditCardStatement(t *testing.T) {
	stmt := Statement{
		ServerDate: civildate.MustNew(2025, time.July, 1),
		Language:   "ENG",
		Org:        "Card Issuer",
		FID:        "5678",
		CreditCard: &CreditCardStatement{
			Account: CreditCardAccount{
				AccountID: "4111111111111111",
			},
			Currency:  "USD",
			StartDate: civildate.MustNew(2025, time.June, 1),
			EndDate:   civildate.MustNew(2025, time.June, 30),
			Transactions: []Transaction{
				{
					Type:       TransactionDebit,
					DatePosted: civildate.MustNew(2025, time.June, 10),
					Amount:     "-120.50",
					ID:         "CC001",
					Name:       "Restaurant",
				},
			},
			LedgerBal: Balance{
				Amount: "-350.75",
				AsOf:   civildate.MustNew(2025, time.July, 1),
			},
		},
	}

	var buf bytes.Buffer
	err := Write(&buf, stmt)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	out := buf.String()

	if !strings.Contains(out, "<CREDITCARDMSGSRSV1>") {
		t.Error("missing CREDITCARDMSGSRSV1")
	}
	if !strings.Contains(out, "<CCSTMTTRNRS>") {
		t.Error("missing CCSTMTTRNRS")
	}
	if !strings.Contains(out, "<CCSTMTRS>") {
		t.Error("missing CCSTMTRS")
	}
	if !strings.Contains(out, "<CCACCTFROM>") {
		t.Error("missing CCACCTFROM")
	}
	if !strings.Contains(out, "<ACCTID>4111111111111111</ACCTID>") {
		t.Error("missing credit card ACCTID")
	}
	// Should NOT contain bank-specific elements
	if strings.Contains(out, "<BANKMSGSRSV1>") {
		t.Error("credit card statement should not contain BANKMSGSRSV1")
	}
	if strings.Contains(out, "<BANKACCTFROM>") {
		t.Error("credit card statement should not contain BANKACCTFROM")
	}
}

func TestWriteEmptyTransactions(t *testing.T) {
	stmt := Statement{
		ServerDate: civildate.MustNew(2025, time.June, 15),
		Org:        "Test Bank",
		FID:        "1234",
		Bank: &BankStatement{
			Account: BankAccount{
				BankID:    "011000015",
				AccountID: "999888777",
				Type:      Savings,
			},
			Currency:     "USD",
			StartDate:    civildate.MustNew(2025, time.June, 1),
			EndDate:      civildate.MustNew(2025, time.June, 15),
			Transactions: []Transaction{},
			LedgerBal: Balance{
				Amount: "1000.00",
				AsOf:   civildate.MustNew(2025, time.June, 15),
			},
		},
	}

	var buf bytes.Buffer
	err := Write(&buf, stmt)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	out := buf.String()

	// BANKTRANLIST should exist but contain no STMTTRN
	if !strings.Contains(out, "<BANKTRANLIST>") {
		t.Error("missing BANKTRANLIST")
	}
	if strings.Contains(out, "<STMTTRN>") {
		t.Error("empty transaction list should not contain STMTTRN")
	}
}

func TestWriteSpecialCharacters(t *testing.T) {
	stmt := Statement{
		ServerDate: civildate.MustNew(2025, time.June, 15),
		Org:        "Bank & Trust",
		FID:        "1234",
		Bank: &BankStatement{
			Account: BankAccount{
				BankID:    "011000015",
				AccountID: "999888777",
				Type:      Checking,
			},
			Currency:  "USD",
			StartDate: civildate.MustNew(2025, time.June, 1),
			EndDate:   civildate.MustNew(2025, time.June, 15),
			Transactions: []Transaction{
				{
					Type:       TransactionDebit,
					DatePosted: civildate.MustNew(2025, time.June, 3),
					Amount:     "-10.00",
					ID:         "TXN<1>",
					Name:       "AT&T Store",
					Memo:       "Purchase <online>",
				},
			},
			LedgerBal: Balance{
				Amount: "500.00",
				AsOf:   civildate.MustNew(2025, time.June, 15),
			},
		},
	}

	var buf bytes.Buffer
	err := Write(&buf, stmt)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	out := buf.String()

	if !strings.Contains(out, "<ORG>Bank &amp; Trust</ORG>") {
		t.Error("Org not properly escaped")
	}
	if !strings.Contains(out, "<NAME>AT&amp;T Store</NAME>") {
		t.Error("Name not properly escaped")
	}
	if !strings.Contains(out, "<MEMO>Purchase &lt;online&gt;</MEMO>") {
		t.Error("Memo not properly escaped")
	}
	if !strings.Contains(out, "<FITID>TXN&lt;1&gt;</FITID>") {
		t.Error("ID not properly escaped")
	}
}

func TestWriteDefaultLanguage(t *testing.T) {
	stmt := Statement{
		ServerDate: civildate.MustNew(2025, time.June, 15),
		Language:   "", // empty — should default to "ENG"
		Org:        "Test Bank",
		FID:        "1234",
		Bank: &BankStatement{
			Account: BankAccount{
				BankID:    "011000015",
				AccountID: "999888777",
				Type:      Checking,
			},
			Currency:     "USD",
			StartDate:    civildate.MustNew(2025, time.June, 1),
			EndDate:      civildate.MustNew(2025, time.June, 15),
			Transactions: []Transaction{},
			LedgerBal: Balance{
				Amount: "0.00",
				AsOf:   civildate.MustNew(2025, time.June, 15),
			},
		},
	}

	var buf bytes.Buffer
	err := Write(&buf, stmt)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	if !strings.Contains(buf.String(), "<LANGUAGE>ENG</LANGUAGE>") {
		t.Error("empty Language should default to ENG")
	}
}

func TestWriteCheckNumber(t *testing.T) {
	stmt := Statement{
		ServerDate: civildate.MustNew(2025, time.June, 15),
		Org:        "Test Bank",
		FID:        "1234",
		Bank: &BankStatement{
			Account: BankAccount{
				BankID:    "011000015",
				AccountID: "999888777",
				Type:      Checking,
			},
			Currency:  "USD",
			StartDate: civildate.MustNew(2025, time.June, 1),
			EndDate:   civildate.MustNew(2025, time.June, 15),
			Transactions: []Transaction{
				{
					Type:        TransactionCheck,
					DatePosted:  civildate.MustNew(2025, time.June, 7),
					Amount:      "-250.00",
					ID:          "CHK1001",
					Name:        "Rent Payment",
					CheckNumber: "1001",
				},
			},
			LedgerBal: Balance{
				Amount: "3000.00",
				AsOf:   civildate.MustNew(2025, time.June, 15),
			},
		},
	}

	var buf bytes.Buffer
	err := Write(&buf, stmt)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	if !strings.Contains(buf.String(), "<CHECKNUM>1001</CHECKNUM>") {
		t.Error("CHECKNUM element missing for transaction with CheckNumber")
	}
}

func TestWriteCheckNumberOmitted(t *testing.T) {
	stmt := Statement{
		ServerDate: civildate.MustNew(2025, time.June, 15),
		Org:        "Test Bank",
		FID:        "1234",
		Bank: &BankStatement{
			Account: BankAccount{
				BankID:    "011000015",
				AccountID: "999888777",
				Type:      Checking,
			},
			Currency:  "USD",
			StartDate: civildate.MustNew(2025, time.June, 1),
			EndDate:   civildate.MustNew(2025, time.June, 15),
			Transactions: []Transaction{
				{
					Type:       TransactionDebit,
					DatePosted: civildate.MustNew(2025, time.June, 3),
					Amount:     "-20.00",
					ID:         "TXN999",
					Name:       "Coffee Shop",
				},
			},
			LedgerBal: Balance{
				Amount: "1000.00",
				AsOf:   civildate.MustNew(2025, time.June, 15),
			},
		},
	}

	var buf bytes.Buffer
	err := Write(&buf, stmt)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	if strings.Contains(buf.String(), "<CHECKNUM>") {
		t.Error("CHECKNUM element should be omitted when CheckNumber is empty")
	}
}

func TestWriteNilStatementType(t *testing.T) {
	stmt := Statement{
		ServerDate: civildate.MustNew(2025, time.June, 15),
		Org:        "Test Bank",
		FID:        "1234",
	}

	var buf bytes.Buffer
	err := Write(&buf, stmt)
	if err == nil {
		t.Fatal("expected error when both Bank and CreditCard are nil")
	}
}

// --- Read tests ---

func TestReadBankStatement_RoundTrip(t *testing.T) {
	original := Statement{
		ServerDate: civildate.MustNew(2025, time.June, 15),
		Language:   "ENG",
		Org:        "Test Bank",
		FID:        "1234",
		Bank: &BankStatement{
			Account: BankAccount{
				BankID:    "011000015",
				AccountID: "999888777",
				Type:      Checking,
			},
			Currency:  "USD",
			StartDate: civildate.MustNew(2025, time.June, 1),
			EndDate:   civildate.MustNew(2025, time.June, 15),
			Transactions: []Transaction{
				{
					Type:       TransactionDebit,
					DatePosted: civildate.MustNew(2025, time.June, 3),
					Amount:     "-45.00",
					ID:         "TXN001",
					Name:       "ACME Store",
				},
				{
					Type:       TransactionDirectDep,
					DatePosted: civildate.MustNew(2025, time.June, 5),
					Amount:     "2500.00",
					ID:         "TXN002",
					Name:       "Employer Inc",
					Memo:       "Payroll deposit",
				},
			},
			LedgerBal: Balance{
				Amount: "5432.10",
				AsOf:   civildate.MustNew(2025, time.June, 15),
			},
		},
	}

	var buf bytes.Buffer
	if err := Write(&buf, original); err != nil {
		t.Fatalf("Write: %v", err)
	}

	parsed, err := Read(&buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if parsed.ServerDate != original.ServerDate {
		t.Errorf("ServerDate = %v, want %v", parsed.ServerDate, original.ServerDate)
	}
	if parsed.Org != original.Org {
		t.Errorf("Org = %q, want %q", parsed.Org, original.Org)
	}
	if parsed.FID != original.FID {
		t.Errorf("FID = %q, want %q", parsed.FID, original.FID)
	}
	if parsed.Bank == nil {
		t.Fatal("expected Bank statement, got nil")
	}
	if parsed.CreditCard != nil {
		t.Error("expected nil CreditCard for bank statement")
	}

	b := parsed.Bank
	if b.Account.BankID != "011000015" {
		t.Errorf("BankID = %q, want 011000015", b.Account.BankID)
	}
	if b.Account.AccountID != "999888777" {
		t.Errorf("AccountID = %q, want 999888777", b.Account.AccountID)
	}
	if b.Account.Type != Checking {
		t.Errorf("Type = %q, want CHECKING", b.Account.Type)
	}
	if b.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", b.Currency)
	}
	if b.StartDate != original.Bank.StartDate {
		t.Errorf("StartDate = %v, want %v", b.StartDate, original.Bank.StartDate)
	}
	if b.EndDate != original.Bank.EndDate {
		t.Errorf("EndDate = %v, want %v", b.EndDate, original.Bank.EndDate)
	}

	if len(b.Transactions) != 2 {
		t.Fatalf("expected 2 transactions, got %d", len(b.Transactions))
	}
	txn := b.Transactions[0]
	if txn.Type != TransactionDebit {
		t.Errorf("txn[0].Type = %q, want DEBIT", txn.Type)
	}
	if txn.Amount != "-45.00" {
		t.Errorf("txn[0].Amount = %q, want -45.00", txn.Amount)
	}
	if txn.ID != "TXN001" {
		t.Errorf("txn[0].ID = %q, want TXN001", txn.ID)
	}
	if txn.Name != "ACME Store" {
		t.Errorf("txn[0].Name = %q, want ACME Store", txn.Name)
	}

	txn2 := b.Transactions[1]
	if txn2.Memo != "Payroll deposit" {
		t.Errorf("txn[1].Memo = %q, want Payroll deposit", txn2.Memo)
	}

	if b.LedgerBal.Amount != "5432.10" {
		t.Errorf("LedgerBal.Amount = %q, want 5432.10", b.LedgerBal.Amount)
	}
}

func TestReadCreditCardStatement_RoundTrip(t *testing.T) {
	original := Statement{
		ServerDate: civildate.MustNew(2025, time.July, 1),
		Language:   "ENG",
		Org:        "Card Issuer",
		FID:        "5678",
		CreditCard: &CreditCardStatement{
			Account:   CreditCardAccount{AccountID: "4111111111111111"},
			Currency:  "USD",
			StartDate: civildate.MustNew(2025, time.June, 1),
			EndDate:   civildate.MustNew(2025, time.June, 30),
			Transactions: []Transaction{
				{
					Type:       TransactionDebit,
					DatePosted: civildate.MustNew(2025, time.June, 10),
					Amount:     "-120.50",
					ID:         "CC001",
					Name:       "Restaurant",
				},
			},
			LedgerBal: Balance{
				Amount: "-350.75",
				AsOf:   civildate.MustNew(2025, time.July, 1),
			},
		},
	}

	var buf bytes.Buffer
	if err := Write(&buf, original); err != nil {
		t.Fatalf("Write: %v", err)
	}

	parsed, err := Read(&buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if parsed.Bank != nil {
		t.Error("expected nil Bank for credit card statement")
	}
	if parsed.CreditCard == nil {
		t.Fatal("expected CreditCard statement, got nil")
	}

	cc := parsed.CreditCard
	if cc.Account.AccountID != "4111111111111111" {
		t.Errorf("AccountID = %q, want 4111111111111111", cc.Account.AccountID)
	}
	if len(cc.Transactions) != 1 {
		t.Fatalf("expected 1 transaction, got %d", len(cc.Transactions))
	}
	if cc.Transactions[0].ID != "CC001" {
		t.Errorf("ID = %q, want CC001", cc.Transactions[0].ID)
	}
	if cc.LedgerBal.Amount != "-350.75" {
		t.Errorf("LedgerBal.Amount = %q, want -350.75", cc.LedgerBal.Amount)
	}
}

func TestReadSpecialCharacters_RoundTrip(t *testing.T) {
	original := Statement{
		ServerDate: civildate.MustNew(2025, time.June, 15),
		Org:        "Bank & Trust",
		FID:        "1234",
		Bank: &BankStatement{
			Account: BankAccount{
				BankID:    "011000015",
				AccountID: "999888777",
				Type:      Checking,
			},
			Currency:  "USD",
			StartDate: civildate.MustNew(2025, time.June, 1),
			EndDate:   civildate.MustNew(2025, time.June, 15),
			Transactions: []Transaction{
				{
					Type:       TransactionDebit,
					DatePosted: civildate.MustNew(2025, time.June, 3),
					Amount:     "-10.00",
					ID:         "TXN<1>",
					Name:       "AT&T Store",
					Memo:       "Purchase <online>",
				},
			},
			LedgerBal: Balance{
				Amount: "500.00",
				AsOf:   civildate.MustNew(2025, time.June, 15),
			},
		},
	}

	var buf bytes.Buffer
	if err := Write(&buf, original); err != nil {
		t.Fatalf("Write: %v", err)
	}

	parsed, err := Read(&buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	txn := parsed.Bank.Transactions[0]
	if txn.Name != "AT&T Store" {
		t.Errorf("Name = %q, want AT&T Store", txn.Name)
	}
	if txn.Memo != "Purchase <online>" {
		t.Errorf("Memo = %q, want Purchase <online>", txn.Memo)
	}
	if txn.ID != "TXN<1>" {
		t.Errorf("ID = %q, want TXN<1>", txn.ID)
	}
	if parsed.Org != "Bank & Trust" {
		t.Errorf("Org = %q, want Bank & Trust", parsed.Org)
	}
}

func TestReadCheckNumber_RoundTrip(t *testing.T) {
	original := Statement{
		ServerDate: civildate.MustNew(2025, time.June, 15),
		Org:        "Test Bank",
		FID:        "1234",
		Bank: &BankStatement{
			Account: BankAccount{
				BankID:    "011000015",
				AccountID: "999888777",
				Type:      Checking,
			},
			Currency:  "USD",
			StartDate: civildate.MustNew(2025, time.June, 1),
			EndDate:   civildate.MustNew(2025, time.June, 15),
			Transactions: []Transaction{
				{
					Type:        TransactionCheck,
					DatePosted:  civildate.MustNew(2025, time.June, 7),
					Amount:      "-250.00",
					ID:          "CHK1001",
					Name:        "Rent Payment",
					CheckNumber: "1001",
				},
			},
			LedgerBal: Balance{
				Amount: "3000.00",
				AsOf:   civildate.MustNew(2025, time.June, 15),
			},
		},
	}

	var buf bytes.Buffer
	if err := Write(&buf, original); err != nil {
		t.Fatalf("Write: %v", err)
	}

	parsed, err := Read(&buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if parsed.Bank.Transactions[0].CheckNumber != "1001" {
		t.Errorf("CheckNumber = %q, want 1001", parsed.Bank.Transactions[0].CheckNumber)
	}
}

func TestReadInvalidXML(t *testing.T) {
	_, err := Read(strings.NewReader("not xml at all"))
	if err == nil {
		t.Error("expected error for invalid XML")
	}
}
