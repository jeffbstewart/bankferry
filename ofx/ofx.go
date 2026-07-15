// Package ofx writes and reads OFX 2.2 XML documents for bank and credit card
// statements (checking, savings, money market, and credit card accounts).
//
// Read is the inverse of Write, not a general OFX 2.2 parser: it expects the
// aggregates Write produces and treats the statement dates and LEDGERBAL as
// mandatory. It tolerates the common OFX datetime and timezone forms in date
// fields but keeps only the calendar date. Pointing it at an arbitrary
// bank-exported .ofx is outside what it promises.
package ofx

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jeffbstewart/bankferry/civildate"
)

// TransactionType represents OFX TRNTYPE values.
// All OFX 2.2 spec values are defined here; not all are currently used.
type TransactionType string

const (
	TransactionCredit      TransactionType = "CREDIT"
	TransactionDebit       TransactionType = "DEBIT"
	TransactionInterest    TransactionType = "INT"
	TransactionDividend    TransactionType = "DIV"
	TransactionFee         TransactionType = "FEE"
	TransactionServiceChg  TransactionType = "SRVCHG"
	TransactionDeposit     TransactionType = "DEP"
	TransactionATM         TransactionType = "ATM"
	TransactionPOS         TransactionType = "POS"
	TransactionTransfer    TransactionType = "XFER"
	TransactionCheck       TransactionType = "CHECK"
	TransactionPayment     TransactionType = "PAYMENT"
	TransactionCash        TransactionType = "CASH"
	TransactionDirectDep   TransactionType = "DIRECTDEP"
	TransactionDirectDebit TransactionType = "DIRECTDEBIT"
	TransactionRepeatPmt   TransactionType = "REPEATPMT"
	TransactionOther       TransactionType = "OTHER"
)

// AccountType represents OFX ACCTTYPE values for bank accounts.
// All OFX 2.2 spec values are defined here; not all are currently used.
type AccountType string

const (
	Checking    AccountType = "CHECKING"
	Savings     AccountType = "SAVINGS"
	MoneyMarket AccountType = "MONEYMRKT"
	CreditLine  AccountType = "CREDITLINE"
)

// Transaction represents a single OFX statement transaction (STMTTRN).
type Transaction struct {
	Type        TransactionType
	DatePosted  civildate.ISO8601Date
	Amount      string // signed decimal string, e.g. "-45.00"
	ID          string // FITID — unique per account
	Name        string // payee/merchant (max 32 chars in spec)
	Memo        string // optional additional description
	CheckNumber string // optional
}

// BankAccount identifies a bank account (BANKACCTFROM).
type BankAccount struct {
	BankID    string // routing number
	AccountID string
	Type      AccountType
}

// CreditCardAccount identifies a credit card account (CCACCTFROM).
type CreditCardAccount struct {
	AccountID string
}

// Balance represents a ledger or available balance.
type Balance struct {
	Amount string // decimal string
	AsOf   civildate.ISO8601Date
}

// BankStatement holds all data for a bank account OFX statement.
type BankStatement struct {
	Account      BankAccount
	Currency     string // ISO 4217 (e.g. "USD")
	StartDate    civildate.ISO8601Date
	EndDate      civildate.ISO8601Date
	Transactions []Transaction
	LedgerBal    Balance
}

// CreditCardStatement holds all data for a credit card OFX statement.
type CreditCardStatement struct {
	Account      CreditCardAccount
	Currency     string
	StartDate    civildate.ISO8601Date
	EndDate      civildate.ISO8601Date
	Transactions []Transaction
	LedgerBal    Balance
}

// Statement is a complete OFX document containing either a bank or
// credit card statement.
type Statement struct {
	ServerDate civildate.ISO8601Date
	Language   string // default "ENG"
	Org        string // financial institution name
	FID        string // financial institution ID

	// Exactly one of these must be set; Write rejects both-nil and both-set.
	Bank       *BankStatement
	CreditCard *CreditCardStatement
}

