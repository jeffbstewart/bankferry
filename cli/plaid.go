package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/jeffbstewart/bankferry/plaid"
)

// linkOptions resolves the redirect URI and bind address, falling back to
// PLAID_REDIRECT_URI and PLAID_BIND_ADDR when the flags are empty. The
// redirect URI is validated here so a bad value fails before any Plaid call.
func linkOptions(uri, bind string) plaid.LinkOptions {
	if uri == "" {
		uri = os.Getenv("PLAID_REDIRECT_URI")
	}
	if err := plaid.ValidateRedirectURI(uri); err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}

	if bind == "" {
		bind = os.Getenv("PLAID_BIND_ADDR")
	}
	if bind != "" && !isLoopbackAddr(bind) {
		stderr("Note: binding to %s, which is reachable beyond this machine.\n", bind)
		stderr("      Restrict it at the reverse proxy. Plaid never fetches the redirect\n")
		stderr("      URI; only your browser does, so it need not face the internet.\n\n")
	}

	return plaid.LinkOptions{RedirectURI: uri, BindAddr: bind}
}

// redirectAndBindFlags registers the flags linkOptions consumes.
func redirectAndBindFlags(fs *flag.FlagSet) (uri, bind *string) {
	uri = fs.String("redirect-uri", "", "HTTPS redirect URI, or PLAID_REDIRECT_URI")
	bind = fs.String("bind", "", "server bind address, or PLAID_BIND_ADDR")
	return uri, bind
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false // ":8570" listens on every interface
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// resolveDuplicateOf validates --duplicate-of against the stored Items and
// returns it, or exits explaining why it cannot be honored.
//
// It resolves before anything is spent — before the browser opens, and before
// NewClient unseals the production secret and costs a touch. An operator who
// mistypes an item ID should lose a re-run, not a gesture, and certainly not
// an Item.
//
// The named Item only has to exist here. Whether it sits at the institution
// the operator eventually picks in the browser is not knowable yet, and is
// checked at the exchange, which is the moment that matters.
func resolveDuplicateOf(wanted string, items []plaid.Item, env plaid.Environment) string {
	if wanted == "" {
		return ""
	}
	for _, item := range items {
		if item.ItemID == wanted {
			return wanted
		}
	}

	stderr("Error: --duplicate-of %s names no Item linked in %s.\n", wanted, env)
	if len(items) == 0 {
		stderr("Nothing is linked here, so no institution can be duplicated.\n")
		os.Exit(1)
	}
	stderr("It must name the existing Item the new one will sit beside:\n")
	for _, item := range items {
		stderr("  %s  %s\n", item.ItemID, item.InstitutionName)
	}
	os.Exit(1)
	return ""
}

// confirmProductionLink stops an accidental production link.
//
// Linking is the one irreversible, billable act in this tool. A production
// account is allowed ten Items for its lifetime, removing one does not free
// its slot, and linking the same institution twice creates a duplicate
// rather than reusing the first.
//
// When --duplicate-of is in play the operator is deliberately creating that
// duplicate, so the prompt says what they are duplicating. A confirmation
// that describes the wrong act is worth nothing.
func confirmProductionLink(env plaid.Environment, duplicateOf string, items []plaid.Item) {
	if env != plaid.Production {
		return
	}

	stdout("\nThis will consume one of the ten Plaid Items allowed for the lifetime\n")
	stdout("of this account. Removing an Item does not return its slot.\n\n")

	if duplicateOf != "" {
		for _, item := range items {
			if item.ItemID != duplicateOf {
				continue
			}
			stdout("You are linking a second login at %s, alongside item %s.\n",
				item.InstitutionName, item.ItemID)
			stdout("If it is the same login as that Item, this spends a slot for a\n")
			stdout("duplicate of what you already have, and nothing can detect that\n")
			stdout("until after the slot is gone.\n\n")
			break
		}
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		stderr("Refusing to link a production Item without an interactive confirmation.\n")
		os.Exit(1)
	}

	answer, err := promptLine("Type 'consume an item' to continue: ")
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}
	if strings.TrimSpace(strings.ToLower(answer)) != "consume an item" {
		stderr("Aborted. Nothing was linked.\n")
		os.Exit(1)
	}
	stdout("\n")
}

