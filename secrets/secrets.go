// Package secrets stores and retrieves credentials in the OS keyring
// (macOS Keychain, Windows Credential Manager, or Linux Secret Service).
package secrets

import (
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"

	"github.com/99designs/keyring"
)

const serviceName = "bankferry"

// ErrNotFound is returned by Load when the key is absent from the
// keyring, so callers can tell "nothing stored yet" from a real failure.
var ErrNotFound = errors.New("secrets: key not found")

// Keyring defines the keyring operations used by this package.
type Keyring interface {
	Get(key string) (keyring.Item, error)
	Set(item keyring.Item) error
	Remove(key string) error
	Keys() ([]string, error)
}

// openKeyringFunc is the function used to open the keyring.
// Tests replace this to inject a mock.
var openKeyringFunc = openKeyring

func openKeyring() (Keyring, error) {
	var backends []keyring.BackendType
	switch runtime.GOOS {
	case "darwin":
		backends = []keyring.BackendType{keyring.KeychainBackend}
	case "windows":
		backends = []keyring.BackendType{keyring.WinCredBackend}
	case "linux":
		backends = []keyring.BackendType{keyring.SecretServiceBackend}
	default:
		return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	return keyring.Open(keyring.Config{
		ServiceName:     serviceName,
		AllowedBackends: backends,
	})
}

// Store writes data to the keyring under key, replacing any existing
// value. The label and description are shown by the OS credential UI.
func Store(key string, data []byte, label, description string) error {
	ring, err := openKeyringFunc()
	if err != nil {
		return fmt.Errorf("opening keyring: %w", err)
	}

	err = ring.Set(keyring.Item{
		Key:         key,
		Data:        data,
		Label:       label,
		Description: description,
	})
	if err != nil {
		return fmt.Errorf("storing %s in keyring: %w", key, err)
	}
	return nil
}

// Load retrieves the data stored under key.
func Load(key string) ([]byte, error) {
	ring, err := openKeyringFunc()
	if err != nil {
		return nil, fmt.Errorf("opening keyring: %w", err)
	}

	item, err := ring.Get(key)
	if errors.Is(err, keyring.ErrKeyNotFound) {
		return nil, fmt.Errorf("retrieving %s from keyring: %w", key, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("retrieving %s from keyring: %w", key, err)
	}
	return item.Data, nil
}

// Keys lists every key stored under this service, sorted. It lets a
// caller enumerate what exists rather than depending on a separate index
// entry, whose loss or corruption would strand the items it points at.
func Keys(prefix string) ([]string, error) {
	ring, err := openKeyringFunc()
	if err != nil {
		return nil, fmt.Errorf("opening keyring: %w", err)
	}

	all, err := ring.Keys()
	if err != nil {
		return nil, fmt.Errorf("listing keyring keys: %w", err)
	}

	var matched []string
	for _, k := range all {
		if strings.HasPrefix(k, prefix) {
			matched = append(matched, k)
		}
	}
	sort.Strings(matched)
	return matched, nil
}

// Delete removes key from the keyring. Removing a key that is not
// present is not an error.
func Delete(key string) error {
	ring, err := openKeyringFunc()
	if err != nil {
		return fmt.Errorf("opening keyring: %w", err)
	}

	if err := ring.Remove(key); err != nil && !errors.Is(err, keyring.ErrKeyNotFound) {
		return fmt.Errorf("removing %s from keyring: %w", key, err)
	}
	return nil
}
