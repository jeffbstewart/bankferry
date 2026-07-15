package plaid

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	plaidsdk "github.com/plaid/plaid-go/v43/plaid"
)

// DefaultBindAddr is where the link server listens unless told otherwise.
// Loopback by default: exposing it, even to a local network, must be opted
// into rather than stumbled into.
//
// Plaid's servers never fetch the redirect URI. The bank redirects the user's
// *browser* there, so this server only has to be reachable from the browser
// completing the flow. A reverse proxy restricted to the local network is
// sufficient, and is the recommended arrangement for production, where Plaid
// requires the redirect URI to be HTTPS.
const DefaultBindAddr = "localhost:8570"

// LinkServerAddr is retained for callers and tests that assume the default.
const LinkServerAddr = DefaultBindAddr

// defaultCallbackPath is where an OAuth institution returns the user when the
// configured redirect URI names no path of its own.
const defaultCallbackPath = "/oauth-return"

// oauthWindow bounds how long the server will wait for a bank to send the
// user back. Outside it, a callback is refused and the server stops. A link
// flow left running is an open door.
//
// Five minutes, not one: a bank's consent screens carry legalese, account
// pickers, and sometimes a second factor. A window that expires mid-consent
// wastes the operator's time and, in production, risks a half-finished flow
// against an irreplaceable Item.
const oauthWindow = 5 * time.Minute

// shutdownGrace bounds the wait for the browser's final response to be
// flushed before the server closes.
const shutdownGrace = 3 * time.Second

// sessionCookie binds a browser to one link session.
const sessionCookie = "plaidlink_session"

// LinkOptions configures a link or relink flow.
type LinkOptions struct {
	// RedirectURI is where OAuth institutions return the user. Required for
	// Chase. Must be HTTPS outside Sandbox, and registered in the Plaid
	// Dashboard.
	RedirectURI string

	// BindAddr defaults to DefaultBindAddr.
	BindAddr string

	// AccessKey is the one-time key that admits a browser to the entry page.
	// Generated when empty; supplied by tests.
	AccessKey string
}

func (o LinkOptions) bindAddr() string {
	if o.BindAddr == "" {
		return DefaultBindAddr
	}
	return o.BindAddr
}

