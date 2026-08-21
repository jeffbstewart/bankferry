package cli

import "github.com/jeffbstewart/bankferry/plaid"

// Brokerferry is the identity of the brokerferry binary: brokerage holdings
// and investment transactions, written as an investments snapshot for
// finance2.
//
// It links under its own Plaid developer account, so its credentials,
// Items, vault and database are its own. The keyring service and relying
// party are new names with nothing behind them yet; once a key is enrolled
// or an Item linked they are as fixed as bankferry's.
func Brokerferry() App {
	return App{
		Name:           "brokerferry",
		Tagline:        "pull brokerage holdings and prepare them for finance2",
		KeyringService: "brokerferry",
		RelyingParty:   plaid.RelyingParty{ID: "brokerferry.invalid", Name: "brokerferry"},
		Link: plaid.LinkIdentity{
			ClientName: "brokerferry",
			// Investments alone: Link then offers only institutions that
			// serve it, which is the set this program can read.
			Products: mustProducts("investments"),
		},
		DefaultDBPath: "brokerferry.db",
		Commands: []Command{
			{Name: "investments", Run: runInvestments},
		},
		Usage:         brokerferryUsage,
		EnvUsage:      brokerferryEnvUsage,
		FirstRunUsage: brokerferryFirstRunUsage,
	}
}

func brokerferryUsage() {
	stderr("Fetching\n")
	stderr("  investments --env <env> [--days <n>] [--json]\n")
	stderr("        Read every linked institution's investment accounts — holdings as of\n")
	stderr("        now, and investment transactions from the last n days (default %d) —\n", defaultInvestmentDays)
	stderr("        and write one snapshot file into INVESTMENTS_OUTPUT_DIR for upload\n")
	stderr("        through finance2's Imports screen. Every number is the exact decimal\n")
	stderr("        Plaid sent. Nothing is recorded between runs: each snapshot is the\n")
	stderr("        full state, and a re-run simply writes another. DRY_RUN=false in .env\n")
	stderr("        enables writing. --json also writes a readable rendering beside the\n")
	stderr("        binary file; finance2 imports only the binary.\n")
	stderr("        An institution that cannot serve the product is reported and skipped;\n")
	stderr("        the snapshot still covers the rest.\n\n")
}

func brokerferryEnvUsage() {
	stderr("  INVESTMENTS_OUTPUT_DIR  where snapshots are written\n")
	stderr("  DATABASE_PATH           SQLite database, default brokerferry.db\n")
	stderr("  DRY_RUN                 investments writes nothing unless this is exactly \"false\"\n")
}

func brokerferryFirstRunUsage() {
	stderr("Typical first run\n")
	stderr("  brokerferry plaid-init --env sandbox\n")
	stderr("  brokerferry plaid-link --env sandbox\n")
	stderr("  brokerferry plaid-items --env sandbox\n")
	stderr("  brokerferry investments --env sandbox\n")
}
