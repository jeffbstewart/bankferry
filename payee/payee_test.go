package payee

import "testing"

var testPayees = []Payee{
	{ID: 1, Name: "Amazon"},
	{ID: 2, Name: "Whole Foods"},
	{ID: 3, Name: "Starbucks"},
	{ID: 4, Name: "Crack'd"},
	{ID: 5, Name: "Ace Hardware"},
}

func TestMatch_SubstringMatch(t *testing.T) {
	rules := []Rule{
		{ID: 1, PayeeID: 1, Pattern: "AMAZON.COM", Priority: 0},
		{ID: 2, PayeeID: 2, Pattern: "WHOLEFDS", Priority: 0},
		{ID: 3, PayeeID: 3, Pattern: "STARBUCKS", Priority: 0},
	}
	m := NewMatcher(testPayees, rules)

	got := m.Match("AMAZON.COM*RT4HF2AW5 AMZN.COM/BILL", "")
	if !got.Matched() {
		t.Fatal("expected match for Amazon")
	}
	if got.Payee.Name != "Amazon" {
		t.Errorf("Name = %q, want Amazon", got.Payee.Name)
	}
	if got.Confidence != "rule" {
		t.Errorf("Confidence = %q, want rule", got.Confidence)
	}
}

func TestMatch_CaseInsensitive(t *testing.T) {
	rules := []Rule{
		{ID: 1, PayeeID: 3, Pattern: "STARBUCKS", Priority: 0},
	}
	m := NewMatcher(testPayees, rules)

	got := m.Match("starbucks #1234 san francisco", "")
	if !got.Matched() {
		t.Fatal("expected case-insensitive match")
	}
	if got.Payee.Name != "Starbucks" {
		t.Errorf("Name = %q, want Starbucks", got.Payee.Name)
	}
}

func TestMatch_PriorityOrder(t *testing.T) {
	rules := []Rule{
		{ID: 1, PayeeID: 1, Pattern: "AMAZON", Priority: 0},
		{ID: 2, PayeeID: 2, Pattern: "AMAZON FRESH", Priority: 10}, // higher priority
	}
	m := NewMatcher(testPayees, rules)

	got := m.Match("AMAZON FRESH DELIVERY #12345", "")
	if got.Payee.Name != "Whole Foods" {
		t.Errorf("Name = %q, want Whole Foods (higher priority)", got.Payee.Name)
	}
}

func TestMatch_LongerPatternWinsAtSamePriority(t *testing.T) {
	rules := []Rule{
		{ID: 1, PayeeID: 1, Pattern: "AMAZON", Priority: 0},
		{ID: 2, PayeeID: 2, Pattern: "AMAZON.COM", Priority: 0}, // same priority, longer
	}
	m := NewMatcher(testPayees, rules)

	got := m.Match("AMAZON.COM*SOMETHING", "")
	if got.Payee.Name != "Whole Foods" {
		t.Errorf("Name = %q, want Whole Foods (longer pattern)", got.Payee.Name)
	}
}

func TestMatch_NoMatch(t *testing.T) {
	rules := []Rule{
		{ID: 1, PayeeID: 1, Pattern: "AMAZON", Priority: 0},
	}
	m := NewMatcher(testPayees, rules)

	got := m.Match("TARGET STORE #5678", "Target")
	if got.Matched() {
		t.Error("expected no match for unknown description")
	}
	if got.Confidence != "" {
		t.Errorf("Confidence = %q, want empty", got.Confidence)
	}
}

func TestMatch_EmptyRules(t *testing.T) {
	m := NewMatcher(testPayees, nil)

	if m.Match("anything", "anything").Matched() {
		t.Error("expected no match with empty rules")
	}
}

func TestMatch_EmptyDescription(t *testing.T) {
	rules := []Rule{{ID: 1, PayeeID: 1, Pattern: "AMAZON", Field: MatchRaw}}
	m := NewMatcher(testPayees, rules)

	if m.Match("", "").Matched() {
		t.Error("expected no match for empty description")
	}
}

// A merchant-keyed rule matches the merchant field, not the raw one.
func TestMatch_MerchantField(t *testing.T) {
	rules := []Rule{
		{ID: 1, PayeeID: 5, Pattern: "ACE HARDWARE", Field: MatchMerchant, Priority: 10},
	}
	m := NewMatcher(testPayees, rules)

	// Cryptic raw, clean merchant. The merchant-keyed rule catches it.
	got := m.Match("MOISON ACE HDWE", "Ace Hardware")
	if !got.Matched() || got.Payee.Name != "Ace Hardware" {
		t.Fatalf("merchant match = %+v, want Ace Hardware", got)
	}

	// The same rule must NOT fire on the raw field.
	if m.Match("ACE HARDWARE STORE", "").Matched() {
		t.Error("a merchant-keyed rule must not match the raw field")
	}
}

// Raw-keyed rules outrank merchant-keyed rules: the Crack'd case. Plaid
// mislabels the raw "CRACKD" as merchant "Cracker Barrel"; a raw rule on
// CRACKD must win over any merchant rule.
func TestMatch_RawBeatsMerchant(t *testing.T) {
	rules := []Rule{
		{ID: 1, PayeeID: 4, Pattern: "CRACKD", Field: MatchRaw, Priority: 10},
		{ID: 2, PayeeID: 2, Pattern: "CRACKER BARREL", Field: MatchMerchant, Priority: 10},
	}
	m := NewMatcher(testPayees, rules)

	got := m.Match("CRACKD 02 - BURLINGT", "Cracker Barrel")
	if got.Payee.Name != "Crack'd" {
		t.Errorf("Name = %q, want Crack'd (raw beats merchant)", got.Payee.Name)
	}
}

// An empty merchant simply means the merchant tier never fires.
func TestMatch_EmptyMerchant(t *testing.T) {
	rules := []Rule{
		{ID: 1, PayeeID: 5, Pattern: "ACE HARDWARE", Field: MatchMerchant},
	}
	m := NewMatcher(testPayees, rules)

	if m.Match("SOME RAW DESC", "").Matched() {
		t.Error("merchant-keyed rule fired against an empty merchant")
	}
}

func TestNewMatcher_SortsCorrectly(t *testing.T) {
	rules := []Rule{
		{ID: 1, PayeeID: 1, Pattern: "A", Priority: 0},
		{ID: 2, PayeeID: 1, Pattern: "ABCDEF", Priority: 5},
		{ID: 3, PayeeID: 1, Pattern: "ABC", Priority: 5},
		{ID: 4, PayeeID: 1, Pattern: "AB", Priority: 10},
	}
	m := NewMatcher(testPayees, rules)

	// Expected order: ID4 (pri 10), ID2 (pri 5, len 6), ID3 (pri 5, len 3), ID1 (pri 0, len 1)
	for i, want := range []int64{4, 2, 3, 1} {
		if m.rules[i].ID != want {
			t.Errorf("rules[%d].ID = %d, want %d", i, m.rules[i].ID, want)
		}
	}
}
