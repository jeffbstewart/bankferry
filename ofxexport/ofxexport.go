// Package ofxexport converts already-fetched transactions into OFX
// documents, one file per account, skipping anything already exported.
//
// It does not fetch, and it does not record what it wrote. Both belong to
// the caller, because a provider's fetch is not necessarily per-account —
// Plaid's /transactions/sync returns one delta for a whole Item — and
// because the record of what was exported must be committed in the same
// database transaction that advances the provider's cursor.
package ofxexport

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/jeffbstewart/bankferry/civildate"
	"github.com/jeffbstewart/bankferry/money"
	"github.com/jeffbstewart/bankferry/ofx"
	"github.com/jeffbstewart/bankferry/source"
)

// ExportStore reports which transactions have already been written.
//
// It has no Mark method on purpose. Recording an export and advancing the
// provider's cursor must happen together, and only the caller holds both.
type ExportStore interface {
	IsExported(transactionID string) (bool, error)
}

// FileCreator creates (or truncates) a file and returns a WriteCloser.
type FileCreator func(path string) (io.WriteCloser, error)

// Exporter writes OFX files.
type Exporter struct {
	Store      ExportStore
	OutputDir  string
	DryRun     bool
	CreateFile FileCreator
}

// AccountResult describes the outcome of exporting a single account.
type AccountResult struct {
	AccountName     string
	NewTransactions int
	FilePath        string
	Skipped         bool
	Err             error

	// ExportedIDs are the transaction IDs written to FilePath. The caller
	// records them, together with the provider's cursor, in one database
	// transaction.
	ExportedIDs []string

	// Transactions is populated in dry-run mode with the posted transactions
	// that would have been exported.
	Transactions []source.Transaction
}

// ExportAll exports each account from the transactions supplied for it,
// continuing past per-account errors.
func (e *Exporter) ExportAll(accounts []source.Account, byAccount map[string][]source.Transaction) []AccountResult {
	results := make([]AccountResult, 0, len(accounts))
	for _, acct := range accounts {
		results = append(results, e.ExportAccount(acct, byAccount[acct.ID]))
	}
	return results
}

// ExportAccount filters out pending and already-exported transactions,
// writes an OFX file, and reports which transaction IDs it contains.
//
// It does not record the export. If the process dies before the caller
// commits, the file is rewritten on the next run and GnuCash deduplicates
// it on FITID, which is stable. The reverse order would lose transactions:
// a provider cursor that advanced past transactions never written is not
// recoverable.
func (e *Exporter) ExportAccount(acct source.Account, txns []source.Transaction) AccountResult {
	result := AccountResult{AccountName: acct.Name}

	// 1. Filter out pending.
	//
	// This is load-bearing, not tidiness. Plaid models a pending transaction
	// that posts as a *new* transaction with a different ID. Exporting the
	// pending one would import the same purchase twice, because GnuCash
	// deduplicates on FITID alone.
	var posted []source.Transaction
	for _, txn := range txns {
		if !txn.Pending {
			posted = append(posted, txn)
		}
	}

	// 2. In dry-run mode, show all posted transactions without filtering.
	if e.DryRun {
		if len(posted) == 0 {
			result.Skipped = true
			return result
		}
		result.NewTransactions = len(posted)
		result.Transactions = posted
		return result
	}

	// 3. Filter out already-exported.
	var newTxns []source.Transaction
	for _, txn := range posted {
		exported, err := e.Store.IsExported(txn.ID)
		if err != nil {
			result.Err = fmt.Errorf("check exported %s: %w", txn.ID, err)
			return result
		}
		if !exported {
			newTxns = append(newTxns, txn)
		}
	}

	if len(newTxns) == 0 {
		result.Skipped = true
		return result
	}

	result.NewTransactions = len(newTxns)

	// 4. Build and write the OFX statement.
	today := civildate.Today()
	stmt, err := e.buildStatement(acct, newTxns, posted, today)
	if err != nil {
		result.Err = fmt.Errorf("build statement: %w", err)
		return result
	}

	timestamp := time.Now().Format("20060102150405")
	filename := sanitizeFilename(acct.Institution.Name) + "_" + acct.LastFour + "_" + timestamp + ".ofx"
	filePath := filepath.Join(e.OutputDir, filename)

	f, err := e.CreateFile(filePath)
	if err != nil {
		result.Err = fmt.Errorf("create file %s: %w", filePath, err)
		return result
	}

	writeErr := ofx.Write(f, stmt)
	closeErr := f.Close()
	if writeErr != nil {
		removeFile(filePath)
		result.Err = fmt.Errorf("write OFX: %w", writeErr)
		return result
	}
	if closeErr != nil {
		removeFile(filePath)
		result.Err = fmt.Errorf("close file: %w", closeErr)
		return result
	}

	result.FilePath = filePath
	result.ExportedIDs = make([]string, len(newTxns))
	for i, txn := range newTxns {
		result.ExportedIDs[i] = txn.ID
	}

	return result
}

