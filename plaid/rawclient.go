package plaid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/jeffbstewart/bankferry/money"
)

// plaidAPIVersion pins the response shape. Plaid guarantees a versioned
// contract; sending this header means new API versions cannot silently
// change what we parse.
const plaidAPIVersion = "2020-09-14"

const httpRequestTimeout = 60 * time.Second

// DataClient talks to Plaid's data endpoints over plain HTTP and JSON.
//
// The generated SDK is used for the Link lifecycle, where the request
// shapes are fiddly and no monetary value appears. It is deliberately not
// used here: the SDK decodes amounts into float64, which destroys the
// exact decimal literal Plaid sends. This client decodes with json.Number
// and parses straight into money.Amount, so no monetary value ever touches
// a binary float.
type DataClient struct {
	baseURL string
	creds   Credentials
	http    *http.Client
}

func dataBaseURL(env Environment) (string, error) {
	switch env {
	case Sandbox:
		return "https://sandbox.plaid.com", nil
	case Production:
		return "https://production.plaid.com", nil
	default:
		return "", fmt.Errorf("plaid: unsupported environment %q", env)
	}
}

// NewDataClient builds the JSON client for an environment, validating the
// environment before reading any credential.
func NewDataClient(env Environment, creds Credentials) (*DataClient, error) {
	base, err := dataBaseURL(env)
	if err != nil {
		return nil, err
	}
	if creds.ClientID == "" || creds.Secret == "" {
		return nil, errors.New("plaid: credentials are incomplete")
	}

	return &DataClient{
		baseURL: base,
		creds:   creds,
		http:    &http.Client{Timeout: httpRequestTimeout},
	}, nil
}

// APIError is a failed Plaid call, carrying Plaid's own error body. It is
// produced by both transports so callers see one error shape.
//
// It never contains a credential: Plaid does not echo client_id or secret,
// and the request payload is never attached.
type APIError struct {
	Op         string
	StatusCode int

	ErrorType    string `json:"error_type"`
	ErrorCode    string `json:"error_code"`
	ErrorMessage string `json:"error_message"`
	RequestID    string `json:"request_id"`

	// Body is the raw response when it could not be parsed as a Plaid
	// error envelope.
	Body string
}

func (e *APIError) Error() string {
	if e.ErrorCode != "" {
		return fmt.Sprintf("plaid: %s: %s (%s): %s [request_id=%s]",
			e.Op, e.ErrorCode, e.ErrorType, e.ErrorMessage, e.RequestID)
	}
	if e.Body != "" {
		return fmt.Sprintf("plaid: %s: status %d: %s", e.Op, e.StatusCode, e.Body)
	}
	return fmt.Sprintf("plaid: %s: status %d", e.Op, e.StatusCode)
}

// newAPIError parses a Plaid error envelope out of a response body.
func newAPIError(op string, status int, body []byte) *APIError {
	e := &APIError{Op: op, StatusCode: status}
	if err := json.Unmarshal(body, e); err != nil || e.ErrorCode == "" {
		e.Body = string(bytes.TrimSpace(body))
	}
	return e
}

// ErrorCodeItemLoginRequired means the Item's authorization has lapsed
// and the user must re-authenticate through Link update mode. There is no
// unattended repair.
const ErrorCodeItemLoginRequired = "ITEM_LOGIN_REQUIRED"

// IsLinkRefreshRequired reports whether an error means the Item can only be
// repaired by sending the user back through Link in update mode. Repair
// always needs a human and a browser; nothing here can fix it unattended.
func IsLinkRefreshRequired(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.ErrorCode == ErrorCodeItemLoginRequired
}

// post sends a JSON body and decodes the response with json.Number so
// numeric literals survive intact.
func (c *DataClient) post(ctx context.Context, op, path string, body map[string]any, out any) error {
	// Copy so credentials never leak into the caller's map.
	payload := make(map[string]any, len(body)+2)
	for k, v := range body {
		payload[k] = v
	}
	payload["client_id"] = c.creds.ClientID
	payload["secret"] = c.creds.Secret

	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("plaid: %s: encoding request: %w", op, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("plaid: %s: building request: %w", op, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Plaid-Version", plaidAPIVersion)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("plaid: %s: %w", op, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("plaid: close response body: %v", cerr)
		}
	}()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("plaid: %s: reading response: %w", op, err)
	}

	if resp.StatusCode != http.StatusOK {
		return newAPIError(op, resp.StatusCode, raw)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("plaid: %s: decoding response: %w", op, err)
	}
	return nil
}

// parseAmount turns a Plaid numeric literal and its currency fields into
// an exact Amount.
//
// Plaid sets exactly one of iso_currency_code and unofficial_currency_code.
// An unofficial currency, or any currency other than USD, is refused
// rather than coerced: an OFX statement carries a single CURDEF and this
// pipeline performs no conversion.
func parseAmount(what string, n *json.Number, iso, unofficial *string) (money.Amount, error) {
	if n == nil {
		return money.Amount{}, fmt.Errorf("plaid: %s has no amount", what)
	}
	if unofficial != nil {
		return money.Amount{}, fmt.Errorf("plaid: %s is denominated in unofficial currency %q",
			what, *unofficial)
	}
	if iso == nil {
		return money.Amount{}, fmt.Errorf("plaid: %s has no currency", what)
	}

	currency, err := money.ParseCurrency(*iso)
	if err != nil {
		return money.Amount{}, fmt.Errorf("plaid: %s: %w", what, err)
	}

	amount, err := money.Parse(n.String(), currency)
	if err != nil {
		return money.Amount{}, fmt.Errorf("plaid: %s: %w", what, err)
	}
	return amount, nil
}
