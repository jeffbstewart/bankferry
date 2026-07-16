package cli

import (
	"context"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/jeffbstewart/bankferry/db"
	"github.com/jeffbstewart/bankferry/plaid"
)

// removalPhrase is what an operator types to destroy a production Item.
const removalPhrase = "remove an item forever"

// confirmProductionRemove stops an accidental production removal.
//
// Removing an Item does not return its slot: a Trial account is allowed ten
// Items for its lifetime, spent or not. So this is strictly a loss. It is
// still worth doing when an Item is an orphan or a duplicate, because it
// drops the authorization at the institution.
func confirmProductionRemove(env plaid.Environment, item plaid.Item) {
	if env != plaid.Production {
		return
	}

	stdout("\nThis will remove %s (item %s) at Plaid and delete its access\n",
		item.InstitutionName, item.ItemID)
	stdout("token from the keyring. Neither can be undone.\n\n")
	stdout("The Item's slot is NOT returned. This account is allowed ten Items\n")
	stdout("for its lifetime, and this one has already been counted.\n\n")

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		stderr("Refusing to remove a production Item without an interactive confirmation.\n")
		os.Exit(1)
	}

	answer, err := promptLine("Type '" + removalPhrase + "' to continue: ")
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}
	if strings.TrimSpace(strings.ToLower(answer)) != removalPhrase {
		stderr("Aborted. Nothing was removed.\n")
		os.Exit(1)
	}
	stdout("\n")
}

// runPlaidRemove removes an Item at Plaid and forgets its access token.
//
// The order is forced: Plaid's /item/remove needs the access token, and the
// keyring is the only place that token exists. Deleting the keyring entry
// first would strand the Item at Plaid, still authorized at the bank, with
// no way to ever remove it.
func runPlaidRemove(args []string) {
	fs := newFlags("plaid-remove")
	envStr := envFlag(fs)
	itemID := fs.String("item", "", "item ID to remove")
	forceForgetFlag := fs.Bool("force-forget", false, "delete the local token even if Plaid removal fails")
	parseFlags(fs, args)

	env := requireEnv(*envStr)
	forceForget := *forceForgetFlag

	// Production removal names its target. selectItem would silently pick the
	// only Item, which is the wrong default when the act is irreversible.
	if env == plaid.Production && *itemID == "" {
		stderr("Error: --item <item_id> is required to remove a production Item.\n")
		stderr("Run 'bankferry plaid-items --env production' to list them.\n")
		os.Exit(1)
	}
	item := selectItem(env, *itemID)

	confirmProductionRemove(env, item)

	client, err := plaid.NewClient(env, plaidCredentials(env))
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()

	removed := true
	if err := plaid.RemoveItem(ctx, client, item.AccessToken, item.ItemID); err != nil {
		removed = false
		if !forceForget {
			stderr("\nError removing the item at Plaid: %v\n\n", err)
			stderr("Nothing was deleted. The access token is still in the keyring,\n")
			stderr("so this can be retried. If the item is already gone at Plaid and\n")
			stderr("you want the token forgotten anyway, rerun with --force-forget.\n")
			os.Exit(1)
		}

		// The operator has chosen to forget a token whose Item may still
		// exist. That token is unrecoverable once deleted, so print it.
		stderr("\nWarning: Plaid did not remove the item: %v\n", err)
		stderr("Proceeding because --force-forget was given.\n\n")
		plaid.LogDoomedToken(item, "--force-forget after /item/remove failed")
	}

	if err := plaid.DeleteItem(env, item.ItemID); err != nil {
		stderr("\nError deleting the keyring entry: %v\n\n", err)
		if removed {
			stderr("The item WAS removed at Plaid. Its access token is now dead but\n")
			stderr("still stored. Delete keyring entry 'plaid-item-%s-%s' by hand.\n",
				env, item.ItemID)
		}
		os.Exit(1)
	}

	if removed {
		stdout("Removed %s (item %s) at Plaid and deleted its access token.\n",
			item.InstitutionName, item.ItemID)
	} else {
		stdout("Deleted the stored access token for %s (item %s).\n",
			item.InstitutionName, item.ItemID)
		stdout("The item may still exist at Plaid; it can no longer be removed.\n")
	}

	forgetSyncCursor(env, item.ItemID)

	stdout("\nThe stored items have changed, so any backup is now out of date:\n")
	stdout("  bankferry plaid-export --env %s --out <file>\n", env)
}

// forgetSyncCursor drops the removed Item's cursor. A cursor outlives nothing
// useful: re-linking the same institution produces a new item_id, so the row
// would never be read again. Failing to drop it is harmless, so it never
// stops the command.
func forgetSyncCursor(env plaid.Environment, itemID string) {
	dbPath := os.Getenv("DATABASE_PATH")
	if dbPath == "" {
		return
	}

	store, err := db.Open(dbPath)
	if err != nil {
		stderr("Warning: could not open the database to drop the sync cursor: %v\n", err)
		return
	}
	defer func() {
		if err := store.Close(); err != nil {
			stderr("Warning: closing the database: %v\n", err)
		}
	}()

	if err := store.ResetSyncCursor(string(env), itemID); err != nil {
		stderr("Warning: could not drop the sync cursor for %s: %v\n", itemID, err)
	}
}
