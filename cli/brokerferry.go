package cli

import "github.com/jeffbstewart/bankferry/plaid"

// Brokerferry is the identity of the brokerferry binary: brokerage holdings
// and investment transactions, written as an investments snapshot for
// finance2.
//
// Its keyring entries, Items, vault and database are its own; the Plaid
// account behind them is the same one bankferry uses, so the ten-Item cap
// is shared (see CLAUDE.md, "Two programs, one module"). The keyring
// service and relying party are as fixed as bankferry's now that a key is
// enrolled and Items are linked under them.
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
	stderr("        full state, and a re-run simply writes another. --json also writes a\n")
	stderr("        readable rendering beside the binary file; finance2 imports only the\n")
	stderr("        binary. DRY_RUN does not apply: there is no cursor to protect.\n")
	stderr("        An institution that cannot serve the product is reported and skipped;\n")
	stderr("        the snapshot still covers the rest.\n\n")
}

func brokerferryEnvUsage() {
	stderr("  INVESTMENTS_OUTPUT_DIR  where snapshots are written\n")
	stderr("  DATABASE_PATH           SQLite database, default brokerferry.db\n")
}

func brokerferryFirstRunUsage() {
	stderr("Typical first run\n")
	stderr("  brokerferry plaid-init --env sandbox\n")
	stderr("  brokerferry plaid-link --env sandbox\n")
	stderr("  brokerferry plaid-items --env sandbox\n")
	stderr("  brokerferry investments --env sandbox\n")
}
