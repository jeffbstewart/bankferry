package plaid

import (
	"context"
	"fmt"
	"time"
)

// ConsentWarningWindow is how far ahead an expiring consent is worth
// reporting. Plaid's own webhooks give seven days' notice, so a weekly
// fetch that warns at fourteen days will see the warning at least once
// before the Item breaks.
const ConsentWarningWindow = 14 * 24 * time.Hour

// ItemStatus is the health of one Item, as reported by /item/get.
//
// It exists because the warnings Plaid actually sends — PENDING_DISCONNECT
// for US and Canadian institutions, PENDING_EXPIRATION for European ones —
// are delivered only by webhook, seven days ahead. A CLI that runs weekly
// has no endpoint listening when that webhook fires, so polling this is the
// only advance notice available.
//
// Even polling is partial. consent_expiration_time is populated only for
// institutions with expiring consent. A disconnect caused by
// INSTITUTION_MIGRATION carries a disconnect_time that appears in the
// webhook and nowhere else, so that failure arrives unannounced and shows
// up as ITEM_LOGIN_REQUIRED on the next call.
type ItemStatus struct {
	ItemID        string
	InstitutionID string

	// ConsentExpiresAt is nil when the institution does not expire
	// consent, which is most US institutions.
	ConsentExpiresAt *time.Time

	// Error is the Item's current error, empty when healthy. The call
	// itself succeeds even when the Item is broken.
	ErrorCode    string
	ErrorType    string
	ErrorMessage string
}

// NeedsLinkRefresh reports whether the Item is already broken and can only
// be repaired by sending the user through Link update mode.
func (s ItemStatus) NeedsLinkRefresh() bool {
	return s.ErrorCode == ErrorCodeItemLoginRequired
}

// ConsentExpiringWithin reports whether the Item's consent lapses inside
// the window, measured from now. It is false when the institution does not
// expire consent, and true for a consent that has already lapsed.
func (s ItemStatus) ConsentExpiringWithin(window time.Duration, now time.Time) bool {
	if s.ConsentExpiresAt == nil {
		return false
	}
	return s.ConsentExpiresAt.Sub(now) <= window
}

type wireItemGet struct {
	Item struct {
		ItemID                string  `json:"item_id"`
		InstitutionID         *string `json:"institution_id"`
		ConsentExpirationTime *string `json:"consent_expiration_time"`
		Error                 *struct {
			ErrorType    string `json:"error_type"`
			ErrorCode    string `json:"error_code"`
			ErrorMessage string `json:"error_message"`
		} `json:"error"`
	} `json:"item"`
}

// FetchItemStatus reads an Item's health. It returns normally for a broken
// Item: the error lives in the response, not in the call.
func (c *DataClient) FetchItemStatus(ctx context.Context, accessToken string) (ItemStatus, error) {
	var resp wireItemGet
	if err := c.post(ctx, "item get", "/item/get", map[string]any{
		"access_token": accessToken,
	}, &resp); err != nil {
		return ItemStatus{}, err
	}

	status := ItemStatus{ItemID: resp.Item.ItemID}
	if resp.Item.InstitutionID != nil {
		status.InstitutionID = *resp.Item.InstitutionID
	}
	if resp.Item.Error != nil {
		status.ErrorCode = resp.Item.Error.ErrorCode
		status.ErrorType = resp.Item.Error.ErrorType
		status.ErrorMessage = resp.Item.Error.ErrorMessage
	}

	if resp.Item.ConsentExpirationTime != nil && *resp.Item.ConsentExpirationTime != "" {
		expiry, err := time.Parse(time.RFC3339, *resp.Item.ConsentExpirationTime)
		if err != nil {
			return ItemStatus{}, fmt.Errorf(
				"plaid: item %s has an unparseable consent_expiration_time %q: %w",
				status.ItemID, *resp.Item.ConsentExpirationTime, err)
		}
		status.ConsentExpiresAt = &expiry
	}

	return status, nil
}