// origin is the address the operator should open: the redirect URI's origin
// when one is configured, since that is where the browser must be for the
// OAuth callback to land on the same site, and the bind address otherwise.
func (o LinkOptions) origin() string {
	if o.RedirectURI != "" {
		if u, err := url.Parse(o.RedirectURI); err == nil && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	return "http://" + o.bindAddr()
}

func (o LinkOptions) callbackPath() string {
	if path := redirectPath(o.RedirectURI); path != "" {
		return path
	}
	return defaultCallbackPath
}

// EntryURL is the address the operator must open, carrying the run's access
// key.
//
// When a redirect URI is configured the browser has to be at that origin, not
// at the bind address: the bank returns it there, and the session cookie is
// scoped to whatever origin issued it. Pointing the operator at loopback
// would set the cookie on loopback, and the callback would arrive at the
// proxied origin with no session.
func (o LinkOptions) EntryURL(accessKey string) string {
	return o.origin() + "/?key=" + accessKey
}

type linkPageData struct {
	Environment  Environment
	IsProduction bool
	LinkToken    string

	// IsUpdate switches the page to update mode, which repairs an existing
	// Item rather than creating a new one.
	IsUpdate        bool
	InstitutionName string

	// CompletePath is where the browser reports success. The two modes must
	// not share a handler: update mode has no public_token to exchange, and
	// exchanging one would create a second Item.
	CompletePath string

	// ReceivedRedirectURI is the address the bank returned the user to, empty
	// on a fresh page. When set, Link is re-created with it and opened at
	// once, rather than waiting for a click that has already happened.
	ReceivedRedirectURI string
}

type exchangeRequest struct {
	PublicToken     string `json:"public_token"`
	InstitutionID   string `json:"institution_id"`
	InstitutionName string `json:"institution_name"`
}

// LinkResult describes the Item produced by a completed Link session.
type LinkResult struct {
	ItemID          string
	InstitutionID   string
	InstitutionName string
}

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

// linkSession guards one Link flow.
//
// While the server runs, its address is reachable by anything that can route
// to it. Without guards, a stranger could fetch the entry page, take the link
// token, drive Link against their own bank, and have the browser POST to this
// server's exchange. We would store an access token for their account and
// spend one of the ten Items a production account is allowed for its lifetime.
//
// Four guards, each covering what the others do not:
//
//  1. An access key, minted per run and printed on startup, admits a browser
//     to the entry page. Reaching the address is not enough.
//  2. A session cookie, set by the entry page, admits every later request. A
//     callback arriving without it was never initiated here.
//  3. An OAuth window: the callback is only accepted while the browser has
//     told us it left for a bank, and for a minute afterwards.
//  4. Single use: the exchange completes exactly once, so a replay or a race
//     cannot consume a second Item.
type linkSession struct {
	mu        sync.Mutex
	accessKey string
	id        string
	secure    bool

	armed     bool
	completed bool

	// oauthStarted records that Link handed the browser to a bank's own
	// sign-in; oauthReturned that the bank sent it back. Together they say
	// whether the link went through OAuth or was completed inline, which the
	// screen cannot tell you: sandbox shows a generic pane for every OAuth
	// institution.
	oauthStarted  bool
	oauthReturned bool

	// oauthExpired closes when an armed OAuth window elapses.
	oauthExpired chan struct{}
	expireOnce   sync.Once
	timer        *time.Timer
}

func newLinkSession(opts LinkOptions) (*linkSession, error) {
	id, err := randomHex(32)
	if err != nil {
		return nil, err
	}

	key := opts.AccessKey
	if key == "" {
		if key, err = randomHex(16); err != nil {
			return nil, err
		}
	}

	return &linkSession{
		accessKey: key,
		id:        id,
		// A Secure cookie is never sent over plain HTTP, so only set it when
		// the browser will reach us over HTTPS.
		secure:       strings.HasPrefix(strings.ToLower(opts.origin()), "https://"),
		oauthExpired: make(chan struct{}),
	}, nil
}

func randomHex(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("plaid: generating random bytes: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *linkSession) issueCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.id,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// admits reports whether a request may open the entry page: either it carries
// the run's access key, or it already holds the session cookie, so a reload
// works.
func (s *linkSession) admits(r *http.Request) bool {
	if constantTimeEqual(r.URL.Query().Get("key"), s.accessKey) {
		return true
	}
	return s.authorized(r)
}

func (s *linkSession) authorized(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return constantTimeEqual(c.Value, s.id)
}

// arm opens the OAuth window. The browser calls this when Link reports it is
// handing the user to a bank.
func (s *linkSession) arm() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.armed {
		return
	}
	s.armed = true
	s.oauthStarted = true
	s.timer = time.AfterFunc(oauthWindow, func() {
		s.expireOnce.Do(func() { close(s.oauthExpired) })
	})
	log.Printf("plaid: OAuth: Link handed the browser to the bank's own sign-in page. "+
		"Waiting up to %s for it to come back.", oauthWindow)
}

// markOAuthReturned records that the bank sent the browser back to us.
func (s *linkSession) markOAuthReturned() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oauthReturned = true
	log.Printf("plaid: OAuth: the bank returned the browser. Resuming Link.")
}

// linkPath describes, in one clause, how the credentials reached us. The
// screen cannot answer this: in sandbox every OAuth institution shows the
// same generic pane, and a bank that supports both flows may use either.
func (s *linkSession) linkPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case s.oauthStarted && s.oauthReturned:
		return "through the bank's OAuth sign-in and back via the redirect URI"
	case s.oauthStarted:
		return "after an OAuth handoff that never returned through the redirect URI, " +
			"which should not happen; investigate"
	default:
		return "without an OAuth handoff; Link collected the credentials itself"
	}
}

// oauthOpen reports whether a callback is currently welcome.
func (s *linkSession) oauthOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.armed {
		return false
	}
	select {
	case <-s.oauthExpired:
		return false
	default:
		return true
	}
}

