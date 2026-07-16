// Package cli implements the bankferry command-line interface,
// dispatching subcommands for payee learning and OFX transformation.
package cli

import (
	"errors"
	"flag"
	"os"

	"github.com/jeffbstewart/bankferry/plaid"
)

// newFlags builds the flag set for one subcommand. Unlike the old hand-rolled
// scanner, a flag.FlagSet rejects unknown flags — a misspelled flag is an
// error, not a silent no-op.
func newFlags(name string) *flag.FlagSet {
	return flag.NewFlagSet(name, flag.ContinueOnError)
}

// parseFlags parses the arguments following the subcommand name. On an unknown
// or malformed flag, flag has already written the message and usage to stderr,
// so this just exits; -h/-help exits cleanly.
func parseFlags(fs *flag.FlagSet, args []string) {
	var rest []string
	if len(args) > 2 {
		rest = args[2:]
	}
	switch err := fs.Parse(rest); {
	case err == nil:
	case errors.Is(err, flag.ErrHelp):
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

// envFlag registers the standard --env flag shared by most commands.
func envFlag(fs *flag.FlagSet) *string {
	return fs.String("env", "", "environment: sandbox or production")
}

// requireEnv validates a parsed --env value, exiting with guidance when it is
// missing or unrecognized.
func requireEnv(s string) plaid.Environment {
	if s == "" {
		stderr("Error: --env <environment> is required.\n")
		os.Exit(1)
	}
	env, err := plaid.ParseEnvironment(s)
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}
	return env
}

// apiKeyDecrypterOverride, when non-nil, replaces the environment-routed
// decrypter. Tests set it to a fake so they never reach hardware or the
// keyring. Production code leaves it nil and gets the real routing below.
//
// It is not a hook for weakening the production guard in a real build: it is
// nil there, and the routing sends production to the security key.
var apiKeyDecrypterOverride plaid.APIKeyDecrypter

// plaidCredentials decrypts the API secret exactly once for this command.
//
// Sandbox comes from the keyring with no ceremony. Production is unwrapped by
// a security key whose blob lives in the database, so this opens the database
// for the duration of the one decryption and closes it again — the touch
// happens while it is open. Nothing downstream decrypts again, because a
// second touch the operator cannot attribute to a decision is worse than none.
func plaidCredentials(env plaid.Environment) plaid.Credentials {
	if apiKeyDecrypterOverride != nil {
		return decryptOrExit(env, apiKeyDecrypterOverride)
	}
	if env == plaid.Production {
		store, closeDB := openDB()
		defer closeDB()
		return decryptOrExit(env, plaid.HardwareDecrypter{Store: store})
	}
	return decryptOrExit(env, plaid.KeyringDecrypter{})
}

func decryptOrExit(env plaid.Environment, dec plaid.APIKeyDecrypter) plaid.Credentials {
	creds, err := plaid.LoadCredentials(env, dec)
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}
	return creds
}

