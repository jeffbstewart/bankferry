package snapshot

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeffbstewart/bankferry/civildate"
	"github.com/jeffbstewart/bankferry/plaid"
	pb "github.com/jeffbstewart/bankferry/proto/investments"
	"google.golang.org/protobuf/proto"
)

func usd(v string) *plaid.DecimalAmount { return &plaid.DecimalAmount{Value: v, Currency: "USD"} }

func sampleItem() ItemData {
	return ItemData{
		Item: plaid.Item{ItemID: "item_1", InstitutionID: "ins_v", InstitutionName: "Vanguard"},
		Holdings: plaid.HoldingsResult{
			Accounts: []plaid.InvestmentAccount{
				{AccountID: "acct_roth", Name: "Roth IRA", OfficialName: "Vanguard Roth IRA", Mask: "4321",
					Type: "investment", Subtype: "roth", Current: usd("23631.9805"), Available: usd("1200.50")},
				{AccountID: "acct_empty", Name: "Empty", Type: "investment", Subtype: "brokerage", Current: usd("0")},
			},
			Holdings: []plaid.Holding{
				{AccountID: "acct_roth", SecurityID: "sec_vti", Quantity: "123.456789123",
					CostBasis: usd("12345678.123456789"), InstitutionPrice: usd("250.1234"),
					InstitutionValue: usd("30873.8771"), PriceAsOf: civildate.MustNew(2026, 8, 20)},
				{AccountID: "acct_roth", SecurityID: "sec_cash", Quantity: "100", InstitutionValue: usd("100")},
			},
			Securities: []plaid.Security{
				{ID: "sec_vti", Ticker: "VTI", CUSIP: "922908769", ISIN: "US9229087690", Name: "VTI ETF", Type: "etf", Currency: "USD"},
				{ID: "sec_cash", Name: "U S Dollar", Type: "cash", Currency: "USD", IsCashEquivalent: true},
			},
		},
		Transactions: plaid.InvestmentTransactionsResult{
			Transactions: []plaid.InvestmentTransaction{
				{ID: "t1", AccountID: "acct_roth", SecurityID: "sec_vti", Date: civildate.MustNew(2026, 3, 1),
					Name: "BUY VTI", Type: "buy", Subtype: "buy", Quantity: "1.5",
					Amount: *usd("-375.1850"), Price: usd("250.1233"), Fees: usd("0")},
				{ID: "t2", AccountID: "acct_roth", Date: civildate.MustNew(2026, 3, 2),
					Name: "FEE", Type: "fee", Subtype: "management fee", Quantity: "0", Amount: *usd("12.34")},
			},
			// The transactions call carries its own securities table; a
			// security only it knows must still resolve.
			Securities: []plaid.Security{{ID: "sec_vti", Ticker: "VTI", Currency: "USD"}},
		},
	}
}

func TestBuild_MapsEveryFieldVerbatim(t *testing.T) {
	s, err := Build(civildate.MustNew(2026, 8, 21), []ItemData{sampleItem()})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if s.SchemaVersion != 1 || s.AsOf.Year != 2026 || s.AsOf.Month != 8 || s.AsOf.Day != 21 {
		t.Errorf("header = %v / %v", s.SchemaVersion, s.AsOf)
	}
	if len(s.Items) != 1 {
		t.Fatalf("items = %d", len(s.Items))
	}
	item := s.Items[0]
	if item.InstitutionEntry != "Vanguard" || item.ItemRef != "item_1" {
		t.Errorf("item = %v", item)
	}
	if len(item.Accounts) != 2 {
		t.Fatalf("accounts = %d", len(item.Accounts))
	}

	roth := item.Accounts[0]
	if roth.AccountRef != "acct_roth" || roth.Name != "Roth IRA" || roth.OfficialName != "Vanguard Roth IRA" ||
		roth.Mask != "4321" || roth.Type != "investment" || roth.Subtype != "roth" {
		t.Errorf("roth = %v", roth)
	}
	if roth.InstitutionValue.Amount.Value != "23631.9805" || roth.InstitutionValue.CurrencyCode != "USD" {
		t.Errorf("institution_value = %v", roth.InstitutionValue)
	}
	if roth.CashBalance.Amount.Value != "1200.50" {
		t.Errorf("cash_balance = %v", roth.CashBalance)
	}

	if len(roth.Holdings) != 2 {
		t.Fatalf("holdings = %d", len(roth.Holdings))
	}
	vti := roth.Holdings[0]
	if vti.Security.Ticker != "VTI" || vti.Security.Cusip != "922908769" || vti.Security.Isin != "US9229087690" ||
		vti.Security.PlaidSecurityId != "sec_vti" || vti.Security.CurrencyCode != "USD" {
		t.Errorf("security = %v", vti.Security)
	}
	if vti.Quantity.Value != "123.456789123" || vti.CostBasis.Amount.Value != "12345678.123456789" ||
		vti.InstitutionPrice.Amount.Value != "250.1234" || vti.InstitutionValue.Amount.Value != "30873.8771" {
		t.Errorf("holding = %v", vti)
	}
	if vti.PriceAsOf == nil || vti.PriceAsOf.Day != 20 {
		t.Errorf("price_as_of = %v", vti.PriceAsOf)
	}
	cash := roth.Holdings[1]
	if cash.CostBasis != nil || cash.InstitutionPrice != nil || cash.PriceAsOf != nil {
		t.Errorf("absent fields were populated: %v", cash)
	}
	if !cash.Security.IsCashEquivalent {
		t.Error("is_cash_equivalent lost")
	}

	if len(roth.Transactions) != 2 {
		t.Fatalf("transactions = %d", len(roth.Transactions))
	}
	buy := roth.Transactions[0]
	if buy.TransactionRef != "t1" || buy.Type != "buy" || buy.Quantity.Value != "1.5" ||
		buy.Amount.Amount.Value != "-375.1850" || buy.Price.Amount.Value != "250.1233" || buy.Fees.Amount.Value != "0" ||
		buy.Date.Month != 3 || buy.Security.Ticker != "VTI" {
		t.Errorf("buy = %v", buy)
	}
	fee := roth.Transactions[1]
	if fee.Security != nil || fee.Price != nil || fee.Fees != nil || fee.Amount.Amount.Value != "12.34" {
		t.Errorf("fee = %v", fee)
	}

	empty := item.Accounts[1]
	if len(empty.Holdings) != 0 || len(empty.Transactions) != 0 || empty.CashBalance != nil {
		t.Errorf("empty account = %v", empty)
	}
}

