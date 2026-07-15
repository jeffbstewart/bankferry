package plaid

// What is tested here, and what is not.
//
// The cryptography moved to github.com/jeffbstewart/touchvault, and it is
// tested there: the AAD coverage, the format pinning, the entropy gate, the
// user-verification mismatch, the resident-key refusal. Repeating those here
// would test the dependency, not this program.
//
// What is still ours is the glue, and it is all load-bearing: one vault per
// environment, the secret named so that the environment is bound into its AAD,
// a policy of two key slots, and the routing that sends sandbox to the keyring
// and production to the security key. Those are what these tests hold.

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeffbstewart/touchvault"
	"github.com/jeffbstewart/touchvault/fido"
)

const testSecret = "plaid_production_secret_abc123"

// fakeBlobStore is an in-memory WrappedKeyStore. It underlines that storage is
// just bytes keyed by a string: no schema, no crypto, nothing plaid-specific.
type fakeBlobStore struct {
	blobs map[string][]byte
}

func newFakeBlobStore() *fakeBlobStore { return &fakeBlobStore{blobs: map[string][]byte{}} }

func (s *fakeBlobStore) SaveWrappedAPIKey(env string, blob []byte) error {
	s.blobs[env] = append([]byte(nil), blob...)
	return nil
}

func (s *fakeBlobStore) LoadWrappedAPIKey(env string) ([]byte, bool, error) {
	b, ok := s.blobs[env]
	return b, ok, nil
}

func (s *fakeBlobStore) DeleteWrappedAPIKey(env string) error {
	delete(s.blobs, env)
	return nil
}

// enrollOne creates a vault sealed under one fake key, with the synthetic
// vendor trusted for the duration of the test.
func enrollOne(t *testing.T, env Environment) (*fakeAuth, []byte) {
	t.Helper()

	auth, roots := newFakeKey(t)
	useTestRoots(t, roots)

	sealed, err := CreateVault(env, testSecret, auth)
	if err != nil {
		t.Fatalf("CreateVault: %v", err)
	}
	return auth, sealed
}

// readSecret unlocks a vault and reads the environment's secret out of it.
func readSecret(t *testing.T, sealed []byte, env Environment, auth touchvault.Authenticator) (string, error) {
	t.Helper()

	sess, err := mustOpen(t, sealed).Unlock(auth)
	if err != nil {
		return "", err
	}
	defer sess.Lock()

	return touchvault.ReadString(sess, secretName(env))
}

// mustOpen opens sealed bytes that this test just produced, so a failure is a
// bug and not a condition worth branching on.
//
// It goes through OpenWith and this package's options, exactly as LoadVault
// does, and that is not incidental: a vault opened with plain touchvault.Open
// carries the BUNDLED trust anchors, so enrolling a backup into it would judge
// the new key against Yubico's roots no matter what this test installed. The
// production path has the same property, which is why LoadVault is the only
// way this package opens a vault.
func mustOpen(t *testing.T, sealed []byte) touchvault.Vault {
	t.Helper()

	v, err := touchvault.OpenWith(sealed, vaultOptions())
	if err != nil {
		t.Fatalf("touchvault.OpenWith: %v", err)
	}
	return v
}

// The credentials the fake mints, in enrollment order.
var (
	primaryCred = []byte("fake-credential-1")
	backupCred  = []byte("fake-credential-2")
)

// ---------------------------------------------------------------------------

func TestCreateVault_RoundTrip(t *testing.T) {
	auth, sealed := enrollOne(t, Production)

	got, err := readSecret(t, sealed, Production, auth)
	if err != nil {
		t.Fatalf("reading the secret back: %v", err)
	}
	if got != testSecret {
		t.Errorf("secret = %q, want %q", got, testSecret)
	}
}

// The sealed bytes sit in the database, which is an ordinary file. The secret
// must not be recoverable from them.
func TestCreateVault_SecretIsNotInTheSealedBytes(t *testing.T) {
	_, sealed := enrollOne(t, Production)

	if strings.Contains(string(sealed), testSecret) {
		t.Fatal("the API secret appears verbatim in the sealed vault")
	}
}

func TestCreateVault_RejectsEmptySecret(t *testing.T) {
	auth, roots := newFakeKey(t)
	useTestRoots(t, roots)

	if _, err := CreateVault(Production, "", auth); err == nil {
		t.Fatal("an empty secret was sealed without complaint")
	}
	if auth.enrollCalls != 0 {
		t.Errorf("the operator was asked for %d gestures to seal nothing", auth.enrollCalls)
	}
}

