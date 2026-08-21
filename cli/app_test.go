package cli

import (
	"testing"

	"github.com/jeffbstewart/bankferry/plaid"
)

// These values name things that already exist outside this program: the
// Items in the operator's keyring, the credentials on their security key,
// the vault whose AAD binds its name. A change here is not a rename, it is a
// loss of access to all of them, so the shipped identity is pinned.
func TestBankferry_IdentityIsTheLegacyOne(t *testing.T) {
	a := Bankferry()
	if err := a.validate(); err != nil {
		t.Fatalf("Bankferry() does not validate: %v", err)
	}

	if a.Name != "bankferry" {
		t.Errorf("Name = %q", a.Name)
	}
	if a.KeyringService != "bankferry" {
		t.Errorf("KeyringService = %q; changing it strands every linked Item", a.KeyringService)
	}
	if a.RelyingParty != (plaid.RelyingParty{ID: "bankferry.invalid", Name: "bankferry"}) {
		t.Errorf("RelyingParty = %+v; changing it orphans every enrolled credential", a.RelyingParty)
	}
	if a.DefaultDBPath != "bankferry.db" {
		t.Errorf("DefaultDBPath = %q", a.DefaultDBPath)
	}
	if a.Link.ClientName != "bankferry" {
		t.Errorf("Link.ClientName = %q", a.Link.ClientName)
	}

	// Transactions only. Adding investments here would hide from Link every
	// bank that lacks them; that product belongs to brokerferry.
	if len(a.Link.Products) != 1 || a.Link.Products[0].String() != "transactions" {
		t.Errorf("Link.Products = %v, want [transactions]", a.Link.Products)
	}

	want := map[string]bool{"fetch": true, "learn": true, "map": true}
	for _, c := range a.Commands {
		if !want[c.Name] {
			t.Errorf("unexpected command %q", c.Name)
		}
		delete(want, c.Name)
	}
	for name := range want {
		t.Errorf("command %q missing", name)
	}
}

func TestApp_ValidateRejectsAnIncompleteIdentity(t *testing.T) {
	mutations := map[string]func(*App){
		"no name":             func(a *App) { a.Name = "" },
		"no keyring service":  func(a *App) { a.KeyringService = "" },
		"no database path":    func(a *App) { a.DefaultDBPath = "" },
		"no relying party":    func(a *App) { a.RelyingParty = plaid.RelyingParty{} },
		"no link products":    func(a *App) { a.Link.Products = nil },
		"no usage":            func(a *App) { a.Usage = nil },
		"no env usage":        func(a *App) { a.EnvUsage = nil },
		"no first-run usage":  func(a *App) { a.FirstRunUsage = nil },
		"nameless command":    func(a *App) { a.Commands = append(a.Commands, Command{Run: func([]string) {}}) },
		"command without Run": func(a *App) { a.Commands = append(a.Commands, Command{Name: "x"}) },
	}
	for name, mutate := range mutations {
		a := Bankferry()
		mutate(&a)
		if err := a.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
