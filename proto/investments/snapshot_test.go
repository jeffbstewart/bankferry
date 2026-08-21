package investments

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// The contract's whole reason to carry decimals as strings is that they
// survive untouched. This pins the round trip through the binary encoding
// for a value with more precision than a float64 would keep, and checks
// the shape finance2 keys on: schema version, item_ref, account_ref,
// ticker.
func TestSnapshot_RoundTripsExactly(t *testing.T) {
	const quantity = "123456789.123456789"
	in := &InvestmentsSnapshot{
		SchemaVersion: 1,
		AsOf:          &Date{Year: 2026, Month: 8, Day: 21},
		Items: []*ItemSnapshot{{
			InstitutionEntry: "Vanguard",
			ItemRef:          "item_1",
			Accounts: []*Account{{
				AccountRef:       "acct_1",
				Name:             "Roth IRA",
				Mask:             "4321",
				Type:             "investment",
				Subtype:          "roth",
				InstitutionValue: &Money{Amount: &Decimal{Value: "23631.9805"}, CurrencyCode: "USD"},
				Holdings: []*Holding{{
					Security: &SecurityRef{PlaidSecurityId: "sec_1", Ticker: "VTI", CurrencyCode: "USD"},
					Quantity: &Decimal{Value: quantity},
				}},
			}},
		}},
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out InvestmentsSnapshot
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !proto.Equal(in, &out) {
		t.Fatalf("round trip changed the snapshot:\n in=%v\nout=%v", in, &out)
	}
	if got := out.Items[0].Accounts[0].Holdings[0].Quantity.Value; got != quantity {
		t.Errorf("quantity = %q, want %q", got, quantity)
	}
	if out.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", out.SchemaVersion)
	}
}

// Absent optional messages stay absent: finance2 distinguishes "Plaid did
// not report cost basis" from a zero, and the writer must be able to say so.
func TestSnapshot_AbsentMoneyStaysNil(t *testing.T) {
	raw, err := proto.Marshal(&Holding{Quantity: &Decimal{Value: "1"}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var h Holding
	if err := proto.Unmarshal(raw, &h); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if h.CostBasis != nil || h.InstitutionPrice != nil || h.PriceAsOf != nil {
		t.Errorf("absent fields came back non-nil: %v", &h)
	}
}
