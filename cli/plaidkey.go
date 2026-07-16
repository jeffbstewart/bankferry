package cli

import (
	"errors"
	"log"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/jeffbstewart/touchvault"
	"github.com/jeffbstewart/touchvault/fido"

	"github.com/jeffbstewart/bankferry/db"
	"github.com/jeffbstewart/bankferry/plaid"
)

// databasePath is the one place the DATABASE_PATH default lives.
func databasePath() string {
	if p := os.Getenv("DATABASE_PATH"); p != "" {
		return p
	}
	return "bankferry.db"
}

// openDB opens the database and returns it with a close function. The caller
// defers the close.
func openDB() (*db.DB, func()) {
	store, err := db.Open(databasePath())
	if err != nil {
		stderr("Error opening database: %v\n", err)
		os.Exit(1)
	}
	return store, func() {
		if cerr := store.Close(); cerr != nil {
			log.Printf("cli: close database: %v", cerr)
		}
	}
}

// requireSecurityKey opens the authenticator, or exits with a clear message.
// It refuses under test or an agent shell, which is the whole point.
func requireSecurityKey() touchvault.Authenticator {
	if !fido.Available() {
		stderr("Security keys are supported only on Windows in this build.\n")
		os.Exit(1)
	}
	auth, err := fido.New()
	if errors.Is(err, fido.ErrUnderAgent) {
		stderr("Refusing to use a security key from an AI coding agent's shell.\n")
		stderr("This must be run by a human at a terminal. (%v)\n", err)
		os.Exit(1)
	}
	if err != nil {
		stderr("Error opening the security key: %v\n", err)
		os.Exit(1)
	}
	return auth
}

// runPlaidEnrollKey puts the production API secret behind a security key.
//
// The slot is not the operator's to choose, and asking would only invite a
// wrong answer. The first key into an environment creates the vault, and
// touchvault always seats it in slot 0; a later key is a backup and goes into
// the lowest free slot. What the operator is actually deciding is "make this
// key work", so that is the whole command:
//
//	plaid-enroll-key --env production
//
// The first key prompts for the secret. A later one does not need it: it
// recovers the data key from a key already enrolled, then wraps it for the new
// one.
func runPlaidEnrollKey(args []string) {
	fs := newFlags("plaid-enroll-key")
	envStr := envFlag(fs)
	parseFlags(fs, args)

	env := requireEnv(*envStr)

	store, closeDB := openDB()
	defer closeDB()

	vault, found, err := plaid.LoadVault(store, env)
	if err != nil {
		stderr("Error reading the enrolled keys: %v\n", err)
		os.Exit(1)
	}

	// Refuse a third key before spending a gesture on it.
	if found && len(vault.Slots()) >= plaid.KeySlots {
		stderr("Error: %v\n", plaid.ErrKeySlotsFull)
		printSlots(vault)
		os.Exit(1)
	}

	auth := requireSecurityKey()

	var sealed []byte
	var slot int
	if !found {
		slot = plaid.FirstKeySlot
		sealed = enrollFirstKeyFlow(env, auth)
	} else {
		sealed, slot = addKeyFlow(vault, env, auth)
	}

	if err := plaid.SaveVault(store, env, sealed); err != nil {
		stderr("Error saving the enrolled key: %v\n", err)
		os.Exit(1)
	}

	stdout("\nEnrolled a security key in %s slot %d (%s).\n", env, slot, plaid.SlotLabel(slot))

	updated, found, err := plaid.LoadVault(store, env)
	if err != nil || !found {
		stderr("Error re-reading the enrolled keys: found=%v err=%v\n", found, err)
		os.Exit(1)
	}
	printSlots(updated)

	if len(updated.Slots()) < plaid.KeySlots {
		stdout("\nOnly one key is enrolled. Enroll a backup so a lost key is not a\n")
		stdout("lockout:\n  bankferry plaid-enroll-key --env %s\n", env)
	}
}

