package plaid

import (
	"errors"
	"fmt"

	"github.com/jeffbstewart/touchvault/fido"
)

// ErrProductionKeyLocked is returned when the production API secret is
// requested but no decrypter can prove a human is present.
var ErrProductionKeyLocked = errors.New(
	"plaid: the production API secret is locked and requires a present human")

// The automated-context refusals live in touchvault's fido provider, next to
// the only door to real hardware — fido.New, which calls them. Re-exported
// here so callers of this package need not import both.
var (
	// ErrDecrypterUnderTest is returned when a decrypter that would prompt
	// for a physical gesture is invoked from a test binary.
	ErrDecrypterUnderTest = fido.ErrUnderTest

	// ErrDecrypterUnderAgent is returned when such a decrypter is invoked
	// from a command started by an AI coding agent.
	ErrDecrypterUnderAgent = fido.ErrUnderAgent
)

// RefuseUnderTest reports an error when called from a test binary.
func RefuseUnderTest() error { return fido.RefuseUnderTest() }

// RefuseUnderAgent reports an error when a coding agent's markers are present.
// It is not the security boundary; see [fido.RefuseUnderAgent].
func RefuseUnderAgent() error { return fido.RefuseUnderAgent() }

// RefuseAutomatedContext is what a human-presence decrypter calls first, to
// fail early with a clear message. It is belt to fido.New's braces: that
// function refuses too, so hardware is unreachable even if a caller forgets
// this.
//
// Sandbox is deliberately not covered anywhere. It is free, its Items are
// replaceable, and an agent must be able to exercise it.
func RefuseAutomatedContext() error { return fido.RefuseAutomatedContext() }

// APIKeyDecrypter yields the Plaid API secret for one environment.
//
// The secret is the half of the credential pair that must be protected; the
// client ID is not sensitive and is read from the keyring directly.
//
// # Production requires human presence
//
// An implementation that returns a production secret MUST first obtain
// evidence that a person is physically at the machine — a security key
// touch, not a passphrase and not a terminal check. Everything reachable in
// this program is reachable by an automated agent running as the operator:
// it can edit code, delete guards, and answer prompts. It cannot touch a
// key.
//
// This is the only structural boundary in this program. A production Plaid
// account is allowed ten Items for its lifetime, removing one does not
// return its slot, and linking is irreversible. Without the secret, none of
// that can be spent.
//
// # Call it once
//
// DecryptAPIKey is expensive in human attention: it may block on a physical
// gesture. Call it once per command, at the composition root, and carry the
// resulting Credentials. Never call it lazily from a request handler — a
// touch prompt raised in the middle of an OAuth callback is a prompt the
// operator cannot connect to a decision.
//
// # Never under test, never under an agent
//
// An implementation that prompts for a gesture MUST return early from
// [RefuseAutomatedContext]. A key that blinks during `go test` teaches the
// operator to touch it without reading, which destroys the only thing the
// touch was ever worth. A key that blinks because an AI agent ran a
// production command is the accident this program exists to prevent.
//
// Neither refusal is the boundary — a test can be renamed, a variable can be
// unset. The touch is the boundary. These stop mistakes from reaching it.
type APIKeyDecrypter interface {
	// DecryptAPIKey returns the API secret for env, or an error if it
	// cannot be produced. For production it returns an error rather than a
	// secret whenever human presence was not established.
	DecryptAPIKey(env Environment) (string, error)
}

// KeyringDecrypter reads the API secret straight from the OS keyring.
//
// It serves sandbox without ceremony: sandbox credentials are fixed values
// that Plaid publishes, no Item is irreplaceable, and nothing there costs
// anything.
//
// It refuses production outright. Reading a production secret from the
// keyring would let any process running as the operator spend an
// irreplaceable Item, which is exactly the exposure the hardware decrypter
// exists to close. Until that lands, production is unreachable rather than
// weakly guarded.
type KeyringDecrypter struct{}

// DecryptAPIKey implements APIKeyDecrypter.
func (KeyringDecrypter) DecryptAPIKey(env Environment) (string, error) {
	if env == Production {
		return "", fmt.Errorf(
			"%w: this build has no hardware decrypter, so production cannot be used",
			ErrProductionKeyLocked)
	}

	secret, err := loadSecret(secretKey(env))
	if err != nil {
		return "", fmt.Errorf(
			"plaid: no %s secret in keyring (run 'plaid-init --env %s'): %w", env, env, err)
	}
	if len(secret) == 0 {
		return "", fmt.Errorf("plaid: the stored %s secret is empty", env)
	}
	return string(secret), nil
}
