package plaid

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jeffbstewart/bankferry/secrets"
)

// fakeStore replaces the keyring indirection with an in-memory map.
type fakeStore struct {
	items     map[string][]byte
	storeErr  error
	loadErr   error
	deleteErr error
	listErr   error
}

func useFakeStore(t *testing.T) *fakeStore {
	t.Helper()

	f := &fakeStore{items: make(map[string][]byte)}

	origStore, origLoad, origDelete := storeSecret, loadSecret, deleteSecret
	storeSecret = func(key string, data []byte, _, _ string) error {
		if f.storeErr != nil {
			return f.storeErr
		}
		f.items[key] = data
		return nil
	}
	loadSecret = func(key string) ([]byte, error) {
		if f.loadErr != nil {
			return nil, f.loadErr
		}
		data, ok := f.items[key]
		if !ok {
			// Match the real store: a missing key is ErrNotFound, which
			// callers distinguish from a broken keyring.
			return nil, fmt.Errorf("retrieving %s: %w", key, secrets.ErrNotFound)
		}
		return data, nil
	}
	deleteSecret = func(key string) error {
		if f.deleteErr != nil {
			return f.deleteErr
		}
		delete(f.items, key)
		return nil
	}
	t.Cleanup(func() {
		storeSecret, loadSecret, deleteSecret = origStore, origLoad, origDelete
	})

	return f
}

// ---------------------------------------------------------------------------
// ParseEnvironment
// ---------------------------------------------------------------------------

func TestParseEnvironment_Sandbox(t *testing.T) {
	for _, in := range []string{"sandbox", "SANDBOX", " Sandbox "} {
		env, err := ParseEnvironment(in)
		if err != nil {
			t.Fatalf("ParseEnvironment(%q) returned error: %v", in, err)
		}
		if env != Sandbox {
			t.Errorf("ParseEnvironment(%q) = %q, want sandbox", in, env)
		}
	}
}

func TestParseEnvironment_Production(t *testing.T) {
	for _, in := range []string{"production", "PRODUCTION", " Production "} {
		env, err := ParseEnvironment(in)
		if err != nil {
			t.Fatalf("ParseEnvironment(%q) returned error: %v", in, err)
		}
		if env != Production {
			t.Errorf("ParseEnvironment(%q) = %q, want production", in, env)
		}
	}
}

func TestParseEnvironment_Invalid(t *testing.T) {
	for _, in := range []string{"development", "", "sandboxx"} {
		if _, err := ParseEnvironment(in); err == nil {
			t.Errorf("ParseEnvironment(%q) should have failed", in)
		}
	}
}

