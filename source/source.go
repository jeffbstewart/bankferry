// Package source defines the provider-neutral account and transaction
// types consumed by the OFX export pipeline. Adapters for a specific
// data provider populate these types, so that swapping providers does
// not ripple through the export, database, or payee-mapping code.
package source

import (
	"github.com/jeffbstewart/bankferry/civildate"
	"github.com/jeffbstewart/bankferry/money"
)

// AccountType distinguishes deposit accounts from lines of credit.
// It selects which OFX message set a statement is written into, and with
// it the sign convention of every amount in that statement.
type AccountType string

const (
	// Depository is a checking, savings, or money market account.
	Depository AccountType = "depository"
	// Credit is a credit card or other line of credit.
	Credit AccountType = "credit"
)

// AccountSubtype refines an account's Type into an OFX ACCTTYPE: the
// depository subtypes (checking, savings, money market) and credit card.
type AccountSubtype string

const (
	Checking    AccountSubtype = "checking"
	Savings     AccountSubtype = "savings"
	MoneyMarket AccountSubtype = "money_market"
	CreditCard  AccountSubtype = "credit_card"
)

// Institution identifies the financial institution holding an account.
type Institution struct {
	ID   string
	Name string
}

// Account is a single bank or credit card account.
type Account struct {
	ID          string
	Name        string
	Type        AccountType
	Subtype     AccountSubtype
	Currency    money.Currency
	LastFour    string
	Institution Institution

	// Balance is the account balance, or nil when the provider supplies
	// none. It is read from the account rather than from the last
	// transaction, because transactions carry no guaranteed order and
	// several may share a date.
	Balance *money.Amount

	// BalanceDate is the date Balance was observed. Zero when unknown.
	BalanceDate civildate.ISO8601Date
}

// Transaction is a single account transaction.
type Transaction struct {
	// ID is written to OFX as the FITID. GnuCash deduplicates imports on
	// FITID alone and never on content, so an ID that changes between
	// fetches of the same transaction causes a duplicate import. Adapters
	// whose provider lacks stable identifiers must synthesize one.
	ID string

	AccountID string

	// Amount is signed so that money leaving the account is positive and
	// money arriving is negative. That is the convention Plaid uses, so
	// an adapter copies it through untouched.
	//
	// It is deliberately not the OFX convention, because OFX has no single
	// convention: a withdrawal is negative on a bank statement while a
	// charge is positive on a credit card statement. Only the writer knows
	// which statement it is producing, so only the writer converts.
	Amount money.Amount

	Date civildate.ISO8601Date

	// Description is the raw bank descriptor, e.g. "MOISON ACE HDWE". It is
	// immutable ground truth and the primary key for payee rules.
	Description string

	// MerchantName is the provider's normalized merchant, e.g. "Ace Hardware",
	// or empty when the provider supplies none. It is a cleaner-but-fallible
	// suggestion — a provider can resolve it wrong — so it never overrides a
	// rule; it only feeds payee resolution and review. Adapters for providers
	// without such a field leave it empty.
	MerchantName string

	// Pending is true for transactions the institution has not yet
	// settled. Pending transactions are never exported.
	Pending bool
}
