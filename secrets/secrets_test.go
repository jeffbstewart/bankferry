package secrets

import (
	"errors"
	"testing"

	"github.com/99designs/keyring"
)

// mockKeyring is an in-memory Keyring implementation for testing.
type mockKeyring struct {
	items map[string]keyring.Item

	// Optional error injectors. When non-nil, the corresponding
	// method returns the error instead of performing the operation.
	getErr    error
	setErr    error
	removeErr error
	keysErr   error
}

func newMockKeyring() *mockKeyring {
	return &mockKeyring{items: make(map[string]keyring.Item)}
}

func (m *mockKeyring) Get(key string) (keyring.Item, error) {
	if m.getErr != nil {
		return keyring.Item{}, m.getErr
	}
	item, ok := m.items[key]
	if !ok {
		return keyring.Item{}, keyring.ErrKeyNotFound
	}
	return item, nil
}

func (m *mockKeyring) Set(item keyring.Item) error {
	if m.setErr != nil {
		return m.setErr
	}
	m.items[item.Key] = item
	return nil
}

func (m *mockKeyring) Remove(key string) error {
	if m.removeErr != nil {
		return m.removeErr
	}
	if _, ok := m.items[key]; !ok {
		return keyring.ErrKeyNotFound
	}
	delete(m.items, key)
	return nil
}

func (m *mockKeyring) Keys() ([]string, error) {
	if m.keysErr != nil {
		return nil, m.keysErr
	}
	keys := make([]string, 0, len(m.items))
	for k := range m.items {
		keys = append(keys, k)
	}
	return keys, nil
}

// useMock installs m as the keyring backend and returns a cleanup
// function that restores the original openKeyringFunc.
func useMock(m *mockKeyring) func() {
	orig := openKeyringFunc
	openKeyringFunc = func() (Keyring, error) { return m, nil }
	return func() { openKeyringFunc = orig }
}

