package money

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
)

// The rendering methods are fallible (they can overflow int64 when scaling
// up). These helpers keep the value-oriented tests readable by failing the
// test on an unexpected error and returning the string.

func mustExact(t *testing.T, a Amount) string {
	t.Helper()
	s, err := a.Exact()
	if err != nil {
		t.Fatalf("Exact(): %v", err)
	}
	return s
}

func mustQuantity(t *testing.T, a Amount) string {
	t.Helper()
	s, err := a.Quantity()
	if err != nil {
		t.Fatalf("Quantity(): %v", err)
	}
	return s
}

func mustDisplay(t *testing.T, a Amount) string {
	t.Helper()
	s, err := a.Display()
	if err != nil {
		t.Fatalf("Display(): %v", err)
	}
	return s
}

func mustAccounting(t *testing.T, a Amount) string {
	t.Helper()
	s, err := a.Accounting()
	if err != nil {
		t.Fatalf("Accounting(): %v", err)
	}
	return s
}

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

// The rejection message quotes the supported currency, so an unsupported code
// reads cleanly even when it is empty or padded.
func TestParseCurrency_MessageQuotesSupported(t *testing.T) {
	_, err := ParseCurrency("EUR")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), `"USD"`) {
		t.Errorf("message %q should quote the supported currency as \"USD\"", err.Error())
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
	if !z.CurrencyIs(USD) {
		t.Error("USD.Zero() is not denominated in USD")
	}
	if got := mustExact(t, z); got != "0.00" {
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
		if e := mustExact(t, got); e != tc.want {
			t.Errorf("Parse(%q).Exact() = %q, want %q", tc.in, e, tc.want)
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
		if e := mustExact(t, got); e != tc.want {
			t.Errorf("Parse(%q).Exact() = %q, want %q", tc.in, e, tc.want)
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
	if got := mustExact(t, a); got != "23631.9805" {
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
		if got := mustQuantity(t, a); got != tc.quantity {
			t.Errorf("Parse(%q).Quantity() = %q, want %q", tc.in, got, tc.quantity)
		}
		if got := mustDisplay(t, a); got != tc.display {
			t.Errorf("Parse(%q).Display() = %q, want %q", tc.in, got, tc.display)
		}
		if got := mustAccounting(t, a); got != tc.accounting {
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
		if got := mustQuantity(t, a); got != tc.want {
			t.Errorf("Parse(%q).Quantity() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Scaling down from the maximum precision must land on a valid power-of-ten
// divisor and round cleanly, exercising the scale-down path.
func TestQuantity_ScaleDownFromMaxPrecision(t *testing.T) {
	if got := mustQuantity(t, MustParse("0.000000001", USD)); got != "0.00" {
		t.Errorf("Quantity() = %q, want 0.00", got)
	}
	if got := mustQuantity(t, MustParse("0.994999999", USD)); got != "0.99" {
		t.Errorf("Quantity() = %q, want 0.99", got)
	}
}

// The value itself is untouched by a rounded rendering.
func TestQuantity_DoesNotMutate(t *testing.T) {
	a := MustParse("23631.9805", USD)
	_, _ = a.Quantity()
	if got := mustExact(t, a); got != "23631.9805" {
		t.Errorf("Exact() after Quantity() = %q, want 23631.9805", got)
	}
}

// An amount near the int64 ceiling cannot be padded or rescaled to two
// decimals without overflowing. Every renderer that multiplies must report
// that as an error, never a truncated or wrapped value.
func TestRender_OverflowIsAnError(t *testing.T) {
	huge, err := New(math.MaxInt64, 0, USD)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := huge.Exact(); !errors.Is(err, ErrRange) {
		t.Errorf("Exact() err = %v, want ErrRange", err)
	}
	if _, err := huge.Quantity(); !errors.Is(err, ErrRange) {
		t.Errorf("Quantity() err = %v, want ErrRange", err)
	}
	if _, err := huge.Display(); !errors.Is(err, ErrRange) {
		t.Errorf("Display() err = %v, want ErrRange", err)
	}
	if _, err := huge.Accounting(); !errors.Is(err, ErrRange) {
		t.Errorf("Accounting() err = %v, want ErrRange", err)
	}
}

// String satisfies fmt.Stringer and so cannot error; on the pathological
// overflow it falls back to the exact stored digits rather than a wrong value.
func TestString_DegradesOnOverflow(t *testing.T) {
	huge, err := New(math.MaxInt64, 0, USD)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := "$" + strconv.FormatInt(math.MaxInt64, 10)
	if got := huge.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
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
		if got := mustExact(t, MustParse(tc.in, USD).Neg()); got != tc.want {
			t.Errorf("Parse(%q).Neg().Exact() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNeg_PreservesCurrency(t *testing.T) {
	if !MustParse("1.00", USD).Neg().CurrencyIs(USD) {
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

func TestCurrencyIs(t *testing.T) {
	a := MustParse("1.00", USD)
	if !a.CurrencyIs(USD) {
		t.Error("CurrencyIs(USD) should be true")
	}
	if a.CurrencyIs(Currency("EUR")) {
		t.Error("CurrencyIs(EUR) should be false")
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
	if got := mustExact(t, a); got != "-45.00" {
		t.Errorf("Exact() = %q, want -45.00", got)
	}
	if got := mustDisplay(t, a); got != "-$45.00" {
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

// math.MinInt64 is excluded so Neg and render can stay infallible: -MinInt64
// overflows int64.
func TestNew_RejectsMinInt64(t *testing.T) {
	if _, err := New(math.MinInt64, 2, USD); !errors.Is(err, ErrRange) {
		t.Errorf("err = %v, want ErrRange", err)
	}
	// One above the floor is fine, and negating it cannot overflow.
	a, err := New(math.MinInt64+1, 0, USD)
	if err != nil {
		t.Fatalf("New(MinInt64+1): %v", err)
	}
	if got := a.Neg().units; got != math.MaxInt64 {
		t.Errorf("Neg().units = %d, want MaxInt64", got)
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
		b := MustParse(mustExact(t, a), USD)
		if !a.Equal(b) {
			t.Errorf("%q did not survive a round trip: %q", lit, mustExact(t, a))
		}
	}
}