// runPlaidLink starts the local Link server and blocks until one
// institution is linked.
func runPlaidLink(args []string) {
	fs := newFlags("plaid-link")
	envStr := envFlag(fs)
	uri, bind := redirectAndBindFlags(fs)
	dupOf := fs.String("duplicate-of", "",
		"item ID of an existing Item at the same institution, to link a second login beside it")
	parseFlags(fs, args)

	env := requireEnv(*envStr)
	opts := linkOptions(*uri, *bind)

	if env == plaid.Production && opts.RedirectURI == "" {
		stderr("Warning: no --redirect-uri and no PLAID_REDIRECT_URI.\n")
		stderr("         OAuth institutions, which include Chase, will not complete.\n")
		stderr("         The URI must be HTTPS and registered in the Plaid Dashboard\n")
		stderr("         under Allowed redirect URIs.\n\n")
	}

	items, broken, err := plaid.LoadItems(env)
	if err != nil {
		stderr("Error reading stored items: %v\n", err)
		os.Exit(1)
	}
	for _, b := range broken {
		stderr("Warning: keyring entry %s is unreadable: %v\n", b.Key, b.Err)
		stderr("         It was left untouched. It may hold a live access token.\n")
	}
	if len(items) > 0 {
		stdout("Already linked in %s:\n", env)
		for _, item := range items {
			stdout("  %s (item %s)\n", item.InstitutionName, item.ItemID)
		}
		stdout("\n")
	}

	// Resolve --duplicate-of here, before the browser and before the security
	// key: a typo in an item ID must cost a re-run, not a gesture.
	opts.DuplicateOfItemID = resolveDuplicateOf(*dupOf, items, env)

	// Confirm before touching the security key. Unsealing the production secret
	// in NewClient costs a physical gesture; prompting for the typed
	// confirmation only afterward would spend that gesture on a link the
	// operator then aborts, and would train touch-before-read — the habit the
	// gesture discipline exists to prevent. plaid-remove confirms first too.
	confirmProductionLink(env, opts.DuplicateOfItemID, items)

	client, err := plaid.NewClient(env, plaidCredentials(env))
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}

	// Ctrl+C aborts the wait without leaving the server running.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	result, err := plaid.StartLinkServer(ctx, env, client, opts)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			stderr("\nAborted. Nothing was linked.\n")
			os.Exit(1)
		}
		if errors.Is(err, plaid.ErrOAuthTimeout) {
			stderr("\nThe bank never returned the browser. Nothing was linked.\n")
			os.Exit(1)
		}
		stderr("Error: %v\n", err)
		os.Exit(1)
	}

	stdout("Linked %s (item %s).\n", result.InstitutionName, result.ItemID)
	stdout("\nThis item's access token now exists only in the OS keyring, and Plaid\n")
	stdout("will not reissue it. Any existing backup is now out of date. Re-export:\n")
	stdout("  bankferry plaid-export --env %s --out <file>\n", env)
}

// minPassphraseLen is a floor against a slip of the finger, not a password
// policy. The file's real defence is Argon2id at 64 MiB and three passes,
// which makes bulk guessing expensive whatever the length.
const minPassphraseLen = 8

// runPlaidExport writes the environment's Items to an encrypted file.
//
// A Plaid access token cannot be recovered: it lives only in this machine's
// keyring, Plaid never reissues it, and re-linking creates a duplicate Item
// that permanently consumes one of the ten a Trial account allows. This file
// is the only thing standing between a dead disk and that outcome.
func runPlaidExport(args []string) {
	fs := newFlags("plaid-export")
	envStr := envFlag(fs)
	outFlag := fs.String("out", "", "path to write the encrypted backup")
	parseFlags(fs, args)

	env := requireEnv(*envStr)
	out := *outFlag
	if out == "" {
		stderr("Error: --out <file> is required.\n")
		stderr("Usage: bankferry plaid-export --env %s --out backup.tapb\n", env)
		os.Exit(1)
	}

	if err := exportItems(env, out); err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}
}

