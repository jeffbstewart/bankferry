package plaid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jeffbstewart/bankferry/civildate"
	"github.com/jeffbstewart/bankferry/money"
)

// This file reads Plaid's investments product: holdings and investment
// transactions. It is brokerferry's, not bankferry's; nothing in the OFX
// pipeline calls it.
//
// Every number here is carried as the exact decimal literal Plaid sent,
// validated but never parsed into a binary float and never converted. That
// is a different discipline from the transactions client, which parses into
// money.Amount so it can be signed and written into a USD statement. An
// investments snapshot is handed to finance2 verbatim: quantities are not
// money, prices may be in any currency, and the contract
// (proto/plaid_snapshot.proto) says the reader gets the string we got.

// DecimalAmount is an exact decimal literal and the ISO 4217 code it is
// denominated in. Value is what Plaid sent, checked against the decimal
// grammar and otherwise untouched.
type DecimalAmount struct {
	Value    string
	Currency string
}

// InvestmentAccount is an investment account as /investments/holdings/get
// describes it.
type InvestmentAccount struct {
	AccountID    string
	Name         string
	OfficialName string
	Mask         string
	Type         string
	Subtype      string

	// Current is the institution-reported total value. Available, when
	// Plaid reports it, is the cash component; nil otherwise, which finance2
	// treats differently from zero.
	Current   *DecimalAmount
	Available *DecimalAmount
}

// Security is one entry of the securities[] table a holdings or
// transactions response carries. Any of Ticker, CUSIP and ISIN may be empty.
type Security struct {
	ID               string
	Ticker           string
	CUSIP            string
	ISIN             string
	Name             string
	Type             string
	Currency         string
	IsCashEquivalent bool
}

// Holding is one position. Quantity is a unitless decimal literal. The
// monetary fields are nil when Plaid omits them.
type Holding struct {
	AccountID  string
	SecurityID string
	Quantity   string

	CostBasis        *DecimalAmount
	InstitutionPrice *DecimalAmount
	InstitutionValue *DecimalAmount

	// PriceAsOf is zero when Plaid does not say.
	PriceAsOf civildate.ISO8601Date
}

// InvestmentTransaction is one row of /investments/transactions/get.
// SecurityID is empty for account-level cash and fee rows.
type InvestmentTransaction struct {
	ID         string
	AccountID  string
	SecurityID string
	Date       civildate.ISO8601Date
	Name       string
	Type       string
	Subtype    string
	Quantity   string
	Amount     DecimalAmount
	Price      *DecimalAmount
	Fees       *DecimalAmount
}

// HoldingsResult is everything /investments/holdings/get returns: the
// Item's investment accounts, their positions, and the securities those
// positions refer to.
type HoldingsResult struct {
	Item       ItemInfo
	Accounts   []InvestmentAccount
	Holdings   []Holding
	Securities []Security
}

// InvestmentTransactionsResult is every transaction in a date window, with
// the securities they refer to.
type InvestmentTransactionsResult struct {
	Transactions []InvestmentTransaction
	Securities   []Security
}

// investmentTxnPageSize is Plaid's maximum count per page.
const investmentTxnPageSize = 500

// investmentTxnPageLimit bounds the pagination loop. 200 pages is 100,000
// transactions, far past any window a brokerage account produces.
const investmentTxnPageLimit = 200

// --- wire -----------------------------------------------------------------

type wireCurrency struct {
	IsoCurrencyCode        *string `json:"iso_currency_code"`
	UnofficialCurrencyCode *string `json:"unofficial_currency_code"`
}

type wireSecurity struct {
	SecurityID       string  `json:"security_id"`
	TickerSymbol     *string `json:"ticker_symbol"`
	CUSIP            *string `json:"cusip"`
	ISIN             *string `json:"isin"`
	Name             *string `json:"name"`
	Type             *string `json:"type"`
	IsCashEquivalent *bool   `json:"is_cash_equivalent"`
	wireCurrency
}

type wireHolding struct {
	AccountID            string       `json:"account_id"`
	SecurityID           string       `json:"security_id"`
	Quantity             *json.Number `json:"quantity"`
	CostBasis            *json.Number `json:"cost_basis"`
	InstitutionPrice     *json.Number `json:"institution_price"`
	InstitutionPriceAsOf *string      `json:"institution_price_as_of"`
	InstitutionValue     *json.Number `json:"institution_value"`
	wireCurrency
}