func enrollFirstKeyFlow(env plaid.Environment, auth touchvault.Authenticator) []byte {
	stdout("No security key is enrolled for %s yet. This will seal the API\n", env)
	stdout("secret so that reading it needs a touch on this key.\n\n")
	stdout("The secret is read from you now and never written to disk in the\n")
	stdout("clear. You can re-read it any time from the Plaid Dashboard.\n\n")

	secret, err := promptSecret("Plaid " + string(env) + " API secret: ")
	if errors.Is(err, ErrPromptCancelled) {
		stderr("Cancelled. Nothing was enrolled.\n")
		os.Exit(1)
	}
	if err != nil {
		stderr("Error reading the secret: %v\n", err)
		os.Exit(1)
	}
	if secret == "" {
		stderr("Error: the secret was empty. Nothing was enrolled.\n")
		os.Exit(1)
	}

	stdout("\nTouch the security key when it blinks. Three times: to create the\n")
	stdout("credential, to bind the secret to it, and to prove the key's\n")
	stdout("derivation depends on the whole salt.\n")

	sealed, err := plaid.CreateVault(env, secret, auth)
	if err != nil {
		stderr("\nError enrolling the key: %v\n", err)
		os.Exit(1)
	}
	return sealed
}

func addKeyFlow(vault touchvault.Vault, env plaid.Environment, auth touchvault.Authenticator) ([]byte, int) {
	stdout("A security key is already enrolled for %s. Adding a backup.\n\n", env)
	stdout("First, touch a key that is ALREADY enrolled, to recover the sealing.\n")
	stdout("Then swap to the NEW key and touch it three times.\n\n")

	sealed, slot, err := plaid.AddKey(vault, auth)
	if err != nil {
		stderr("\nError adding the key: %v\n", err)
		os.Exit(1)
	}
	return sealed, slot
}

// runPlaidListKeySlots shows which slots are enrolled. No gesture: the slots
// are authenticated metadata, readable without unlocking anything.
func runPlaidListKeySlots(args []string) {
	fs := newFlags("plaid-list-key-slots")
	envStr := envFlag(fs)
	parseFlags(fs, args)
	env := requireEnv(*envStr)

	store, closeDB := openDB()
	defer closeDB()

	vault, found, err := plaid.LoadVault(store, env)
	if err != nil {
		stderr("Error reading the enrolled keys: %v\n", err)
		os.Exit(1)
	}
	if !found {
		stdout("No security key is enrolled for %s.\n", env)
		stdout("Enroll one:\n  bankferry plaid-enroll-key --env %s\n", env)
		return
	}

	stdout("Security keys enrolled for %s:\n", env)
	printSlots(vault)
}

func printSlots(v touchvault.Vault) {
	for _, s := range v.Slots() {
		stdout("  slot %d  %-8s  credential %s...\n", s.Slot, s.Label, s.CredentialIDHex)
	}
}

// runPlaidDeleteKeySlot removes one enrolled key.
//
// It costs one touch, on a key that is still enrolled. --force is a different
// act entirely: it destroys the vault, because the only reason to remove the
// last key is that it is gone, and a vault that demanded a touch to forget
// could never be forgotten.
func runPlaidDeleteKeySlot(args []string) {
	fs := newFlags("plaid-delete-key-slot")
	envStr := envFlag(fs)
	slotFlag := fs.Int("key-slot", -1, "which key slot to delete, as shown by plaid-list-key-slots")
	forceFlag := fs.Bool("force", false, "destroy the vault outright, even if it is the last enrolled key")
	parseFlags(fs, args)

	env := requireEnv(*envStr)
	slot := *slotFlag
	force := *forceFlag

	// --force is not "remove that slot, harder". It destroys the whole vault,
	// so naming a slot alongside it describes an act that does not exist.
	// Refuse rather than guess: one reading of the command loses every key.
	if force && slot >= 0 {
		stderr("Error: --force destroys the whole vault, so it takes no --key-slot.\n")
		stderr("To remove one key and keep the other, drop --force.\n")
		os.Exit(1)
	}

	store, closeDB := openDB()
	defer closeDB()

	vault, found, err := plaid.LoadVault(store, env)

	// --force is deliberately not gated on the vault being readable. The reason
	// to destroy a vault is that it can no longer be used, and "the stored bytes
	// will not parse" is one of the ways that happens. A destroy path that
	// demanded a well-formed vault could not clear the very state it exists to
	// clear, and the operator's only recourse would be hand-editing SQLite.
	if force {
		destroyVaultFlow(store, env, vault, found, err)
		return
	}

	if err != nil {
		stderr("Error reading the enrolled keys: %v\n", err)
		stderr("If the vault is beyond repair, destroy it: plaid-delete-key-slot --env %s --force\n", env)
		os.Exit(1)
	}
	if !found {
		stderr("No security key is enrolled for %s.\n", env)
		os.Exit(1)
	}

	if slot < 0 {
		stderr("Error: --key-slot is required. The enrolled slots are:\n")
		printSlots(vault)
		os.Exit(1)
	}

	// Everything below costs the operator a touch, so establish that the removal
	// can actually succeed before asking for one. A gesture spent to be told
	// "no such slot" is a gesture that teaches the operator to touch the key
	// without reading the prompt.
	if !slotOccupied(vault, slot) {
		stderr("Error: no key is enrolled in slot %d. The enrolled slots are:\n", slot)
		printSlots(vault)
		os.Exit(1)
	}

	// Removing the last key would need a touch from the key being given up, so
	// touchvault refuses it. Say what to do instead, rather than spend a gesture
	// discovering it.
	if len(vault.Slots()) == 1 {
		stderr("Error: slot %d is the last enrolled key. Removing it makes %s\n", slot, env)
		stderr("unreachable until you re-enroll from the Plaid Dashboard.\n")
		stderr("Re-run with --force (and no --key-slot) if that is intended.\n")
		os.Exit(1)
	}

	stdout("Removing slot %d from %s. Touch a key that is still enrolled — the\n", slot, env)
	stdout("one you are keeping, not the one you are removing.\n\n")

	auth := requireSecurityKey()

	sealed, err := plaid.RemoveSlot(vault, auth, slot)
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}

	if err := plaid.SaveVault(store, env, sealed); err != nil {
		stderr("Error saving: %v\n", err)
		os.Exit(1)
	}

	stdout("\nRemoved slot %d.\n", slot)

	updated, found, err := plaid.LoadVault(store, env)
	if err != nil || !found {
		stderr("Error re-reading the enrolled keys: found=%v err=%v\n", found, err)
		os.Exit(1)
	}
	printSlots(updated)
}

