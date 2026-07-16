package plaid

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	plaidsdk "github.com/plaid/plaid-go/v43/plaid"
)

// clientName is shown to the user inside the Link flow.
const clientName = "bankferry"

// clientUserID identifies the end user to Plaid. This tool serves a
// single person — the operator — so the value is constant.
const clientUserID = "bankferry-local-user"

// historyDaysRequested is the transaction history window requested when
// an Item is created. Plaid fixes this at link time: "Once Transactions
// has been added to an Item, this value cannot be updated." 730 is the
// maximum, so ask for all of it — there is no second chance.
const historyDaysRequested = 730

// sdkEnvironment maps our Environment onto the SDK's base URL constant.
func sdkEnvironment(env Environment) (plaidsdk.Environment, error) {
	switch env {
	case Sandbox:
		return plaidsdk.Sandbox, nil
	case Production:
		return plaidsdk.Production, nil
	default:
		return "", fmt.Errorf("plaid: unsupported environment %q", env)
	}
}

// NewClient builds a Plaid API client for the given environment, reading
// credentials from the OS keyring.
//
// The environment is validated before any credential is read, so refusing
// production never depends on whether a production secret happens to be
// stored.
func NewClient(env Environment, creds Credentials) (*plaidsdk.APIClient, error) {
	base, err := sdkEnvironment(env)
	if err != nil {
		return nil, err
	}
	if creds.ClientID == "" || creds.Secret == "" {
		return nil, errors.New("plaid: credentials are incomplete")
	}

	cfg := plaidsdk.NewConfiguration()
	cfg.AddDefaultHeader("PLAID-CLIENT-ID", creds.ClientID)
	cfg.AddDefaultHeader("PLAID-SECRET", creds.Secret)
	cfg.UseEnvironment(base)
	// Without an explicit client the SDK uses http.DefaultClient, whose timeout
	// is zero — so a stalled connection during ExchangePublicToken (the call
	// that spends an Item) would hang until the caller's context, if any, fired.
	// Bound every SDK call the same way the raw client bounds its own.
	cfg.HTTPClient = &http.Client{Timeout: httpRequestTimeout}

	return plaidsdk.NewAPIClient(cfg), nil
}

// apiError turns a failed SDK call into an error carrying Plaid's own
// error body, which is far more useful than the generic message. The
// body never contains credentials.
func apiError(op string, httpResp *http.Response, err error) error {
	if httpResp == nil {
		return fmt.Errorf("plaid: %s: %w", op, err)
	}
	defer func() {
		if cerr := httpResp.Body.Close(); cerr != nil {
			log.Printf("plaid: close response body: %v", cerr)
		}
	}()

	body, readErr := io.ReadAll(httpResp.Body)
	if readErr != nil {
		return fmt.Errorf("plaid: %s: %w (status %d, body unreadable: %v)",
			op, err, httpResp.StatusCode, readErr)
	}
	if len(body) == 0 {
		return fmt.Errorf("plaid: %s: %w (status %d)", op, err, httpResp.StatusCode)
	}
	return fmt.Errorf("plaid: %s: %w (status %d): %s", op, err, httpResp.StatusCode, body)
}

// CreateLinkToken requests a short-lived link_token used to initialize
// Link in the browser.
//
// redirectURI may be empty. It is required for OAuth institutions, which
// includes Chase, and Plaid demands it be registered in the Dashboard under
// Allowed redirect URIs and served over HTTPS. Only Sandbox permits an
// http://localhost redirect.
func CreateLinkToken(ctx context.Context, client *plaidsdk.APIClient, redirectURI string) (string, error) {
	if err := ValidateRedirectURI(redirectURI); err != nil {
		return "", err
	}

	user := plaidsdk.NewLinkTokenCreateRequestUser(clientUserID)

	req := plaidsdk.NewLinkTokenCreateRequest(
		clientName,
		"en",
		[]plaidsdk.CountryCode{plaidsdk.COUNTRYCODE_US},
	)
	req.SetUser(*user)
	req.SetProducts([]plaidsdk.Products{plaidsdk.PRODUCTS_TRANSACTIONS})
	if redirectURI != "" {
		req.SetRedirectUri(redirectURI)
	}

	txns := plaidsdk.NewLinkTokenTransactions()
	txns.SetDaysRequested(historyDaysRequested)
	req.SetTransactions(*txns)

	resp, httpResp, err := client.PlaidApi.LinkTokenCreate(ctx).
		LinkTokenCreateRequest(*req).Execute()
	if err != nil {
		return "", apiError("link token create", httpResp, err)
	}

	return resp.GetLinkToken(), nil
}

