package secrets

import (
	"errors"
	"testing"
)

// withServiceName runs f with the package's service name swapped, restoring
// whatever was there afterwards so tests do not leak identity into each
// other.
func withServiceName(t *testing.T, name string, f func()) {
	t.Helper()
	saved := serviceName
	serviceName = name
	t.Cleanup(func() { serviceName = saved })
	f()
}

// A process that never installed an identity must not open the keyring at
// all: defaulting would let a new binary read another program's tokens.
func TestOpenKeyring_RefusesWithoutAService(t *testing.T) {
	withServiceName(t, "", func() {
		if _, err := openKeyring(); !errors.Is(err, ErrNoService) {
			t.Fatalf("openKeyring() error = %v, want ErrNoService", err)
		}
	})
}

func TestSetServiceName(t *testing.T) {
	withServiceName(t, "", func() {
		SetServiceName("ferry-a")
		SetServiceName("ferry-a") // the same name again is fine
		if serviceName != "ferry-a" {
			t.Fatalf("serviceName = %q", serviceName)
		}

		defer func() {
			if recover() == nil {
				t.Error("changing the service name did not panic")
			}
		}()
		SetServiceName("ferry-b")
	})
}

func TestSetServiceName_RefusesEmpty(t *testing.T) {
	withServiceName(t, "", func() {
		defer func() {
			if recover() == nil {
				t.Error("an empty service name did not panic")
			}
		}()
		SetServiceName("")
	})
}
