package plaid

// The production API secret, held in a touchvault so that reading it requires
// a human to touch a security key.
//
// # What lives here and what does not
//
// The cryptography — the data key, the per-key wrapping, the salt-dependence
// gate, the attestation policy, the sealed format — is
// github.com/jeffbstewart/touchvault. It was extracted from this file, and
// nothing here reimplements any of it.
//
// What remains is the glue that is genuinely Plaid's:
//
//   - which vault an environment uses (one sealed vault per environment, in
//     the database) and under what name the secret sits inside it,
//   - the relying-party identity our credentials are scoped to,
//   - the slot policy: a primary and one backup,
//   - the routing that sends sandbox to the keyring and production to the key.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jeffbstewart/touchvault"
	"github.com/jeffbstewart/touchvault/fido"
)

// hardwareRPID scopes our credentials on the key.
//
// It is a reserved, permanently unresolvable domain (RFC 2606), which is
// correct for a relying party that is not a web origin. WebAuthn isolates by
// RP ID, so credentials created here cannot see, and cannot be seen by, the
// operator's Google or bank credentials on the same key.
//
// Changing this string orphans every enrolled credential. Do not.
const hardwareRPID = "bankferry.invalid"

const hardwareRPName = "bankferry"

// KeySlots is how many security keys may be enrolled per environment: a
// primary and one backup. More would be storage without a threat model; the
// point of two is that losing one is not a lockout.
//
// touchvault itself imposes no maximum — slots there are arbitrary
// non-negative integers. Two is this program's policy, enforced here.
const KeySlots = 2

var (
	// ErrNoWrappedKey means nothing has been enrolled for this environment.
	ErrNoWrappedKey = errors.New("plaid: no hardware-wrapped API key is enrolled")

	// ErrKeySlotsFull means both slots already hold a key. Removing one is a
	// deliberate act, not something an enrollment should do for you.
	ErrKeySlotsFull = fmt.Errorf(
		"plaid: both key slots are occupied; delete one first (plaid-delete-key-slot)")
)

// secretName is what the API secret is called inside the vault.
//
// The environment is part of the name, and that is load-bearing rather than
// decorative: touchvault binds a secret's name into its AAD, so a sandbox
// vault's ciphertext cannot be relabeled as production's. It restores, at the
// library's own boundary, the environment binding this file used to carry in
// its blob header.
func secretName(env Environment) string {
	return "plaid-" + string(env) + "-api-key"
}

// attestationRoots yields the trust anchors an enrolling key must chain to.
//
// It is a variable so tests can enroll a synthetic authenticator against a
// synthetic vendor root; no key that a test can conjure will ever chain to
// Yubico's. It is unexported and never reassigned outside a test binary, so a
// shipped build trusts exactly the bundled roots.
//
// This weakens nothing that matters. Attestation cannot be switched off here —
// touchvault always requires it, and this only chooses whom to trust. The
// property the attestation check exists to defend, that the secret binds to
// non-exportable hardware, is defended in the shipped binary by the value
// below; a test process that swaps it can only fool itself.
var attestationRoots = touchvault.BundledRoots

// vaultOptions is the enrollment-time policy for our vaults: our RP identity,
// and the attestation anchors a key must chain to.
func vaultOptions() touchvault.Options {
	return touchvault.Options{
		RPID:   hardwareRPID,
		RPName: hardwareRPName,
		Roots:  attestationRoots(),
		Label:  SlotLabel(FirstKeySlot),
	}
}

// FirstKeySlot is where touchvault.Create enrolls the first key. It is not a
// choice we make: Create always uses slot 0. Named so the CLI can speak of it.
const FirstKeySlot = 0

// SlotLabel names a slot for the operator.
func SlotLabel(slot int) string {
	switch slot {
	case 0:
		return "primary"
	case 1:
		return "backup"
	default:
		return fmt.Sprintf("slot-%d", slot)
	}
}

