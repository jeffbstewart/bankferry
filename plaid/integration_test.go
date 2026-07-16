// This file is an external test package on purpose. The unit tests in
// package plaid swap the keyring indirection for an in-memory fake; these
// tests must see the real OS keyring, so they may only touch the exported
// API.
package plaid_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/jeffbstewart/bankferry/plaid"
	"github.com/jeffbstewart/bankferry/secrets"
)

// requireSandboxCredentials skips the test unless sandbox credentials are
// in the OS keyring. Nothing here creates a Plaid Item, so it costs no
// slot against the Trial plan's lifetime cap: only a link token, which is
// free and short-lived.
func requireSandboxCredentials(t *testing.T) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping: integration test reaches the Plaid sandbox over the network")
	}

	if _, err := plaid.LoadCredentials(plaid.Sandbox, plaid.KeyringDecrypter{}); err != nil {
		if errors.Is(err, secrets.ErrNotFound) {
			t.Skip("skipping: no Plaid sandbox credentials in the OS keyring " +
				"(run 'bankferry plaid-init --env sandbox')")
		}
		t.Fatalf("reading sandbox credentials: %v", err)
	}
}

// sandboxCredentials decrypts once for a test that needs a client. Call
// requireSandboxCredentials first.
func sandboxCredentials(t *testing.T) plaid.Credentials {
	t.Helper()

	creds, err := plaid.LoadCredentials(plaid.Sandbox, plaid.KeyringDecrypter{})
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	return creds
}

// ---------------------------------------------------------------------------
// Link token
// ---------------------------------------------------------------------------

// A link token proves the stored credentials authenticate against the
// sandbox. Its value is short-lived but still a credential, so only its
// shape is asserted, never its contents.
func TestIntegration_CreateLinkToken(t *testing.T) {
	requireSandboxCredentials(t)

	client, err := plaid.NewClient(plaid.Sandbox, sandboxCredentials(t))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	token, err := plaid.CreateLinkToken(ctx, client, "")
	if err != nil {
		t.Fatalf("CreateLinkToken: %v", err)
	}
	if !strings.HasPrefix(token, "link-sandbox-") {
		t.Errorf("link token has unexpected prefix (value withheld), length %d", len(token))
	}
}

// KeyringDecrypter refuses production, and it does so whether or not a
// production secret happens to be stored on this machine.
//
// This is the structural guard, so it is asserted without preconditions: no
// skip, no dependence on keyring contents. If a production secret is sitting
// in the keyring, this test still demands a refusal, because a keyring read
// is exactly what an automated agent running as the operator can do.
func TestIntegration_KeyringDecrypterRefusesProduction(t *testing.T) {
	_, err := plaid.KeyringDecrypter{}.DecryptAPIKey(plaid.Production)
	if err == nil {
		t.Fatal("KeyringDecrypter returned a production secret")
	}
	if !errors.Is(err, plaid.ErrProductionKeyLocked) {
		t.Errorf("err = %v, want it to wrap ErrProductionKeyLocked", err)
	}

	// And the refusal propagates: no client can be built for production.
	if _, err := plaid.LoadCredentials(plaid.Production, plaid.KeyringDecrypter{}); err == nil {
		t.Fatal("LoadCredentials produced production credentials")
	}
}

// Sandbox is served without ceremony. Nothing there is irreplaceable.
func TestIntegration_KeyringDecrypterServesSandbox(t *testing.T) {
	requireSandboxCredentials(t)

	secret, err := plaid.KeyringDecrypter{}.DecryptAPIKey(plaid.Sandbox)
	if err != nil {
		t.Fatalf("DecryptAPIKey(sandbox): %v", err)
	}
	if secret == "" {
		t.Error("sandbox secret is empty")
	}
}

// ---------------------------------------------------------------------------
// Link server
// ---------------------------------------------------------------------------

// testAccessKey is supplied to the server so the tests can open the entry
// page. In a real run the key is random and printed on startup.
const testAccessKey = "test-access-key"

func testLinkOptions() plaid.LinkOptions {
	return plaid.LinkOptions{AccessKey: testAccessKey}
}

// entryURL is the entry page with the access key, which is the only way in.
func entryURL(baseURL string) string {
	return baseURL + "/?key=" + testAccessKey
}

// startLinkServer runs the server in the background and returns a cancel
// func. It fails the test if the server never accepts a connection.
func startLinkServer(t *testing.T) (baseURL string, stop func()) {
	t.Helper()

	client, err := plaid.NewClient(plaid.Sandbox, sandboxCredentials(t))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := plaid.StartLinkServer(ctx, plaid.Sandbox, client, testLinkOptions())
		errCh <- err
	}()

	if !waitForListener(plaid.LinkServerAddr, 20*time.Second) {
		cancel()
		<-errCh
		t.Fatalf("link server never listened on %s", plaid.LinkServerAddr)
	}

	return "http://" + plaid.LinkServerAddr, func() {
		cancel()
		// The server returns the context error once it stops. Anything
		// else means it failed for a reason worth surfacing.
		if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("StartLinkServer returned %v, want context.Canceled", err)
		}
	}
}