type wireHoldingsGet struct {
	Accounts   []wireAccount  `json:"accounts"`
	Holdings   []wireHolding  `json:"holdings"`
	Securities []wireSecurity `json:"securities"`
	Item       struct {
		ItemID          string  `json:"item_id"`
		InstitutionID   *string `json:"institution_id"`
		InstitutionName *string `json:"institution_name"`
	} `json:"item"`
}

type wireInvestmentTransaction struct {
	InvestmentTransactionID string       `json:"investment_transaction_id"`
	AccountID               string       `json:"account_id"`
	SecurityID              *string      `json:"security_id"`
	Date                    string       `json:"date"`
	Name                    string       `json:"name"`
	Quantity                *json.Number `json:"quantity"`
	Amount                  *json.Number `json:"amount"`
	Price                   *json.Number `json:"price"`
	Fees                    *json.Number `json:"fees"`
	Type                    string       `json:"type"`
	Subtype                 string       `json:"subtype"`
	wireCurrency
}

type wireInvestmentTransactionsGet struct {
	InvestmentTransactions      []wireInvestmentTransaction `json:"investment_transactions"`
	Securities                  []wireSecurity              `json:"securities"`
	TotalInvestmentTransactions int                         `json:"total_investment_transactions"`
}

// --- calls ------------------------------------------------------------------

// FetchHoldings returns the Item's investment accounts, positions and
// securities as of now. Holdings are levels, not deltas: every call is the
// full state, and nothing about it is cursor-driven.
func (c *DataClient) FetchHoldings(ctx context.Context, accessToken string) (HoldingsResult, error) {
	var resp wireHoldingsGet
	err := c.post(ctx, "investments holdings get", "/investments/holdings/get", map[string]any{
		"access_token": accessToken,
	}, &resp)
	if err != nil {
		return HoldingsResult{}, err
	}

	result := HoldingsResult{Item: ItemInfo{ItemID: resp.Item.ItemID}}
	if resp.Item.InstitutionID != nil {
		result.Item.InstitutionID = *resp.Item.InstitutionID
	}
	if resp.Item.InstitutionName != nil {
		result.Item.InstitutionName = *resp.Item.InstitutionName
	}

	for _, a := range resp.Accounts {
		account, err := convertInvestmentAccount(a)
		if err != nil {
			return HoldingsResult{}, err
		}
		result.Accounts = append(result.Accounts, account)
	}
	for _, h := range resp.Holdings {
		holding, err := convertHolding(h)
		if err != nil {
			return HoldingsResult{}, err
		}
		result.Holdings = append(result.Holdings, holding)
	}
	securities, err := convertSecurities(resp.Securities)
	if err != nil {
		return HoldingsResult{}, err
	}
	result.Securities = securities

	return result, nil
}

// FetchInvestmentTransactions returns every investment transaction dated
// within [start, end], inclusive, paging until Plaid's reported total is
// reached. A failure on any page discards the whole result: a partial
// window would look complete to the reader.
func (c *DataClient) FetchInvestmentTransactions(ctx context.Context, accessToken string, start, end civildate.ISO8601Date) (InvestmentTransactionsResult, error) {
	if start.IsZero() || end.IsZero() {
		return InvestmentTransactionsResult{}, errors.New("plaid: investment transactions need a start and end date")
	}
	if start.Compare(end) > 0 {
		return InvestmentTransactionsResult{}, fmt.Errorf("plaid: investment transactions window is inverted (%s after %s)", start, end)
	}

	var result InvestmentTransactionsResult
	seen := make(map[string]bool)
	offset := 0

	for page := 0; ; page++ {
		if page >= investmentTxnPageLimit {
			return InvestmentTransactionsResult{}, fmt.Errorf(
				"plaid: investment transactions did not finish after %d pages", investmentTxnPageLimit)
		}

		var resp wireInvestmentTransactionsGet
		err := c.post(ctx, "investments transactions get", "/investments/transactions/get", map[string]any{
			"access_token": accessToken,
			"start_date":   start.String(),
			"end_date":     end.String(),
			"options": map[string]any{
				"count":  investmentTxnPageSize,
				"offset": offset,
			},
		}, &resp)
		if err != nil {
			return InvestmentTransactionsResult{}, err
		}

		for _, t := range resp.InvestmentTransactions {
			txn, err := convertInvestmentTransaction(t)
			if err != nil {
				return InvestmentTransactionsResult{}, err
			}
			result.Transactions = append(result.Transactions, txn)
		}
		// The securities table repeats per page; keep each once.
		securities, err := convertSecurities(resp.Securities)
		if err != nil {
			return InvestmentTransactionsResult{}, err
		}
		for _, s := range securities {
			if !seen[s.ID] {
				seen[s.ID] = true
				result.Securities = append(result.Securities, s)
			}
		}

		offset += len(resp.InvestmentTransactions)
		if offset >= resp.TotalInvestmentTransactions {
			return result, nil
		}
		if len(resp.InvestmentTransactions) == 0 {
			return InvestmentTransactionsResult{}, fmt.Errorf(
				"plaid: investment transactions page was empty at offset %d of %d",
				offset, resp.TotalInvestmentTransactions)
		}
	}
}

