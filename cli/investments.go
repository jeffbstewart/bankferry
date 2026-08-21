package cli

import (
	"context"
	"os"
	"os/signal"
	"time"

	"github.com/jeffbstewart/bankferry/civildate"
	"github.com/jeffbstewart/bankferry/plaid"
	"github.com/jeffbstewart/bankferry/snapshot"
)

// defaultInvestmentDays is the transactions window when --days is not given.
// Brokerage activity is rare, so the default is wide — Plaid serves up to
// two years — and what an institution actually returns is reported per Item
// rather than assumed.
const defaultInvestmentDays = 720

// runInvestments reads every Item's holdings and recent investment
// transactions and writes one snapshot for finance2.
//
// There is no cursor and nothing is committed: holdings are levels, the
// snapshot is the full state at fetch time, and the next run writes another.
// That makes the ordering far less delicate than fetch's — a crash loses
// nothing Plaid will not re-serve — so the only file discipline that carries
// over is the one that protects what is already on disk: a snapshot is never
// written over.
func runInvestments(args []string) {
	fs := newFlags("investments")
	envStr := envFlag(fs)
	daysFlag := fs.Int("days", defaultInvestmentDays, "include investment transactions from the last N days")
	jsonFlag := fs.Bool("json", false, "also write a readable JSON rendering beside the binary snapshot")
	parseFlags(fs, args)
	env := requireEnv(*envStr)

	days := *daysFlag
	if days <= 0 {
		stderr("Error: --days must be positive, got %d.\n", days)
		os.Exit(1)
	}

	outputDir := os.Getenv("INVESTMENTS_OUTPUT_DIR")
	if outputDir == "" {
		stderr("Error: INVESTMENTS_OUTPUT_DIR is not set.\n")
		stderr("Set it in .env to the directory for snapshot files.\n")
		os.Exit(1)
	}
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
		stderr("Run '%s plaid-link --env %s' first.\n", prog(), env)
		os.Exit(1)
	}

	// One decryption — one touch on production — for the whole run.
	client, err := plaid.NewDataClient(env, plaidCredentials(env))
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	today := civildate.Today()
	start := civildate.FromTime(time.Now().AddDate(0, 0, -days))

	var fetched []snapshot.ItemData
	failed, skipped := 0, 0
	for _, item := range items {
		data, err := fetchInvestments(ctx, client, env, item, start, today)
		if err != nil {
			stderr("  %s: %v\n\n", itemLabel(item), err)
			failed++
			continue
		}
		if data == nil {
			skipped++
			continue
		}
		fetched = append(fetched, *data)
	}

	if len(fetched) == 0 {
		stderr("Nothing to write: no institution returned investment data.\n")
		os.Exit(1)
	}

	snap, err := snapshot.Build(today, fetched)
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}

	// No DRY_RUN here, unlike fetch. fetch's dry run guards an irreversible
	// side effect: a real run advances a cursor Plaid never rewinds. This
	// verb's only side effect is a new file in a directory the operator
	// owns, a dry run would cost the same touch and the same calls, and the
	// shared .env would couple the switch to bankferry's cursor.
	writer := snapshot.Writer{
		Dir:        outputDir,
		JSON:       *jsonFlag,
		CreateFile: createExclusive,
		Exists:     pathExists,
	}
	res, err := writer.Write(snap, time.Now())
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}
	stdout("Wrote %s\n", res.Binary)
	if res.JSON != "" {
		stdout("Wrote %s (readable rendering; finance2 imports the .pb)\n", res.JSON)
	}
	stdout("Upload it through finance2's Imports screen.\n")

	if skipped > 0 {
		stdout("%d institution(s) skipped; see above.\n", skipped)
	}
	if failed > 0 {
		os.Exit(1)
	}
}

// fetchInvestments reads one Item. It returns nil, nil when the Item cannot
// serve the product at all, after saying so: that is a property of the
// login, not a failure of the run, and the rest of the snapshot should not
// be held hostage to it.
func fetchInvestments(
	ctx context.Context,
	client *plaid.DataClient,
	env plaid.Environment,
	item plaid.Item,
	start, end civildate.ISO8601Date,
) (*snapshot.ItemData, error) {
	stdout("%s\n", itemLabel(item))

	status, err := client.FetchItemStatus(ctx, item.AccessToken)
	if err != nil {
		if plaid.IsLinkRefreshRequired(err) {
			return nil, relinkNeeded(env, item)
		}
		return nil, err
	}
	if status.NeedsLinkRefresh() {
		return nil, relinkNeeded(env, item)
	}

	holdings, err := client.FetchHoldings(ctx, item.AccessToken)
	if err != nil {
		switch {
		case plaid.IsLinkRefreshRequired(err):
			return nil, relinkNeeded(env, item)
		case plaid.IsInvestmentsUnavailable(err):
			stdout("  cannot serve the investments product; skipped (%v)\n\n", err)
			return nil, nil
		case plaid.IsProductNotReady(err):
			stdout("  Plaid is still preparing this Item's investments data; skipped. Retry later.\n\n")
			return nil, nil
		}
		return nil, err
	}

	txns, err := client.FetchInvestmentTransactions(ctx, item.AccessToken, start, end)
	if err != nil {
		if plaid.IsLinkRefreshRequired(err) {
			return nil, relinkNeeded(env, item)
		}
		return nil, err
	}

	for _, a := range holdings.Accounts {
		n := 0
		for _, h := range holdings.Holdings {
			if h.AccountID == a.AccountID {
				n++
			}
		}
		m := 0
		for _, t := range txns.Transactions {
			if t.AccountID == a.AccountID {
				m++
			}
		}
		stdout("  %s: %d holding(s), %d transaction(s) since %s\n", accountLabel(a.Name, a.Mask), n, m, start)
	}
	if len(holdings.Accounts) == 0 {
		stdout("  no investment accounts\n")
	}
	stdout("\n")

	return &snapshot.ItemData{Item: item, Holdings: holdings, Transactions: txns}, nil
}