// exportItems performs an export, prompting for the passphrase. It is shared
// by the command and by the reminder's offer to run it.
func exportItems(env plaid.Environment, out string) error {
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite a backup", out)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	items, broken, err := plaid.LoadItems(env)
	if err != nil {
		return fmt.Errorf("reading stored items: %w", err)
	}
	for _, b := range broken {
		stderr("Warning: keyring entry %s is unreadable and will NOT be backed up: %v\n", b.Key, b.Err)
	}
	if len(items) == 0 {
		return fmt.Errorf("no linked institutions in %s; nothing to export", env)
	}

	stdout("Exporting %d item(s) from %s.\n", len(items), env)
	stdout("The passphrase is the only thing protecting these access tokens.\n")
	stdout("It cannot be recovered. Store it where you store the file's backup.\n\n")

	passphrase, err := promptSecret("Passphrase: ")
	if err != nil {
		exitOnPromptError(err)
	}
	if len(passphrase) < minPassphraseLen {
		return fmt.Errorf("use at least %d characters; nothing was written", minPassphraseLen)
	}
	confirm, err := promptSecret("Confirm passphrase: ")
	if err != nil {
		exitOnPromptError(err)
	}
	if passphrase != confirm {
		return errors.New("the passphrases do not match; nothing was written")
	}

	blob, err := plaid.EncryptItems(env, items, []byte(passphrase))
	if err != nil {
		return err
	}

	if err := os.WriteFile(out, blob, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", out, err)
	}

	// Record what this backup covers, so the reminder knows when it goes
	// stale. A failure here is not fatal: the file on disk is good.
	if err := plaid.RecordBackup(env, out, items); err != nil {
		stderr("Warning: the backup was written but could not be recorded: %v\n", err)
		stderr("         You will be reminded to export again.\n")
	}

	stdout("\nWrote %s (%d bytes).\n", out, len(blob))
	stdout("Verify it now, before you rely on it:\n")
	stdout("  bankferry plaid-verify-backup --env %s --in %s\n", env, out)
	return nil
}

// warnStaleBackups runs before every command. An access token that exists
// only in this machine's keyring is one disk failure from being gone for
// good, and Plaid will not reissue it, so silence here is not neutral.
//
// It never fails the command. A keyring that cannot be read is skipped:
// nagging is not worth breaking a run over.
func warnStaleBackups(cmd string) {
	// The backup commands speak for themselves, and prompting inside an
	// export would recurse.
	switch cmd {
	case "plaid-export", "plaid-verify-backup", "help":
		return
	}

	for _, env := range plaid.AllEnvironments() {
		// Sandbox items are free and re-linkable, so a lost sandbox token
		// costs nothing. The backup nag is only worth it for production, whose
		// access tokens are irreplaceable.
		if env == plaid.Sandbox {
			continue
		}

		warning, err := plaid.CheckBackup(env)
		if err != nil || warning == nil {
			continue
		}

		stderr("\n--- backup reminder ---\n")
		stderr("%s\n", warning.Message())

		if offerExport(*warning) {
			continue
		}
		stderr("-----------------------\n\n")
	}
}

// offerExport asks whether to export now, but only at a terminal: a piped
// stdin belongs to the command being run, not to this prompt.
func offerExport(warning plaid.BackupWarning) bool {
	out := warning.SuggestedPath()
	if out == "" || !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}

	stderr("\n")
	answer, err := promptLine(fmt.Sprintf("Export now to %s? [y/N] ", out))
	if err != nil {
		return false
	}
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
		stderr("-----------------------\n\n")
		return false
	}

	if err := exportItems(warning.Environment, out); err != nil {
		stderr("Export failed: %v\n", err)
		stderr("-----------------------\n\n")
		return false
	}
	stderr("-----------------------\n\n")
	return true
}

