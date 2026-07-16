package cli

import (
	"log"
	"os"

	"github.com/jeffbstewart/bankferry/db"
	"github.com/jeffbstewart/bankferry/gnucash"
)

func runLearn(args []string) {
	fs := newFlags("learn")
	gnucashFlag := fs.String("gnucash", "", "path to the GnuCash file, or GNUCASH_FILE")
	resetFlag := fs.Bool("reset", false, "purge all payees and rules before learning")
	parseFlags(fs, args)

	gnucashPath := *gnucashFlag
	if gnucashPath == "" {
		gnucashPath = os.Getenv("GNUCASH_FILE")
	}
	if gnucashPath == "" {
		stderr("Error: GnuCash file path required.\n")
		stderr("Usage: bankferry learn --gnucash /path/to/file.gnucash\n")
		stderr("Or set GNUCASH_FILE in .env\n")
		os.Exit(1)
	}

	dbPath := os.Getenv("DATABASE_PATH")
	if dbPath == "" {
		dbPath = "bankferry.db"
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

	// --reset is the clean-slate purge for the payee rework: drop every learned
	// and hand-created rule, then rebuild from the GnuCash file below. Priority-0
	// rules regenerate identically; hand curations are lost on purpose, to be
	// rebuilt through map review with merchant names now visible.
	if *resetFlag {
		rules, payees, rerr := store.ResetPayeeData()
		if rerr != nil {
			stderr("Error resetting payee data: %v\n", rerr)
			os.Exit(1)
		}
		stdout("Reset: removed %d rule(s) and %d payee(s).\n", rules, payees)
	}

	stdout("Parsing %s...\n", gnucashPath)
	gf, err := gnucash.Parse(gnucashPath)
	if err != nil {
		stderr("Error parsing GnuCash file: %v\n", err)
		os.Exit(1)
	}

	payeeCount := 0
	ruleCount := 0
	for _, description := range gf.Payees {
		payeeID, err := store.UpsertPayee(description)
		if err != nil {
			stderr("Error storing payee %q: %v\n", description, err)
			continue
		}
		payeeCount++

		// Create a rule using the exact GnuCash description as the match
		// pattern, keyed on the raw descriptor. Priority 0 = auto-learned.
		if err := store.InsertRuleIfNew(payeeID, description, db.MatchRaw, 0); err != nil {
			stderr("Error storing rule for %q: %v\n", description, err)
			continue
		}
		ruleCount++
	}

	stdout("Learned %d payees with %d rules.\n", payeeCount, ruleCount)
}
