package cli

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeffbstewart/bankferry/db"
	"github.com/jeffbstewart/bankferry/ofx"
	"github.com/jeffbstewart/bankferry/payee"
)

// UnmappedSubdir and MappedSubdir partition OFX_OUTPUT_DIR. `fetch` writes raw
// files into unmapped/; `map` reads them and writes finished files into
// mapped/. Keeping the raw files in a folder named "unmapped" is the guard
// against importing an unmapped file by mistake — the finished ones you import
// live only in mapped/.
const (
	UnmappedSubdir = "unmapped"
	MappedSubdir   = "mapped"
)

func runMap(args []string) {
	// No flags of its own, but parse anyway so an unknown flag is rejected
	// rather than silently ignored.
	parseFlags(newFlags("map"), args)

	outputDir := os.Getenv("OFX_OUTPUT_DIR")
	if outputDir == "" {
		stderr("Error: OFX_OUTPUT_DIR is not set.\n")
		stderr("Set OFX_OUTPUT_DIR in .env or environment.\n")
		os.Exit(1)
	}

	store, closeDB := openDB()
	defer closeDB()

	matcher, err := buildMatcher(store)
	if err != nil {
		stderr("Error loading payee rules: %v\n", err)
		os.Exit(1)
	}

	unmappedDir := filepath.Join(outputDir, UnmappedSubdir)
	mappedDir := filepath.Join(outputDir, MappedSubdir)
	if err := os.MkdirAll(mappedDir, 0o755); err != nil {
		stderr("Error creating mapped directory: %v\n", err)
		os.Exit(1)
	}

	ofxFiles := findUnmappedFiles(unmappedDir, mappedDir)
	if len(ofxFiles) == 0 {
		stdout("No unmapped OFX files in %s.\n", unmappedDir)
		return
	}

	stdout("Found %d OFX file(s) to map.\n\n", len(ofxFiles))

	scanner := bufio.NewScanner(os.Stdin)
	totalMapped, totalFiles := 0, 0

	for _, fname := range ofxFiles {
		stmt, txns, err := readStatement(filepath.Join(unmappedDir, fname))
		if err != nil {
			stderr("  %v\n", err)
			continue
		}

		stdout("Mapping: %s (%d transactions)\n", fname, len(txns))

		mapped, quit := mapTransactions(scanner, store, &matcher, txns)
		totalMapped += mapped

		writeMemosCleared(stmt, txns)
		if err := writeMapped(stmt, filepath.Join(mappedDir, fname)); err != nil {
			stderr("  %v\n", err)
			continue
		}
		totalFiles++
		stdout("\nWrote: %s/%s (%d transactions)\n\n", MappedSubdir, fname, len(txns))

		if quit {
			break
		}
	}

	stdout("Done. %d file(s) written, %d transaction(s) mapped.\n", totalFiles, totalMapped)
}

// findUnmappedFiles lists the .ofx files in unmapped/ that do not already have
// a counterpart in mapped/.
func findUnmappedFiles(unmappedDir, mappedDir string) []string {
	entries, err := os.ReadDir(unmappedDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		stderr("Error reading %s: %v\n", unmappedDir, err)
		os.Exit(1)
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".ofx") {
			continue
		}
		if _, serr := os.Stat(filepath.Join(mappedDir, e.Name())); serr == nil {
			continue // already mapped
		}
		files = append(files, e.Name())
	}
	return files
}

func readStatement(path string) (*ofx.Statement, []ofx.Transaction, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	stmt, err := ofx.Read(f)
	if cerr := f.Close(); cerr != nil {
		log.Printf("cli: close %s: %v", path, cerr)
	}
	if err != nil {
		return nil, nil, err
	}
	return &stmt, statementTxns(stmt), nil
}

// statementTxns returns the transaction slice, bank or credit card. The
// returned slice aliases the statement's own, so edits are reflected.
func statementTxns(stmt ofx.Statement) []ofx.Transaction {
	if stmt.Bank != nil {
		return stmt.Bank.Transactions
	}
	if stmt.CreditCard != nil {
		return stmt.CreditCard.Transactions
	}
	return nil
}