// runPlaidVerifyBackup is the restore dry run. It proves two things: the
// file decrypts, and its contents match what is in the keyring today.
//
// It never restores. A real restore is only needed on a machine where the
// keyring is empty, and it can be written when that day comes. What must be
// known in advance is whether the file would work.
func runPlaidVerifyBackup(args []string) {
	fs := newFlags("plaid-verify-backup")
	envStr := envFlag(fs)
	inFlag := fs.String("in", "", "path to the encrypted backup to verify")
	parseFlags(fs, args)

	env := requireEnv(*envStr)
	in := *inFlag
	if in == "" {
		stderr("Error: --in <file> is required.\n")
		stderr("Usage: bankferry plaid-verify-backup --env %s --in backup.tapb\n", env)
		os.Exit(1)
	}

	blob, err := os.ReadFile(in)
	if err != nil {
		stderr("Error reading %s: %v\n", in, err)
		os.Exit(1)
	}

	// The environment is readable before any passphrase is offered, and it
	// is authenticated, so it cannot have been forged.
	fileEnv, err := plaid.BackupEnvironment(blob)
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}
	if fileEnv != env {
		stderr("Error: %s holds a %s backup, but --env is %s.\n", in, fileEnv, env)
		os.Exit(1)
	}

	passphrase, err := promptSecret("Passphrase: ")
	if err != nil {
		exitOnPromptError(err)
	}

	file, err := plaid.DecryptItems(blob, []byte(passphrase))
	if err != nil {
		stderr("\nError: %v\n", err)
		os.Exit(1)
	}

	stdout("\nDecrypted %s: %d item(s), exported %s.\n",
		in, len(file.Items), file.ExportedAt.Local().Format(time.RFC1123))

	items, broken, err := plaid.LoadItems(env)
	if err != nil {
		stderr("Error reading stored items: %v\n", err)
		os.Exit(1)
	}
	for _, b := range broken {
		stderr("Warning: keyring entry %s is unreadable: %v\n", b.Key, b.Err)
	}

	report := plaid.VerifyBackup(file.Items, items)
	stdout("\n")
	for _, v := range report {
		marker := " "
		if !v.Covered() {
			marker = "!"
		}
		stdout(" %s %-38s %-20s %s\n", marker, v.ItemID, v.InstitutionName, v.Status)
	}

	unprotected := plaid.UnprotectedItems(report)
	if len(unprotected) > 0 {
		stdout("\n%d item(s) exist only in the keyring. Their access tokens exist\n", len(unprotected))
		stdout("nowhere else, and Plaid will not reissue them. Re-export now:\n")
		stdout("  bankferry plaid-export --env %s --out <new file>\n", env)
	}

	if !plaid.BackupIsFaithful(report) {
		stdout("\nThis backup would NOT faithfully restore %s.\n", env)
		os.Exit(1)
	}

	stdout("\nThis backup decrypts, and every keyring item is present in it with a\n")
	stdout("matching access token. A restore from this file would be faithful.\n")
}

func exitOnPromptError(err error) {
	if errors.Is(err, ErrPromptCancelled) {
		stderr("Cancelled. Nothing was written.\n")
		os.Exit(1)
	}
	stderr("Error reading passphrase: %v\n", err)
	os.Exit(1)
}

// runPlaidItems lists the linked institutions and their health, which is
// where the operator learns the item IDs that plaid-relink and
// plaid-reset-login take.
func runPlaidItems(args []string) {
	fs := newFlags("plaid-items")
	envStr := envFlag(fs)
	parseFlags(fs, args)
	env := requireEnv(*envStr)

	items, broken, err := plaid.LoadItems(env)
	if err != nil {
		stderr("Error reading stored items: %v\n", err)
		os.Exit(1)
	}
	for _, b := range broken {
		stderr("Warning: keyring entry %s is unreadable: %v\n", b.Key, b.Err)
		stderr("         It was left untouched. It may hold a live access token.\n")
	}
	if len(items) == 0 {
		stdout("No linked institutions in %s.\n", env)
		stdout("Run 'bankferry plaid-link --env %s' to add one.\n", env)
		return
	}

	data, err := plaid.NewDataClient(env, plaidCredentials(env))
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	stdout("Linked institutions (%s):\n\n", env)
	needsRelink := 0

	for _, item := range items {
		status, err := data.FetchItemStatus(ctx, item.AccessToken)

		var health string
		switch {
		case err != nil && plaid.IsLinkRefreshRequired(err):
			health = "NEEDS RE-AUTHENTICATION"
			needsRelink++
		case err != nil:
			health = "unknown: " + err.Error()
		case status.NeedsLinkRefresh():
			health = "NEEDS RE-AUTHENTICATION (" + status.ErrorCode + ")"
			needsRelink++
		case status.ConsentExpiresAt != nil:
			health = "consent expires " + status.ConsentExpiresAt.Format(time.RFC3339)
			if status.ConsentExpiringWithin(plaid.ConsentWarningWindow, time.Now()) {
				health = "EXPIRING SOON: " + health
				needsRelink++
			}
		default:
			health = "ok"
		}

		stdout("  %s\n", item.InstitutionName)
		stdout("    item:   %s\n", item.ItemID)
		stdout("    status: %s\n", health)
		stdout("    oauth:  %s\n\n", describeOAuth(ctx, data, env, item.InstitutionID))
	}

	if needsRelink > 0 {
		stdout("Repair with: bankferry plaid-relink --env %s --item <item_id>\n", env)
		stdout("Update mode changes no token and consumes no Item.\n")
	}
}

