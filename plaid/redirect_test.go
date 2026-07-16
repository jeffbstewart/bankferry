package plaid

import (
	"errors"
	"testing"
)

// An OAuth institution returns the user to the redirect URI carrying the
// oauth_state_id that completes the Link session. Over plain HTTP that
// crosses the network in the clear.
func TestValidateRedirectURI_HTTPSAccepted(t *testing.T) {
	for _, uri := range []string{
		"https://plaid.example.net/oauth-return",
		"https://plaid.example.net/",
		"https://plaid.example.net",
		"HTTPS://PLAID.EXAMPLE.NET/oauth-return",
		"https://plaid.example.net:8443/oauth-return",
	} {
		if err := ValidateRedirectURI(uri); err != nil {
			t.Errorf("ValidateRedirectURI(%q) = %v, want nil", uri, err)
		}
	}
}

// Loopback over plain HTTP never leaves the machine, and Plaid permits it in
// Sandbox.
func TestValidateRedirectURI_HTTPLoopbackAccepted(t *testing.T) {
	for _, uri := range []string{
		"http://localhost:8570/oauth-return",
		"http://LOCALHOST:8570/",
		"http://127.0.0.1:8570/oauth-return",
		"http://[::1]:8570/oauth-return",
	} {
		if err := ValidateRedirectURI(uri); err != nil {
			t.Errorf("ValidateRedirectURI(%q) = %v, want nil", uri, err)
		}
	}
}

// The rule that matters: plain HTTP to anywhere but this machine.
func TestValidateRedirectURI_HTTPNonLoopbackRefused(t *testing.T) {
	for _, uri := range []string{
		"http://plaid.example.net/oauth-return",
		"http://192.168.1.10:8570/oauth-return",
		"http://10.0.0.5/",
		"http://example.com",
	} {
		err := ValidateRedirectURI(uri)
		if !errors.Is(err, ErrRedirectURI) {
			t.Errorf("ValidateRedirectURI(%q) = %v, want ErrRedirectURI", uri, err)
		}
	}
}

// A private address is still not this machine. Encrypt it.
func TestValidateRedirectURI_PrivateNetworkIsNotLoopback(t *testing.T) {
	if err := ValidateRedirectURI("http://192.168.1.10/oauth-return"); err == nil {
		t.Error("a LAN address is not loopback and must require https")
	}
}

func TestValidateRedirectURI_EmptyIsValid(t *testing.T) {
	if err := ValidateRedirectURI(""); err != nil {
		t.Errorf("an empty redirect URI is valid: %v", err)
	}
}

func TestValidateRedirectURI_Malformed(t *testing.T) {
	for _, uri := range []string{
		"not a url",
		"https://",
		"/oauth-return",
		"ftp://plaid.example.net/",
		"plaid.example.net/oauth-return",
	} {
		if err := ValidateRedirectURI(uri); !errors.Is(err, ErrRedirectURI) {
			t.Errorf("ValidateRedirectURI(%q) = %v, want ErrRedirectURI", uri, err)
		}
	}
}

// Plaid matches the registered redirect URI exactly; a fragment never
// reaches the server anyway.
func TestValidateRedirectURI_FragmentRefused(t *testing.T) {
	if err := ValidateRedirectURI("https://plaid.example.net/oauth#frag"); !errors.Is(err, ErrRedirectURI) {
		t.Error("a fragment must be refused")
	}
}

// ---------------------------------------------------------------------------
// redirectPath
// ---------------------------------------------------------------------------

// The link server must answer the path the institution sends the user back
// to. A bare host or a root path is already served by "/".
func TestRedirectPath(t *testing.T) {
	cases := []struct{ uri, want string }{
		{"", ""},
		{"https://plaid.example.net/oauth-return", "/oauth-return"},
		{"https://plaid.example.net/deep/path", "/deep/path"},
		{"https://plaid.example.net/", ""},
		{"https://plaid.example.net", ""},
		{"http://localhost:8570/oauth-return", "/oauth-return"},
	}
	for _, tc := range cases {
		if got := redirectPath(tc.uri); got != tc.want {
			t.Errorf("redirectPath(%q) = %q, want %q", tc.uri, got, tc.want)
		}
	}
}