// failOpen installs a keyring opener that always fails and returns a
// cleanup function.
func failOpen() func() {
	orig := openKeyringFunc
	openKeyringFunc = func() (Keyring, error) { return nil, errors.New("no keyring") }
	return func() { openKeyringFunc = orig }
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

func TestStore_Success(t *testing.T) {
	m := newMockKeyring()
	defer useMock(m)()

	if err := Store("access-url", []byte("https://user:pass@example.com"), "Label", "Desc"); err != nil {
		t.Fatalf("Store returned error: %v", err)
	}

	item, ok := m.items["access-url"]
	if !ok {
		t.Fatal("value not stored in keyring")
	}
	if string(item.Data) != "https://user:pass@example.com" {
		t.Errorf("stored data = %q", item.Data)
	}
	if item.Label != "Label" || item.Description != "Desc" {
		t.Errorf("label/description not preserved: %q / %q", item.Label, item.Description)
	}
}

func TestStore_Overwrites(t *testing.T) {
	m := newMockKeyring()
	defer useMock(m)()

	if err := Store("k", []byte("first"), "", ""); err != nil {
		t.Fatal(err)
	}
	if err := Store("k", []byte("second"), "", ""); err != nil {
		t.Fatal(err)
	}

	if got := string(m.items["k"].Data); got != "second" {
		t.Errorf("data = %q, want %q", got, "second")
	}
}

func TestStore_SetError(t *testing.T) {
	m := newMockKeyring()
	m.setErr = errors.New("keyring locked")
	defer useMock(m)()

	if err := Store("k", []byte("v"), "", ""); err == nil {
		t.Fatal("expected error when Set fails, got nil")
	}
}

func TestStore_KeyringOpenError(t *testing.T) {
	defer failOpen()()

	if err := Store("k", []byte("v"), "", ""); err == nil {
		t.Fatal("expected error when openKeyring fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

func TestLoad_Success(t *testing.T) {
	m := newMockKeyring()
	m.items["k"] = keyring.Item{Key: "k", Data: []byte("my-value")}
	defer useMock(m)()

	data, err := Load("k")
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if string(data) != "my-value" {
		t.Errorf("data = %q, want %q", data, "my-value")
	}
}

// A missing key reports ErrNotFound so callers can distinguish it from a
// locked or broken keyring.
func TestLoad_NotFound(t *testing.T) {
	m := newMockKeyring()
	defer useMock(m)()

	_, err := Load("missing")
	if err == nil {
		t.Fatal("expected error when key not in keyring, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// A keyring failure that is not a missing key must not masquerade as
// ErrNotFound.
func TestLoad_OtherErrorIsNotNotFound(t *testing.T) {
	m := newMockKeyring()
	m.getErr = errors.New("keyring locked")
	defer useMock(m)()

	_, err := Load("k")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("a locked keyring must not report ErrNotFound")
	}
}

func TestLoad_GetError(t *testing.T) {
	m := newMockKeyring()
	m.getErr = errors.New("keyring locked")
	defer useMock(m)()

	if _, err := Load("k"); err == nil {
		t.Fatal("expected error when Get fails, got nil")
	}
}

func TestLoad_KeyringOpenError(t *testing.T) {
	defer failOpen()()

	if _, err := Load("k"); err == nil {
		t.Fatal("expected error when openKeyring fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestDelete_Success(t *testing.T) {
	m := newMockKeyring()
	defer useMock(m)()

	if err := Store("k", []byte("v"), "", ""); err != nil {
		t.Fatal(err)
	}
	if err := Delete("k"); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if _, ok := m.items["k"]; ok {
		t.Error("key not removed")
	}
}

// Deleting a key that was never stored is a no-op, so that cleanup paths
// stay idempotent.
func TestDelete_MissingKeyIsNotAnError(t *testing.T) {
	m := newMockKeyring()
	defer useMock(m)()

	if err := Delete("never-stored"); err != nil {
		t.Fatalf("expected no error deleting a missing key, got: %v", err)
	}
}

func TestDelete_RemoveError(t *testing.T) {
	m := newMockKeyring()
	m.removeErr = errors.New("permission denied")
	defer useMock(m)()

	if err := Delete("k"); err == nil {
		t.Fatal("expected error when Remove fails, got nil")
	}
}

func TestDelete_KeyringOpenError(t *testing.T) {
	defer failOpen()()

	if err := Delete("k"); err == nil {
		t.Fatal("expected error when openKeyring fails, got nil")
	}
}

// ---------------------------------------------------------------------------
// Keys
// ---------------------------------------------------------------------------

func TestKeys_FiltersByPrefixAndSorts(t *testing.T) {
	m := newMockKeyring()
	defer useMock(m)()

	for _, k := range []string{"plaid-item-sandbox-b", "plaid-item-sandbox-a", "plaid-client-id", "other"} {
		if err := Store(k, []byte("v"), "", ""); err != nil {
			t.Fatal(err)
		}
	}

	keys, err := Keys("plaid-item-sandbox-")
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %v, want 2 entries", keys)
	}
	if keys[0] != "plaid-item-sandbox-a" || keys[1] != "plaid-item-sandbox-b" {
		t.Errorf("keys = %v, want sorted [a b]", keys)
	}
}

func TestKeys_EmptyWhenNoMatch(t *testing.T) {
	m := newMockKeyring()
	defer useMock(m)()

	keys, err := Keys("plaid-item-sandbox-")
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("keys = %v, want empty", keys)
	}
}

func TestKeys_Error(t *testing.T) {
	m := newMockKeyring()
	m.keysErr = errors.New("keyring locked")
	defer useMock(m)()

	if _, err := Keys(""); err == nil {
		t.Fatal("expected an error when Keys fails")
	}
}

// ---------------------------------------------------------------------------
// Round trip
// ---------------------------------------------------------------------------

func TestStoreLoadDelete_RoundTrip(t *testing.T) {
	m := newMockKeyring()
	defer useMock(m)()

	if err := Store("token", []byte("secret"), "Token", "A token"); err != nil {
		t.Fatal(err)
	}

	data, err := Load("token")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "secret" {
		t.Errorf("data = %q, want %q", data, "secret")
	}

	if err := Delete("token"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("token"); err == nil {
		t.Error("expected Load to fail after Delete")
	}
}
