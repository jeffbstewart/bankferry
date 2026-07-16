package plaid

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeffbstewart/bankferry/money"
)

// newTestClient points a rawClient at a stub server.
func newTestClient(t *testing.T, handler http.HandlerFunc) *DataClient {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return &DataClient{
		baseURL: srv.URL,
		creds:   Credentials{ClientID: "cid_test", Secret: "sec_test"},
		http:    srv.Client(),
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("writing stub response: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

// Credentials are injected into the request, the API version is pinned,
// and the caller's map is never mutated.
func TestRawClient_PostSendsCredentialsAndVersion(t *testing.T) {
	var gotBody map[string]any
	var gotVersion, gotContentType string

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.Header.Get("Plaid-Version")
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		writeJSON(t, w, http.StatusOK, `{}`)
	})

	caller := map[string]any{"access_token": "tok_1"}
	var out struct{}
	if err := c.post(context.Background(), "test", "/x", caller, &out); err != nil {
		t.Fatalf("post: %v", err)
	}

	if gotVersion != plaidAPIVersion {
		t.Errorf("Plaid-Version = %q, want %q", gotVersion, plaidAPIVersion)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	if gotBody["client_id"] != "cid_test" || gotBody["secret"] != "sec_test" {
		t.Error("credentials were not sent")
	}
	if gotBody["access_token"] != "tok_1" {
		t.Error("caller field was not sent")
	}

	// The caller's map must not have gained the credentials.
	if _, ok := caller["secret"]; ok {
		t.Error("post leaked the secret into the caller's map")
	}
	if len(caller) != 1 {
		t.Errorf("caller map was mutated: %v", caller)
	}
}

// Numeric literals must survive decoding untouched.
func TestRawClient_DecodesWithJSONNumber(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"v": 23631.9805}`)
	})

	var out struct {
		V json.Number `json:"v"`
	}
	if err := c.post(context.Background(), "test", "/x", nil, &out); err != nil {
		t.Fatalf("post: %v", err)
	}
	if out.V.String() != "23631.9805" {
		t.Errorf("v = %q, want the exact literal", out.V.String())
	}
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

func TestRawClient_ParsesPlaidErrorEnvelope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, `{
			"error_type": "ITEM_ERROR",
			"error_code": "ITEM_LOGIN_REQUIRED",
			"error_message": "the login details of this item have changed",
			"request_id": "req_123"
		}`)
	})

	err := c.post(context.Background(), "sync", "/x", nil, &struct{}{})
	if err == nil {
		t.Fatal("expected an error")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %T, want *APIError", err)
	}
	if apiErr.ErrorCode != "ITEM_LOGIN_REQUIRED" || apiErr.RequestID != "req_123" {
		t.Errorf("apiErr = %+v", apiErr)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", apiErr.StatusCode)
	}

	// The message must name the request_id, which is what Plaid support asks for.
	if !strings.Contains(err.Error(), "req_123") {
		t.Errorf("error text lacks the request id: %v", err)
	}
}

// A body that is not a Plaid error envelope is preserved rather than lost.
func TestRawClient_NonEnvelopeErrorKeepsBody(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusBadGateway, `upstream exploded`)
	})

	err := c.post(context.Background(), "sync", "/x", nil, &struct{}{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %T, want *APIError", err)
	}
	if apiErr.Body != "upstream exploded" {
		t.Errorf("body = %q", apiErr.Body)
	}
}

func TestIsLinkRefreshRequired(t *testing.T) {
	refresh := &APIError{Op: "sync", ErrorCode: ErrorCodeItemLoginRequired}
	other := &APIError{Op: "sync", ErrorCode: "RATE_LIMIT_EXCEEDED"}

	if !IsLinkRefreshRequired(refresh) {
		t.Error("ITEM_LOGIN_REQUIRED should require a link refresh")
	}
	if IsLinkRefreshRequired(other) {
		t.Error("RATE_LIMIT_EXCEEDED must not require a link refresh")
	}
	if IsLinkRefreshRequired(errors.New("network down")) {
		t.Error("a plain error must not require a link refresh")
	}
	if IsLinkRefreshRequired(nil) {
		t.Error("nil must not require a link refresh")
	}
}

// ---------------------------------------------------------------------------
// parseAmount
// ---------------------------------------------------------------------------

func num(s string) *json.Number {
	n := json.Number(s)
	return &n
}

func str(s string) *string { return &s }

func TestParseAmount(t *testing.T) {
	got, err := parseAmount("txn_1", num("89.4"), str("USD"), nil)
	if err != nil {
		t.Fatalf("parseAmount: %v", err)
	}
	if e := mustExact(t, got); e != "89.40" {
		t.Errorf("Exact() = %q", e)
	}
	if !got.CurrencyIs(money.USD) {
		t.Error("currency was not USD")
	}
}

// A currency Plaid does not officially support must be refused, never
// coerced into dollars.
func TestParseAmount_RejectsUnofficialCurrency(t *testing.T) {
	if _, err := parseAmount("txn_1", num("1.0"), nil, str("BTC")); err == nil {
		t.Fatal("expected an error for an unofficial currency")
	}
}

func TestParseAmount_RejectsNonUSD(t *testing.T) {
	_, err := parseAmount("txn_1", num("1.0"), str("EUR"), nil)
	if !errors.Is(err, money.ErrCurrency) {
		t.Errorf("err = %v, want ErrCurrency", err)
	}
}

func TestParseAmount_RejectsMissingFields(t *testing.T) {
	if _, err := parseAmount("txn_1", nil, str("USD"), nil); err == nil {
		t.Error("expected an error for a missing amount")
	}
	if _, err := parseAmount("txn_1", num("1.0"), nil, nil); err == nil {
		t.Error("expected an error for a missing currency")
	}
}