// mapTransactions resolves and reviews each transaction, editing txns in
// place. Returns how many were named and whether the operator quit.
//
// Resolution: a rule (raw beats merchant) auto-applies. Anything unmatched
// goes to review, where the raw and merchant names are shown side by side —
// because merchant_name can be confidently wrong, it is never applied
// silently.
func mapTransactions(scanner *bufio.Scanner, store *db.DB, matcher **payee.Matcher, txns []ofx.Transaction) (mapped int, quit bool) {
	auto, reviewed := 0, 0
	for i := range txns {
		txn := &txns[i]
		raw, merchant := txn.Name, txn.Memo

		if m := (*matcher).Match(raw, merchant); m.Matched() {
			txn.Name = m.Payee.Name
			auto++
			mapped++
			continue
		}

		reviewed++
		action := reviewTransaction(scanner, store, txn, raw, merchant)
		switch action {
		case reviewNamed:
			mapped++
			// A new rule may cover other transactions still ahead in this
			// file; rebuild so it applies without asking again. The rule is
			// already persisted, so a rebuild failure only costs re-asking
			// within this session — but log it rather than swallow it.
			if updated, merr := buildMatcher(store); merr == nil {
				*matcher = updated
			} else {
				log.Printf("cli: rebuild matcher after new rule: %v", merr)
			}
		case reviewSkip:
			// Leave the raw name in place.
		case reviewQuit:
			stdout("  %d auto-mapped, %d reviewed (quit).\n", auto, reviewed)
			return mapped, true
		}
	}
	stdout("  %d auto-mapped, %d reviewed.\n", auto, reviewed)
	return mapped, false
}

type reviewAction int

const (
	reviewNamed reviewAction = iota
	reviewSkip
	reviewQuit
)

// reviewTransaction shows one unmatched transaction and its two candidate
// names, and turns the operator's choice into a rule so it is not asked again.
func reviewTransaction(scanner *bufio.Scanner, store *db.DB, txn *ofx.Transaction, raw, merchant string) reviewAction {
	stdout("\n[review]  %s  %s\n", txn.DatePosted.String(), txn.Amount)
	stdout("  raw:       %s\n", raw)
	if merchant != "" {
		stdout("  merchant:  %s   <- Plaid's guess (may be wrong)\n", merchant)
	} else {
		stdout("  merchant:  (none)\n")
	}

	for {
		if merchant != "" {
			stdout("  [m] accept merchant   [r] keep raw   [t] type a name   [s] skip   [q] quit\n")
		} else {
			stdout("  [r] keep raw   [t] type a name   [s] skip   [q] quit\n")
		}
		stdout("> ")
		if !scanner.Scan() {
			return reviewQuit
		}
		switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
		case "m", "merchant":
			if merchant == "" {
				stdout("  No merchant name for this transaction.\n")
				continue
			}
			// Trusting Plaid's label: a merchant-keyed rule catches every
			// future transaction Plaid resolves to this merchant, whatever
			// the raw descriptor.
			if saveRule(store, txn, merchant, merchant, db.MatchMerchant) {
				return reviewNamed
			}
			return reviewSkip
		case "r", "raw":
			// Keep the raw descriptor as the name, but stop asking: a
			// raw-keyed rule on a pattern from the raw text.
			pattern := promptPattern(scanner, raw)
			if saveRule(store, txn, raw, pattern, db.MatchRaw) {
				return reviewNamed
			}
			return reviewSkip
		case "t", "type", "e", "edit":
			return typePayee(scanner, store, txn, raw)
		case "s", "skip":
			return reviewSkip
		case "q", "quit":
			return reviewQuit
		default:
			stdout("  Unknown command.\n")
		}
	}
}

// typePayee reads a payee name and a raw-keyed pattern, and saves the rule.
// Raw-keyed by default because the reason to type a correction is usually that
// the merchant name is wrong (the case where Plaid mislabels a raw descriptor).
func typePayee(scanner *bufio.Scanner, store *db.DB, txn *ofx.Transaction, raw string) reviewAction {
	stdout("  Payee name: ")
	if !scanner.Scan() {
		return reviewQuit
	}
	name := strings.TrimSpace(scanner.Text())
	if name == "" {
		stdout("  Skipped (empty name).\n")
		return reviewSkip
	}
	pattern := promptPattern(scanner, raw)
	if saveRule(store, txn, name, pattern, db.MatchRaw) {
		return reviewNamed
	}
	return reviewSkip
}