// ofxDate formats a date as YYYYMMDD for OFX output.
func ofxDate(d civildate.ISO8601Date) string {
	return d.Format("20060102")
}

// xmlEscape makes s safe as XML 1.0 element text. NAME, MEMO, and FITID come
// from raw bank descriptors, so they can carry control characters the XML 1.0
// spec forbids; those are dropped, since emitting them produces a document
// neither Read nor GnuCash can parse. The markup-significant &, <, and > are
// replaced with entity references. Quotes need no escaping here because
// everything written is element text, never an attribute value.
func xmlEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !isXMLChar(r) {
			continue
		}
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isXMLChar reports whether r is a legal XML 1.0 character (tab, newline,
// carriage return, and the printable ranges — everything else, notably the C0
// control characters, is forbidden).
func isXMLChar(r rune) bool {
	return r == 0x09 || r == 0x0A || r == 0x0D ||
		(r >= 0x20 && r <= 0xD7FF) ||
		(r >= 0xE000 && r <= 0xFFFD) ||
		(r >= 0x10000 && r <= 0x10FFFF)
}

// printer wraps an io.Writer and captures the first write error.
// After an error, subsequent calls are no-ops.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) print(s string) {
	if p.err != nil {
		return
	}
	_, p.err = fmt.Fprint(p.w, s)
}

func (p *printer) printf(format string, args ...any) {
	if p.err != nil {
		return
	}
	_, p.err = fmt.Fprintf(p.w, format, args...)
}

// validate rejects a Statement that would serialize to a malformed or
// non-round-trippable document. An unset required date renders through ofxDate
// as a nonsense "-00011130", and an empty BALAMT produces an empty required
// element; Write refuses these rather than emit them silently.
func validate(stmt Statement) error {
	if stmt.ServerDate.IsZero() {
		return errors.New("ofx: ServerDate (DTSERVER) is required")
	}
	switch {
	case stmt.Bank != nil:
		b := stmt.Bank
		return validateBody(b.StartDate, b.EndDate, b.LedgerBal, b.Transactions)
	case stmt.CreditCard != nil:
		c := stmt.CreditCard
		return validateBody(c.StartDate, c.EndDate, c.LedgerBal, c.Transactions)
	}
	return nil
}

func validateBody(start, end civildate.ISO8601Date, bal Balance, txns []Transaction) error {
	if start.IsZero() || end.IsZero() {
		return errors.New("ofx: statement DTSTART and DTEND are required")
	}
	if bal.Amount == "" || bal.AsOf.IsZero() {
		return errors.New("ofx: LEDGERBAL requires both BALAMT and DTASOF")
	}
	for _, t := range txns {
		if t.DatePosted.IsZero() {
			return fmt.Errorf("ofx: transaction %q has no DTPOSTED", t.ID)
		}
		if t.Amount == "" {
			return fmt.Errorf("ofx: transaction %q has no TRNAMT", t.ID)
		}
	}
	return nil
}

// Write serializes the Statement as OFX 2.2 XML to the given writer.
func Write(w io.Writer, stmt Statement) error {
	if (stmt.Bank == nil) == (stmt.CreditCard == nil) {
		return errors.New("ofx: exactly one of Bank or CreditCard must be set")
	}
	if err := validate(stmt); err != nil {
		return err
	}

	lang := stmt.Language
	if lang == "" {
		lang = "ENG"
	}

	p := &printer{w: w}

	// OFX 2.2 XML header
	p.print("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	p.print("<?OFX OFXHEADER=\"200\" VERSION=\"220\" SECURITY=\"NONE\" OLDFILEUID=\"NONE\" NEWFILEUID=\"NONE\"?>\n")

	p.print("<OFX>\n")

	// Signon response
	p.print("<SIGNONMSGSRSV1>\n")
	p.print("<SONRS>\n")
	p.print("<STATUS>\n")
	p.print("<CODE>0</CODE>\n")
	p.print("<SEVERITY>INFO</SEVERITY>\n")
	p.print("</STATUS>\n")
	p.printf("<DTSERVER>%s</DTSERVER>\n", ofxDate(stmt.ServerDate))
	p.printf("<LANGUAGE>%s</LANGUAGE>\n", lang)
	p.print("<FI>\n")
	p.printf("<ORG>%s</ORG>\n", xmlEscape(stmt.Org))
	p.printf("<FID>%s</FID>\n", xmlEscape(stmt.FID))
	p.print("</FI>\n")
	p.print("</SONRS>\n")
	p.print("</SIGNONMSGSRSV1>\n")

	if stmt.Bank != nil {
		writeBankStatement(p, stmt.Bank)
	} else {
		writeCreditCardStatement(p, stmt.CreditCard)
	}

	p.print("</OFX>\n")
	return p.err
}