// slotOccupied reports whether a key is enrolled in slot.
func slotOccupied(v touchvault.Vault, slot int) bool {
	for _, s := range v.Slots() {
		if s.Slot == slot {
			return true
		}
	}
	return false
}

// destroyPhrase is what an operator types to destroy a vault.
const destroyPhrase = "destroy the vault"

// destroyVaultFlow forgets the whole vault: every enrolled key, and the sealed
// secret with them.
//
// It costs no gesture, and it must not. The reason to be here is that the keys
// are gone, or that the stored bytes no longer parse — so it accepts an
// unreadable vault (found=false, or a load error) and destroys the row anyway.
// A vault that demanded a touch, or a well-formed document, in order to be
// forgotten could never be forgotten at all.
//
// What it does demand is a typed phrase. A touch cannot guard this act, so the
// only thing left to prove is intent, and this destroys more than the command's
// name suggests — including a key that still works.
func destroyVaultFlow(store plaid.WrappedKeyStore, env plaid.Environment, vault touchvault.Vault, found bool, loadErr error) {
	switch {
	case loadErr != nil:
		stdout("The %s vault cannot be read: %v\n", env, loadErr)
		stdout("Destroying it is the way out of that.\n\n")
	case !found:
		stdout("No security key is enrolled for %s; there is no vault to destroy.\n", env)
		return
	default:
		stdout("This destroys the %s vault entirely, including these keys:\n", env)
		printSlots(vault)
		stdout("\n")
	}

	stdout("The sealed secret becomes unreadable, and any key above stops working.\n")
	stdout("Nothing else is lost: re-read the secret from the Plaid Dashboard and\n")
	stdout("enroll again.\n\n")

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		stderr("Refusing to destroy a vault without an interactive confirmation.\n")
		os.Exit(1)
	}

	answer, err := promptLine("Type '" + destroyPhrase + "' to continue: ")
	if err != nil {
		stderr("Error: %v\n", err)
		os.Exit(1)
	}
	if strings.TrimSpace(strings.ToLower(answer)) != destroyPhrase {
		stderr("Aborted. Nothing was destroyed.\n")
		os.Exit(1)
	}

	if err := plaid.DestroyVault(store, env); err != nil {
		stderr("Error removing the enrolled keys: %v\n", err)
		os.Exit(1)
	}

	stdout("\nDestroyed. %s is unreachable until you enroll again:\n", env)
	stdout("  bankferry plaid-enroll-key --env %s\n", env)
}