// --- conversion ---------------------------------------------------------------

func convertInvestmentAccount(a wireAccount) (InvestmentAccount, error) {
	what := fmt.Sprintf("account %s (%s)", a.AccountID, a.Name)
	cur := wireCurrency{a.Balances.IsoCurrencyCode, a.Balances.UnofficialCurrencyCode}

	current, err := optionalDecimal(what+" current balance", a.Balances.Current, cur)
	if err != nil {
		return InvestmentAccount{}, err
	}
	available, err := optionalDecimal(what+" available balance", a.Balances.Available, cur)
	if err != nil {
		return InvestmentAccount{}, err
	}

	out := InvestmentAccount{
		AccountID: a.AccountID,
		Name:      a.Name,
		Type:      a.Type,
		Current:   current,
		Available: available,
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

func convertHolding(h wireHolding) (Holding, error) {
	what := fmt.Sprintf("holding of %s in %s", h.SecurityID, h.AccountID)

	quantity, err := requiredLiteral(what+" quantity", h.Quantity)
	if err != nil {
		return Holding{}, err
	}
	costBasis, err := optionalDecimal(what+" cost basis", h.CostBasis, h.wireCurrency)
	if err != nil {
		return Holding{}, err
	}
	price, err := optionalDecimal(what+" price", h.InstitutionPrice, h.wireCurrency)
	if err != nil {
		return Holding{}, err
	}
	value, err := optionalDecimal(what+" value", h.InstitutionValue, h.wireCurrency)
	if err != nil {
		return Holding{}, err
	}

	out := Holding{
		AccountID:        h.AccountID,
		SecurityID:       h.SecurityID,
		Quantity:         quantity,
		CostBasis:        costBasis,
		InstitutionPrice: price,
		InstitutionValue: value,
	}
	if h.InstitutionPriceAsOf != nil {
		asOf, err := civildate.Parse("2006-01-02", *h.InstitutionPriceAsOf)
		if err != nil {
			return Holding{}, fmt.Errorf("plaid: %s has an unparseable price date %q: %w", what, *h.InstitutionPriceAsOf, err)
		}
		out.PriceAsOf = asOf
	}
	return out, nil
}

func convertSecurities(raw []wireSecurity) ([]Security, error) {
	out := make([]Security, 0, len(raw))
	for _, s := range raw {
		if s.SecurityID == "" {
			return nil, errors.New("plaid: a security has no security_id")
		}
		sec := Security{ID: s.SecurityID}
		if s.TickerSymbol != nil {
			sec.Ticker = *s.TickerSymbol
		}
		if s.CUSIP != nil {
			sec.CUSIP = *s.CUSIP
		}
		if s.ISIN != nil {
			sec.ISIN = *s.ISIN
		}
		if s.Name != nil {
			sec.Name = *s.Name
		}
		if s.Type != nil {
			sec.Type = *s.Type
		}
		if s.IsCashEquivalent != nil {
			sec.IsCashEquivalent = *s.IsCashEquivalent
		}
		// A security's currency is optional: Plaid omits it for some
		// derivative and unknown types, and finance2 tolerates an empty code.
		if s.IsoCurrencyCode != nil || s.UnofficialCurrencyCode != nil {
			currency, err := currencyCode("security "+s.SecurityID, s.wireCurrency)
			if err != nil {
				return nil, err
			}
			sec.Currency = currency
		}
		out = append(out, sec)
	}
	return out, nil
}

func convertInvestmentTransaction(t wireInvestmentTransaction) (InvestmentTransaction, error) {
	what := fmt.Sprintf("investment transaction %s", t.InvestmentTransactionID)

	date, err := civildate.Parse("2006-01-02", t.Date)
	if err != nil {
		return InvestmentTransaction{}, fmt.Errorf("plaid: %s has an unparseable date %q: %w", what, t.Date, err)
	}
	quantity, err := requiredLiteral(what+" quantity", t.Quantity)
	if err != nil {
		return InvestmentTransaction{}, err
	}
	amount, err := optionalDecimal(what+" amount", t.Amount, t.wireCurrency)
	if err != nil {
		return InvestmentTransaction{}, err
	}
	if amount == nil {
		return InvestmentTransaction{}, fmt.Errorf("plaid: %s has no amount", what)
	}
	price, err := optionalDecimal(what+" price", t.Price, t.wireCurrency)
	if err != nil {
		return InvestmentTransaction{}, err
	}
	fees, err := optionalDecimal(what+" fees", t.Fees, t.wireCurrency)
	if err != nil {
		return InvestmentTransaction{}, err
	}

	out := InvestmentTransaction{
		ID:        t.InvestmentTransactionID,
		AccountID: t.AccountID,
		Date:      date,
		Name:      t.Name,
		Type:      t.Type,
		Subtype:   t.Subtype,
		Quantity:  quantity,
		Amount:    *amount,
		Price:     price,
		Fees:      fees,
	}
	if t.SecurityID != nil {
		out.SecurityID = *t.SecurityID
	}
	return out, nil
}

// requiredLiteral validates a decimal Plaid must send.
func requiredLiteral(what string, n *json.Number) (string, error) {
	if n == nil {
		return "", fmt.Errorf("plaid: %s is missing", what)
	}
	if err := money.CheckLiteral(n.String()); err != nil {
		return "", fmt.Errorf("plaid: %s: %w", what, err)
	}
	return n.String(), nil
}

// optionalDecimal validates a nullable monetary literal together with its
// currency. A null value yields nil, which is not the same as zero. A present
// value with no ISO currency, or an unofficial one, is refused: the reader
// cannot interpret an amount it cannot denominate.
func optionalDecimal(what string, n *json.Number, cur wireCurrency) (*DecimalAmount, error) {
	if n == nil {
		return nil, nil
	}
	if err := money.CheckLiteral(n.String()); err != nil {
		return nil, fmt.Errorf("plaid: %s: %w", what, err)
	}
	currency, err := currencyCode(what, cur)
	if err != nil {
		return nil, err
	}
	return &DecimalAmount{Value: n.String(), Currency: currency}, nil
}

// currencyCode accepts any ISO 4217 code — the snapshot carries the code, so
// a foreign-denominated security is data, not an error — and refuses
// unofficial currencies, which no reader can denominate.
func currencyCode(what string, cur wireCurrency) (string, error) {
	if cur.UnofficialCurrencyCode != nil {
		return "", fmt.Errorf("plaid: %s is denominated in unofficial currency %q", what, *cur.UnofficialCurrencyCode)
	}
	if cur.IsoCurrencyCode == nil {
		return "", fmt.Errorf("plaid: %s has no currency", what)
	}
	code := *cur.IsoCurrencyCode
	if len(code) != 3 {
		return "", fmt.Errorf("plaid: %s has a malformed currency code %q", what, code)
	}
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return "", fmt.Errorf("plaid: %s has a malformed currency code %q", what, code)
		}
	}
	return code, nil
}