// describeOAuth reports whether the institution behind an Item uses OAuth.
//
// Plaid's flag says the institution *has* an OAuth login flow, not that this
// particular Item was linked over it, so the wording stays honest. In sandbox
// the OAuth code path runs for real — the redirect, the state, the resume —
// but the pane shown is Plaid's generic Platypus one rather than the bank's.
func describeOAuth(ctx context.Context, data *plaid.DataClient, env plaid.Environment, institutionID string) string {
	if institutionID == "" {
		return "unknown: this item recorded no institution ID"
	}

	info, err := data.FetchInstitution(ctx, institutionID)
	if err != nil {
		return "unknown: " + err.Error()
	}
	if !info.OAuth {
		return "no; this institution has no OAuth flow"
	}
	if env == plaid.Sandbox {
		return "yes; sandbox runs the OAuth path with Plaid's generic pane, not the bank's"
	}
	return "yes; a redirect URI is required to link or re-authenticate it"
}

// selectItem resolves --item to a stored Item, defaulting to the only one
// when the environment has exactly one.
func selectItem(env plaid.Environment, wanted string) plaid.Item {
	items, broken, err := plaid.LoadItems(env)
	if err != nil {
		stderr("Error reading stored items: %v\n", err)
		os.Exit(1)
	}
	for _, b := range broken {
		stderr("Warning: keyring entry %s is unreadable: %v\n", b.Key, b.Err)
		stderr("         It was left untouched. It may hold a live access token.\n")
	}
	if len(items) == 0 {
		stderr("No linked institutions in %s.\n", env)
		stderr("Run 'bankferry plaid-link --env %s' first.\n", env)
		os.Exit(1)
	}

	if wanted == "" {
		if len(items) == 1 {
			return items[0]
		}
		stderr("Error: --item <item_id> is required; %d institutions are linked:\n", len(items))
		for _, item := range items {
			stderr("  %s  %s\n", item.ItemID, item.InstitutionName)
		}
		os.Exit(1)
	}

	for _, item := range items {
		if item.ItemID == wanted {
			return item
		}
	}
	stderr("Error: no item %q in %s.\n", wanted, env)
	os.Exit(1)
	return plaid.Item{}
}

// runPlaidRelink repairs an existing Item through Link update mode. It
// creates no Item, exchanges no token, and stores nothing: the access token
// is unchanged.
func runPlaidRelink(args []string) {
	fs := newFlags("plaid-relink")
	envStr := envFlag(fs)
	uri, bind := redirectAndBindFlags(fs)
	itemID := fs.String("item", "", "item ID to re-authenticate; optional when one is linked")
	parseFlags(fs, args)

	env := requireEnv(*envStr)
	opts := linkOptions(*uri, *bind)
	item := selectItem(env, *itemID)

	// Decrypted once. Both clients, and the server's own item check, are
	// built from this value.
	creds := plaidCredentials(env)

	client, err := plaid.NewClient(env, creds)
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	data, err := plaid.NewDataClient(env, creds)
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}

	status, err := data.FetchItemStatus(ctx, item.AccessToken)
	if err != nil {
		stderr("Error reading item status: %v\n", err)
		os.Exit(1)
	}
	if status.NeedsLinkRefresh() {
		stdout("%s needs re-authentication (%s).\n", item.InstitutionName, status.ErrorCode)
	} else {
		stdout("%s reports no error; re-authenticating anyway.\n", item.InstitutionName)
	}

	if err := plaid.StartRelinkServer(ctx, env, client, data, item, opts); err != nil {
		if errors.Is(err, context.Canceled) {
			stderr("\nAborted. Nothing changed.\n")
			os.Exit(1)
		}
		if errors.Is(err, plaid.ErrOAuthTimeout) {
			stderr("\nThe bank never returned the browser. Nothing changed.\n")
			os.Exit(1)
		}
		stderr("Error: %v\n", err)
		os.Exit(1)
	}

	stdout("Re-authenticated %s (item %s). The access token is unchanged.\n",
		item.InstitutionName, item.ItemID)
}

// runPlaidResetLogin forces a sandbox Item into ITEM_LOGIN_REQUIRED so the
// relink flow can be exercised on demand.
func runPlaidResetLogin(args []string) {
	fs := newFlags("plaid-reset-login")
	envStr := envFlag(fs)
	itemID := fs.String("item", "", "item ID to reset; optional when one is linked")
	parseFlags(fs, args)

	env := requireEnv(*envStr)
	item := selectItem(env, *itemID)

	client, err := plaid.NewClient(env, plaidCredentials(env))
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := plaid.SandboxResetLogin(ctx, env, client, item.AccessToken); err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}

	stdout("Item %s (%s) now requires re-authentication.\n", item.ItemID, item.InstitutionName)
	stdout("Repair it with: bankferry plaid-relink --env %s --item %s\n", env, item.ItemID)
}

