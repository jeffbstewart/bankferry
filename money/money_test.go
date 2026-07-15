package money

import (
	"errors"
	"math"
	"strconv"
	"testing"
)

// ---------------------------------------------------------------------------
// Currency
// ---------------------------------------------------------------------------

func TestParseCurrency(t *testing.T) {
	for _, in := range []string{"USD", "usd", " Usd "} {
		got, err := ParseCurrency(in)
		if err != nil {
			t.Errorf("ParseCurrency(%q) returned error: %v", in, err)
		}
		if got != USD {
			t.Errorf("ParseCurrency(%q) = %q, want USD", in, got)
		}
	}
}

// A euro account must be refused, not silently treated as dollars: an OFX
// statement carries one CURDEF and nothing here converts.
func TestParseCurrency_RejectsOthers(t *testing.T) {
	for _, in := range []string{"EUR", "GBP", "BTC", ""} {
		if _, err := ParseCurrency(in); !errors.Is(err, ErrCurrency) {
			t.Errorf("ParseCurrency(%q) err = %v, want ErrCurrency", in, err)
		}
	}
}

func TestCurrency_DecimalsAndSymbol(t *testing.T) {
	if got := USD.Decimals(); got != 2 {
		t.Errorf("USD.Decimals() = %d, want 2", got)
	}
	if got := USD.Symbol(); got != "$" {
		t.Errorf("USD.Symbol() = %q, want $", got)
	}
}

func TestCurrency_Zero(t *testing.T) {
	z := USD.Zero()
	if !z.IsZero() {
		t.Error("USD.Zero() is not zero")
	}
	if !z.Is(USD) {
		t.Error("USD.Zero() is not denominated in USD")
	}
	if got := z.Exact(); got != "0.00" {
		t.Errorf("USD.Zero().Exact() = %q, want 0.00", got)
	}
}

// ---------------------------------------------------------------------------
// Parse
// ---------------------------------------------------------------------------

// The literals in the left column are exactly what Plaid was observed to
// send on the wire.
func TestParse_Exact(t *testing.T) {
	cases := []struct {
		in   string
		want string // Exact()
	}{
		{"89.4", "89.40"},
		{"12", "12.00"},
		{"-500", "-500.00"},
		{"-4.22", "-4.22"},
		{"23631.9805", "23631.9805"},
		{"0", "0.00"},
		{"-0", "0.00"},
		{"0.00", "0.00"},
		{"+1.5", "1.50"},
		{".5", "0.50"},
		{"-.5", "-0.50"},
		{"5.", "5.00"},
		{"000123.4500", "123.4500"},
		{"0.000000001", "0.000000001"},
	}
	for _, tc := range cases {
		got, err := Parse(tc.in, USD)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.in, err)
			continue
		}
		if got.Exact() != tc.want {
			t.Errorf("Parse(%q).Exact() = %q, want %q", tc.in, got.Exact(), tc.want)
		}
	}
}

// JSON permits exponent notation. Plaid has not been seen to use it, but
// a parser that silently mangles it would be worse than one that does not
// accept it at all.
func TestParse_Exponents(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"1e2", "100.00"},
		{"1E2", "100.00"},
		{"1.5e2", "150.00"},
		{"-1.5e2", "-150.00"},
		{"1500e-2", "15.00"},
		{"1.23456e3", "1234.56"},
		{"5e+1", "50.00"},
	}
	for _, tc := range cases {
		got, err := Parse(tc.in, USD)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.in, err)
			continue
		}
		if got.Exact() != tc.want {
			t.Errorf("Parse(%q).Exact() = %q, want %q", tc.in, got.Exact(), tc.want)
		}
	}
}

func TestParse_Invalid(t *testing.T) {
	for _, in := range []string{"", "abc", "1.2.3", "1,000", "1 2", "--1", "1e", "1e999", "$5", "5%", "1.2e1.5"} {
		if _, err := Parse(in, USD); err == nil {
			t.Errorf("Parse(%q) should have failed", in)
		}
	}
}

// Precision beyond what an int64 can hold exactly is an error, never a
// silent approximation.
func TestParse_TooPrecise(t *testing.T) {
	if _, err := Parse("0.0000000001", USD); !errors.Is(err, ErrRange) {
		t.Errorf("err = %v, want ErrRange for scale 10", err)
	}
}

func TestParse_TooLarge(t *testing.T) {
	huge := strconv.FormatUint(math.MaxUint64, 10)
	if _, err := Parse(huge, USD); !errors.Is(err, ErrRange) {
		t.Errorf("err = %v, want ErrRange", err)
	}
}

func TestParse_RequiresCurrency(t *testing.T) {
	if _, err := Parse("1.00", ""); !errors.Is(err, ErrCurrency) {
		t.Errorf("err = %v, want ErrCurrency", err)
	}
}

// Exact must never round. A four-decimal balance keeps four decimals.
func TestExact_NeverRounds(t *testing.T) {
	a := MustParse("23631.9805", USD)
	if got := a.Exact(); got != "23631.9805" {
		t.Errorf("Exact() = %q, want 23631.9805", got)
	}
}

// ---------------------------------------------------------------------------
// Formatters
// ---------------------------------------------------------------------------