// --- storage -------------------------------------------------------------

// WrappedKeyStore persists one sealed vault per environment. *db.DB satisfies
// it.
//
// The sealed bytes are not in the OS keyring: they are ciphertext, inert
// without a touch, and the secret they protect is re-readable from the Plaid
// Dashboard. They belong with the other replaceable, machine-local Plaid
// state, and the keyring is reserved for the irreplaceable access tokens. See
// migration 008.
type WrappedKeyStore interface {
	SaveWrappedAPIKey(environment string, blob []byte) error
	LoadWrappedAPIKey(environment string) (blob []byte, found bool, err error)
	DeleteWrappedAPIKey(environment string) error
}

// SaveVault persists a vault's sealed bytes for an environment.
func SaveVault(store WrappedKeyStore, env Environment, sealed []byte) error {
	return store.SaveWrappedAPIKey(string(env), sealed)
}

// LoadVault opens the sealed vault for an environment. found is false when
// none is enrolled, which is distinct from a read error.
//
// It costs no gesture: opening reads only the authenticated metadata. The
// touch is spent at Unlock or Administer.
func LoadVault(store WrappedKeyStore, env Environment) (v touchvault.Vault, found bool, err error) {
	sealed, found, err := store.LoadWrappedAPIKey(string(env))
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}

	v, err = touchvault.OpenWith(sealed, vaultOptions())
	if err != nil {
		return nil, false, fmt.Errorf("plaid: the stored %s vault is unreadable: %w", env, err)
	}
	return v, true, nil
}

// DestroyVault removes an environment's sealed vault entirely.
//
// It costs no gesture, and it must not: the reason to reach for this is that
// every enrolled key is lost, and a vault that demanded a touch to forget
// would be a vault that could never be forgotten. Nothing is lost that the
// Plaid Dashboard cannot reissue — the secret is re-readable, and the sealed
// bytes without a key are inert either way.
func DestroyVault(store WrappedKeyStore, env Environment) error {
	return store.DeleteWrappedAPIKey(string(env))
}

// CreateVault seals secret into a new vault for env and enrolls auth as its
// first security key.
//
// It costs three gestures: create the credential, derive from it, and prove
// the derivation depends on the whole salt. The credential's attestation must
// chain to a trusted hardware root, so a software authenticator cannot be
// enrolled. The caller persists the returned bytes.
//
// There is no automated-context refusal here, and there must not be: it would
// make this function untestable while adding nothing. auth can only be real
// hardware if it came from fido.New, which refuses on its own.
func CreateVault(env Environment, secret string, auth touchvault.Authenticator) (sealed []byte, err error) {
	if secret == "" {
		return nil, errors.New("plaid: secret is empty")
	}

	admin, err := touchvault.Create(auth, vaultOptions())
	if err != nil {
		return nil, err
	}
	defer admin.Lock()

	if err := admin.Put(secretName(env), strings.NewReader(secret)); err != nil {
		return nil, err
	}
	return admin.Sealed()
}

// AddKey enrolls a backup security key into an existing vault and returns the
// new sealed bytes.
//
// It costs one gesture on a key already enrolled, to recover the data key,
// then three on the new key. The API secret is never needed again and never
// leaves the vault. The operator swaps keys between the first gesture and the
// second.
func AddKey(v touchvault.Vault, auth touchvault.Authenticator) (sealed []byte, slot int, err error) {
	if len(v.Slots()) >= KeySlots {
		return nil, 0, ErrKeySlotsFull
	}

	admin, err := v.Administer(auth)
	if err != nil {
		return nil, 0, err
	}
	defer admin.Lock()

	slot = touchvault.FreeSlot(admin.Slots())
	if err := admin.EnrollKey(auth, slot, SlotLabel(slot)); err != nil {
		return nil, 0, err
	}

	sealed, err = admin.Sealed()
	if err != nil {
		return nil, 0, err
	}
	return sealed, slot, nil
}