// disarm stops the window once the browser is back.
func (s *linkSession) disarm() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armed = false
	if s.timer != nil {
		s.timer.Stop()
	}
}

// claim marks the session complete, returning false if it already was.
func (s *linkSession) claim() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completed {
		return false
	}
	s.completed = true
	return true
}

// guard admits only the browser holding this session.
func (s *linkSession) guard(name string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			log.Printf("plaid: refused %s from %s: no valid session cookie. "+
				"If this was not you, a request arrived that this server did not initiate.",
				name, r.RemoteAddr)
			http.Error(w, "This link session did not originate here.", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// ---------------------------------------------------------------------------
// Servers
// ---------------------------------------------------------------------------

// StartLinkServer serves the Plaid Link page, waits for the user to complete a
// Link session in the browser, persists the resulting Item, and returns.
func StartLinkServer(ctx context.Context, env Environment, client *plaidsdk.APIClient, opts LinkOptions) (LinkResult, error) {
	linkToken, err := CreateLinkToken(ctx, client, opts.RedirectURI)
	if err != nil {
		return LinkResult{}, err
	}

	data := linkPageData{
		Environment:  env,
		IsProduction: env == Production,
		LinkToken:    linkToken,
		CompletePath: "/exchange",
	}

	var result LinkResult
	err = serveLink(ctx, env, data, opts, func(mux *http.ServeMux, session *linkSession, finish func()) {
		mux.HandleFunc("/exchange", session.guard("an exchange", func(w http.ResponseWriter, r *http.Request) {
			handleExchange(ctx, w, r, env, client, session, func(res LinkResult) {
				result = res
				finish()
			})
		}))
	})
	if err != nil {
		return LinkResult{}, err
	}
	return result, nil
}

// StartRelinkServer serves Link in update mode, repairing an existing Item in
// place. Nothing is exchanged and nothing is stored: the access token is
// unchanged and no Item is consumed.
// data is built by the caller, before the browser is involved. Building it
// here, or inside handleRelinked, would decrypt the API secret in the middle
// of an OAuth callback — raising a security-key touch at a moment the
// operator cannot connect to any decision they made.
func StartRelinkServer(ctx context.Context, env Environment, client *plaidsdk.APIClient, dataClient *DataClient, item Item, opts LinkOptions) error {
	linkToken, err := CreateUpdateLinkToken(ctx, client, item.AccessToken, opts.RedirectURI)
	if err != nil {
		return err
	}

	data := linkPageData{
		Environment:     env,
		IsProduction:    env == Production,
		LinkToken:       linkToken,
		IsUpdate:        true,
		InstitutionName: item.InstitutionName,
		CompletePath:    "/relinked",
	}

	return serveLink(ctx, env, data, opts, func(mux *http.ServeMux, session *linkSession, finish func()) {
		mux.HandleFunc("/relinked", session.guard("a relink confirmation", func(w http.ResponseWriter, r *http.Request) {
			handleRelinked(ctx, w, r, dataClient, item, session, finish)
		}))
	})
}

// ErrOAuthTimeout reports that a bank never returned the browser.
var ErrOAuthTimeout = errors.New("plaid: the bank did not return within the OAuth window")

func serveLink(ctx context.Context, env Environment, data linkPageData, opts LinkOptions, routes func(*http.ServeMux, *linkSession, func())) error {
	tmpl, err := template.New("link").Parse(linkPageTemplate)
	if err != nil {
		return fmt.Errorf("plaid: parsing link page template: %w", err)
	}

	session, err := newLinkSession(opts)
	if err != nil {
		return err
	}

	var once sync.Once
	done := make(chan struct{})
	finish := func() { once.Do(func() { close(done) }) }

	render := func(w http.ResponseWriter, received string) {
		page := data
		page.ReceivedRedirectURI = received

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, page); err != nil {
			http.Error(w, "template error", http.StatusInternalServerError)
			log.Printf("plaid: rendering link page: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := buf.WriteTo(w); err != nil {
			log.Printf("plaid: writing link page: %v", err)
		}
	}

	mux := http.NewServeMux()

	// The entry page is the only handler that mints a session, and the only
	// one the access key opens.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if !session.admits(r) {
			log.Printf("plaid: refused the entry page to %s: wrong or missing access key", r.RemoteAddr)
			http.Error(w, "Open the address printed by bankferry, including its key.",
				http.StatusForbidden)
			return
		}
		session.issueCookie(w)
		render(w, "")
	})

	mux.HandleFunc("/healthz", handleHealthz)

	// The browser tells us it is handing the user to a bank. Only then will a
	// callback be accepted, and only for a minute.
	mux.HandleFunc("/oauth-arm", session.guard("an OAuth arm", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		session.arm()
		w.WriteHeader(http.StatusNoContent)
	}))

	// The OAuth return.
	callbackPath := opts.callbackPath()
	mux.HandleFunc(callbackPath, session.guard("an OAuth callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("oauth_state_id") == "" {
			http.Error(w, "Not an OAuth return.", http.StatusBadRequest)
			return
		}
		if !session.oauthOpen() {
			log.Printf("plaid: refused an OAuth callback from %s: no OAuth handoff is in progress",
				r.RemoteAddr)
			http.Error(w, "No OAuth sign-in is in progress for this session.", http.StatusForbidden)
			return
		}
		session.disarm()
		session.markOAuthReturned()

		// Reconstruct the address the browser is at from the configured
		// redirect URI, so a caller cannot choose what Link re-initializes
		// against.
		received := opts.origin() + r.URL.Path
		if r.URL.RawQuery != "" {
			received += "?" + r.URL.RawQuery
		}
		render(w, received)
	}))

	routes(mux, session, finish)

	ln, err := net.Listen("tcp", opts.bindAddr())
	if err != nil {
		return fmt.Errorf("plaid: listen %s: %w", opts.bindAddr(), err)
	}

	srv := &http.Server{Handler: mux}
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	action := "link an institution"
	if data.IsUpdate {
		action = "re-authenticate " + data.InstitutionName
	}

	log.Printf("plaid: link server listening on %s (environment: %s)", opts.bindAddr(), env)
	log.Printf("plaid: open this address in a browser to %s:", action)
	log.Printf("plaid:   %s", opts.EntryURL(session.accessKey))
	log.Printf("plaid: the key is good for this run only. Health: %s/healthz", opts.origin())
	if opts.RedirectURI != "" {
		log.Printf("plaid: OAuth institutions return to %s", opts.RedirectURI)
		log.Printf("plaid: open the address above, not %s, or the callback will "+
			"arrive without a session", "http://"+opts.bindAddr())
	}
	log.Printf("plaid: Ctrl+C to abort")

	var outErr error
	select {
	case <-done:
	case err := <-serveErr:
		outErr = err
	case <-session.oauthExpired:
		outErr = ErrOAuthTimeout
	case <-ctx.Done():
		outErr = ctx.Err()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("plaid: shutting down link server: %v", err)
	}

	return outErr
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// handleHealthz reports that this server is reachable, and at which host.
//
// The host is the point. A production OAuth link is reached through a reverse
// proxy over HTTPS, and echoing the Host header back proves the proxy is
// forwarding the expected virtual host to this listener. Checking that before
// linking is cheaper than discovering it after spending an Item.
//
// It is the one unauthenticated endpoint, so it reveals nothing but its own
// existence. The Host header is client-controlled: it is served as plain text
// with nosniff and stripped of control characters, so a reflected value stays
// a reflected value.
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	msg := fmt.Sprintf("plaid link manager is running, reachable, and healthy at %s\n",
		sanitizeHost(r.Host))
	if _, err := io.WriteString(w, msg); err != nil {
		log.Printf("plaid: writing healthz response: %v", err)
	}
}