func TestFormatters(t *testing.T) {
	cases := []struct {
		in                            string
		quantity, display, accounting string
	}{
		{"12.56", "12.56", "$12.56", "$12.56"},
		{"-12.56", "-12.56", "-$12.56", "($12.56)"},
		{"0", "0.00", "$0.00", "$0.00"},
		{"-0.00", "0.00", "$0.00", "$0.00"},
		{"1000", "1000.00", "$1000.00", "$1000.00"},
		{"0.5", "0.50", "$0.50", "$0.50"},
		{"-0.5", "-0.50", "-$0.50", "($0.50)"},
	}
	for _, tc := range cases {
		a := MustParse(tc.in, USD)
		if got := a.Quantity(); got != tc.quantity {
			t.Errorf("Parse(%q).Quantity() = %q, want %q", tc.in, got, tc.quantity)
		}
		if got := a.Display(); got != tc.display {
			t.Errorf("Parse(%q).Display() = %q, want %q", tc.in, got, tc.display)
		}
		if got := a.Accounting(); got != tc.accounting {
			t.Errorf("Parse(%q).Accounting() = %q, want %q", tc.in, got, tc.accounting)
		}
	}
}

// Quantity uses the currency's precision, so it rounds where Exact does
// not. Rounding is half away from zero, symmetric about zero.
func TestQuantity_RoundsHalfAwayFromZero(t *testing.T) {
	cases := []struct{ in, want string }{
		{"23631.9805", "23631.98"},
		{"1.005", "1.01"},
		{"-1.005", "-1.01"},
		{"1.004", "1.00"},
		{"-1.004", "-1.00"},
		{"1.015", "1.02"},
		{"2.675", "2.68"}, // the classic float64 trap: 2.675 rounds down in binary
		{"0.005", "0.01"},
		{"-0.005", "-0.01"},
		{"0.004", "0.00"},
	}
	for _, tc := range cases {
		a := MustParse(tc.in, USD)
		if got := a.Quantity(); got != tc.want {
			t.Errorf("Parse(%q).Quantity() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The value itself is untouched by a rounded rendering.
func TestQuantity_DoesNotMutate(t *testing.T) {
	a := MustParse("23631.9805", USD)
	_ = a.Quantity()
	if got := a.Exact(); got != "23631.9805" {
		t.Errorf("Exact() after Quantity() = %q, want 23631.9805", got)
	}
}

// ---------------------------------------------------------------------------
// Sign, equality, currency
// ---------------------------------------------------------------------------

func TestNeg(t *testing.T) {
	cases := []struct{ in, want string }{
		{"89.4", "-89.40"},
		{"-89.4", "89.40"},
		{"0", "0.00"},
		{"23631.9805", "-23631.9805"},
	}
	for _, tc := range cases {
		if got := MustParse(tc.in, USD).Neg().Exact(); got != tc.want {
			t.Errorf("Parse(%q).Neg().Exact() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNeg_PreservesCurrency(t *testing.T) {
	if !MustParse("1.00", USD).Neg().Is(USD) {
		t.Error("Neg() dropped the currency")
	}
}

func TestIsNegativeAndIsZero(t *testing.T) {
	if !MustParse("-0.01", USD).IsNegative() {
		t.Error("-0.01 should be negative")
	}
	if MustParse("0", USD).IsNegative() {
		t.Error("zero must not be negative")
	}
	if MustParse("-0.00", USD).IsNegative() {
		t.Error("negative zero must not be negative")
	}
	if !MustParse("0.000", USD).IsZero() {
		t.Error("0.000 should be zero")
	}
}

// Equality is by value, not by the scale the value happens to be held at.
func TestEqual_IgnoresScale(t *testing.T) {
	if !MustParse("1.5", USD).Equal(MustParse("1.50", USD)) {
		t.Error("1.5 should equal 1.50")
	}
	if !MustParse("12", USD).Equal(MustParse("12.0000", USD)) {
		t.Error("12 should equal 12.0000")
	}
	if MustParse("1.5", USD).Equal(MustParse("1.51", USD)) {
		t.Error("1.5 must not equal 1.51")
	}
}

func TestIs(t *testing.T) {
	a := MustParse("1.00", USD)
	if !a.Is(USD) {
		t.Error("Is(USD) should be true")
	}
	if a.Is(Currency("EUR")) {
		t.Error("Is(EUR) should be false")
	}
}

// An amount in another currency is never equal to a dollar amount, even
// when the digits match.
func TestEqual_DifferentCurrenciesAreNotEqual(t *testing.T) {
	usd := MustParse("1.00", USD)
	eur, err := New(100, 2, Currency("EUR"))
	if err != nil {
		t.Fatal(err)
	}
	if usd.Equal(eur) {
		t.Error("$1.00 must not equal EUR 1.00")
	}
}

// ---------------------------------------------------------------------------
// New
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	a, err := New(-4500, 2, USD)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := a.Exact(); got != "-45.00" {
		t.Errorf("Exact() = %q, want -45.00", got)
	}
	if got := a.Display(); got != "-$45.00" {
		t.Errorf("Display() = %q, want -$45.00", got)
	}
}

func TestNew_RejectsBadScaleAndCurrency(t *testing.T) {
	if _, err := New(1, maxScale+1, USD); !errors.Is(err, ErrRange) {
		t.Errorf("err = %v, want ErrRange", err)
	}
	if _, err := New(1, 2, ""); !errors.Is(err, ErrCurrency) {
		t.Errorf("err = %v, want ErrCurrency", err)
	}
}

// ---------------------------------------------------------------------------
// Round trip
// ---------------------------------------------------------------------------

// Every literal Plaid sent must survive Parse and Exact unchanged in
// value, which is the whole reason this type exists.
func TestRoundTrip_PlaidLiterals(t *testing.T) {
	for _, lit := range []string{"89.4", "12", "-500", "-4.22", "23631.9805", "6.33", "5.4", "4.33"} {
		a := MustParse(lit, USD)
		b := MustParse(a.Exact(), USD)
		if !a.Equal(b) {
			t.Errorf("%q did not survive a round trip: %q", lit, a.Exact())
		}
	}
}
