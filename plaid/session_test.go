package plaid

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testSession(t *testing.T) *linkSession {
	t.Helper()

	s, err := newLinkSession(LinkOptions{AccessKey: "key-123"})
	if err != nil {
		t.Fatalf("newLinkSession: %v", err)
	}
	return s
}

func requestWithCookie(value string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: value})
	return r
}

// ---------------------------------------------------------------------------
// Access key
// ---------------------------------------------------------------------------

func TestLinkSession_AdmitsOnlyTheAccessKey(t *testing.T) {
	s := testSession(t)

	if !s.admits(httptest.NewRequest(http.MethodGet, "/?key=key-123", nil)) {
		t.Error("the correct key must admit")
	}
	if s.admits(httptest.NewRequest(http.MethodGet, "/?key=wrong", nil)) {
		t.Error("a wrong key must not admit")
	}
	if s.admits(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Error("no key must not admit")
	}
}

// A reload carries the cookie, not the key, and must still work.
func TestLinkSession_AdmitsAnEstablishedSession(t *testing.T) {
	s := testSession(t)

	if !s.admits(requestWithCookie(s.id)) {
		t.Error("an established session must admit without the key")
	}
}

// A key is generated when none is supplied, and is not guessable.
func TestNewLinkSession_GeneratesDistinctSecrets(t *testing.T) {
	a, err := newLinkSession(LinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := newLinkSession(LinkOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if a.accessKey == "" || a.id == "" {
		t.Fatal("a session must have both a key and an id")
	}
	if a.accessKey == b.accessKey || a.id == b.id {
		t.Fatal("two sessions share a secret")
	}
	if a.accessKey == a.id {
		t.Error("the access key and the session id must be distinct secrets")
	}
}

// The cookie is only marked Secure when the browser will reach us over HTTPS,
// because a Secure cookie is never sent over plain HTTP.
func TestNewLinkSession_SecureCookieOnlyForHTTPS(t *testing.T) {
	https, err := newLinkSession(LinkOptions{RedirectURI: "https://plaid.example.net/oauth-return"})
	if err != nil {
		t.Fatal(err)
	}
	if !https.secure {
		t.Error("an https origin must set a Secure cookie")
	}

	loopback, err := newLinkSession(LinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if loopback.secure {
		t.Error("a plain http loopback origin must not set a Secure cookie")
	}
}

// ---------------------------------------------------------------------------
// Session cookie
// ---------------------------------------------------------------------------

func TestLinkSession_Authorized(t *testing.T) {
	s := testSession(t)

	if !s.authorized(requestWithCookie(s.id)) {
		t.Error("the right cookie must authorize")
	}
	if s.authorized(requestWithCookie("forged")) {
		t.Error("a forged cookie must not authorize")
	}
	if s.authorized(httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Error("no cookie must not authorize")
	}
}

func TestLinkSession_GuardRefusesWithoutTheCookie(t *testing.T) {
	s := testSession(t)

	called := false
	h := s.guard("a test", func(http.ResponseWriter, *http.Request) { called = true })

	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodPost, "/exchange", nil))

	if called {
		t.Fatal("the handler ran for an unauthorized request")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// ---------------------------------------------------------------------------
// OAuth window
// ---------------------------------------------------------------------------

// A callback is only welcome while the browser says it is away at a bank.
func TestLinkSession_OAuthClosedUntilArmed(t *testing.T) {
	s := testSession(t)

	if s.oauthOpen() {
		t.Fatal("the OAuth window must start closed")
	}
	s.arm()
	if !s.oauthOpen() {
		t.Fatal("arming must open the window")
	}
	s.disarm()
	if s.oauthOpen() {
		t.Error("disarming must close the window")
	}
}

// The window closes on its own, so a link flow left running does not sit
// waiting for a callback forever.
func TestLinkSession_OAuthWindowExpires(t *testing.T) {
	s := testSession(t)

	// Arm, then expire the window by hand rather than waiting a minute.
	s.arm()
	s.expireOnce.Do(func() { close(s.oauthExpired) })

	if s.oauthOpen() {
		t.Error("an expired window must be closed")
	}

	select {
	case <-s.oauthExpired:
	case <-time.After(time.Second):
		t.Error("oauthExpired never signalled")
	}
}

// Arming twice must not start a second timer or reopen a closed window.
func TestLinkSession_ArmIsIdempotent(t *testing.T) {
	s := testSession(t)

	s.arm()
	first := s.timer
	s.arm()

	if s.timer != first {
		t.Error("arming twice replaced the timer")
	}
}

// ---------------------------------------------------------------------------
// Single use
// ---------------------------------------------------------------------------

// The exchange consumes an Item. A replay, or a race with a second callback,
// must not consume another.
func TestLinkSession_ClaimSucceedsOnce(t *testing.T) {
	s := testSession(t)

	if !s.claim() {
		t.Fatal("the first claim must succeed")
	}
	if s.claim() {
		t.Fatal("the second claim must fail")
	}
}

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------

func TestLinkOptions_Origin(t *testing.T) {
	cases := []struct{ uri, bind, want string }{
		{"", "", "http://" + DefaultBindAddr},
		{"", "localhost:9999", "http://localhost:9999"},
		{"https://plaid.example.net/oauth-return", "", "https://plaid.example.net"},
		{"https://plaid.example.net:8443/x", "0.0.0.0:8570", "https://plaid.example.net:8443"},
	}
	for _, tc := range cases {
		opts := LinkOptions{RedirectURI: tc.uri, BindAddr: tc.bind}
		if got := opts.origin(); got != tc.want {
			t.Errorf("origin(%q,%q) = %q, want %q", tc.uri, tc.bind, got, tc.want)
		}
	}
}

// The address printed for the operator must be the redirect URI's origin
// whenever one is configured. Sending them to loopback would set the session
// cookie on loopback, and the bank's callback would then arrive at the
// proxied origin carrying no session at all.
func TestLinkOptions_EntryURLFollowsTheRedirectURI(t *testing.T) {
	opts := LinkOptions{
		RedirectURI: "https://plaid.example.net/oauth-return",
		BindAddr:    "0.0.0.0:8570",
	}

	got := opts.EntryURL("abc123")
	want := "https://plaid.example.net/?key=abc123"
	if got != want {
		t.Errorf("EntryURL = %q, want %q", got, want)
	}
	if strings.Contains(got, "0.0.0.0") || strings.Contains(got, "localhost") {
		t.Error("the entry URL must not point at the bind address when a redirect URI is set")
	}
}

// Without a redirect URI there is no proxied origin, so the bind address is
// the only address there is.
func TestLinkOptions_EntryURLFallsBackToTheBindAddress(t *testing.T) {
	if got := (LinkOptions{}).EntryURL("k"); got != "http://"+DefaultBindAddr+"/?key=k" {
		t.Errorf("EntryURL = %q", got)
	}
	if got := (LinkOptions{BindAddr: "localhost:9999"}).EntryURL("k"); got != "http://localhost:9999/?key=k" {
		t.Errorf("EntryURL = %q", got)
	}
}

// A non-default port on the redirect URI survives into the entry URL.
func TestLinkOptions_EntryURLKeepsThePort(t *testing.T) {
	opts := LinkOptions{RedirectURI: "https://plaid.example.net:8443/oauth-return"}
	if got := opts.EntryURL("k"); got != "https://plaid.example.net:8443/?key=k" {
		t.Errorf("EntryURL = %q", got)
	}
}

func TestLinkOptions_CallbackPath(t *testing.T) {
	cases := []struct{ uri, want string }{
		{"", defaultCallbackPath},
		{"https://plaid.example.net/", defaultCallbackPath},
		{"https://plaid.example.net/oauth-return", "/oauth-return"},
		{"https://plaid.example.net/deep/cb", "/deep/cb"},
	}
	for _, tc := range cases {
		if got := (LinkOptions{RedirectURI: tc.uri}).callbackPath(); got != tc.want {
			t.Errorf("callbackPath(%q) = %q, want %q", tc.uri, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// healthz
// ---------------------------------------------------------------------------

func TestSanitizeHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plaid.example.net", "plaid.example.net"},
		{"localhost:8570", "localhost:8570"},
		{"", "(no host header)"},
		{"bad\r\nInjected: header", "badInjected: header"},
		{"\x00\x01", "(no host header)"},
	}
	for _, tc := range cases {
		if got := sanitizeHost(tc.in); got != tc.want {
			t.Errorf("sanitizeHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A Host header cannot smuggle a newline into the response.
func TestHandleHealthz_StripsControlCharacters(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.Host = "evil\r\nX-Injected: yes"

	w := httptest.NewRecorder()
	handleHealthz(w, r)

	body := w.Body.String()
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff header = %q", got)
	}
	if body != "plaid link manager is running, reachable, and healthy at evilX-Injected: yes\n" {
		t.Errorf("body = %q", body)
	}
}

// linkPath is the only durable record of whether a link went through the
// bank's OAuth flow. The screen cannot say: in sandbox every OAuth
// institution shows the same generic pane.
func TestLinkPath_ReportsHowTheCredentialsArrived(t *testing.T) {
	t.Run("no handoff", func(t *testing.T) {
		s := testSession(t)
		if got := s.linkPath(); !strings.Contains(got, "without an OAuth handoff") {
			t.Errorf("linkPath() = %q", got)
		}
	})

	t.Run("handoff and return", func(t *testing.T) {
		s := testSession(t)
		s.arm()
		s.disarm()
		s.markOAuthReturned()
		if got := s.linkPath(); !strings.Contains(got, "through the bank's OAuth sign-in") {
			t.Errorf("linkPath() = %q", got)
		}
	})

	// Armed but never returned means Link reported an OAuth handoff and the
	// exchange still completed. That should be impossible; say so rather than
	// quietly reporting a plain link.
	t.Run("handoff without return", func(t *testing.T) {
		s := testSession(t)
		s.arm()
		got := s.linkPath()
		if !strings.Contains(got, "never returned") || !strings.Contains(got, "investigate") {
			t.Errorf("linkPath() = %q", got)
		}
	})
}
