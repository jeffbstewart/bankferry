package plaid

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	plaidsdk "github.com/plaid/plaid-go/v43/plaid"
)

// ---------------------------------------------------------------------------
// The duplicate gate
//
// An Item is one login, not one institution. Re-linking a login you already
// have is a mistake that costs one of the ten permanently, so a duplicate is
// refused by default; a second login at one bank is legitimate, so the
// refusal has an escape hatch. Neither case can be told from the other at
// exchange time — only the operator knows — which is why the permission is
// something they declare beforehand rather than something inferred here.
// ---------------------------------------------------------------------------

func TestPermitsDuplicate(t *testing.T) {
	capOne := []Item{
		testItem("item_1", "ins_capone", "Capital One"),
		testItem("item_2", "ins_capone", "Capital One"),
	}

	cases := []struct {
		name           string
		existing       []Item
		allowDuplicate string
		want           bool
	}{
		{
			name:     "no permission refuses",
			existing: capOne[:1],
			want:     false,
		},
		{
			name:           "naming the existing item permits",
			existing:       capOne[:1],
			allowDuplicate: "item_1",
			want:           true,
		},
		{
			// The misclick this guard exists for: permission to duplicate
			// Capital One must not license a second Chase.
			name:           "naming an item at another institution refuses",
			existing:       []Item{testItem("item_9", "ins_chase", "Chase")},
			allowDuplicate: "item_1",
			want:           false,
		},
		{
			// A third login. Naming either existing Item permits it: all the
			// flag can prove is that the operator knows this institution is
			// already linked, not which login they are about to add.
			name:           "with two linked, naming the first permits a third",
			existing:       capOne,
			allowDuplicate: "item_1",
			want:           true,
		},
		{
			name:           "with two linked, naming the second permits a third",
			existing:       capOne,
			allowDuplicate: "item_2",
			want:           true,
		},
		{
			name:           "an unknown item id refuses",
			existing:       capOne,
			allowDuplicate: "item_nonexistent",
			want:           false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := permitsDuplicate(tc.existing, tc.allowDuplicate); got != tc.want {
				t.Errorf("permitsDuplicate(%v, %q) = %v, want %v",
					itemIDs(tc.existing), tc.allowDuplicate, got, tc.want)
			}
		})
	}
}

// An empty permission is the default, and must never be satisfied by an Item
// whose ID somehow reads as empty.
func TestPermitsDuplicate_EmptyPermissionNeverMatches(t *testing.T) {
	if permitsDuplicate([]Item{{ItemID: ""}}, "") {
		t.Error("an empty --duplicate-of must permit nothing")
	}
}

// The refusal is a dead end unless it says how to get through it. An operator
// with a genuine second login must not have to read the source to find out.
func TestDuplicateRefusal_NamesTheWayThrough(t *testing.T) {
	existing := []Item{testItem("item_1", "ins_capone", "Capital One")}

	msg := duplicateRefusal("Capital One", existing)

	for _, want := range []string{
		"Capital One",
		"item_1",
		"no Item was consumed",
		"--duplicate-of item_1",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, msg)
		}
	}
}

func TestItemIDs(t *testing.T) {
	got := itemIDs([]Item{
		testItem("item_1", "ins_capone", "Capital One"),
		testItem("item_2", "ins_capone", "Capital One"),
	})
	if got != "item item_1, item item_2" {
		t.Errorf("itemIDs = %q", got)
	}
}

// ---------------------------------------------------------------------------
// handleExchange, over the gate
//
// These drive the handler itself, not just its decision function, because the
// wiring is the part that fails silently: an opts field that never reaches
// the handler would leave every unit test above passing.
// ---------------------------------------------------------------------------

// fakeExchangeServer stands in for Plaid's /item/public_token/exchange. It
// records whether it was called at all, which is the assertion that matters:
// reaching it is what spends an Item.
type fakeExchangeServer struct {
	called bool
}

