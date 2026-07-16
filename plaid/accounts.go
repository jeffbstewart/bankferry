package plaid

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jeffbstewart/bankferry/money"
)

// Account types Plaid reports. Only depository and credit produce OFX
// statements; the rest are ignored by the adapter.
const (
	AccountTypeDepository = "depository"
	AccountTypeCredit     = "credit"
)

// Balance holds the balances Plaid reports for an account. Each is
// nullable: Plaid omits available for most credit accounts and limit for
// most depository ones, and an absent balance is meaningfully different
// from a balance of zero.
type Balance struct {
	Current   *money.Amount
	Available *money.Amount
	Limit     *money.Amount
}

// AccountInfo is one account as Plaid describes it. It mirrors the API
// rather than the OFX pipeline; mapping onto source.Account happens in an
// adapter.
type AccountInfo struct {
	AccountID    string
	Name         string
	OfficialName string

	// Mask is the last 2 to 4 characters of the account number, not
	// always 4, and sometimes absent.
	Mask string

	// Type is depository, credit, loan, investment, brokerage, or other.
	// Subtype refines it: checking, savings, credit card, 401k, and so on.
	Type    string
	Subtype string

	Currency money.Currency
	Balance  Balance
}

// ItemInfo identifies the institution behind an Item.
type ItemInfo struct {
	ItemID          string
	InstitutionID   string
	InstitutionName string
}

type wireBalances struct {
	Current   *json.Number `json:"current"`
	Available *json.Number `json:"available"`
	Limit     *json.Number `json:"limit"`

	// Plaid sets exactly one of these.
	IsoCurrencyCode        *string `json:"iso_currency_code"`
	UnofficialCurrencyCode *string `json:"unofficial_currency_code"`
}

type wireAccount struct {
	AccountID    string       `json:"account_id"`
	Name         string       `json:"name"`
	OfficialName *string      `json:"official_name"`
	Mask         *string      `json:"mask"`
	Type         string       `json:"type"`
	Subtype      *string      `json:"subtype"`
	Balances     wireBalances `json:"balances"`
}

type wireAccountsGet struct {
	Accounts []wireAccount `json:"accounts"`
	Item     struct {
		ItemID          string  `json:"item_id"`
		InstitutionID   *string `json:"institution_id"`
		InstitutionName *string `json:"institution_name"`
	} `json:"item"`
}

// FetchAccounts returns every account under an Item, and the Item's own
// institution metadata.
//
// It decodes the raw JSON rather than going through the SDK, so that
// balances keep the exact decimal literal Plaid sent. A 401k balance of
// 23631.9805 arrives with all four decimal places intact.
func (c *DataClient) FetchAccounts(ctx context.Context, accessToken string) ([]AccountInfo, ItemInfo, error) {
	var resp wireAccountsGet
	err := c.post(ctx, "accounts get", "/accounts/get", map[string]any{
		"access_token": accessToken,
	}, &resp)
	if err != nil {
		return nil, ItemInfo{}, err
	}

	item := ItemInfo{ItemID: resp.Item.ItemID}
	if resp.Item.InstitutionID != nil {
		item.InstitutionID = *resp.Item.InstitutionID
	}
	if resp.Item.InstitutionName != nil {
		item.InstitutionName = *resp.Item.InstitutionName
	}

	accounts := make([]AccountInfo, 0, len(resp.Accounts))
	for _, a := range resp.Accounts {
		account, err := convertAccount(a)
		if err != nil {
			return nil, ItemInfo{}, err
		}
		accounts = append(accounts, account)
	}

	return accounts, item, nil
}

func convertAccount(a wireAccount) (AccountInfo, error) {
	what := fmt.Sprintf("account %s (%s)", a.AccountID, a.Name)

	bal := a.Balances
	if bal.UnofficialCurrencyCode != nil {
		return AccountInfo{}, fmt.Errorf("plaid: %s is denominated in unofficial currency %q",
			what, *bal.UnofficialCurrencyCode)
	}
	if bal.IsoCurrencyCode == nil {
		return AccountInfo{}, fmt.Errorf("plaid: %s reports no currency", what)
	}
	currency, err := money.ParseCurrency(*bal.IsoCurrencyCode)
	if err != nil {
		return AccountInfo{}, fmt.Errorf("plaid: %s: %w", what, err)
	}

	current, err := optionalAmount(what+" current balance", bal.Current, bal.IsoCurrencyCode, bal.UnofficialCurrencyCode)
	if err != nil {
		return AccountInfo{}, err
	}
	available, err := optionalAmount(what+" available balance", bal.Available, bal.IsoCurrencyCode, bal.UnofficialCurrencyCode)
	if err != nil {
		return AccountInfo{}, err
	}
	limit, err := optionalAmount(what+" limit", bal.Limit, bal.IsoCurrencyCode, bal.UnofficialCurrencyCode)
	if err != nil {
		return AccountInfo{}, err
	}

	out := AccountInfo{
		AccountID: a.AccountID,
		Name:      a.Name,
		Type:      a.Type,
		Currency:  currency,
		Balance:   Balance{Current: current, Available: available, Limit: limit},
	}
	if a.OfficialName != nil {
		out.OfficialName = *a.OfficialName
	}
	if a.Mask != nil {
		out.Mask = *a.Mask
	}
	if a.Subtype != nil {
		out.Subtype = *a.Subtype
	}
	return out, nil
}

// optionalAmount parses a nullable monetary field. A null value yields nil,
// which is not the same as zero.
func optionalAmount(what string, n *json.Number, iso, unofficial *string) (*money.Amount, error) {
	if n == nil {
		return nil, nil
	}
	amount, err := parseAmount(what, n, iso, unofficial)
	if err != nil {
		return nil, err
	}
	return &amount, nil
}