// The first key always lands in slot 0. touchvault decides that, not us, which
// is why the CLI tells the operator the slot rather than asking for one.
func TestCreateVault_FirstKeyTakesTheFirstSlot(t *testing.T) {
	_, sealed := enrollOne(t, Production)

	slots := mustOpen(t, sealed).Slots()
	if len(slots) != 1 {
		t.Fatalf("len(slots) = %d, want 1", len(slots))
	}
	if slots[0].Slot != FirstKeySlot {
		t.Errorf("first key is in slot %d, want %d", slots[0].Slot, FirstKeySlot)
	}
	if slots[0].Label != SlotLabel(FirstKeySlot) {
		t.Errorf("first key is labeled %q, want %q", slots[0].Label, SlotLabel(FirstKeySlot))
	}
}

// Enrollment must refuse an authenticator that cannot prove it is hardware.
// That refusal is what binds the secret to a key nobody can copy.
func TestCreateVault_RefusesUnattestedAuthenticator(t *testing.T) {
	auth, roots := newFakeKey(t)
	useTestRoots(t, roots)
	auth.pki = nil // a software authenticator attests to nothing

	if _, err := CreateVault(Production, testSecret, auth); !errors.Is(err, touchvault.ErrNoAttestation) {
		t.Fatalf("err = %v, want ErrNoAttestation", err)
	}
}

// The shipped policy trusts the bundled Yubico roots and nothing else. With
// attestationRoots left at its default, the synthetic vendor's key — as
// genuine as any test can make one, properly signed and chained — must still
// be refused. This is what every other test here suspends, so it is the one
// that must hold.
func TestCreateVault_DefaultTrustAnchorsRejectAnUnknownVendor(t *testing.T) {
	auth, _ := newFakeKey(t) // deliberately NOT installing the test roots

	if _, err := CreateVault(Production, testSecret, auth); !errors.Is(err, touchvault.ErrUntrustedAuthenticator) {
		t.Fatalf("err = %v, want ErrUntrustedAuthenticator", err)
	}
}

// --- the environment binding ----------------------------------------------

// The environment is part of the secret's name, and touchvault binds a name
// into that secret's AAD. So a sandbox vault, even when filed under
// production's row in the database, cannot yield a production secret: the name
// production asks for is not in it.
//
// This replaces the environment field the old blob header carried, and it is
// stronger than what it replaces, because the decryption enforces it rather
// than an if statement.
func TestVault_SecretIsBoundToItsEnvironment(t *testing.T) {
	auth, sealed := enrollOne(t, Sandbox)

	// The sandbox vault, filed under production.
	store := newFakeBlobStore()
	if err := SaveVault(store, Production, sealed); err != nil {
		t.Fatal(err)
	}

	v, found, err := LoadVault(store, Production)
	if err != nil || !found {
		t.Fatalf("LoadVault: found=%v err=%v", found, err)
	}

	sess, err := v.Unlock(auth)
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	defer sess.Lock()

	if _, err := touchvault.ReadString(sess, secretName(Production)); !errors.Is(err, touchvault.ErrNoSuchSecret) {
		t.Fatalf("a sandbox vault yielded a production secret: err = %v", err)
	}
}

func TestSecretName_DistinguishesEnvironments(t *testing.T) {
	if secretName(Production) == secretName(Sandbox) {
		t.Fatal("production and sandbox secrets share a name; the AAD could not tell them apart")
	}
}

// --- the backup key -------------------------------------------------------

// Either enrolled key must open the vault on its own. That is the entire point
// of a backup: losing one key is not a lockout.
func TestAddKey_EitherKeyUnlocks(t *testing.T) {
	auth, sealed := enrollOne(t, Production)

	sealed, slot, err := AddKey(mustOpen(t, sealed), auth)
	if err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	if slot != 1 {
		t.Errorf("the backup went into slot %d, want 1", slot)
	}

	for _, tc := range []struct {
		name string
		cred []byte
	}{
		{"the primary alone", primaryCred},
		{"the backup alone", backupCred},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth.hold(tc.cred) // the operator holds only this key

			got, err := readSecret(t, sealed, Production, auth)
			if err != nil {
				t.Fatalf("unlocking with %s: %v", tc.name, err)
			}
			if got != testSecret {
				t.Errorf("secret = %q, want %q", got, testSecret)
			}
		})
	}
}