func newFakeExchangeClient(t *testing.T, accessToken, itemID string) (*plaidsdk.APIClient, *fakeExchangeServer) {
	t.Helper()

	fake := &fakeExchangeServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.called = true
		w.Header().Set("Content-Type", "application/json")
		body := fmt.Sprintf(`{"access_token":%q,"item_id":%q,"request_id":"req_test"}`,
			accessToken, itemID)
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("writing the fake exchange response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := plaidsdk.NewConfiguration()
	cfg.Servers = plaidsdk.ServerConfigurations{{URL: srv.URL}}
	return plaidsdk.NewAPIClient(cfg), fake
}

func exchangePost(t *testing.T, instID, instName string) *http.Request {
	t.Helper()
	body := fmt.Sprintf(`{"public_token":"public-sandbox-token","institution_id":%q,"institution_name":%q}`,
		instID, instName)
	return httptest.NewRequest(http.MethodPost, "/exchange", strings.NewReader(body))
}

func newTestSession(t *testing.T) *linkSession {
	t.Helper()
	session, err := newLinkSession(LinkOptions{AccessKey: "test-key"})
	if err != nil {
		t.Fatalf("newLinkSession: %v", err)
	}
	return session
}

// The refusal must land before Plaid is called, because the call is what
// creates the duplicate Item. A fake server that records being reached is the
// only way to prove the ordering.
func TestHandleExchange_RefusesADuplicateWithoutExchanging(t *testing.T) {
	useFakeItemStore(t)
	if err := SaveItem(Sandbox, testItem("item_1", "ins_capone", "Capital One")); err != nil {
		t.Fatal(err)
	}

	client, fake := newFakeExchangeClient(t, "access-new", "item_new")
	w := httptest.NewRecorder()
	finished := false

	handleExchange(context.Background(), w, exchangePost(t, "ins_capone", "Capital One"),
		Sandbox, client, newTestSession(t), "", func(LinkResult) { finished = true })

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", w.Code, http.StatusConflict)
	}
	if fake.called {
		t.Error("Plaid was called: the duplicate check ran too late to save the slot")
	}
	if finished {
		t.Error("the link completed despite the refusal")
	}
	if !strings.Contains(w.Body.String(), "--duplicate-of item_1") {
		t.Errorf("the refusal does not name the way through it:\n%s", w.Body.String())
	}

	// The existing Item is untouched, and no second one appeared.
	items, _, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ItemID != "item_1" {
		t.Errorf("stored items = %+v, want only item_1", items)
	}
}

// The whole point of the flag: a second login at a bank already linked.
func TestHandleExchange_DuplicateOfPermitsASecondLogin(t *testing.T) {
	useFakeItemStore(t)
	if err := SaveItem(Sandbox, testItem("item_1", "ins_capone", "Capital One")); err != nil {
		t.Fatal(err)
	}

	client, fake := newFakeExchangeClient(t, "access-second", "item_2")
	w := httptest.NewRecorder()
	var result LinkResult

	handleExchange(context.Background(), w, exchangePost(t, "ins_capone", "Capital One"),
		Sandbox, client, newTestSession(t), "item_1", func(res LinkResult) { result = res })

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", w.Code, w.Body.String())
	}
	if !fake.called {
		t.Error("the exchange never reached Plaid")
	}
	if result.ItemID != "item_2" {
		t.Errorf("result.ItemID = %q, want item_2", result.ItemID)
	}

	// Both logins are stored, and the first one's token was not disturbed.
	items, _, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("stored %d items, want both logins: %+v", len(items), items)
	}
	byID := map[string]Item{}
	for _, item := range items {
		byID[item.ItemID] = item
	}
	if byID["item_1"].AccessToken != "access-item_1" {
		t.Errorf("the existing item's token changed: %+v", byID["item_1"])
	}
	if byID["item_2"].AccessToken != "access-second" {
		t.Errorf("the new item's token = %q, want access-second", byID["item_2"].AccessToken)
	}
}

// Permission to duplicate Capital One must not license a second Chase, which
// is what a misclick in the browser's institution picker produces.
func TestHandleExchange_DuplicateOfDoesNotLicenseAnotherInstitution(t *testing.T) {
	useFakeItemStore(t)
	if err := SaveItem(Sandbox, testItem("item_1", "ins_capone", "Capital One")); err != nil {
		t.Fatal(err)
	}
	if err := SaveItem(Sandbox, testItem("item_9", "ins_chase", "Chase")); err != nil {
		t.Fatal(err)
	}

	client, fake := newFakeExchangeClient(t, "access-new", "item_new")
	w := httptest.NewRecorder()

	// Permission names the Capital One item; the browser reports Chase.
	handleExchange(context.Background(), w, exchangePost(t, "ins_chase", "Chase"),
		Sandbox, client, newTestSession(t), "item_1", func(LinkResult) {})

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", w.Code, http.StatusConflict)
	}
	if fake.called {
		t.Error("Plaid was called: a permission for one institution licensed another")
	}
}

// An institution with nothing linked is not a duplicate, and the flag being
// set must not change that.
func TestHandleExchange_FirstLinkAtAnInstitutionIsNotADuplicate(t *testing.T) {
	useFakeItemStore(t)
	if err := SaveItem(Sandbox, testItem("item_1", "ins_capone", "Capital One")); err != nil {
		t.Fatal(err)
	}

	client, fake := newFakeExchangeClient(t, "access-chase", "item_chase")
	w := httptest.NewRecorder()

	handleExchange(context.Background(), w, exchangePost(t, "ins_chase", "Chase"),
		Sandbox, client, newTestSession(t), "", func(LinkResult) {})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", w.Code, w.Body.String())
	}
	if !fake.called {
		t.Error("the exchange never reached Plaid")
	}
}

// ---------------------------------------------------------------------------
// The gate, reached through the real server
//
// The tests above call handleExchange directly, so they cannot see whether
// LinkOptions.DuplicateOfItemID ever reaches it. Severing that one line
// leaves every one of them passing, which is precisely the kind of break
// worth a slower test. This one drives StartLinkServer over HTTP, with the
// SDK pointed at a fake Plaid.
//
// It can prove the refusal, and it can prove the permission — but only
// because Plaid is fake here. Nothing in this package's tests may ever reach
// the real /item/public_token/exchange: that call is what spends one of the
// ten Items for the lifetime of the account, and no test is worth a slot.
// ---------------------------------------------------------------------------