func writeBankStatement(p *printer, bs *BankStatement) {
	p.print("<BANKMSGSRSV1>\n")
	p.print("<STMTTRNRS>\n")
	p.print("<TRNUID>0</TRNUID>\n")
	p.print("<STATUS>\n")
	p.print("<CODE>0</CODE>\n")
	p.print("<SEVERITY>INFO</SEVERITY>\n")
	p.print("</STATUS>\n")
	p.print("<STMTRS>\n")
	p.printf("<CURDEF>%s</CURDEF>\n", bs.Currency)

	// Account info
	p.print("<BANKACCTFROM>\n")
	p.printf("<BANKID>%s</BANKID>\n", xmlEscape(bs.Account.BankID))
	p.printf("<ACCTID>%s</ACCTID>\n", xmlEscape(bs.Account.AccountID))
	p.printf("<ACCTTYPE>%s</ACCTTYPE>\n", bs.Account.Type)
	p.print("</BANKACCTFROM>\n")

	writeTransactionList(p, bs.StartDate, bs.EndDate, bs.Transactions)
	writeBalance(p, bs.LedgerBal)

	p.print("</STMTRS>\n")
	p.print("</STMTTRNRS>\n")
	p.print("</BANKMSGSRSV1>\n")
}

func writeCreditCardStatement(p *printer, cc *CreditCardStatement) {
	p.print("<CREDITCARDMSGSRSV1>\n")
	p.print("<CCSTMTTRNRS>\n")
	p.print("<TRNUID>0</TRNUID>\n")
	p.print("<STATUS>\n")
	p.print("<CODE>0</CODE>\n")
	p.print("<SEVERITY>INFO</SEVERITY>\n")
	p.print("</STATUS>\n")
	p.print("<CCSTMTRS>\n")
	p.printf("<CURDEF>%s</CURDEF>\n", cc.Currency)

	// Account info
	p.print("<CCACCTFROM>\n")
	p.printf("<ACCTID>%s</ACCTID>\n", xmlEscape(cc.Account.AccountID))
	p.print("</CCACCTFROM>\n")

	writeTransactionList(p, cc.StartDate, cc.EndDate, cc.Transactions)
	writeBalance(p, cc.LedgerBal)

	p.print("</CCSTMTRS>\n")
	p.print("</CCSTMTTRNRS>\n")
	p.print("</CREDITCARDMSGSRSV1>\n")
}

func writeTransactionList(p *printer, start, end civildate.ISO8601Date, txns []Transaction) {
	p.print("<BANKTRANLIST>\n")
	p.printf("<DTSTART>%s</DTSTART>\n", ofxDate(start))
	p.printf("<DTEND>%s</DTEND>\n", ofxDate(end))
	for _, txn := range txns {
		p.print("<STMTTRN>\n")
		p.printf("<TRNTYPE>%s</TRNTYPE>\n", txn.Type)
		p.printf("<DTPOSTED>%s</DTPOSTED>\n", ofxDate(txn.DatePosted))
		p.printf("<TRNAMT>%s</TRNAMT>\n", txn.Amount)
		p.printf("<FITID>%s</FITID>\n", xmlEscape(txn.ID))
		p.printf("<NAME>%s</NAME>\n", xmlEscape(txn.Name))
		if txn.Memo != "" {
			p.printf("<MEMO>%s</MEMO>\n", xmlEscape(txn.Memo))
		}
		if txn.CheckNumber != "" {
			p.printf("<CHECKNUM>%s</CHECKNUM>\n", xmlEscape(txn.CheckNumber))
		}
		p.print("</STMTTRN>\n")
	}
	p.print("</BANKTRANLIST>\n")
}