// Two slots is this program's policy. A third key is refused before it costs a
// gesture, rather than after.
func TestAddKey_RefusesAThirdKey(t *testing.T) {
	auth, sealed := enrollOne(t, Production)

	sealed, _, err := AddKey(mustOpen(t, sealed), auth)
	if err != nil {
		t.Fatalf("AddKey (backup): %v", err)
	}

	full := mustOpen(t, sealed)
	before := auth.enrollCalls

	if _, _, err := AddKey(full, auth); !errors.Is(err, ErrKeySlotsFull) {
		t.Fatalf("err = %v, want ErrKeySlotsFull", err)
	}
	if auth.enrollCalls != before {
		t.Error("the operator was asked to touch a key for an enrollment that was refused")
	}
}

// --- removing a key -------------------------------------------------------

func TestRemoveSlot_TheKeptKeyStillOpensIt(t *testing.T) {
	auth, sealed := enrollOne(t, Production)

	sealed, backupSlot, err := AddKey(mustOpen(t, sealed), auth)
	if err != nil {
		t.Fatalf("AddKey: %v", err)
	}

	// Retire the backup by touching the primary — the key being kept, not the
	// one being removed. An operator who has lost the backup can still do this.
	auth.hold(primaryCred)

	sealed, err = RemoveSlot(mustOpen(t, sealed), auth, backupSlot)
	if err != nil {
		t.Fatalf("RemoveSlot: %v", err)
	}

	if slots := mustOpen(t, sealed).Slots(); len(slots) != 1 || slots[0].Slot != FirstKeySlot {
		t.Fatalf("slots after removal = %v, want only slot %d", slots, FirstKeySlot)
	}

	got, err := readSecret(t, sealed, Production, auth)
	if err != nil {
		t.Fatalf("the kept key no longer opens the vault: %v", err)
	}
	if got != testSecret {
		t.Errorf("secret = %q, want %q", got, testSecret)
	}
}

// The removed key must stop working. Otherwise "retiring" a key would be
// theater.
func TestRemoveSlot_TheRemovedKeyNoLongerOpensIt(t *testing.T) {
	auth, sealed := enrollOne(t, Production)

	sealed, backupSlot, err := AddKey(mustOpen(t, sealed), auth)
	if err != nil {
		t.Fatalf("AddKey: %v", err)
	}

	auth.hold(primaryCred)
	sealed, err = RemoveSlot(mustOpen(t, sealed), auth, backupSlot)
	if err != nil {
		t.Fatalf("RemoveSlot: %v", err)
	}

	auth.hold(backupCred) // the retired key, and only it
	if _, err := readSecret(t, sealed, Production, auth); err == nil {
		t.Fatal("the removed key still opened the vault")
	}
}

// Removing the last key would strand the secret, and would need a touch from
// the very key being given up. It is refused; destroying the vault is the
// deliberate, gesture-free act instead.
func TestRemoveSlot_RefusesTheLastKey(t *testing.T) {
	auth, sealed := enrollOne(t, Production)

	if _, err := RemoveSlot(mustOpen(t, sealed), auth, FirstKeySlot); !errors.Is(err, touchvault.ErrLastKey) {
		t.Fatalf("err = %v, want ErrLastKey", err)
	}
}

func TestDestroyVault_ForgetsEverythingAndCostsNoGesture(t *testing.T) {
	auth, sealed := enrollOne(t, Production)

	store := newFakeBlobStore()
	if err := SaveVault(store, Production, sealed); err != nil {
		t.Fatal(err)
	}

	before := auth.deriveCalls
	if err := DestroyVault(store, Production); err != nil {
		t.Fatalf("DestroyVault: %v", err)
	}
	if auth.deriveCalls != before {
		t.Error("destroying the vault asked for a gesture; a lost key could then never be forgotten")
	}

	if _, found, err := LoadVault(store, Production); err != nil || found {
		t.Fatalf("the vault survived: found=%v err=%v", found, err)
	}
}

// --- storage --------------------------------------------------------------