func waitForListener(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			if cerr := conn.Close(); cerr != nil {
				return false
			}
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing response body: %v", cerr)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", url, err)
	}
	return resp.StatusCode, string(body)
}

// The sandbox page must carry a usable link token, name the environment,
// and must not display the production warning.
func TestIntegration_LinkPage_Sandbox(t *testing.T) {
	requireSandboxCredentials(t)

	baseURL, stop := startLinkServer(t)
	defer stop()

	status, body := get(t, entryURL(baseURL))
	if status != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", status)
	}

	// The token itself is a credential; assert only that one was injected
	// into the JavaScript string, never its value.
	if !strings.Contains(body, `token: "link-sandbox-`) {
		t.Error("no sandbox link token was injected into the page")
	}
	if !strings.Contains(body, "cdn.plaid.com/link/v2") {
		t.Error("Plaid Link JS is not referenced")
	}
	if !strings.Contains(body, "Sandbox environment") {
		t.Error("page does not identify itself as sandbox")
	}
	if !strings.Contains(body, "user_good") {
		t.Error("sandbox page should show the fixed public test credentials")
	}
	if strings.Contains(body, "consumes a production Item") {
		t.Error("sandbox page must not show the production Item warning")
	}
}

// The update-mode page repairs an Item in place. It must not offer to
// exchange a public token, and must not carry the production Item warning,
// because update mode consumes nothing.
func TestIntegration_RelinkPage_UpdateMode(t *testing.T) {
	item := requireSandboxItem(t)
	creds := sandboxCredentials(t)

	client, err := plaid.NewClient(plaid.Sandbox, creds)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	data, err := plaid.NewDataClient(plaid.Sandbox, creds)
	if err != nil {
		t.Fatalf("NewDataClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- plaid.StartRelinkServer(ctx, plaid.Sandbox, client, data, item, testLinkOptions()) }()

	if !waitForListener(plaid.LinkServerAddr, 20*time.Second) {
		cancel()
		<-errCh
		t.Fatal("relink server never listened")
	}
	defer func() {
		cancel()
		if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("StartRelinkServer returned %v, want context.Canceled", err)
		}
	}()

	status, body := get(t, entryURL("http://"+plaid.LinkServerAddr))
	if status != http.StatusOK {
		t.Fatalf("GET / status = %d", status)
	}

	if !strings.Contains(body, "Re-authenticate "+item.InstitutionName) {
		t.Error("page does not name the institution being re-authenticated")
	}
	if !strings.Contains(body, `token: "link-sandbox-`) {
		t.Error("no update-mode link token was injected")
	}
	// html/template escapes "/" as "\/" inside a JavaScript string, so that
	// a value can never close the enclosing <script> element.
	if !strings.Contains(body, `fetch("\/relinked"`) {
		t.Error("update mode must post to /relinked, not /exchange")
	}
	if strings.Contains(body, `fetch("\/exchange"`) {
		t.Error("update mode must never exchange a public token: it would create a second Item")
	}
	if !strings.Contains(body, "var isUpdate = true") {
		t.Error("the page did not render in update mode")
	}
	if strings.Contains(body, "consumes a production Item") {
		t.Error("update mode consumes no Item and must not warn that it does")
	}
	if !strings.Contains(body, "No new Item is created") {
		t.Error("page should say that nothing is consumed")
	}

	// The exchange endpoint must not exist at all in this mode.
	if s, _ := get(t, "http://"+plaid.LinkServerAddr+"/exchange"); s != http.StatusNotFound {
		t.Errorf("GET /exchange in update mode = %d, want 404", s)
	}
}

func TestIntegration_LinkServer_UnknownPathIs404(t *testing.T) {
	requireSandboxCredentials(t)

	baseURL, stop := startLinkServer(t)
	defer stop()

	if status, _ := get(t, baseURL+"/nope"); status != http.StatusNotFound {
		t.Errorf("GET /nope status = %d, want 404", status)
	}
}

// ---------------------------------------------------------------------------
// Guards
// ---------------------------------------------------------------------------

// Reaching the address is not enough. Without the per-run key, no session is
// minted and no link token is handed out.
func TestIntegration_EntryPageRequiresTheAccessKey(t *testing.T) {
	requireSandboxCredentials(t)

	baseURL, stop := startLinkServer(t)
	defer stop()

	status, body := get(t, baseURL+"/")
	if status != http.StatusForbidden {
		t.Errorf("GET / without a key = %d, want 403", status)
	}
	if strings.Contains(body, "link-sandbox-") {
		t.Fatal("the entry page leaked a link token to an unauthenticated caller")
	}

	if status, _ := get(t, baseURL+"/?key=wrong"); status != http.StatusForbidden {
		t.Errorf("GET / with a wrong key = %d, want 403", status)
	}
}

// Health is the one unauthenticated endpoint, and it echoes the Host header
// so a reverse proxy can be checked before an Item is spent.
func TestIntegration_HealthzIsOpenAndEchoesHost(t *testing.T) {
	requireSandboxCredentials(t)

	baseURL, stop := startLinkServer(t)
	defer stop()

	status, body := get(t, baseURL+"/healthz")
	if status != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", status)
	}
	if !strings.Contains(body, "plaid link manager is running, reachable, and healthy at ") {
		t.Errorf("healthz body = %q", body)
	}
	if !strings.Contains(body, plaid.LinkServerAddr) {
		t.Errorf("healthz did not echo the host: %q", body)
	}
}