func TestBuild_Refusals(t *testing.T) {
	cases := map[string]struct {
		mutate func(*ItemData)
		want   string
	}{
		"holding with unknown security": {
			func(d *ItemData) { d.Holdings.Holdings[0].SecurityID = "sec_nope" }, "unknown security",
		},
		"transaction with unknown security": {
			func(d *ItemData) { d.Transactions.Transactions[0].SecurityID = "sec_nope" }, "unknown security",
		},
		"holding for unlisted account": {
			func(d *ItemData) { d.Holdings.Holdings[0].AccountID = "acct_ghost" }, "unlisted account",
		},
		"transaction for unlisted account": {
			func(d *ItemData) { d.Transactions.Transactions[0].AccountID = "acct_ghost" }, "unlisted account",
		},
		"item without an ID": {
			func(d *ItemData) { d.Item.ItemID = "" }, "no item ID",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := sampleItem()
			tc.mutate(&d)
			_, err := Build(civildate.MustNew(2026, 8, 21), []ItemData{d})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}

	if _, err := Build(civildate.ISO8601Date{}, nil); err == nil {
		t.Error("a zero as_of was accepted")
	}
}

// ---------------------------------------------------------------------------
// Writer
// ---------------------------------------------------------------------------

func createExclusive(path string) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func testWriter(dir string, json bool) Writer {
	return Writer{Dir: dir, JSON: json, CreateFile: createExclusive, Exists: pathExists}
}

var testNow = time.Date(2026, 8, 21, 14, 5, 9, 0, time.UTC)

func TestWriter_WritesABinaryThatReadsBack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	s, err := Build(civildate.MustNew(2026, 8, 21), []ItemData{sampleItem()})
	if err != nil {
		t.Fatal(err)
	}

	res, err := testWriter(dir, true).Write(s, testNow)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if filepath.Base(res.Binary) != "investments_20260821_140509.pb" {
		t.Errorf("binary = %s", res.Binary)
	}
	if filepath.Base(res.JSON) != "investments_20260821_140509.json" {
		t.Errorf("json = %s", res.JSON)
	}

	raw, err := os.ReadFile(res.Binary)
	if err != nil {
		t.Fatal(err)
	}
	var back pb.InvestmentsSnapshot
	if err := proto.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !proto.Equal(s, &back) {
		t.Error("the file does not decode to the snapshot written")
	}

	text, err := os.ReadFile(res.JSON)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), `"23631.9805"`) {
		t.Errorf("JSON rendering lost the literal:\n%s", text)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), PendingSuffix) {
			t.Errorf("pending file left behind: %s", e.Name())
		}
	}
}

func TestWriter_NeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	s := &pb.InvestmentsSnapshot{SchemaVersion: 1}
	w := testWriter(dir, false)

	if _, err := w.Write(s, testNow); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "investments_20260821_140509.pb"))
	if err != nil {
		t.Fatal(err)
	}

	// Same second, same name: refused, and the first file untouched.
	s2 := &pb.InvestmentsSnapshot{SchemaVersion: 1, Items: []*pb.ItemSnapshot{{ItemRef: "x"}}}
	if _, err := w.Write(s2, testNow); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("second write: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "investments_20260821_140509.pb"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("the existing snapshot was replaced")
	}
}

// A pending file from a crashed run occupies the name the next write wants.
// Exclusive creation catches it; nothing is truncated.
func TestWriter_PendingCollisionIsAnError(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "investments_20260821_140509.pb"+PendingSuffix)
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := testWriter(dir, false).Write(&pb.InvestmentsSnapshot{SchemaVersion: 1}, testNow)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("error = %v, want ErrExist", err)
	}
	got, err := os.ReadFile(stale)
	if err != nil || string(got) != "stale" {
		t.Errorf("the stale pending file was touched: %q, %v", got, err)
	}
}

func TestWriter_RequiresConfiguration(t *testing.T) {
	s := &pb.InvestmentsSnapshot{SchemaVersion: 1}
	if _, err := (Writer{}).Write(s, testNow); err == nil {
		t.Error("an empty writer was accepted")
	}
	if _, err := (Writer{Dir: t.TempDir()}).Write(s, testNow); err == nil {
		t.Error("a writer without file functions was accepted")
	}
}
