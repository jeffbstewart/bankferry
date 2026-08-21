package plaid

import (
	"errors"
	"testing"
)

func TestValidateRedirectHost_Accepted(t *testing.T) {
	for _, host := range []string{
		"plaid.example.net",
		"plaid.example.net:8443",
		"PLAID.EXAMPLE.NET",
		"myfoo.mydomain.com",
		"192.0.2.10",
		"192.0.2.10:443",
		"[2001:db8::1]:8443",
	} {
		if err := ValidateRedirectHost(host); err != nil {
			t.Errorf("ValidateRedirectHost(%q) = %v, want nil", host, err)
		}
	}
}

func TestValidateRedirectHost_EmptyIsValid(t *testing.T) {
	if err := ValidateRedirectHost(""); err != nil {
		t.Errorf("empty host rejected: %v", err)
	}
	if RedirectURI("") != "" {
		t.Error("an empty host produced a redirect URI")
	}
}

// The flag takes a host, not a URI. Anything that looks like the old full
// form — a scheme, a path, the callback itself — is refused with a message
// that says what the URI will be, so the operator registers the right thing.
func TestValidateRedirectHost_RefusesURIs(t *testing.T) {
	for _, host := range []string{
		"https://plaid.example.net",
		"https://plaid.example.net/oauth-return",
		"http://localhost:8570",
		"plaid.example.net/oauth-return",
		"plaid.example.net/",
		"plaid.example.net?x=1",
		"plaid.example.net#frag",
		"user@plaid.example.net",
		"plaid.example.net:abc",
		"plaid.example.net:0",
		"plaid.example.net:70000",
		":8443",
		"plaid example.net",
	} {
		err := ValidateRedirectHost(host)
		if !errors.Is(err, ErrRedirectHost) {
			t.Errorf("ValidateRedirectHost(%q) = %v, want ErrRedirectHost", host, err)
		}
	}
}

// Loopback over plain HTTP is gone: the scheme is not the operator's to
// choose, so there is nothing to make an exception for.
func TestValidateRedirectHost_NoPlainHTTPLoopback(t *testing.T) {
	if err := ValidateRedirectHost("http://localhost:8570"); !errors.Is(err, ErrRedirectHost) {
		t.Errorf("got %v", err)
	}
}

func TestRedirectURI_IsAlwaysHTTPSAtTheCallbackPath(t *testing.T) {
	cases := map[string]string{
		"plaid.example.net":      "https://plaid.example.net/oauth-return",
		"plaid.example.net:8443": "https://plaid.example.net:8443/oauth-return",
	}
	for host, want := range cases {
		if got := RedirectURI(host); got != want {
			t.Errorf("RedirectURI(%q) = %q, want %q", host, got, want)
		}
	}
}