func writeBalance(p *printer, bal Balance) {
	p.print("<LEDGERBAL>\n")
	p.printf("<BALAMT>%s</BALAMT>\n", bal.Amount)
	p.printf("<DTASOF>%s</DTASOF>\n", ofxDate(bal.AsOf))
	p.print("</LEDGERBAL>\n")
}

// --- OFX reader (parses OFX 2.2 XML back into Statement) ---

// XML unmarshaling types mirror the OFX 2.2 structure we produce.

type xmlOFX struct {
	XMLName xml.Name  `xml:"OFX"`
	Signon  xmlSignon `xml:"SIGNONMSGSRSV1>SONRS"`
	Bank    *xmlBank  `xml:"BANKMSGSRSV1"`
	CC      *xmlCC    `xml:"CREDITCARDMSGSRSV1"`
}

type xmlSignon struct {
	ServerDate string `xml:"DTSERVER"`
	Language   string `xml:"LANGUAGE"`
	FI         struct {
		Org string `xml:"ORG"`
		FID string `xml:"FID"`
	} `xml:"FI"`
}

type xmlBank struct {
	StmtRS xmlBankStmtRS `xml:"STMTTRNRS>STMTRS"`
}

type xmlBankStmtRS struct {
	Currency string `xml:"CURDEF"`
	Account  struct {
		BankID    string `xml:"BANKID"`
		AccountID string `xml:"ACCTID"`
		Type      string `xml:"ACCTTYPE"`
	} `xml:"BANKACCTFROM"`
	TranList  xmlTranList `xml:"BANKTRANLIST"`
	LedgerBal xmlBalance  `xml:"LEDGERBAL"`
}

type xmlCC struct {
	StmtRS xmlCCStmtRS `xml:"CCSTMTTRNRS>CCSTMTRS"`
}

type xmlCCStmtRS struct {
	Currency string `xml:"CURDEF"`
	Account  struct {
		AccountID string `xml:"ACCTID"`
	} `xml:"CCACCTFROM"`
	TranList  xmlTranList `xml:"BANKTRANLIST"`
	LedgerBal xmlBalance  `xml:"LEDGERBAL"`
}

type xmlTranList struct {
	Start string   `xml:"DTSTART"`
	End   string   `xml:"DTEND"`
	Txns  []xmlTxn `xml:"STMTTRN"`
}

type xmlTxn struct {
	Type       string `xml:"TRNTYPE"`
	DatePosted string `xml:"DTPOSTED"`
	Amount     string `xml:"TRNAMT"`
	ID         string `xml:"FITID"`
	Name       string `xml:"NAME"`
	Memo       string `xml:"MEMO"`
	CheckNum   string `xml:"CHECKNUM"`
}

type xmlBalance struct {
	Amount string `xml:"BALAMT"`
	AsOf   string `xml:"DTASOF"`
}

// parseOFXDate converts an OFX date to an ISO8601Date. OFX dates are YYYYMMDD,
// optionally followed by a time, fractional seconds, and a [gmt offset:zone]
// suffix (YYYYMMDDHHMMSS.XXX[-5:EST]). The date is always the leading eight
// digits; only that calendar date is retained.
func parseOFXDate(s string) (civildate.ISO8601Date, error) {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return civildate.ISO8601Date{}, fmt.Errorf("ofx: date %q is too short", s)
	}
	return civildate.Parse("20060102", s[:8])
}