// promptPattern offers a pattern derived from the raw description and lets the
// operator refine it.
func promptPattern(scanner *bufio.Scanner, raw string) string {
	suggestion := suggestPattern(raw)
	stdout("  Match pattern [%s]: ", suggestion)
	if !scanner.Scan() {
		return suggestion
	}
	if p := strings.TrimSpace(scanner.Text()); p != "" {
		return p
	}
	return suggestion
}

// saveRule persists a payee and a rule, and applies the name to the
// transaction. Returns false (and leaves the raw name) on a storage error.
func saveRule(store *db.DB, txn *ofx.Transaction, name, pattern string, field db.MatchField) bool {
	if pattern == "" {
		stdout("  Empty pattern; skipped.\n")
		return false
	}
	payeeID, err := store.UpsertPayee(name)
	if err != nil {
		stderr("  Error saving payee: %v\n", err)
		return false
	}
	if err := store.InsertRuleIfNew(payeeID, pattern, field, 10); err != nil {
		stderr("  Error saving rule: %v\n", err)
		return false
	}
	txn.Name = name
	stdout("  Saved: %s rule %q -> %s\n", field, pattern, name)
	return true
}

// writeMemosCleared blanks the MEMO on every transaction. MEMO carried the
// merchant name from fetch to here; it has served its purpose and must not
// reach the final file.
func writeMemosCleared(stmt *ofx.Statement, txns []ofx.Transaction) {
	for i := range txns {
		txns[i].Memo = ""
	}
	if stmt.Bank != nil {
		stmt.Bank.Transactions = txns
	} else if stmt.CreditCard != nil {
		stmt.CreditCard.Transactions = txns
	}
}

func writeMapped(stmt *ofx.Statement, dstPath string) error {
	// Write to a temp file and rename into place, so mapped/<name>.ofx only ever
	// appears once it is complete. findUnmappedFiles skips any name already
	// present in mapped/, so a partial file — a full disk or a crash mid-write —
	// would otherwise never be regenerated, and mapped/ is exactly what GnuCash
	// imports. (The fetch path in ofxexport guards against the same hazard.)
	tmp, err := os.CreateTemp(filepath.Dir(dstPath), ".map-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	writeErr := ofx.Write(tmp, *stmt)
	closeErr := tmp.Close()
	if writeErr != nil {
		removeTemp(tmpName)
		return writeErr
	}
	if closeErr != nil {
		removeTemp(tmpName)
		return closeErr
	}
	if err := os.Rename(tmpName, dstPath); err != nil {
		removeTemp(tmpName)
		return err
	}
	return nil
}

func removeTemp(path string) {
	if err := os.Remove(path); err != nil {
		log.Printf("cli: remove temp map file %s: %v", path, err)
	}
}

func buildMatcher(store *db.DB) (*payee.Matcher, error) {
	payees, err := store.LoadPayees()
	if err != nil {
		return nil, err
	}
	rules, err := store.LoadRules()
	if err != nil {
		return nil, err
	}
	pPayees := make([]payee.Payee, len(payees))
	for i, p := range payees {
		pPayees[i] = payee.Payee{ID: p.ID, Name: p.Name}
	}
	pRules := make([]payee.Rule, len(rules))
	for i, r := range rules {
		pRules[i] = payee.Rule{
			ID: r.ID, PayeeID: r.PayeeID, Pattern: r.Pattern,
			Field: payee.MatchField(r.MatchField), Priority: r.Priority,
		}
	}
	return payee.NewMatcher(pPayees, pRules)
}

// suggestPattern extracts a likely match pattern from a raw transaction
// description by taking the portion before the first common delimiter.
func suggestPattern(description string) string {
	upper := strings.ToUpper(strings.TrimSpace(description))
	for _, delim := range []string{"*", "#", "  "} {
		if i := strings.Index(upper, delim); i > 0 {
			upper = upper[:i]
		}
	}
	return strings.TrimSpace(upper)
}