func removeFile(path string) {
	if err := os.Remove(path); err != nil {
		log.Printf("ofxexport: remove failed file %s: %v", path, err)
	}
}

func (e *Exporter) buildStatement(acct source.Account, newTxns, allPosted []source.Transaction, today civildate.ISO8601Date) (ofx.Statement, error) {
	// The OFX sign convention depends on which statement is being written,
	// so it is applied here rather than in an adapter.
	ofxTxns := make([]ofx.Transaction, len(newTxns))
	for i, txn := range newTxns {
		amount, err := ofxAmount(acct.Type, txn.Amount)
		if err != nil {
			return ofx.Statement{}, fmt.Errorf("transaction %s amount: %w", txn.ID, err)
		}
		ofxTxns[i] = ofx.Transaction{
			Type:       ofxTransactionType(txn.Amount),
			DatePosted: txn.Date,
			Amount:     amount,
			ID:         txn.ID,
			Name:       txn.Description,
			// MerchantName rides in MEMO so the separate `map` stage, which
			// reads only the OFX file, can offer it during payee resolution.
			// map clears it before writing the final file. GnuCash discards
			// MEMO on import, so it never reaches the book regardless.
			Memo: txn.MerchantName,
		}
	}

	// The statement period spans all posted transactions, not just the new
	// ones. Providers make no promise about the order they return them in,
	// so both ends are found by scanning.
	startDate, endDate := today, today
	for i, txn := range allPosted {
		if i == 0 || txn.Date.Compare(startDate) < 0 {
			startDate = txn.Date
		}
		if i == 0 || txn.Date.Compare(endDate) > 0 {
			endDate = txn.Date
		}
	}

	// OFX requires a LEDGERBAL, so a provider that supplies no balance still
	// needs one: default to a well-formed zero rather than let Write reject the
	// statement. balanceAsOf falls back to the statement's own as-of date.
	ledgerAmt := "0.00"
	if acct.Balance != nil {
		var err error
		ledgerAmt, err = acct.Balance.Exact()
		if err != nil {
			return ofx.Statement{}, fmt.Errorf("ledger balance: %w", err)
		}
	}
	balanceAsOf := today
	if !acct.BalanceDate.IsZero() {
		balanceAsOf = acct.BalanceDate
	}

	stmt := ofx.Statement{
		ServerDate: today,
		Org:        acct.Institution.Name,
		FID:        acct.Institution.ID,
	}

	if acct.Type == source.Credit {
		stmt.CreditCard = &ofx.CreditCardStatement{
			Account:      ofx.CreditCardAccount{AccountID: acct.LastFour},
			Currency:     string(acct.Currency),
			StartDate:    startDate,
			EndDate:      endDate,
			Transactions: ofxTxns,
			LedgerBal:    ofx.Balance{Amount: ledgerAmt, AsOf: balanceAsOf},
		}
	} else {
		stmt.Bank = &ofx.BankStatement{
			Account: ofx.BankAccount{
				BankID:    acct.Institution.ID,
				AccountID: acct.LastFour,
				Type:      mapAccountSubtype(acct.Subtype),
			},
			Currency:     string(acct.Currency),
			StartDate:    startDate,
			EndDate:      endDate,
			Transactions: ofxTxns,
			LedgerBal:    ofx.Balance{Amount: ledgerAmt, AsOf: balanceAsOf},
		}
	}

	return stmt, nil
}

// ofxAmount converts a source amount, in which money leaving the account
// is positive, into the TRNAMT convention of the statement being written.
//
// OFX has no single sign convention. On a bank statement TRNAMT is signed
// from the account's perspective, so a withdrawal is negative. On a credit
// card statement a charge increases what is owed and is written positive.
// Negating unconditionally, as this code once did, produced correct credit
// card statements and inverted every bank transaction.
func ofxAmount(acctType source.AccountType, amount money.Amount) (string, error) {
	if acctType == source.Credit {
		return amount.Exact()
	}
	return amount.Neg().Exact()
}

func mapAccountSubtype(st source.AccountSubtype) ofx.AccountType {
	switch st {
	case source.Checking:
		return ofx.Checking
	case source.Savings:
		return ofx.Savings
	case source.MoneyMarket:
		return ofx.MoneyMarket
	default:
		return ofx.Checking
	}
}

// ofxTransactionType derives an OFX TRNTYPE from the sign of the source
// amount, in which money leaving the account is positive. Unlike TRNAMT,
// TRNTYPE does not depend on the statement type: money out is a debit
// whether it left a checking account or a credit line.
//
// Providers vary in what transaction metadata they expose — some supply
// none — and the finer OFX types did not survive import into GnuCash, so
// the sign is the only signal used.
func ofxTransactionType(amount money.Amount) ofx.TransactionType {
	if amount.IsNegative() {
		return ofx.TransactionCredit
	}
	return ofx.TransactionDebit
}

var nonAlphanumeric = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func sanitizeFilename(s string) string {
	return nonAlphanumeric.ReplaceAllString(s, "_")
}
