package plaid

import (
	"github.com/jeffbstewart/bankferry/source"
)

// This file is the only place Plaid's shapes become the pipeline's shapes.
// Nothing downstream — ofxexport, ofx, db — may import this package.

// Plaid account subtypes, spelled exactly as the API sends them. Two carry
// a space, which is easy to get wrong.
const (
	subtypeChecking    = "checking"
	subtypeSavings     = "savings"
	subtypeMoneyMarket = "money market"
	subtypeCreditCard  = "credit card"
)

// SourceAccounts converts the accounts under an Item, dropping those that
// have no place in a bank OFX statement.
//
// Only depository and credit accounts are exported. Plaid also returns
// loan, investment and brokerage accounts — the sandbox Item carries a
// 401k — and writing an investment account into a BANKMSGSRSV1 statement
// would be nonsense.
func SourceAccounts(accounts []AccountInfo, item ItemInfo) []source.Account {
	institution := source.Institution{
		ID:   item.InstitutionID,
		Name: item.InstitutionName,
	}

	out := make([]source.Account, 0, len(accounts))
	for _, a := range accounts {
		accountType, ok := sourceAccountType(a.Type)
		if !ok {
			continue
		}

		account := source.Account{
			ID:          a.AccountID,
			Name:        a.Name,
			Type:        accountType,
			Subtype:     sourceSubtype(a.Subtype),
			Currency:    a.Currency,
			LastFour:    a.Mask,
			Institution: institution,
		}
		if a.Balance.Current != nil {
			balance := *a.Balance.Current
			account.Balance = &balance
		}
		out = append(out, account)
	}
	return out
}

// SkippedAccounts names the accounts SourceAccounts dropped, so the
// operator is told rather than left to wonder where their 401k went.
func SkippedAccounts(accounts []AccountInfo) []AccountInfo {
	var skipped []AccountInfo
	for _, a := range accounts {
		if _, ok := sourceAccountType(a.Type); !ok {
			skipped = append(skipped, a)
		}
	}
	return skipped
}

func sourceAccountType(t string) (source.AccountType, bool) {
	switch t {
	case AccountTypeDepository:
		return source.Depository, true
	case AccountTypeCredit:
		return source.Credit, true
	default:
		return "", false
	}
}

// sourceSubtype maps a Plaid subtype onto an OFX ACCTTYPE. An unrecognized
// depository subtype falls back to checking, which is what the OFX writer
// would have chosen anyway.
func sourceSubtype(subtype string) source.AccountSubtype {
	switch subtype {
	case subtypeChecking:
		return source.Checking
	case subtypeSavings:
		return source.Savings
	case subtypeMoneyMarket:
		return source.MoneyMarket
	case subtypeCreditCard:
		return source.CreditCard
	default:
		return source.Checking
	}
}

// SourceTransaction converts one transaction.
//
// The amount passes through untouched. Plaid signs money leaving the
// account as positive, and that is exactly what source.Transaction
// documents, so the adapter performs no negation at all. The OFX writer
// applies the sign convention of whichever statement it is producing.
//
// Description carries Plaid's raw `name` and MerchantName carries the
// normalized `merchant_name`. Both travel downstream: the raw descriptor is
// the authoritative key for payee rules, and merchant_name is the cleaner
// suggestion the map stage falls back to and shows during review. See
// payee.md — merchant_name is often better but sometimes confidently wrong,
// so it never overrides a rule.
func SourceTransaction(t Transaction) source.Transaction {
	return source.Transaction{
		ID:           t.ID,
		AccountID:    t.AccountID,
		Amount:       t.Amount,
		Date:         t.Date,
		Description:  t.Name,
		MerchantName: t.MerchantName,
		Pending:      t.Pending,
	}
}

// SourceTransactionsByAccount converts transactions and groups them by
// account, which is the shape the exporter consumes.
//
// Transactions belonging to an account that was filtered out — an
// investment account, say — are dropped, because no statement will be
// written for them.
func SourceTransactionsByAccount(txns []Transaction, accounts []source.Account) map[string][]source.Transaction {
	wanted := make(map[string]bool, len(accounts))
	for _, a := range accounts {
		wanted[a.ID] = true
	}

	byAccount := make(map[string][]source.Transaction, len(accounts))
	for _, t := range txns {
		if !wanted[t.AccountID] {
			continue
		}
		byAccount[t.AccountID] = append(byAccount[t.AccountID], SourceTransaction(t))
	}
	return byAccount
}