// The two environments never share a base URL, which is what keeps a sandbox
// token from reaching production.
func TestEnvironmentBaseURLs(t *testing.T) {
	sandbox, err := dataBaseURL(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	production, err := dataBaseURL(Production)
	if err != nil {
		t.Fatal(err)
	}
	if sandbox == production {
		t.Fatal("sandbox and production must not share a base URL")
	}
	if production != "https://production.plaid.com" {
		t.Errorf("production base URL = %q", production)
	}
}

// ---------------------------------------------------------------------------
// Key naming
// ---------------------------------------------------------------------------

// Each environment gets its own secret key, so a sandbox secret can never
// be sent to production or vice versa.
func TestSecretKey_PerEnvironment(t *testing.T) {
	if secretKey(Sandbox) == secretKey(Production) {
		t.Fatal("sandbox and production must not share a secret key")
	}
	if got, want := secretKey(Sandbox), "plaid-secret-sandbox"; got != want {
		t.Errorf("secretKey(Sandbox) = %q, want %q", got, want)
	}
	if got, want := secretKey(Production), "plaid-secret-production"; got != want {
		t.Errorf("secretKey(Production) = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Store / Load / Delete
// ---------------------------------------------------------------------------

func TestStoreLoadCredentials_RoundTrip(t *testing.T) {
	f := useFakeStore(t)

	if err := StoreCredentials(Sandbox, "cid_123", "sec_abc"); err != nil {
		t.Fatalf("StoreCredentials: %v", err)
	}

	if got := string(f.items[clientIDKey]); got != "cid_123" {
		t.Errorf("stored client ID = %q", got)
	}
	if got := string(f.items["plaid-secret-sandbox"]); got != "sec_abc" {
		t.Errorf("stored secret = %q", got)
	}

	creds, err := LoadCredentials(Sandbox, KeyringDecrypter{})
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if creds.ClientID != "cid_123" || creds.Secret != "sec_abc" {
		t.Errorf("creds = %+v", creds)
	}
}

// The two environments keep independent secrets while sharing one client ID.
func TestStoreCredentials_EnvironmentsAreIsolated(t *testing.T) {
	f := useFakeStore(t)

	if err := StoreCredentials(Sandbox, "cid_123", "sandbox_secret"); err != nil {
		t.Fatal(err)
	}
	if err := StoreCredentials(Production, "cid_123", "production_secret"); err != nil {
		t.Fatal(err)
	}

	if got := string(f.items["plaid-secret-sandbox"]); got != "sandbox_secret" {
		t.Errorf("sandbox secret = %q", got)
	}
	if got := string(f.items["plaid-secret-production"]); got != "production_secret" {
		t.Errorf("production secret = %q", got)
	}

	creds, err := LoadCredentials(Sandbox, KeyringDecrypter{})
	if err != nil {
		t.Fatal(err)
	}
	if creds.Secret != "sandbox_secret" {
		t.Errorf("loading sandbox returned %q", creds.Secret)
	}
}

func TestStoreCredentials_RejectsEmpty(t *testing.T) {
	useFakeStore(t)

	if err := StoreCredentials(Sandbox, "", "sec"); err == nil {
		t.Error("expected an error for an empty client ID")
	}
	if err := StoreCredentials(Sandbox, "cid", ""); err == nil {
		t.Error("expected an error for an empty secret")
	}
}

// StoreClientID stores only the client ID, never a secret. It is the
// production path: the production secret never touches the keyring.
func TestStoreClientID(t *testing.T) {
	f := useFakeStore(t)

	if err := StoreClientID("cid_prod"); err != nil {
		t.Fatalf("StoreClientID: %v", err)
	}
	if got := string(f.items[clientIDKey]); got != "cid_prod" {
		t.Errorf("stored client ID = %q", got)
	}
	// No environment secret was written.
	if _, ok := f.items[secretKey(Production)]; ok {
		t.Error("StoreClientID wrote a production secret; it must not")
	}

	if err := StoreClientID(""); err == nil {
		t.Error("expected an error for an empty client ID")
	}
}

// Missing credentials must surface as secrets.ErrNotFound. The sandbox
// integration tests key their skip on exactly that, so a wrapped error
// here would turn a clean skip into a hard failure on a machine with no
// credentials stored.
func TestLoadCredentials_MissingClientID(t *testing.T) {
	useFakeStore(t)

	_, err := LoadCredentials(Sandbox, KeyringDecrypter{})
	if err == nil {
		t.Fatal("expected an error when nothing is stored")
	}
	if !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("err = %v, want it to wrap secrets.ErrNotFound", err)
	}
}

func TestLoadCredentials_MissingSecretIsNotFound(t *testing.T) {
	f := useFakeStore(t)
	f.items[clientIDKey] = []byte("cid_123")

	_, err := LoadCredentials(Sandbox, KeyringDecrypter{})
	if err == nil {
		t.Fatal("expected an error when the environment secret is absent")
	}
	if !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("err = %v, want it to wrap secrets.ErrNotFound", err)
	}
}

// A stored client ID with no secret for the requested environment must
// fail, not silently return an empty secret.
func TestLoadCredentials_MissingSecretForEnvironment(t *testing.T) {
	f := useFakeStore(t)
	f.items[clientIDKey] = []byte("cid_123")

	if _, err := LoadCredentials(Sandbox, KeyringDecrypter{}); err == nil {
		t.Fatal("expected an error when the environment secret is absent")
	}
}

func TestStoreCredentials_KeyringError(t *testing.T) {
	f := useFakeStore(t)
	f.storeErr = errors.New("keyring locked")

	if err := StoreCredentials(Sandbox, "cid", "sec"); err == nil {
		t.Fatal("expected an error when the keyring write fails")
	}
}

// Deleting one environment's secret leaves the other environment and the
// shared client ID intact.
func TestDeleteCredentials_LeavesClientIDAndOtherEnvironment(t *testing.T) {
	f := useFakeStore(t)

	if err := StoreCredentials(Sandbox, "cid_123", "sandbox_secret"); err != nil {
		t.Fatal(err)
	}
	if err := StoreCredentials(Production, "cid_123", "production_secret"); err != nil {
		t.Fatal(err)
	}

	if err := DeleteCredentials(Sandbox); err != nil {
		t.Fatalf("DeleteCredentials: %v", err)
	}

	if _, ok := f.items["plaid-secret-sandbox"]; ok {
		t.Error("sandbox secret was not removed")
	}
	if _, ok := f.items["plaid-secret-production"]; !ok {
		t.Error("production secret must survive deleting sandbox")
	}
	if _, ok := f.items[clientIDKey]; !ok {
		t.Error("shared client ID must survive deleting one environment")
	}
}

func TestDeleteCredentials_Error(t *testing.T) {
	f := useFakeStore(t)
	f.deleteErr = errors.New("permission denied")

	if err := DeleteCredentials(Sandbox); err == nil {
		t.Fatal("expected an error when the keyring delete fails")
	}
}