// sanitizeHost makes a client-supplied Host header safe to echo: printable
// characters only, and bounded in length.
func sanitizeHost(host string) string {
	const maxHostLen = 255

	if host == "" {
		return "(no host header)"
	}
	if len(host) > maxHostLen {
		host = host[:maxHostLen]
	}

	cleaned := strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return -1
		}
		return r
	}, host)

	if cleaned == "" {
		return "(no host header)"
	}
	return cleaned
}

// handleExchange turns a public_token into a stored Item.
//
// Ordering is deliberate. The duplicate check runs before the exchange,
// because it is requesting an access token that creates the duplicate Item.
// And an exchanged token that cannot be persisted is an orphan: it is
// unrecoverable from Plaid, so the Item behind it is removed while the token
// is still in hand rather than left dangling.
func handleExchange(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	env Environment,
	client *plaidsdk.APIClient,
	session *linkSession,
	finish func(LinkResult),
) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req exchangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.PublicToken == "" {
		http.Error(w, "public_token is required", http.StatusBadRequest)
		return
	}

	// Refuse before exchanging: it is the exchange that creates a duplicate
	// Item, and a duplicate spends a slot that never returns.
	existing, found, err := FindItemByInstitution(env, req.InstitutionID)
	if err != nil {
		log.Printf("plaid: checking for an existing item: %v", err)
		http.Error(w, "could not check existing items; nothing was exchanged",
			http.StatusInternalServerError)
		return
	}
	if found {
		log.Printf("plaid: refusing to link %s (%s): already linked as item %s",
			req.InstitutionName, req.InstitutionID, existing.ItemID)
		http.Error(w, fmt.Sprintf(
			"%s is already linked (item %s). Nothing was exchanged, so no Item was consumed.",
			existing.InstitutionName, existing.ItemID), http.StatusConflict)
		return
	}

	// One exchange per run. A replay, or a race with a second callback, must
	// not consume a second Item.
	if !session.claim() {
		log.Printf("plaid: refused a second exchange from %s", r.RemoteAddr)
		http.Error(w, "This session has already completed.", http.StatusConflict)
		return
	}

	accessToken, itemID, err := ExchangePublicToken(ctx, client, req.PublicToken)
	if err != nil {
		log.Printf("plaid: exchanging public token: %v", err)
		http.Error(w, "could not exchange the public token", http.StatusBadGateway)
		return
	}

	item := Item{
		ItemID:          itemID,
		AccessToken:     accessToken,
		InstitutionID:   req.InstitutionID,
		InstitutionName: req.InstitutionName,
	}

	if err := SaveItem(env, item); err != nil {
		handleSaveFailure(ctx, w, client, item, err)
		return
	}

	log.Printf("plaid: linked %s as item %s, %s", item.InstitutionName, itemID, session.linkPath())

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, werr := fmt.Fprintf(w, "Linked %s. You may close this tab.", item.InstitutionName); werr != nil {
		log.Printf("plaid: writing exchange response: %v", werr)
	}

	finish(LinkResult{
		ItemID:          itemID,
		InstitutionID:   req.InstitutionID,
		InstitutionName: req.InstitutionName,
	})
}

