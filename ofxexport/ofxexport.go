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
	"errors"
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

// pendingSuffix is appended to an account's final filename to name the file
// its statement is written into. The caller renames it onto the final name
// once every account has been written. It must not end in ".ofx": the `map`
// command reads every .ofx file in the directory, and a statement that is
// still being written is not one of them.
const pendingSuffix = ".part"

// FileCreator creates a new file and returns a WriteCloser for it.
//
// It must refuse a path that already exists rather than truncate it, and
// report the refusal as an error satisfying errors.Is(err, os.ErrExist).
// Truncating would lose transactions silently — see ExportAccount.
type FileCreator func(path string) (io.WriteCloser, error)

// ExistsFunc reports whether a path is already taken. A failure to tell is
// an error, never a false: treating an unreadable directory as "nothing
// there" is how the file this check protects gets overwritten.
type ExistsFunc func(path string) (bool, error)

// Exporter writes OFX files.
type Exporter struct {
	Store      ExportStore
	OutputDir  string
	DryRun     bool
	CreateFile FileCreator
	Exists     ExistsFunc

	// Now supplies the statement's as-of date and the second that
	// distinguishes one export's filenames from the next's. It is injected
	// because a test of a filename collision cannot be at the mercy of which
	// side of a second boundary two accounts land on.
	Now func() time.Time
}

// AccountResult describes the outcome of exporting a single account.
type AccountResult struct {
	AccountName     string
	NewTransactions int

	// FilePath is where the statement belongs. Nothing is there yet: the
	// statement is written to PendingPath, and the caller renames it onto
	// FilePath once every account has been exported.
	FilePath string

	// PendingPath holds the written statement, under a name `map` ignores.
	// It is empty for a skipped account and in dry-run mode.
	PendingPath string

	Skipped bool
	Err     error

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
//
// Every result that carries a PendingPath has a complete statement waiting
// under it, including results produced after one that failed. A caller that
// abandons the batch must remove all of them.
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
// It does not record the export, and it does not put the file under its
// final name. Both belong to the caller, which renames every account's file
// into place together and only then advances the provider's cursor. If the
// process dies before that, the file is rewritten on the next run and
// GnuCash deduplicates it on FITID, which is stable. The reverse order would
// lose transactions: a provider cursor that advanced past transactions never
// written is not recoverable.
//
// Two collision guards stand between a filename and that loss, because a
// filename carries only the institution, the account mask, and a timestamp
// good to one second, and none of the three is unique:
//
//   - The final name must be free before anything is written. A rename
//     replaces whatever occupies its target, on every OS this runs on, so
//     checking at rename time would be too late — the replaced file's
//     transactions are recorded as exported and the cursor moves past them.
//
//   - The pending file is created exclusively. Two accounts exported
//     together resolve to the same final name if they share an institution
//     and a mask, and neither final name exists yet, so the free-name check
//     passes for both; the second one's pending file is what collides.
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
	now := e.Now()
	today := civildate.FromTime(now)
	stmt, err := e.buildStatement(acct, newTxns, posted, today)
	if err != nil {
		result.Err = fmt.Errorf("build statement: %w", err)
		return result
	}

	timestamp := now.Format("20060102150405")
	filename := sanitizeFilename(acct.Institution.Name) + "_" + acct.LastFour + "_" + timestamp + ".ofx"
	filePath := filepath.Join(e.OutputDir, filename)

	taken, err := e.Exists(filePath)
	if err != nil {
		result.Err = fmt.Errorf("check whether %s already exists: %w", filePath, err)
		return result
	}
	if taken {
		result.Err = fmt.Errorf("%s already exists, so %s was not exported: renaming "+
			"onto it would destroy the transactions it holds. Nothing was committed; "+
			"run fetch again, which names the file for a later second: %w",
			filePath, acct.Name, os.ErrExist)
		return result
	}

	// The pending name is the final name plus a constant suffix, and nothing
	// else. Appending a constant is injective, so two accounts collide here if
	// and only if they would have collided on the final name — which makes the
	// exclusive create below the check the free-name check above cannot be:
	// within one batch no final name exists yet, so both accounts pass it.
	pendingPath := filePath + pendingSuffix

	f, err := e.CreateFile(pendingPath)
	if errors.Is(err, os.ErrExist) {
		// Nothing is removed: the pending file belongs to an account exported
		// moments ago, and its statement is complete and waiting to be renamed.
		result.Err = fmt.Errorf("%s and an account already exported produce the same "+
			"file name, %s: they share an institution and the mask %q. Nothing was "+
			"committed: %w", acct.Name, filePath, acct.LastFour, err)
		return result
	}
	if err != nil {
		result.Err = fmt.Errorf("create file %s: %w", pendingPath, err)
		return result
	}

	writeErr := ofx.Write(f, stmt)
	closeErr := f.Close()
	if writeErr != nil {
		removeFile(pendingPath)
		result.Err = fmt.Errorf("write OFX: %w", writeErr)
		return result
	}
	if closeErr != nil {
		removeFile(pendingPath)
		result.Err = fmt.Errorf("close file: %w", closeErr)
		return result
	}

	result.FilePath = filePath
	result.PendingPath = pendingPath
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
	// Source amounts sign money leaving the account positive; OFX signs it
	// negative. The flip is applied here, in the writer, and nowhere else.
	ofxTxns := make([]ofx.Transaction, len(newTxns))
	for i, txn := range newTxns {
		amount, err := ofxAmount(txn.Amount)
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
// is positive, into the OFX TRNAMT convention, in which money leaving the
// account is negative. The negation is unconditional: TRNAMT is signed
// from the account holder's perspective on every statement type, so a
// credit card charge is negative exactly like a bank withdrawal.
//
// This code once wrote credit card charges positive, on the theory that a
// charge increases what is owed. The first real GnuCash import proved that
// wrong: every positive charge landed in the register's Payment column.
// The Teller-era exporter negated unconditionally, and months of real
// credit card imports validated it. Do not bring the account-type branch
// back without evidence from an actual import.
func ofxAmount(amount money.Amount) (string, error) {
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
