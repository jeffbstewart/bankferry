package plaid

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// CallbackPath is where an OAuth institution returns the user. It is fixed:
// the redirect URI Plaid is given is always https://<redirect host>/oauth-return,
// and that exact string is what must be registered in the Plaid Dashboard
// under Allowed redirect URIs. Pinning it removes a whole class of
// misconfiguration — a registered URI whose path the server did not serve,
// or a root path that landed on the access-keyed entry page — in exchange
// for one path the operator cannot choose.
const CallbackPath = "/oauth-return"

// ErrRedirectHost reports a redirect host Plaid would reject or that could
// not be served over HTTPS.
var ErrRedirectHost = errors.New("plaid: invalid redirect host")

// ValidateRedirectHost checks a redirect host before it is sent to Plaid.
//
// The host is `name` or `name:port`, nothing more: no scheme, no path, no
// credentials. The scheme is always HTTPS, because an OAuth institution
// returns the user here carrying the oauth_state_id that completes the Link
// session, and that must not cross the network in the clear. There is no
// loopback exception: a plain-HTTP localhost redirect only ever worked in
// Sandbox, and Sandbox institutions link without OAuth anyway.
//
// An empty host is valid: it simply means no OAuth institution will work.
func ValidateRedirectHost(host string) error {
	if host == "" {
		return nil
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/?#@ ") {
		return fmt.Errorf("%w: %q must be a bare host or host:port, with no scheme or path; "+
			"the redirect URI is always https://<host>%s", ErrRedirectHost, host, CallbackPath)
	}

	name, port := host, ""
	if h, p, err := net.SplitHostPort(host); err == nil {
		name, port = h, p
	} else if strings.Contains(host, ":") {
		return fmt.Errorf("%w: %q: %v", ErrRedirectHost, host, err)
	}
	if name == "" {
		return fmt.Errorf("%w: %q has no host name", ErrRedirectHost, host)
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("%w: %q has an invalid port", ErrRedirectHost, host)
		}
	}

	// url.Parse is the authority on what survives as a host in a URL.
	u, err := url.Parse(RedirectURI(host))
	if err != nil || u.Host != host || u.Path != CallbackPath {
		return fmt.Errorf("%w: %q does not form a valid URL", ErrRedirectHost, host)
	}
	return nil
}

// RedirectURI is the full redirect URI for a host, or empty for an empty
// host. It is the one string the Dashboard registration must match.
func RedirectURI(host string) string {
	if host == "" {
		return ""
	}
	return "https://" + host + CallbackPath
}
