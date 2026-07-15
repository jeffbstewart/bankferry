package plaid

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestFetchItemStatus_Healthy(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{
			"item": {
				"item_id": "item_1",
				"institution_id": "ins_1",
				"consent_expiration_time": null,
				"error": null
			}
		}`)
	})

	got, err := c.FetchItemStatus(context.Background(), "tok")
	if err != nil {
		t.Fatalf("FetchItemStatus: %v", err)
	}
	if got.ItemID != "item_1" || got.InstitutionID != "ins_1" {
		t.Errorf("status = %+v", got)
	}
	if got.NeedsLinkRefresh() {
		t.Error("a healthy item must not need a link refresh")
	}
	if got.ConsentExpiresAt != nil {
		t.Error("no consent expiry was reported")
	}
	if got.ConsentExpiringWithin(ConsentWarningWindow, time.Now()) {
		t.Error("an item with no consent expiry is never expiring")
	}
}

// A broken Item is reported in the body of a successful call, not as a
// failed call. Treating it as a transport error would hide it.
func TestFetchItemStatus_BrokenItemIsNotACallFailure(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{
			"item": {
				"item_id": "item_1",
				"institution_id": "ins_1",
				"error": {
					"error_type": "ITEM_ERROR",
					"error_code": "ITEM_LOGIN_REQUIRED",
					"error_message": "the login details of this item have changed"
				}
			}
		}`)
	})

	got, err := c.FetchItemStatus(context.Background(), "tok")
	if err != nil {
		t.Fatalf("FetchItemStatus returned an error for a broken item: %v", err)
	}
	if !got.NeedsLinkRefresh() {
		t.Error("ITEM_LOGIN_REQUIRED should require a link refresh")
	}
	if got.ErrorType != "ITEM_ERROR" {
		t.Errorf("ErrorType = %q", got.ErrorType)
	}
}

func TestFetchItemStatus_ConsentExpiry(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{
			"item": {
				"item_id": "item_1",
				"consent_expiration_time": "2026-07-20T00:00:00Z"
			}
		}`)
	})

	got, err := c.FetchItemStatus(context.Background(), "tok")
	if err != nil {
		t.Fatalf("FetchItemStatus: %v", err)
	}
	if got.ConsentExpiresAt == nil {
		t.Fatal("consent expiry was not parsed")
	}

	now := time.Date(2026, time.July, 9, 0, 0, 0, 0, time.UTC)
	if !got.ConsentExpiringWithin(14*24*time.Hour, now) {
		t.Error("11 days out should be inside a 14 day window")
	}
	if got.ConsentExpiringWithin(7*24*time.Hour, now) {
		t.Error("11 days out should be outside a 7 day window")
	}
}

// A consent that already lapsed is reported as expiring, not as fine.
func TestFetchItemStatus_AlreadyExpiredConsent(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{
			"item": {"item_id": "item_1", "consent_expiration_time": "2026-01-01T00:00:00Z"}
		}`)
	})

	got, err := c.FetchItemStatus(context.Background(), "tok")
	if err != nil {
		t.Fatalf("FetchItemStatus: %v", err)
	}
	now := time.Date(2026, time.July, 9, 0, 0, 0, 0, time.UTC)
	if !got.ConsentExpiringWithin(ConsentWarningWindow, now) {
		t.Error("an already expired consent must report as expiring")
	}
}

// An unparseable timestamp is an error rather than a silently ignored
// expiry, which would let an Item die unannounced.
func TestFetchItemStatus_BadConsentTimeIsAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{
			"item": {"item_id": "item_1", "consent_expiration_time": "not a timestamp"}
		}`)
	})

	if _, err := c.FetchItemStatus(context.Background(), "tok"); err == nil {
		t.Fatal("expected an error for an unparseable consent_expiration_time")
	}
}

func TestFetchItemStatus_APIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, `{
			"error_type": "INVALID_INPUT",
			"error_code": "INVALID_ACCESS_TOKEN",
			"error_message": "provided access token is in an invalid format",
			"request_id": "req_9"
		}`)
	})

	if _, err := c.FetchItemStatus(context.Background(), "bad"); err == nil {
		t.Fatal("expected an error")
	} else if IsLinkRefreshRequired(err) {
		t.Error("an invalid token is not a link refresh")
	}
}