// RemoveSlot removes one enrolled key from a vault that has another, and
// returns the new sealed bytes.
//
// It costs one gesture, on a key that is still enrolled — not necessarily the
// one being removed. That is a change from the code this replaced, where the
// entry was ciphertext and deleting it needed nothing: touchvault requires
// presence to change what presence protects, and the trade is worth it.
// Retiring a key you no longer hold is still possible, by touching the one you
// do.
//
// It refuses to remove the last enrolled key, with [touchvault.ErrLastKey].
// That is not a lesser version of DestroyVault, it is the case DestroyVault
// exists for: the last key cannot be removed here because doing so would need
// a touch from the very key you are giving up.
func RemoveSlot(v touchvault.Vault, auth touchvault.Authenticator, slot int) (sealed []byte, err error) {
	admin, err := v.Administer(auth)
	if err != nil {
		return nil, err
	}
	defer admin.Lock()

	if err := admin.RemoveKey(slot); err != nil {
		return nil, err
	}
	return admin.Sealed()
}

// --- the decrypter -------------------------------------------------------

// HardwareDecrypter unwraps an API secret with a FIDO2 security key.
//
// This is the structural boundary described on [APIKeyDecrypter]. No process
// running as the operator can produce the touch it requires — not an
// attacker, not a coding agent, not this program run by mistake.
type HardwareDecrypter struct {
	// Store holds the sealed vault. Required.
	Store WrappedKeyStore

	// New opens the authenticator. Injectable so tests never reach hardware.
	// Defaults to fido.New.
	New func() (touchvault.Authenticator, error)
}

// DecryptAPIKey implements APIKeyDecrypter. It costs one gesture.
func (h HardwareDecrypter) DecryptAPIKey(env Environment) (string, error) {
	if err := RefuseAutomatedContext(); err != nil {
		return "", err
	}
	if h.Store == nil {
		return "", errors.New("plaid: HardwareDecrypter has no store")
	}

	v, found, err := LoadVault(h.Store, env)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("%w for %s (run 'plaid-enroll-key --env %s')", ErrNoWrappedKey, env, env)
	}

	newAuth := h.New
	if newAuth == nil {
		newAuth = fido.New
	}
	auth, err := newAuth()
	if err != nil {
		return "", err
	}

	sess, err := v.Unlock(auth)
	if err != nil {
		return "", err
	}
	defer sess.Lock()

	secret, err := touchvault.ReadString(sess, secretName(env))
	if err != nil {
		return "", fmt.Errorf("plaid: reading the %s API secret: %w", env, err)
	}
	return secret, nil
}

// EnvironmentDecrypter routes each environment to the decrypter it deserves.
//
// Sandbox is free and its Items are replaceable, so it is served from the
// keyring and an agent may exercise it. Production consumes irreplaceable
// Items, so it requires a human.
type EnvironmentDecrypter struct {
	Sandbox    APIKeyDecrypter
	Production APIKeyDecrypter
}

// DefaultDecrypter is what the CLI installs: sandbox from the keyring,
// production from a security key whose sealed vault lives in store.
func DefaultDecrypter(store WrappedKeyStore) EnvironmentDecrypter {
	return EnvironmentDecrypter{
		Sandbox:    KeyringDecrypter{},
		Production: HardwareDecrypter{Store: store},
	}
}

// DecryptAPIKey implements APIKeyDecrypter.
func (e EnvironmentDecrypter) DecryptAPIKey(env Environment) (string, error) {
	switch env {
	case Production:
		if e.Production == nil {
			return "", ErrProductionKeyLocked
		}
		return e.Production.DecryptAPIKey(env)
	case Sandbox:
		if e.Sandbox == nil {
			return "", errors.New("plaid: no sandbox decrypter")
		}
		return e.Sandbox.DecryptAPIKey(env)
	default:
		return "", fmt.Errorf("plaid: unsupported environment %q", env)
	}
}
