package cli

import "github.com/jeffbstewart/bankferry/plaid"

// Bankferry is the identity of the bankferry binary: bank and credit card
// transactions, written as OFX for GnuCash.
//
// The keyring service, relying party and database path are the values this
// program has always used. They are not free to change: the service names
// the Items already in the operator's keyring, the relying party names the
// credentials already on their security key, and the secret's vault binds
// its own name.
func Bankferry() App {
	return App{
		Name:           "bankferry",
		Tagline:        "pull bank transactions and prepare them for GnuCash",
		KeyringService: "bankferry",
		RelyingParty:   plaid.RelyingParty{ID: "bankferry.invalid", Name: "bankferry"},
		Link: plaid.LinkIdentity{
			ClientName: "bankferry",
			// Transactions alone. Asking for investments too would hide every
			// bank that lacks them, and brokerferry exists for those.
			Products: mustProducts("transactions"),
		},
		DefaultDBPath: "bankferry.db",
		Commands: []Command{
			{Name: "fetch", Run: runFetch},
			{Name: "learn", Run: runLearn},
			{Name: "map", Run: runMap},
		},
		Usage:         bankferryUsage,
		EnvUsage:      bankferryEnvUsage,
		FirstRunUsage: bankferryFirstRunUsage,
	}
}

func bankferryUsage() {
	stderr("Fetching\n")
	stderr("  fetch --env <env> [--days <n>]\n")
	stderr("        Sync each linked institution and write one .ofx file per bank or\n")
	stderr("        credit card account into OFX_OUTPUT_DIR/unmapped/. Investment and loan\n")
	stderr("        accounts are skipped. Pending transactions are never exported. Files\n")
	stderr("        are written before the sync cursor advances, so an interrupted run\n")
	stderr("        simply repeats itself. DRY_RUN=false in .env enables writing.\n")
	stderr("        --days emits only transactions within the last n days. It bounds\n")
	stderr("        output, not the sync: in a real run the cursor still advances past\n")
	stderr("        the held-back older transactions, so they are not re-delivered.\n\n")

	stderr("GnuCash\n")
	stderr("  learn --gnucash <path>\n")
	stderr("        Read a GnuCash file and extract every distinct transaction description\n")
	stderr("        as a payee. Never writes to the file. Idempotent; re-run as it grows.\n")
	stderr("        Falls back to GNUCASH_FILE.\n\n")

	stderr("  learn --reset --gnucash <path>\n")
	stderr("        Purge every payee and rule, then re-learn. The clean-slate rebuild for\n")
	stderr("        the payee model; auto-learned rules regenerate, hand-made ones do not.\n\n")

	stderr("  map\n")
	stderr("        Rewrite the .ofx files in OFX_OUTPUT_DIR/unmapped/ using the learned\n")
	stderr("        payee names, prompting for anything unmatched (raw and merchant names\n")
	stderr("        shown side by side). Output goes to mapped/; import from there, never\n")
	stderr("        from unmapped/.\n\n")
}

func bankferryEnvUsage() {
	stderr("  OFX_OUTPUT_DIR       fetch writes to unmapped/ beneath it; map writes mapped/\n")
	stderr("  DATABASE_PATH        SQLite database, default bankferry.db\n")
	stderr("  DRY_RUN              fetch writes nothing unless this is exactly \"false\"\n")
	stderr("  GNUCASH_FILE         Default for learn --gnucash\n")
}

func bankferryFirstRunUsage() {
	stderr("Typical first run\n")
	stderr("  bankferry plaid-init --env sandbox\n")
	stderr("  bankferry plaid-link --env sandbox\n")
	stderr("  bankferry plaid-items --env sandbox\n")
	stderr("  bankferry learn --gnucash /path/to/finances.gnucash\n")
	stderr("  bankferry fetch --env sandbox\n")
	stderr("  bankferry map\n")
}
