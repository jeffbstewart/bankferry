// Package plaid holds configuration and credential storage for the Plaid
// API. Credentials live in the OS keyring, never in .env or on disk.
package plaid

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jeffbstewart/bankferry/secrets"
)

// Environment identifies a Plaid environment. Plaid issues one secret per
// environment; the client ID is shared across them.
type Environment string

const (
	// Sandbox is Plaid's free test environment. Test institutions, fake
	// data, unlimited Items.
	Sandbox Environment = "sandbox"
	// Production is Plaid's live environment. Recognized but not yet
	// enabled — see ParseEnvironment.
	Production Environment = "production"
)

// ParseEnvironment converts a string to a validated Environment.
func ParseEnvironment(s string) (Environment, error) {
	switch Environment(strings.ToLower(strings.TrimSpace(s))) {
	case Sandbox:
		return Sandbox, nil
	case Production:
		return Production, nil
	default:
		return "", fmt.Errorf("plaid: invalid environment %q (want sandbox or production)", s)
	}
}

// Keyring item keys. The client ID is shared across environments; each
// environment has its own secret.
const clientIDKey = "plaid-client-id"

func secretKey(env Environment) string {
	return "plaid-secret-" + string(env)
}

// Credentials are the API credentials for one Plaid environment.
type Credentials struct {
	ClientID string
	Secret   string
}

// Indirection over the keyring so tests can substitute a fake.
var (
	storeSecret  = secrets.Store
	loadSecret   = secrets.Load
	deleteSecret = secrets.Delete
)

// StoreClientID writes only the client ID to the keyring.
//
// The client ID is shared across environments and is not a secret -- it is
// closer to a username. Production uses this path: the production secret is
// never stored in the keyring, only wrapped behind a security key. See
// plaid-enroll-key.
func StoreClientID(clientID string) error {
	if clientID == "" {
		return errors.New("plaid: client ID is empty")
	}
	return storeSecret(clientIDKey, []byte(clientID),
		"Plaid client ID",
		"Plaid client ID, shared across environments")
}

// StoreCredentials writes the client ID and the environment's secret to
// the OS keyring, replacing any existing values.
func StoreCredentials(env Environment, clientID, secret string) error {
	if clientID == "" {
		return errors.New("plaid: client ID is empty")
	}
	if secret == "" {
		return errors.New("plaid: secret is empty")
	}

	err := storeSecret(clientIDKey, []byte(clientID),
		"Plaid client ID",
		"Plaid client ID, shared across environments")
	if err != nil {
		return err
	}

	return storeSecret(secretKey(env), []byte(secret),
		fmt.Sprintf("Plaid %s secret", env),
		fmt.Sprintf("Plaid API secret for the %s environment", env))
}

// LoadCredentials reads the client ID from the keyring and obtains the
// environment's secret from dec.
//
// The secret comes from the decrypter rather than the keyring because
// production must be gated on a present human. The client ID is not
// sensitive and needs no such ceremony.
//
// Calling this may block on a physical gesture. Call it once per command
// and carry the result; see [APIKeyDecrypter].
func LoadCredentials(env Environment, dec APIKeyDecrypter) (Credentials, error) {
	if dec == nil {
		return Credentials{}, errors.New("plaid: no API key decrypter")
	}

	clientID, err := loadSecret(clientIDKey)
	if err != nil {
		return Credentials{}, fmt.Errorf(
			"plaid: no client ID in keyring (run 'plaid-init --env %s'): %w", env, err)
	}

	secret, err := dec.DecryptAPIKey(env)
	if err != nil {
		return Credentials{}, err
	}

	return Credentials{ClientID: string(clientID), Secret: secret}, nil
}

// DeleteCredentials removes the environment's secret from the keyring.
// The client ID is left in place because it is shared with any other
// environment that may still be configured.
func DeleteCredentials(env Environment) error {
	return deleteSecret(secretKey(env))
}