// The exchange consumes an Item. A caller without the session cookie is a
// caller this server never invited.
func TestIntegration_ExchangeRequiresTheSessionCookie(t *testing.T) {
	requireSandboxCredentials(t)

	baseURL, stop := startLinkServer(t)
	defer stop()

	resp, err := http.Post(baseURL+"/exchange", "application/json",
		strings.NewReader(`{"public_token":"public-sandbox-forged"}`))
	if err != nil {
		t.Fatalf("POST /exchange: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing response body: %v", cerr)
		}
	}()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /exchange without a cookie = %d, want 403", resp.StatusCode)
	}
}

// An OAuth callback that arrives without a session, or outside the window the
// browser opened, is a callback this server did not start.
func TestIntegration_OAuthCallbackRequiresSessionAndWindow(t *testing.T) {
	requireSandboxCredentials(t)

	baseURL, stop := startLinkServer(t)
	defer stop()

	callback := baseURL + "/oauth-return?oauth_state_id=forged"

	if status, _ := get(t, callback); status != http.StatusForbidden {
		t.Errorf("OAuth callback without a cookie = %d, want 403", status)
	}

	// With a session, but no OAuth handoff in progress, it is still refused.
	client := sessionClient(t, baseURL)
	if status, _ := getWith(t, client, callback); status != http.StatusForbidden {
		t.Errorf("OAuth callback outside the window = %d, want 403", status)
	}

	// A request to the callback path with no oauth_state_id at all.
	if status, _ := getWith(t, client, baseURL+"/oauth-return"); status != http.StatusBadRequest {
		t.Errorf("callback with no oauth_state_id = %d, want 400", status)
	}
}

// Once the browser reports that Link handed the user to a bank, the callback
// is accepted.
func TestIntegration_OAuthCallbackAcceptedInsideTheWindow(t *testing.T) {
	requireSandboxCredentials(t)

	baseURL, stop := startLinkServer(t)
	defer stop()

	client := sessionClient(t, baseURL)

	resp, err := client.Post(baseURL+"/oauth-arm", "", nil)
	if err != nil {
		t.Fatalf("POST /oauth-arm: %v", err)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("closing response body: %v", cerr)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /oauth-arm = %d, want 204", resp.StatusCode)
	}

	status, body := getWith(t, client, baseURL+"/oauth-return?oauth_state_id=abc")
	if status != http.StatusOK {
		t.Fatalf("armed OAuth callback = %d, want 200", status)
	}
	// The page resumes Link against the address the server reconstructed, not
	// one the caller chose.
	if !strings.Contains(body, "oauth_state_id=abc") {
		t.Error("the resumed page does not carry the received redirect URI")
	}
	if !strings.Contains(body, "var isOAuthReturn = receivedRedirectUri !== \"\"") {
		t.Error("the page is not in resume mode")
	}
}

// Arming requires the session too, or anyone could open the window.
func TestIntegration_OAuthArmRequiresTheSessionCookie(t *testing.T) {
	requireSandboxCredentials(t)

	baseURL, stop := startLinkServer(t)
	defer stop()

	resp, err := http.Post(baseURL+"/oauth-arm", "", nil)
	if err != nil {
		t.Fatalf("POST /oauth-arm: %v", err)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("closing response body: %v", cerr)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST /oauth-arm without a cookie = %d, want 403", resp.StatusCode)
	}
}

// sessionClient opens the entry page with the access key and keeps the cookie.
func sessionClient(t *testing.T, baseURL string) *http.Client {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{Jar: jar}

	if status, _ := getWith(t, client, entryURL(baseURL)); status != http.StatusOK {
		t.Fatalf("GET the entry page = %d, want 200", status)
	}
	return client
}

func getWith(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()

	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing response body: %v", cerr)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", url, err)
	}
	return resp.StatusCode, string(body)
}

// A malformed or empty exchange must be rejected before any public token
// is sent to Plaid, because the exchange is what consumes an Item.
func TestIntegration_Exchange_RejectsBadRequests(t *testing.T) {
	requireSandboxCredentials(t)

	baseURL, stop := startLinkServer(t)
	defer stop()

	client := sessionClient(t, baseURL)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"empty object", `{}`, http.StatusBadRequest},
		{"missing public_token", `{"institution_id":"ins_1"}`, http.StatusBadRequest},
		{"not json", `garbage`, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := client.Post(baseURL+"/exchange", "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("POST /exchange: %v", err)
			}
			defer func() {
				if cerr := resp.Body.Close(); cerr != nil {
					t.Errorf("closing response body: %v", cerr)
				}
			}()

			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}