// runPlaidInit prompts for the Plaid client ID and the secret for one
// environment, and stores both in the OS keyring. Values are read from
// stdin and never echoed, logged, or written to disk.
func runPlaidInit(args []string) {
	fs := newFlags("plaid-init")
	envStr := envFlag(fs)
	parseFlags(fs, args)
	env := requireEnv(*envStr)

	clientID, err := promptLine("Plaid client ID: ")
	if err != nil {
		stderr("Error reading client ID: %v\n", err)
		os.Exit(1)
	}
	if clientID == "" {
		stderr("Error: the client ID was empty. Nothing was stored.\n")
		os.Exit(1)
	}

	// Production never stores its secret in the keyring. The client ID is
	// shared across environments and is not a secret; the secret is captured
	// and wrapped behind a security key by plaid-enroll-key.
	if env == plaid.Production {
		if err := plaid.StoreClientID(clientID); err != nil {
			stderr("Error: %v\n", err)
			os.Exit(1)
		}
		stdout("Stored the Plaid client ID in the OS keyring.\n")
		stdout("\nProduction's secret is not stored here. Seal it behind a security key:\n")
		stdout("  bankferry plaid-enroll-key --env production\n")
		return
	}

	secret, err := promptSecret("Plaid " + string(env) + " secret: ")
	if errors.Is(err, ErrPromptCancelled) {
		stderr("Cancelled. Nothing was stored.\n")
		os.Exit(1)
	}
	if err != nil {
		stderr("Error reading secret: %v\n", err)
		os.Exit(1)
	}
	if secret == "" {
		stderr("Error: the secret was empty. Nothing was stored.\n")
		os.Exit(1)
	}

	if err := plaid.StoreCredentials(env, clientID, secret); err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}

	stdout("Stored Plaid client ID and %s secret in the OS keyring.\n", env)
}

// stdin is shared by every prompt. A fresh bufio reader per prompt would
// buffer ahead and swallow the input the next prompt is waiting for, so two
// prompts in a row could never both be answered from a pipe, and a fast
// paste could lose a line even at a terminal.
var stdin = bufio.NewReader(os.Stdin)

// readLine reads one line from the shared stdin reader.
func readLine() (string, error) {
	line, err := stdin.ReadString('\n')
	if err != nil {
		// A final line without a trailing newline is still a line.
		if errors.Is(err, io.EOF) && line != "" {
			return strings.TrimSpace(line), nil
		}
		if errors.Is(err, io.EOF) {
			return "", errors.New("no input")
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// promptLine reads a single visible line from stdin.
func promptLine(prompt string) (string, error) {
	stdout("%s", prompt)
	return readLine()
}

// Control characters handled while reading a masked secret.
const (
	keyCtrlC     = 3
	keyBackspace = 8
	keyCtrlU     = 21
	keyDelete    = 127
)

// ErrPromptCancelled is returned when the user interrupts a prompt.
var ErrPromptCancelled = errors.New("cancelled")

// promptSecret reads a line from stdin, echoing an asterisk per character
// so that a failed paste is distinguishable from an empty entry. When
// stdin is not a terminal (a pipe, or a test), it falls back to a plain
// read with no echo.
func promptSecret(prompt string) (string, error) {
	stdout("%s", prompt)

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return readLine()
	}

	// Raw mode delivers each keystroke unbuffered and suppresses the
	// terminal's own echo, so we can print the mask ourselves.
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer func() {
		if rerr := term.Restore(fd, oldState); rerr != nil {
			log.Printf("cli: restore terminal: %v", rerr)
		}
	}()

	// In raw mode the terminal no longer translates \n into \r\n.
	newline := func() { stdout("\r\n") }

	var secret []rune

	for {
		r, _, err := stdin.ReadRune()
		if err != nil {
			newline()
			return "", err
		}

		switch r {
		case '\r', '\n':
			newline()
			return strings.TrimSpace(string(secret)), nil

		case keyCtrlC:
			newline()
			return "", ErrPromptCancelled

		case keyBackspace, keyDelete:
			if len(secret) > 0 {
				secret = secret[:len(secret)-1]
				stdout("\b \b")
			}

		case keyCtrlU:
			for range secret {
				stdout("\b \b")
			}
			secret = secret[:0]

		default:
			// Ignore any other control character; echo the rest.
			if r < ' ' {
				continue
			}
			secret = append(secret, r)
			stdout("*")
		}
	}
}
