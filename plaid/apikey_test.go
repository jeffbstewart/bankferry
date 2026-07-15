package plaid

import (
	"errors"
	"testing"

	"github.com/jeffbstewart/touchvault/fido"
)

// plaid re-exports the refusals; the marker list itself is tested in fido,
// where it lives. What matters here is that the re-export is wired to the same
// errors, so errors.Is works across the seam.
func TestRefusals_DelegateToTheFIDOProvider(t *testing.T) {
	if !errors.Is(ErrDecrypterUnderTest, fido.ErrUnderTest) {
		t.Error("ErrDecrypterUnderTest is not fido.ErrUnderTest")
	}
	if !errors.Is(ErrDecrypterUnderAgent, fido.ErrUnderAgent) {
		t.Error("ErrDecrypterUnderAgent is not fido.ErrUnderAgent")
	}
	if err := RefuseUnderTest(); !errors.Is(err, ErrDecrypterUnderTest) {
		t.Fatalf("RefuseUnderTest() = %v, want ErrDecrypterUnderTest", err)
	}
	if err := RefuseAutomatedContext(); !errors.Is(err, ErrDecrypterUnderTest) {
		t.Fatalf("RefuseAutomatedContext() = %v, want ErrDecrypterUnderTest", err)
	}
}

// KeyringDecrypter reaches no hardware, so it is the one decrypter tests may
// use. It must NOT refuse under test.
func TestKeyringDecrypter_UsableUnderTest(t *testing.T) {
	f := useFakeStore(t)
	f.items[secretKey(Sandbox)] = []byte("sec_abc")

	secret, err := KeyringDecrypter{}.DecryptAPIKey(Sandbox)
	if err != nil {
		t.Fatalf("DecryptAPIKey(sandbox): %v", err)
	}
	if secret != "sec_abc" {
		t.Errorf("secret = %q", secret)
	}
}

// The production refusal does not depend on the keyring. Even with a
// production secret stored — which is precisely what an automated agent
// running as the operator could read — the decrypter must not return it.
func TestKeyringDecrypter_RefusesProductionEvenWhenStored(t *testing.T) {
	f := useFakeStore(t)
	f.items[secretKey(Production)] = []byte("production_secret")

	secret, err := KeyringDecrypter{}.DecryptAPIKey(Production)
	if !errors.Is(err, ErrProductionKeyLocked) {
		t.Fatalf("err = %v, want ErrProductionKeyLocked", err)
	}
	if secret != "" {
		t.Errorf("a secret was returned alongside the refusal: %q", secret)
	}
}

// An empty stored secret is a corrupt keyring entry, not a valid credential.
func TestKeyringDecrypter_RejectsEmptySecret(t *testing.T) {
	f := useFakeStore(t)
	f.items[secretKey(Sandbox)] = []byte("")

	if _, err := (KeyringDecrypter{}).DecryptAPIKey(Sandbox); err == nil {
		t.Fatal("expected an error for an empty stored secret")
	}
}

// LoadCredentials must not silently proceed without a decrypter.
func TestLoadCredentials_NilDecrypter(t *testing.T) {
	useFakeStore(t)

	if _, err := LoadCredentials(Sandbox, nil); err == nil {
		t.Fatal("expected an error with a nil decrypter")
	}
}