// Run parses command-line arguments and dispatches the appropriate
// subcommand. Pass os.Args as the args parameter.
func Run(args []string) {
	cmd := "help"
	if len(args) >= 2 {
		cmd = args[1]
	}

	// An access token that exists only in this machine's keyring is one disk
	// failure from being gone for good, and Plaid will not reissue it.
	warnStaleBackups(cmd)

	switch cmd {
	case "help":
		usage()
	case "plaid-init":
		runPlaidInit(args)
	case "plaid-link":
		runPlaidLink(args)
	case "plaid-items":
		runPlaidItems(args)
	case "plaid-relink":
		runPlaidRelink(args)
	case "plaid-reset-login":
		runPlaidResetLogin(args)
	case "plaid-remove":
		runPlaidRemove(args)
	case "plaid-enroll-key":
		runPlaidEnrollKey(args)
	case "plaid-list-key-slots":
		runPlaidListKeySlots(args)
	case "plaid-delete-key-slot":
		runPlaidDeleteKeySlot(args)
	case "plaid-export":
		runPlaidExport(args)
	case "plaid-verify-backup":
		runPlaidVerifyBackup(args)
	case "fetch":
		runFetch(args)
	case "learn":
		runLearn(args)
	case "map":
		runMap(args)
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	stderr("bankferry — pull bank transactions and prepare them for GnuCash\n\n")
	stderr("Usage: bankferry <command> [flags]\n\n")

	stderr("Plaid setup\n")
	stderr("  plaid-init --env sandbox\n")
	stderr("        Initialize the sandbox: store the Plaid client ID and the sandbox\n")
	stderr("        secret in the OS keyring. Prompts for both; the secret is masked.\n")
	stderr("        Get them from dashboard.plaid.com/developers/keys.\n")
	stderr("        The client ID is shared across environments, so this also supplies\n")
	stderr("        the client ID that production needs. Run this before enrolling a\n")
	stderr("        production key.\n")
	stderr("        Sandbox is free and served straight from the keyring; its secret is\n")
	stderr("        not worth protecting. The integration and re-auth regression tests\n")
	stderr("        run against these sandbox credentials, and sandbox items reach\n")
	stderr("        ITEM_LOGIN_REQUIRED thirty days after creation, so the linked items\n")
	stderr("        must be refreshed (plaid-relink) roughly monthly to keep them green.\n\n")
	stderr("  plaid-init --env production\n")
	stderr("        Store ONLY the production client ID in the keyring; the production\n")
	stderr("        secret is never kept here. To reach production, wrap the secret\n")
	stderr("        behind a security key with plaid-enroll-key, which requires a\n")
	stderr("        physical touch to read. Until a key is enrolled, production is\n")
	stderr("        unreachable by design, not merely guarded. The shared client ID is\n")
	stderr("        stored by whichever environment you init first.\n\n")

	stderr("  plaid-link --env <env> [--redirect-uri <https url>] [--bind <addr>]\n")
	stderr("        Link an institution. Prints an address to open in a browser, carrying\n")
	stderr("        a key good for that run only. Each institution consumes one Item, and\n")
	stderr("        a production account allows ten for its lifetime. Refuses to link an\n")
	stderr("        institution twice, since that creates a duplicate Item.\n")
	stderr("        Sandbox sign-in is user_good / pass_good.\n\n")
	stderr("        OAuth institutions, which include Chase, need --redirect-uri. It must\n")
	stderr("        be HTTPS unless it is loopback, and registered in the Plaid Dashboard\n")
	stderr("        under Allowed redirect URIs. When it is set, open the printed address\n")
	stderr("        at that origin, not at the bind address, or the bank's callback will\n")
	stderr("        arrive without a session.\n\n")
	stderr("        The server binds %s unless --bind says otherwise. Plaid never\n", plaid.DefaultBindAddr)
	stderr("        fetches the redirect URI; only your browser does, so it need not face\n")
	stderr("        the internet. A reverse proxy limited to your network is enough.\n\n")

	stderr("Plaid maintenance\n")
	stderr("  plaid-items --env <env>\n")
	stderr("        List linked institutions with their item IDs and health. Start here\n")
	stderr("        when you need an --item value, or to see what needs re-authentication.\n\n")

	stderr("  plaid-relink --env <env> [--item <id>]\n")
	stderr("        Re-authenticate an institution through Link update mode, in a browser.\n")
	stderr("        Repairs the connection in place: no Item is consumed and the stored\n")
	stderr("        access token does not change. Needed when an item reports\n")
	stderr("        ITEM_LOGIN_REQUIRED, which happens when consent lapses, a password\n")
	stderr("        changes, or the institution migrates. There is no unattended repair.\n")
	stderr("        --item may be omitted when only one institution is linked.\n\n")

	stderr("  plaid-reset-login --env sandbox [--item <id>]\n")
	stderr("        Force a sandbox item into ITEM_LOGIN_REQUIRED so plaid-relink can be\n")
	stderr("        exercised on demand. Sandbox items reach that state on their own\n")
	stderr("        thirty days after creation.\n\n")

	stderr("  plaid-remove --env <env> --item <id> [--force-forget]\n")
	stderr("        Remove an item at Plaid and delete its stored access token. This does\n")
	stderr("        NOT return the item's slot: a production account is allowed ten for\n")
	stderr("        its lifetime, spent or not. It does drop the authorization at the\n")
	stderr("        institution, which is why you would do it. In production it names its\n")
	stderr("        target and demands a typed confirmation. --force-forget deletes the\n")
	stderr("        token even when Plaid refuses; the token is printed first, because\n")
	stderr("        nothing else can ever recover it.\n\n")

	stderr("Production security key\n")
	stderr("  plaid-enroll-key --env production\n")
	stderr("        Seal the production API secret behind a FIDO2 security key. The first\n")
	stderr("        key prompts for the secret (never written to disk in the clear) and\n")
	stderr("        seals it; a second key needs no secret, only a touch on an\n")
	stderr("        already-enrolled key and then three on the new one. The slot is not\n")
	stderr("        yours to pick: the first key is the primary, the second the backup.\n")
	stderr("        Enroll two so a lost key is not a lockout. Reading production then\n")
	stderr("        costs one touch. Only genuine hardware can be enrolled: the key must\n")
	stderr("        attest to a trusted vendor root.\n\n")

	stderr("  plaid-list-key-slots --env production\n")
	stderr("        Show which key slots are enrolled. No touch, no secret revealed.\n\n")

	stderr("  plaid-delete-key-slot --env production --key-slot <n> | --force\n")
	stderr("        Forget one enrolled key. Costs one touch, on a key that is STILL\n")
	stderr("        enrolled — the one you are keeping, not the one you are removing, so\n")
	stderr("        a lost key can still be retired. It refuses to remove the last key,\n")
	stderr("        which would need a touch from the key you are giving up.\n")
	stderr("        --force is the other act: it destroys the whole vault, every key with\n")
	stderr("        it, and takes no --key-slot. No touch — the reason to be there is that\n")
	stderr("        the keys are gone, or the stored vault no longer parses — so it asks\n")
	stderr("        you to type a phrase instead. Production is then unreachable until you\n")
	stderr("        re-read the secret from the Plaid Dashboard and enroll again.\n\n")

	stderr("Backup\n")
	stderr("  plaid-export --env <env> --out <file>\n")
	stderr("        Write the linked items to a passphrase-encrypted file. An access\n")
	stderr("        token cannot be recovered: it lives only in this machine's keyring,\n")
	stderr("        Plaid never reissues it, and re-linking consumes another of the ten\n")
	stderr("        Items a production account gets for its lifetime. Losing the disk\n")
	stderr("        without this file means losing those Items permanently.\n\n")

	stderr("  plaid-verify-backup --env <env> --in <file>\n")
	stderr("        Restore dry run. Proves the file decrypts and that its contents match\n")
	stderr("        the keyring today. Flags any item that exists only in the keyring and\n")
	stderr("        would therefore be lost. Restores nothing.\n\n")

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

	stderr("  help\n")
	stderr("        Show this text.\n\n")

	stderr("Environment (.env)\n")
	stderr("  OFX_OUTPUT_DIR       fetch writes to unmapped/ beneath it; map writes mapped/\n")
	stderr("  DATABASE_PATH        SQLite database, default bankferry.db\n")
	stderr("  DRY_RUN              fetch writes nothing unless this is exactly \"false\"\n")
	stderr("  GNUCASH_FILE         Default for learn --gnucash\n")
	stderr("  PLAID_REDIRECT_URI   Default for --redirect-uri (note: URI, not URL)\n")
	stderr("  PLAID_BIND_ADDR      Default for --bind\n\n")

	stderr("Typical first run\n")
	stderr("  bankferry plaid-init --env sandbox\n")
	stderr("  bankferry plaid-link --env sandbox\n")
	stderr("  bankferry plaid-items --env sandbox\n")
	stderr("  bankferry learn --gnucash /path/to/finances.gnucash\n")
	stderr("  bankferry fetch --env sandbox\n")
	stderr("  bankferry map\n")
}