// --- errors -------------------------------------------------------------------

// Error codes an Item returns when the investments product cannot serve it.
const (
	// ErrorCodeProductsNotSupported: the institution does not offer the
	// product. Permanent for this Item.
	ErrorCodeProductsNotSupported = "PRODUCTS_NOT_SUPPORTED"
	// ErrorCodeInvalidProduct: the Item was not created with the product.
	// Permanent for this Item; it must be re-linked.
	ErrorCodeInvalidProduct = "INVALID_PRODUCT"
	// ErrorCodeNoInvestmentAccounts: the login holds no investment accounts.
	ErrorCodeNoInvestmentAccounts = "NO_INVESTMENT_ACCOUNTS"
	// ErrorCodeProductNotReady: Plaid is still preparing the product for a
	// freshly linked Item. Transient; retry later.
	ErrorCodeProductNotReady = "PRODUCT_NOT_READY"
)

// IsInvestmentsUnavailable reports whether an error means this Item cannot
// serve the investments product at all — not supported, not enrolled, or no
// such accounts — as distinct from a failure worth retrying.
func IsInvestmentsUnavailable(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode {
	case ErrorCodeProductsNotSupported, ErrorCodeInvalidProduct, ErrorCodeNoInvestmentAccounts:
		return true
	}
	return false
}

// IsProductNotReady reports the transient case: a newly linked Item whose
// investments data Plaid has not finished preparing.
func IsProductNotReady(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode == ErrorCodeProductNotReady
}
