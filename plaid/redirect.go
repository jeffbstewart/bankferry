package plaid

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrRedirectURI reports a redirect URI Plaid would reject, or one that
// would send an OAuth callback in the clear.
var ErrRedirectURI = errors.New("plaid: invalid redirect URI")

// ValidateRedirectURI checks a redirect URI before it is sent to Plaid.
//
// An OAuth institution returns the user to this address carrying the
// oauth_state_id that completes the Link session. Over plain HTTP that
// travels in the clear across the network, so anything but loopback must be
// HTTPS. Plaid enforces the same rule — "Redirect URIs must use HTTPS. The
// only exception is on Sandbox, where... redirect URIs pointing to localhost
// are allowed over HTTP" — but a local check fails immediately and says why,
// rather than after a round trip.
//
// An empty URI is valid: it simply means no OAuth institution will work.
func ValidateRedirectURI(raw string) error {
	if raw == "" {
		return nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %q: %v", ErrRedirectURI, raw, err)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: %q has no host", ErrRedirectURI, raw)
	}
	if u.Fragment != "" {
		return fmt.Errorf("%w: %q must not carry a fragment", ErrRedirectURI, raw)
	}

	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
		return fmt.Errorf(
			"%w: %q is http but not loopback; an OAuth callback would cross the "+
				"network in the clear. Use https, and register it in the Plaid Dashboard",
			ErrRedirectURI, raw)
	default:
		return fmt.Errorf("%w: %q has scheme %q, want https", ErrRedirectURI, raw, u.Scheme)
	}
}

// isLoopback reports whether the host names this machine. Plain HTTP is
// tolerable there because nothing leaves the loopback interface.
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// redirectPath is the path component of a redirect URI, which the link
// server must also serve, since the institution returns the user there.
func redirectPath(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" || u.Path == "/" {
		return ""
	}
	return u.Path
}