// CreateUpdateLinkToken requests a link_token that opens Link in update
// mode, repairing an existing Item rather than creating a new one.
//
// No products are set. Plaid: "No products should be added to the products
// array and no product-specific request parameters should be specified when
// creating a link_token for update mode."
//
// Nothing is exchanged afterwards, and nothing is stored: "You do not need
// to repeat the /item/public_token/exchange process when a user uses Link
// in update mode. The Item's access_token has not changed." Exchanging the
// public_token here would be a mistake, and on production could cost one of
// the ten Items allowed for the lifetime of the account.
func CreateUpdateLinkToken(ctx context.Context, client *plaidsdk.APIClient, accessToken, redirectURI string) (string, error) {
	if err := ValidateRedirectURI(redirectURI); err != nil {
		return "", err
	}

	user := plaidsdk.NewLinkTokenCreateRequestUser(clientUserID)

	req := plaidsdk.NewLinkTokenCreateRequest(
		clientName,
		"en",
		[]plaidsdk.CountryCode{plaidsdk.COUNTRYCODE_US},
	)
	req.SetUser(*user)
	req.SetAccessToken(accessToken)
	if redirectURI != "" {
		req.SetRedirectUri(redirectURI)
	}

	resp, httpResp, err := client.PlaidApi.LinkTokenCreate(ctx).
		LinkTokenCreateRequest(*req).Execute()
	if err != nil {
		return "", apiError("update link token create", httpResp, err)
	}

	return resp.GetLinkToken(), nil
}

// SandboxResetLogin forces a sandbox Item into ITEM_LOGIN_REQUIRED, so the
// update-mode flow can be exercised without waiting for real consent to
// lapse. Sandbox Items also enter that state on their own 30 days after
// creation.
func SandboxResetLogin(ctx context.Context, env Environment, client *plaidsdk.APIClient, accessToken string) error {
	if env != Sandbox {
		return fmt.Errorf("plaid: reset login is a sandbox-only operation, not %s", env)
	}

	req := plaidsdk.NewSandboxItemResetLoginRequest(accessToken)

	_, httpResp, err := client.PlaidApi.SandboxItemResetLogin(ctx).
		SandboxItemResetLoginRequest(*req).Execute()
	if err != nil {
		return apiError("sandbox item reset login", httpResp, err)
	}

	log.Printf("plaid: sandbox item forced into ITEM_LOGIN_REQUIRED")
	return nil
}

// RemoveItem asks Plaid to delete the Item behind an access token. It is
// the only cleanup available once a token exists, and it needs the token
// itself, so it must be called while the token is still in hand.
//
// On the Trial plan this does not return the Item's slot: ten Items are
// allowed for the lifetime of the account whether or not they are
// removed. Removing an orphan still stops it billing on a paid plan and
// leaves no dangling authorization at the institution.
//
// Every call is logged. Destroying an Item spends one of ten
// irreplaceable slots and cannot be undone, so it should never happen
// without a trace. itemID is taken separately for the log; the access
// token is never logged.
func RemoveItem(ctx context.Context, client *plaidsdk.APIClient, accessToken, itemID string) error {
	log.Printf("plaid: removing item %s at Plaid. This spends one of the ten "+
		"Items allowed for the lifetime of the account; removal does not return the slot.", itemID)

	req := plaidsdk.NewItemRemoveRequest(accessToken)

	_, httpResp, err := client.PlaidApi.ItemRemove(ctx).
		ItemRemoveRequest(*req).Execute()
	if err != nil {
		wrapped := apiError("item remove", httpResp, err)
		log.Printf("plaid: item %s was NOT removed: %v", itemID, wrapped)
		return wrapped
	}

	log.Printf("plaid: item %s removed at Plaid", itemID)
	return nil
}

// ExchangePublicToken trades the short-lived public_token produced by
// Link for a long-lived access_token and its item_id.
func ExchangePublicToken(ctx context.Context, client *plaidsdk.APIClient, publicToken string) (accessToken, itemID string, err error) {
	req := plaidsdk.NewItemPublicTokenExchangeRequest(publicToken)

	resp, httpResp, err := client.PlaidApi.ItemPublicTokenExchange(ctx).
		ItemPublicTokenExchangeRequest(*req).Execute()
	if err != nil {
		return "", "", apiError("public token exchange", httpResp, err)
	}

	return resp.GetAccessToken(), resp.GetItemId(), nil
}