// logDoomedToken prints an access token that is about to be discarded without
// having been stored and without its Item having been removed.
//
// This deliberately writes a secret to the log, and it is the only place in
// this program that does. The alternative is worse: Plaid never reissues an
// access token, so once this function returns, the Item behind it can never be
// read, never be removed, and its slot — one of ten for the lifetime of the
// account — is gone for good. A token in a log file can be copied back out and
// used. A token in no file cannot.
// LogDoomedToken prints an access token that is about to become
// unrecoverable. It is the last resort, and the only place in this program
// that a secret reaches the log. Callers must be sure the token is otherwise
// lost: printing one that is still safely stored leaks it for nothing.
func LogDoomedToken(item Item, reason string) { logDoomedToken(item, reason) }

func logDoomedToken(item Item, reason string) {
	log.Printf("plaid: ================ ABOUT TO LOSE AN ACCESS TOKEN ================")
	log.Printf("plaid: item_id:     %s", item.ItemID)
	log.Printf("plaid: institution: %s (%s)", item.InstitutionName, item.InstitutionID)
	log.Printf("plaid: reason:      %s", reason)
	log.Printf("plaid: access_token below is a SECRET, printed only because it is")
	log.Printf("plaid: otherwise unrecoverable. Save it, then scrub this output.")
	log.Printf("plaid: access_token: %s", item.AccessToken)
	log.Printf("plaid: The Item still exists at Plaid. With this token you can store")
	log.Printf("plaid: it by hand or remove the Item. Without it, you can do neither.")
	log.Printf("plaid: ===============================================================")
}

