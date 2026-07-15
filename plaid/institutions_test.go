package plaid

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestFetchInstitution(t *testing.T) {
	var got map[string]any

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		writeJSON(t, w, http.StatusOK, `{"institution":{
			"institution_id":"ins_56","name":"Chase","oauth":true}}`)
	})

	info, err := c.FetchInstitution(context.Background(), "ins_56")
	if err != nil {
		t.Fatalf("FetchInstitution: %v", err)
	}
	if info.ID != "ins_56" || info.Name != "Chase" || !info.OAuth {
		t.Errorf("info = %+v", info)
	}

	// country_codes is required by the API.
	if got["institution_id"] != "ins_56" {
		t.Errorf("request institution_id = %v", got["institution_id"])
	}
	if _, ok := got["country_codes"]; !ok {
		t.Error("country_codes was not sent")
	}
}

// A missing oauth field decodes as false, which is the safe reading: assume
// no OAuth rather than assume a redirect URI is needed.
func TestFetchInstitution_MissingOAuthIsFalse(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, `{"institution":{"institution_id":"ins_1","name":"Small Bank"}}`)
	})

	info, err := c.FetchInstitution(context.Background(), "ins_1")
	if err != nil {
		t.Fatal(err)
	}
	if info.OAuth {
		t.Error("an absent oauth field must not read as true")
	}
}

func TestFetchInstitution_RequiresAnID(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should have been made")
	})

	if _, err := c.FetchInstitution(context.Background(), ""); err == nil {
		t.Fatal("expected an error for an empty institution ID")
	}
}

func TestFetchInstitution_APIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, `{
			"error_type":"INVALID_INPUT","error_code":"INVALID_INSTITUTION",
			"error_message":"institution not found","request_id":"req_1"}`)
	})

	if _, err := c.FetchInstitution(context.Background(), "ins_nope"); err == nil {
		t.Fatal("expected an error")
	}
}