// newFakePlaid answers the two endpoints a link flow touches: creating a link
// token, and exchanging a public token. It reports whether the exchange — the
// call that consumes an Item — was reached.
func newFakePlaid(t *testing.T, accessToken, itemID string) (*plaidsdk.APIClient, *fakeExchangeServer) {
	t.Helper()

	fake := &fakeExchangeServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/link/token/create", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body := `{"link_token":"link-sandbox-test","expiration":"2099-01-01T00:00:00Z","request_id":"req_test"}`
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("writing the fake link token response: %v", err)
		}
	})
	mux.HandleFunc("/item/public_token/exchange", func(w http.ResponseWriter, r *http.Request) {
		fake.called = true
		w.Header().Set("Content-Type", "application/json")
		body := fmt.Sprintf(`{"access_token":%q,"item_id":%q,"request_id":"req_test"}`,
			accessToken, itemID)
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("writing the fake exchange response: %v", err)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := plaidsdk.NewConfiguration()
	cfg.Servers = plaidsdk.ServerConfigurations{{URL: srv.URL}}
	return plaidsdk.NewAPIClient(cfg), fake
}

// freeAddr reserves a loopback port and releases it, so the link server can
// bind somewhere that will not collide with a developer's own run.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// runLinkServer starts StartLinkServer against a fake Plaid and returns a
// browser-ish client that has already been admitted to the session.
func runLinkServer(t *testing.T, opts LinkOptions, client *plaidsdk.APIClient) (baseURL string, http4 *http.Client, result func() (LinkResult, error)) {
	t.Helper()

	opts.AccessKey = "test-access-key"
	opts.BindAddr = freeAddr(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	type outcome struct {
		res LinkResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := StartLinkServer(ctx, Sandbox, client, opts)
		done <- outcome{res, err}
	}()

	base := "http://" + opts.BindAddr
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Jar: jar}

	// Wait for the listener, then take the entry page, which mints the cookie
	// every other handler requires.
	var resp *http.Response
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = browser.Get(base + "/?key=" + opts.AccessKey)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the link server never served the entry page: %v", err)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close entry page body: %v", cerr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("entry page status = %d, want 200", resp.StatusCode)
	}

	return base, browser, func() (LinkResult, error) {
		select {
		case o := <-done:
			return o.res, o.err
		case <-time.After(10 * time.Second):
			t.Fatal("the link server never returned")
			return LinkResult{}, nil
		}
	}
}

func postExchange(t *testing.T, browser *http.Client, baseURL, instID, instName string) *http.Response {
	t.Helper()
	body := fmt.Sprintf(`{"public_token":"public-sandbox-token","institution_id":%q,"institution_name":%q}`,
		instID, instName)
	resp, err := browser.Post(baseURL+"/exchange", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /exchange: %v", err)
	}
	return resp
}

func TestStartLinkServer_RefusesADuplicateByDefault(t *testing.T) {
	useFakeItemStore(t)
	if err := SaveItem(Sandbox, testItem("item_1", "ins_capone", "Capital One")); err != nil {
		t.Fatal(err)
	}

	client, fake := newFakePlaid(t, "access-new", "item_new")
	base, browser, _ := runLinkServer(t, LinkOptions{}, client)

	resp := postExchange(t, browser, base, "ins_capone", "Capital One")
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close body: %v", cerr)
	}

	if resp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
	if fake.called {
		t.Error("Plaid was called: the refusal came too late to save the slot")
	}
	if !strings.Contains(string(body), "--duplicate-of item_1") {
		t.Errorf("the refusal does not name the way through it:\n%s", body)
	}
}

// This is the test that catches a severed LinkOptions.DuplicateOfItemID: with
// the option set, the same request that was refused above must go through.
func TestStartLinkServer_DuplicateOfReachesTheGate(t *testing.T) {
	useFakeItemStore(t)
	if err := SaveItem(Sandbox, testItem("item_1", "ins_capone", "Capital One")); err != nil {
		t.Fatal(err)
	}

	client, fake := newFakePlaid(t, "access-second", "item_2")
	base, browser, await := runLinkServer(t, LinkOptions{DuplicateOfItemID: "item_1"}, client)

	resp := postExchange(t, browser, base, "ins_capone", "Capital One")
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close body: %v", cerr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", resp.StatusCode, body)
	}
	if !fake.called {
		t.Error("the exchange never reached Plaid")
	}

	result, err := await()
	if err != nil {
		t.Fatalf("StartLinkServer: %v", err)
	}
	if result.ItemID != "item_2" {
		t.Errorf("result.ItemID = %q, want item_2", result.ItemID)
	}

	items, _, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("stored %d items, want both logins: %+v", len(items), items)
	}
}
