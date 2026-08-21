package plaid

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	plaidsdk "github.com/plaid/plaid-go/v43/plaid"
)

func TestParseProduct(t *testing.T) {
	for _, ok := range []string{"transactions", "investments"} {
		p, err := ParseProduct(ok)
		if err != nil || p.String() != ok {
			t.Errorf("ParseProduct(%q) = %q, %v", ok, p, err)
		}
	}
	for _, bad := range []string{"", "auth", "Transactions", "transactions "} {
		if _, err := ParseProduct(bad); err == nil {
			t.Errorf("ParseProduct(%q) accepted", bad)
		}
	}
}

func TestLinkIdentity_Validate(t *testing.T) {
	good := LinkIdentity{ClientName: "x", Products: []Product{mustProduct("transactions")}}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid identity rejected: %v", err)
	}

	cases := map[string]LinkIdentity{
		"no name":          {Products: good.Products},
		"no products":      {ClientName: "x"},
		"unparsed product": {ClientName: "x", Products: []Product{{}}},
	}
	for name, id := range cases {
		if err := id.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// captureLinkTokenCreate serves /link/token/create and hands back the decoded
// request body, so a test can see exactly what an identity asks Plaid for.
func captureLinkTokenCreate(t *testing.T) (*plaidsdk.APIClient, *map[string]any) {
	t.Helper()

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/link/token/create" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w,
			`{"link_token":"link-sandbox-test","expiration":"2099-01-01T00:00:00Z","request_id":"r"}`); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := plaidsdk.NewConfiguration()
	cfg.Servers = plaidsdk.ServerConfigurations{{URL: srv.URL}}
	return plaidsdk.NewAPIClient(cfg), &got
}

// The identity decides what an Item is created with. bankferry's asks for
// transactions and the 730-day history window; an investments-only identity
// must send neither, because product-specific parameters for a product that
// was not requested are Plaid's to reject.
func TestCreateLinkToken_RequestsTheIdentitysProducts(t *testing.T) {
	cases := []struct {
		name         string
		products     []string
		wantTxnParam bool
	}{
		{"transactions", []string{"transactions"}, true},
		{"investments", []string{"investments"}, false},
		{"both", []string{"transactions", "investments"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, got := captureLinkTokenCreate(t)
			id := LinkIdentity{ClientName: "ferry-" + tc.name}
			for _, p := range tc.products {
				id.Products = append(id.Products, mustProduct(p))
			}

			if _, err := CreateLinkToken(context.Background(), client, id, ""); err != nil {
				t.Fatalf("CreateLinkToken: %v", err)
			}

			req := *got
			if req["client_name"] != id.ClientName {
				t.Errorf("client_name = %v, want %q", req["client_name"], id.ClientName)
			}
			user, _ := req["user"].(map[string]any)
			if user["client_user_id"] != id.ClientName+"-local-user" {
				t.Errorf("client_user_id = %v", user["client_user_id"])
			}

			products, _ := req["products"].([]any)
			if len(products) != len(tc.products) {
				t.Fatalf("products = %v, want %v", products, tc.products)
			}
			for i, p := range tc.products {
				if products[i] != p {
					t.Errorf("products[%d] = %v, want %q", i, products[i], p)
				}
			}

			_, hasTxn := req["transactions"]
			if hasTxn != tc.wantTxnParam {
				t.Errorf("transactions parameter present = %v, want %v", hasTxn, tc.wantTxnParam)
			}
		})
	}
}

func TestCreateLinkToken_RefusesAnInvalidIdentity(t *testing.T) {
	client, got := captureLinkTokenCreate(t)
	if _, err := CreateLinkToken(context.Background(), client, LinkIdentity{}, ""); err == nil {
		t.Fatal("an empty identity created a link token")
	}
	if *got != nil {
		t.Error("the request reached Plaid")
	}
}
