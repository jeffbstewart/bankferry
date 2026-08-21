package cli

import (
	"errors"
	"fmt"

	"github.com/jeffbstewart/bankferry/plaid"
)

// App is the identity of one program built from this module.
//
// Two programs share every package here — bankferry, which pulls bank and
// credit card transactions for GnuCash, and brokerferry, which pulls
// brokerage holdings for finance2. They are separate binaries rather than
// modes of one so that their Plaid credentials, Items, security-key vaults
// and databases are kept apart by construction: each has its own keyring
// service, its own relying party, its own default database, and its own
// product set at link time. Nothing in a command needs to ask which program
// it is running in, and no flag can point one program at the other's Items.
type App struct {
	// Name is the binary's name, used in usage text and in the commands the
	// program tells the operator to run next.
	Name string

	// Tagline is the one-line description at the top of the help text.
	Tagline string

	// KeyringService is the OS-keyring service the program's secrets and
	// Items are stored under. It is the namespace that separates one
	// program's access tokens from another's; changing it strands every
	// Item the program has linked.
	KeyringService string

	// RelyingParty is the WebAuthn identity the production vault is
	// enrolled under. Changing it orphans every enrolled credential.
	RelyingParty plaid.RelyingParty

	// Link is what the program calls itself inside Plaid Link and the
	// products its Items are created with.
	Link plaid.LinkIdentity

	// DefaultDBPath is the SQLite path when DATABASE_PATH is unset.
	DefaultDBPath string

	// Commands are the program's own verbs, dispatched after the shared
	// Plaid lifecycle commands.
	Commands []Command

	// Usage prints the help text for Commands; EnvUsage the program's own
	// .env variables; FirstRunUsage the "typical first run" walkthrough.
	Usage         func()
	EnvUsage      func()
	FirstRunUsage func()
}

// Command is one program-specific verb.
type Command struct {
	Name string
	Run  func(args []string)
}

// app is the identity Run installed. Every command reads it; nothing else
// writes it.
var app App

// prog is the program's name, for messages that tell the operator what to
// run next.
func prog() string { return app.Name }

func (a App) validate() error {
	switch {
	case a.Name == "":
		return errors.New("cli: app has no name")
	case a.KeyringService == "":
		return fmt.Errorf("cli: %s has no keyring service", a.Name)
	case a.DefaultDBPath == "":
		return fmt.Errorf("cli: %s has no default database path", a.Name)
	case a.Usage == nil || a.EnvUsage == nil || a.FirstRunUsage == nil:
		return fmt.Errorf("cli: %s is missing usage text", a.Name)
	}
	if err := a.RelyingParty.Validate(); err != nil {
		return fmt.Errorf("cli: %s: %w", a.Name, err)
	}
	if err := a.Link.Validate(); err != nil {
		return fmt.Errorf("cli: %s: %w", a.Name, err)
	}
	for _, c := range a.Commands {
		if c.Name == "" || c.Run == nil {
			return fmt.Errorf("cli: %s has an incomplete command", a.Name)
		}
	}
	return nil
}

// mustProducts parses a product list that is fixed at compile time.
func mustProducts(names ...string) []plaid.Product {
	out := make([]plaid.Product, 0, len(names))
	for _, n := range names {
		p, err := plaid.ParseProduct(n)
		if err != nil {
			panic(err)
		}
		out = append(out, p)
	}
	return out
}
