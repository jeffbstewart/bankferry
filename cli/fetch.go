package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/jeffbstewart/bankferry/civildate"
	"github.com/jeffbstewart/bankferry/db"
	"github.com/jeffbstewart/bankferry/ofxexport"
	"github.com/jeffbstewart/bankferry/plaid"
	"github.com/jeffbstewart/bankferry/source"
)

// runFetch syncs each linked Item, writes one OFX file per account, and
// advances the sync cursor.
//
// The ordering is the whole point. Files are written first, and the cursor
// is advanced last, in the same database transaction that records which
// transactions those files contain. Plaid never re-delivers a transaction
// once the cursor passes it, so a cursor that moved without its
// transactions being recorded loses them permanently. The reverse — a crash
// after writing files but before committing — is harmless: the next run
// receives the same transactions, rewrites the files, and GnuCash
// deduplicates them on FITID.
//
// Files are written into OFX_OUTPUT_DIR/unmapped/. The `map` command reads
// them from there and writes finished files into mapped/, which is what gets
// imported into GnuCash. Keeping raw output in a folder named "unmapped"
// guards against importing an unmapped file by mistake.
func runFetch(args []string) {
	fs := newFlags("fetch")
	envStr := envFlag(fs)
	daysFlag := fs.Int("days", 0, "emit only transactions within the last N days (0 = all)")
	parseFlags(fs, args)
	env := requireEnv(*envStr)

	days := *daysFlag
	if days < 0 {
		stderr("Error: --days must be zero or positive, got %d.\n", days)
		os.Exit(1)
	}

	outputDir := os.Getenv("OFX_OUTPUT_DIR")
	if outputDir == "" {
		stderr("Error: OFX_OUTPUT_DIR is not set.\n")
		stderr("Set it in .env to the directory for generated .ofx files.\n")
		os.Exit(1)
	}

	dbPath := os.Getenv("DATABASE_PATH")
	if dbPath == "" {
		dbPath = "bankferry.db"
	}

	dryRun := os.Getenv("DRY_RUN") != "false"

	items, broken, err := plaid.LoadItems(env)
	if err != nil {
		stderr("Error reading stored items: %v\n", err)
		os.Exit(1)
	}
	for _, b := range broken {
		stderr("Warning: keyring entry %s is unreadable: %v\n", b.Key, b.Err)
	}
	if len(items) == 0 {
		stderr("No linked institutions in %s.\n", env)
		stderr("Run 'bankferry plaid-link --env %s' first.\n", env)
		os.Exit(1)
	}

	client, err := plaid.NewDataClient(env, plaidCredentials(env))
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}

	store, err := db.Open(dbPath)
	if err != nil {
		stderr("Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if cerr := store.Close(); cerr != nil {
			log.Printf("cli: close database: %v", cerr)
		}
	}()

	unmappedDir := filepath.Join(outputDir, UnmappedSubdir)
	if err := os.MkdirAll(unmappedDir, 0o755); err != nil {
		stderr("Error creating %s: %v\n", unmappedDir, err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	exporter := &ofxexport.Exporter{
		Store:      store,
		OutputDir:  unmappedDir,
		DryRun:     dryRun,
		CreateFile: createExclusive,
		Exists:     pathExists,
		Now:        time.Now,
	}

	failed := 0
	totalNew := 0

	for _, item := range items {
		n, err := fetchItem(ctx, client, store, exporter, env, item, dryRun, days)
		if err != nil {
			stderr("  %s: %v\n\n", itemLabel(item), err)
			failed++
			continue
		}
		totalNew += n
	}

	if dryRun {
		stdout("Dry run: %d posted transaction(s). No files written, no cursor advanced.\n", totalNew)
		stdout("Set DRY_RUN=false in .env to write OFX files.\n")
	} else {
		stdout("Exported %d new transaction(s) to %s.\n", totalNew, unmappedDir)
		stdout("Run 'bankferry map' to clean payee names into mapped/.\n")
	}
	if failed > 0 {
		os.Exit(1)
	}
}

// createExclusive satisfies ofxexport.FileCreator: it opens a new file and
// fails with os.ErrExist rather than truncating one that is already there.
// os.Create, which truncates, would turn a filename collision into silently
// lost transactions instead of an error.
func createExclusive(path string) (io.WriteCloser, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// pathExists satisfies ofxexport.ExistsFunc. Only a definitive "not there"
// reports false; any other failure is returned, because the caller uses false
// to decide it may rename a file onto this path.
func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// itemLabel names an Item in console output.
//
// The institution name alone does not identify one. An Item is one login, so
// two logins at a bank are two Items with the same name, and a run that
// printed "Capital One" twice would leave the operator unable to tell which
// one reported what. The item ID is also the value --item wants.
func itemLabel(item plaid.Item) string {
	return fmt.Sprintf("%s (item %s)", item.InstitutionName, item.ItemID)
}

// accountLabel names an account in console output.
//
// The name alone does not identify one: Plaid passes through whatever the
// bank calls the account, and Chase calls every credit card "CREDIT", so two
// cards on one login print identically. The mask disambiguates them. It is
// the last 2 to 4 characters of the account number, and is occasionally
// absent, in which case the name is all there is.
func accountLabel(name, mask string) string {
	if mask == "" {
		return name
	}
	return fmt.Sprintf("%s (*%s)", name, mask)
}

// fetchItem syncs one Item and exports its accounts. It returns the number
// of transactions written.
func fetchItem(
	ctx context.Context,
	client *plaid.DataClient,
	store *db.DB,
	exporter *ofxexport.Exporter,
	env plaid.Environment,
	item plaid.Item,
	dryRun bool,
	days int,
) (int, error) {
	stdout("%s\n", itemLabel(item))

	// A broken Item cannot be repaired here: update mode needs a browser.
	status, err := client.FetchItemStatus(ctx, item.AccessToken)
	if err != nil {
		if plaid.IsLinkRefreshRequired(err) {
			return 0, relinkNeeded(env, item)
		}
		return 0, err
	}
	if status.NeedsLinkRefresh() {
		return 0, relinkNeeded(env, item)
	}

	accounts, info, err := client.FetchAccounts(ctx, item.AccessToken)
	if err != nil {
		return 0, err
	}

	srcAccounts := plaid.SourceAccounts(accounts, info)
	for _, skipped := range plaid.SkippedAccounts(accounts) {
		stdout("  skipping %s (%s/%s): not a bank or credit card account\n",
			accountLabel(skipped.Name, skipped.Mask), skipped.Type, skipped.Subtype)
	}
	if len(srcAccounts) == 0 {
		stdout("  no exportable accounts\n\n")
		return 0, nil
	}

	cursor, _, err := store.LoadSyncCursor(string(env), item.ItemID)
	if err != nil {
		return 0, err
	}

	result, err := client.SyncTransactions(ctx, item.AccessToken, cursor)
	if err != nil {
		return 0, err
	}

	// A modified or removed transaction may already be in the user's book.
	// Nothing here can un-import it, so it is reported rather than applied.
	reportRevisions(result)

	// --days bounds only what is emitted, not what is synced: /transactions/sync
	// is cursor-based and cannot be date-limited. Dropped transactions are not
	// written, and in a real run the cursor still advances past them, so they
	// will not be re-delivered — hence the warning. In a dry run nothing
	// commits, so it is purely a preview filter.
	if days > 0 {
		kept, dropped := withinDays(result.Added, days)
		result.Added = kept
		if dropped > 0 {
			stdout("  --days=%d: holding back %d transaction(s) older than the window.\n", days, dropped)
			if !dryRun {
				stdout("  The cursor advances past them; they will not be re-delivered.\n")
			}
		}
	}

	byAccount := plaid.SourceTransactionsByAccount(result.Added, srcAccounts)

	results := exporter.ExportAll(srcAccounts, byAccount)

	// One account's failure abandons the whole Item. The cursor covers every
	// account under it, so advancing it while one account's statement is
	// missing would lose exactly that account's transactions. Every pending
	// file goes, including those of the accounts that succeeded.
	for _, r := range results {
		if r.Err != nil {
			removeAll(pendingPaths(results))
			return 0, r.Err
		}
	}

	// Every statement is complete. They take their final names together,
	// before anything is recorded.
	var renamed []string
	if !dryRun {
		var err error
		renamed, err = commitFiles(results)
		if err != nil {
			return 0, err
		}
	}

	exported := make(map[string][]string)
	total := 0

	for i, r := range results {
		acct := srcAccounts[i]
		label := accountLabel(acct.Name, acct.LastFour)
		switch {
		case r.Skipped:
			stdout("  %s: no new transactions\n", label)
		case dryRun:
			total += r.NewTransactions
			stdout("  %s: %d posted transaction(s) (dry run)\n", label, r.NewTransactions)
			printTransactions(r.Transactions)
		default:
			total += r.NewTransactions
			exported[acct.ID] = r.ExportedIDs
			stdout("  %s: %d new transaction(s) -> %s\n", label, r.NewTransactions, r.FilePath)
		}
	}

	if dryRun {
		stdout("\n")
		return total, nil
	}

	// The files exist under the names `map` reads. Recording them and
	// advancing the cursor happen together, or not at all. If this fails, the
	// files are removed and the cursor stays put, so the next run delivers the
	// same transactions.
	if err := store.CommitSync(string(env), item.ItemID, exported, result.NextCursor); err != nil {
		removeAll(renamed)
		return 0, err
	}

	stdout("\n")
	return total, nil
}

// commitFiles renames every written statement onto its final name, and
// reports the names that now exist.
//
// No filesystem renames a set of files atomically, so this is not all-or-
// nothing in the way CommitSync is. It does not need to be. A rename is far
// likelier to succeed than the write that preceded it — the disk space is
// already spent and the directory entry already exists — so the window in
// which only some of the files are visible is a few microseconds wide, and
// nothing has been recorded while it is open. A rename that does fail undoes
// the ones before it and discards the rest.
func commitFiles(results []ofxexport.AccountResult) ([]string, error) {
	var renamed []string
	for i, r := range results {
		if r.PendingPath == "" {
			continue
		}
		if err := os.Rename(r.PendingPath, r.FilePath); err != nil {
			removeAll(renamed)
			removeAll(pendingPaths(results[i:]))
			return nil, err
		}
		renamed = append(renamed, r.FilePath)
	}
	return renamed, nil
}

// pendingPaths names the written-but-not-yet-renamed files in a batch.
func pendingPaths(results []ofxexport.AccountResult) []string {
	var paths []string
	for _, r := range results {
		if r.PendingPath != "" {
			paths = append(paths, r.PendingPath)
		}
	}
	return paths
}

func relinkNeeded(env plaid.Environment, item plaid.Item) error {
	stderr("  needs re-authentication; nothing was fetched\n")
	stderr("  repair it in a browser, which consumes no Item:\n")
	stderr("    bankferry plaid-relink --env %s --item %s\n", env, item.ItemID)
	return errNeedsRelink
}

var errNeedsRelink = errRelink{}

type errRelink struct{}

func (errRelink) Error() string { return "item needs re-authentication" }

// reportRevisions surfaces transactions Plaid has changed or withdrawn.
//
// Removed fires both when a transaction is genuinely reversed and when a
// pending transaction posts under a new ID. Only the first matters here,
// because pending transactions are never exported.
func reportRevisions(result plaid.SyncResult) {
	if len(result.Modified) > 0 {
		stdout("  %d transaction(s) were modified upstream. If already imported,\n",
			len(result.Modified))
		stdout("  they will not be corrected in GnuCash automatically:\n")
		for _, txn := range result.Modified {
			if txn.Pending {
				continue
			}
			stdout("    %s  %s  %s  %s\n",
				txn.Date.String(), txn.Amount.String(), txn.ID, txn.Name)
		}
	}
	if len(result.Removed) > 0 {
		stdout("  %d transaction(s) were removed upstream (a reversal, or a pending\n",
			len(result.Removed))
		stdout("  transaction that posted under a new ID). Already-imported ones\n")
		stdout("  must be corrected by hand:\n")
		for _, txn := range result.Removed {
			stdout("    %s\n", txn.ID)
		}
	}
}

// withinDays partitions transactions into those dated on or after the cutoff
// (today minus days) and the count of older ones dropped. Pending status is
// irrelevant here; the exporter drops pending separately.
func withinDays(txns []plaid.Transaction, days int) (kept []plaid.Transaction, dropped int) {
	cutoff := civildate.FromTime(time.Now().AddDate(0, 0, -days))
	kept = make([]plaid.Transaction, 0, len(txns))
	for _, t := range txns {
		if t.Date.Compare(cutoff) >= 0 {
			kept = append(kept, t)
		} else {
			dropped++
		}
	}
	return kept, dropped
}

func removeAll(paths []string) {
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			log.Printf("cli: remove %s after a failed commit: %v", path, err)
		}
	}
}

func printTransactions(txns []source.Transaction) {
	for _, txn := range txns {
		// Quantity is fallible only on a value too large to render, which Plaid
		// data cannot produce; degrade to the stored digits rather than thread
		// an error through a preview printer.
		amt, err := txn.Amount.Quantity()
		if err != nil {
			amt = txn.Amount.String()
		}
		stdout("      %s  %10s  %s\n", txn.Date.String(), amt, txn.Description)
	}
}