func TestStorage_RoundTripThroughStore(t *testing.T) {
	auth, sealed := enrollOne(t, Production)

	store := newFakeBlobStore()
	if err := SaveVault(store, Production, sealed); err != nil {
		t.Fatal(err)
	}

	// The store holds bytes under the environment key, nothing structured.
	if _, ok := store.blobs[string(Production)]; !ok {
		t.Fatal("the sealed vault was not stored under the environment key")
	}

	loaded, found, err := LoadVault(store, Production)
	if err != nil || !found {
		t.Fatalf("LoadVault: found=%v err=%v", found, err)
	}

	sess, err := loaded.Unlock(auth)
	if err != nil {
		t.Fatalf("Unlock after reload: %v", err)
	}
	defer sess.Lock()

	got, err := touchvault.ReadString(sess, secretName(Production))
	if err != nil {
		t.Fatalf("reading after reload: %v", err)
	}
	if got != testSecret {
		t.Errorf("secret after reload = %q", got)
	}
}

func TestStorage_AbsentIsNotAnError(t *testing.T) {
	_, found, err := LoadVault(newFakeBlobStore(), Production)
	if err != nil {
		t.Fatalf("an absent vault returned an error: %v", err)
	}
	if found {
		t.Fatal("found is true for an empty store")
	}
}

// Corrupt bytes must be reported as unreadable — never panicked on, and never
// mistaken for "nothing is enrolled", which would invite a silent re-enroll.
func TestStorage_CorruptVaultIsAnError(t *testing.T) {
	store := newFakeBlobStore()
	if err := store.SaveWrappedAPIKey(string(Production), []byte("{not a vault")); err != nil {
		t.Fatal(err)
	}

	if _, found, err := LoadVault(store, Production); err == nil {
		t.Fatalf("corrupt bytes loaded without complaint (found=%v)", found)
	}
}

// --- the boundary ---------------------------------------------------------

// fido.New is the only door to hardware, and it refuses under a test binary.
// Everything else in this file reaches touchvault through a fake, which is the
// only reason a key never blinks during `go test`.
func TestSecurityKey_NewRefusesUnderTest(t *testing.T) {
	if _, err := fido.New(); !errors.Is(err, fido.ErrUnderTest) {
		t.Fatalf("fido.New() = %v, want ErrUnderTest", err)
	}
}

// The hardware decrypter must refuse before it reaches the store or the key. A
// test that could decrypt production would be a test that taught the operator
// to touch the key on demand.
func TestHardwareDecrypter_RefusesUnderTest(t *testing.T) {
	auth, sealed := enrollOne(t, Production)

	store := newFakeBlobStore()
	if err := SaveVault(store, Production, sealed); err != nil {
		t.Fatal(err)
	}

	h := HardwareDecrypter{
		Store: store,
		New:   func() (touchvault.Authenticator, error) { return auth, nil },
	}

	before := auth.deriveCalls
	if _, err := h.DecryptAPIKey(Production); !errors.Is(err, ErrDecrypterUnderTest) {
		t.Fatalf("err = %v, want ErrDecrypterUnderTest", err)
	}
	if auth.deriveCalls != before {
		t.Error("the decrypter reached the authenticator from a test binary")
	}
}

func TestHardwareDecrypter_RequiresAStore(t *testing.T) {
	if _, err := (HardwareDecrypter{}).DecryptAPIKey(Production); err == nil {
		t.Fatal("a decrypter with no store returned without complaint")
	}
}

// Sandbox is served from the keyring and must stay reachable from a test;
// production must not. That routing is the whole shape of the guard.
func TestEnvironmentDecrypter_Routes(t *testing.T) {
	f := useFakeStore(t)
	f.items[secretKey(Sandbox)] = []byte("sandbox_secret")

	dec := DefaultDecrypter(newFakeBlobStore())

	secret, err := dec.DecryptAPIKey(Sandbox)
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	if secret != "sandbox_secret" {
		t.Errorf("sandbox secret = %q", secret)
	}

	if _, err := dec.DecryptAPIKey(Production); !errors.Is(err, ErrDecrypterUnderTest) {
		t.Fatalf("production: err = %v, want ErrDecrypterUnderTest", err)
	}
}

func TestEnvironmentDecrypter_RejectsUnknownEnvironment(t *testing.T) {
	dec := DefaultDecrypter(newFakeBlobStore())

	if _, err := dec.DecryptAPIKey(Environment("staging")); err == nil {
		t.Fatal("an unknown environment was routed without complaint")
	}
}