// handleSaveFailure deals with an access token that exists at Plaid but could
// not be stored locally.
//
// If the key was already occupied, the stored entry refers to this same
// item_id. Removing the Item would destroy what that entry points at, so the
// Item is left alone and the operator is told to resolve it. Otherwise the
// token is a true orphan and the Item is removed now, while the token is still
// in memory — afterwards it can never be reached again.
//
// Any path that ends with the token neither stored nor removed logs the token
// itself, because it is about to cease to exist anywhere.
func handleSaveFailure(
	ctx context.Context,
	w http.ResponseWriter,
	client *plaidsdk.APIClient,
	item Item,
	saveErr error,
) {
	log.Printf("plaid: item %s was exchanged but could not be stored: %v", item.ItemID, saveErr)

	if errors.Is(saveErr, ErrItemExists) || errors.Is(saveErr, ErrRefusingToClobber) {
		log.Printf("plaid: leaving item %s at Plaid: the keyring already holds an entry "+
			"for this item_id, and removing the Item would invalidate it too", item.ItemID)
		logDoomedToken(item, fmt.Sprintf("not stored (%v), and the Item was not removed "+
			"because an existing keyring entry points at the same item_id", saveErr))
		http.Error(w, fmt.Sprintf(
			"Item %s already has a keyring entry, which was left untouched. "+
				"The Item still exists at Plaid. Its access token was written to the log; "+
				"save it before it scrolls away. Resolve the entry before retrying.", item.ItemID),
			http.StatusConflict)
		return
	}

	if rerr := RemoveItem(ctx, client, item.AccessToken, item.ItemID); rerr != nil {
		log.Printf("plaid: ORPHANED item %s: it could not be stored and could not be removed.",
			item.ItemID)
		logDoomedToken(item, fmt.Sprintf("not stored (%v), and removal failed (%v)", saveErr, rerr))
		http.Error(w, fmt.Sprintf(
			"Item %s could not be stored (%v) and could not be removed (%v). "+
				"It is orphaned at Plaid. Its access token was written to the log; "+
				"save it before it scrolls away.", item.ItemID, saveErr, rerr),
			http.StatusInternalServerError)
		return
	}

	// The Item is gone, so the token is inert. Nothing to preserve.
	http.Error(w, fmt.Sprintf(
		"Item %s could not be stored (%v), so it was removed at Plaid. Nothing is dangling.",
		item.ItemID, saveErr), http.StatusInternalServerError)
}

// handleRelinked confirms an update-mode session actually repaired the Item.
//
// It deliberately ignores any public_token the browser reports. Update mode
// does not issue a usable one, the access_token is unchanged, and exchanging
// it would create a second Item.
func handleRelinked(ctx context.Context, w http.ResponseWriter, r *http.Request, data *DataClient, item Item, session *linkSession, finish func()) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !session.claim() {
		http.Error(w, "This session has already completed.", http.StatusConflict)
		return
	}

	status, err := data.FetchItemStatus(ctx, item.AccessToken)
	if err != nil {
		log.Printf("plaid: verifying item %s after update mode: %v", item.ItemID, err)
		http.Error(w, "could not verify the item after re-authentication", http.StatusBadGateway)
		return
	}

	if status.NeedsLinkRefresh() {
		log.Printf("plaid: item %s still reports %s after update mode", item.ItemID, status.ErrorCode)
		http.Error(w, "The item still needs re-authentication. Try again.", http.StatusConflict)
		return
	}

	log.Printf("plaid: item %s re-authenticated %s; the access token is unchanged",
		item.ItemID, session.linkPath())

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, werr := fmt.Fprintf(w, "Re-authenticated %s. You may close this tab.", item.InstitutionName); werr != nil {
		log.Printf("plaid: writing relink response: %v", werr)
	}

	finish()
}