func convertTxns(raw []xmlTxn) ([]Transaction, error) {
	txns := make([]Transaction, len(raw))
	for i, r := range raw {
		posted, err := parseOFXDate(r.DatePosted)
		if err != nil {
			return nil, fmt.Errorf("parse date for %s: %w", r.ID, err)
		}
		txns[i] = Transaction{
			Type:        TransactionType(r.Type),
			DatePosted:  posted,
			Amount:      r.Amount,
			ID:          r.ID,
			Name:        r.Name,
			Memo:        r.Memo,
			CheckNumber: r.CheckNum,
		}
	}
	return txns, nil
}

// Read parses an OFX 2.2 document in the shape this package writes and returns
// the Statement; Write and Read round-trip. It is not a general OFX reader (see
// the package comment): it expects the aggregates Write produces, treats the
// statement dates and LEDGERBAL as mandatory, and tolerates datetime/timezone
// suffixes on date fields while keeping only the calendar date.
func Read(r io.Reader) (Statement, error) {
	var raw xmlOFX
	if err := xml.NewDecoder(r).Decode(&raw); err != nil {
		return Statement{}, fmt.Errorf("decode OFX XML: %w", err)
	}

	serverDate, err := parseOFXDate(raw.Signon.ServerDate)
	if err != nil {
		return Statement{}, fmt.Errorf("parse server date: %w", err)
	}

	stmt := Statement{
		ServerDate: serverDate,
		Language:   raw.Signon.Language,
		Org:        raw.Signon.FI.Org,
		FID:        raw.Signon.FI.FID,
	}

	if raw.Bank != nil {
		b := raw.Bank.StmtRS
		startDate, err := parseOFXDate(b.TranList.Start)
		if err != nil {
			return Statement{}, fmt.Errorf("parse bank start date: %w", err)
		}
		endDate, err := parseOFXDate(b.TranList.End)
		if err != nil {
			return Statement{}, fmt.Errorf("parse bank end date: %w", err)
		}
		balAsOf, err := parseOFXDate(b.LedgerBal.AsOf)
		if err != nil {
			return Statement{}, fmt.Errorf("parse bank balance date: %w", err)
		}
		txns, err := convertTxns(b.TranList.Txns)
		if err != nil {
			return Statement{}, err
		}
		stmt.Bank = &BankStatement{
			Account: BankAccount{
				BankID:    b.Account.BankID,
				AccountID: b.Account.AccountID,
				Type:      AccountType(b.Account.Type),
			},
			Currency:     b.Currency,
			StartDate:    startDate,
			EndDate:      endDate,
			Transactions: txns,
			LedgerBal:    Balance{Amount: b.LedgerBal.Amount, AsOf: balAsOf},
		}
	}

	if raw.CC != nil {
		c := raw.CC.StmtRS
		startDate, err := parseOFXDate(c.TranList.Start)
		if err != nil {
			return Statement{}, fmt.Errorf("parse cc start date: %w", err)
		}
		endDate, err := parseOFXDate(c.TranList.End)
		if err != nil {
			return Statement{}, fmt.Errorf("parse cc end date: %w", err)
		}
		balAsOf, err := parseOFXDate(c.LedgerBal.AsOf)
		if err != nil {
			return Statement{}, fmt.Errorf("parse cc balance date: %w", err)
		}
		txns, err := convertTxns(c.TranList.Txns)
		if err != nil {
			return Statement{}, err
		}
		stmt.CreditCard = &CreditCardStatement{
			Account:      CreditCardAccount{AccountID: c.Account.AccountID},
			Currency:     c.Currency,
			StartDate:    startDate,
			EndDate:      endDate,
			Transactions: txns,
			LedgerBal:    Balance{Amount: c.LedgerBal.Amount, AsOf: balAsOf},
		}
	}

	if stmt.Bank == nil && stmt.CreditCard == nil {
		return Statement{}, errors.New("ofx: no bank or credit card statement found")
	}

	return stmt, nil
}
