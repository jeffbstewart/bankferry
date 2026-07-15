// Package money provides an exact fixed-point monetary amount carrying
// its currency.
//
// It exists because financial data must never pass through a binary
// float. Plaid sends amounts as JSON number literals — "89.4", "-500",
// "23631.9805" — which are exact decimal text on the wire. Decoding one
// into a float64 discards that exactness: 89.4 has no exact binary
// representation. Parsing the literal directly preserves every digit.
package money

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// maxScale bounds the digits after the decimal point. Plaid has been seen
// to report four (an investment balance of 23631.9805); nine leaves ample
// headroom while keeping the scaled value inside an int64.
const maxScale = 9

// minExactScale is the number of decimal places Exact always shows, so a
// whole-dollar amount still renders with cents.
const minExactScale = 2

var (
	// ErrInvalid reports a string that is not a decimal literal.
	ErrInvalid = errors.New("money: invalid decimal literal")
	// ErrRange reports a value that cannot be held exactly.
	ErrRange = errors.New("money: value out of range")
	// ErrCurrency reports an unsupported or mismatched currency.
	ErrCurrency = errors.New("money: unsupported currency")
)

// Currency is an ISO 4217 currency code. Only USD is supported: an OFX
// statement carries a single CURDEF and this pipeline performs no
// conversion, so any other currency must be refused rather than silently
// folded into a dollar statement.
type Currency string

const USD Currency = "USD"

// ParseCurrency validates an ISO 4217 code.
func ParseCurrency(s string) (Currency, error) {
	switch Currency(strings.ToUpper(strings.TrimSpace(s))) {
	case USD:
		return USD, nil
	default:
		return "", fmt.Errorf("%w: %q (only %q is supported)", ErrCurrency, s, USD)
	}
}

// Decimals is the number of decimal places the currency is conventionally
// written with: two for USD.
func (c Currency) Decimals() uint8 {
	switch c {
	case USD:
		return 2
	default:
		return 2
	}
}

// Symbol is the currency's display symbol.
func (c Currency) Symbol() string {
	switch c {
	case USD:
		return "$"
	default:
		return ""
	}
}

func (c Currency) String() string { return string(c) }

// Zero is the zero amount in this currency, held at the currency's
// conventional precision.
func (c Currency) Zero() Amount {
	return Amount{units: 0, scale: c.Decimals(), currency: c}
}

// Amount is an exact monetary value: units scaled by 10^-scale, in a
// currency. The zero value is not usable; build one with Parse, New, or
// Currency.Zero.
type Amount struct {
	units    int64
	scale    uint8
	currency Currency
}

// New builds an Amount from a scaled integer, so New(-4500, 2, USD) is
// -$45.00.
//
// units may not be math.MinInt64. Excluding that one value is what lets Neg
// and render stay infallible: -math.MinInt64 overflows int64, so a value that
// could hold it would make sign-flipping and absolute-value rendering fallible
// for the sake of a magnitude no monetary amount needs.
func New(units int64, scale uint8, c Currency) (Amount, error) {
	if scale > maxScale {
		return Amount{}, fmt.Errorf("%w: scale %d exceeds %d", ErrRange, scale, maxScale)
	}
	if units == math.MinInt64 {
		return Amount{}, fmt.Errorf("%w: units may not be math.MinInt64", ErrRange)
	}
	if c == "" {
		return Amount{}, fmt.Errorf("%w: empty", ErrCurrency)
	}
	return Amount{units: units, scale: scale, currency: c}, nil
}

// MustParse is Parse for literals known to be valid. It panics otherwise,
// so it belongs in tests and package-level constants, never in a path
// handling provider data.
func MustParse(s string, c Currency) Amount {
	a, err := Parse(s, c)
	if err != nil {
		panic(err)
	}
	return a
}

// Parse reads an exact decimal literal: an optional sign, digits, an
// optional fractional part, and an optional base-10 exponent. JSON permits
// the exponent form, so it is handled rather than rejected, even though
// Plaid has not been observed to use it.
//
// No rounding ever occurs. A literal too precise or too large for an exact
// int64 representation is an error, not an approximation.
func Parse(s string, c Currency) (Amount, error) {
	if c == "" {
		return Amount{}, fmt.Errorf("%w: empty", ErrCurrency)
	}
	if s == "" {
		return Amount{}, fmt.Errorf("%w: empty string", ErrInvalid)
	}

	i := 0
	negative := false
	switch s[0] {
	case '+':
		i++
	case '-':
		negative = true
		i++
	}

	intPart, n := readDigits(s[i:])
	i += n

	var fracPart string
	if i < len(s) && s[i] == '.' {
		i++
		var m int
		fracPart, m = readDigits(s[i:])
		i += m
	}

	if intPart == "" && fracPart == "" {
		return Amount{}, fmt.Errorf("%w: %q has no digits", ErrInvalid, s)
	}

	exponent := 0
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		var err error
		exponent, err = readExponent(s[i:])
		if err != nil {
			return Amount{}, fmt.Errorf("%w: %q: %v", ErrInvalid, s, err)
		}
		i = len(s)
	}

	if i != len(s) {
		return Amount{}, fmt.Errorf("%w: %q has trailing characters", ErrInvalid, s)
	}

	digits := intPart + fracPart
	scale := len(fracPart) - exponent

	// A negative scale means the value is larger than its digits suggest;
	// materialize the implied trailing zeros rather than track it.
	for scale < 0 {
		digits += "0"
		scale++
	}
	if scale > maxScale {
		return Amount{}, fmt.Errorf("%w: %q needs scale %d, limit is %d", ErrRange, s, scale, maxScale)
	}

	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		digits = "0"
	}

	units, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || units > math.MaxInt64 {
		return Amount{}, fmt.Errorf("%w: %q does not fit in an int64", ErrRange, s)
	}

	signed := int64(units)
	if negative {
		signed = -signed
	}
	return Amount{units: signed, scale: uint8(scale), currency: c}, nil
}

func readDigits(s string) (string, int) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i], i
}

func readExponent(s string) (int, error) {
	if s == "" {
		return 0, errors.New("exponent has no digits")
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	// Bound it so a pathological exponent cannot drive the digit loop.
	if v < -maxScale || v > 18 {
		return 0, fmt.Errorf("exponent %d out of range", v)
	}
	return v, nil
}

// Currency returns the amount's currency.
func (a Amount) Currency() Currency { return a.currency }

// CurrencyIs reports whether the amount is denominated in the given currency.
func (a Amount) CurrencyIs(c Currency) bool { return a.currency == c }

// Neg returns the amount with its sign flipped. It is infallible because New
// and Parse both refuse math.MinInt64, so -units can never overflow.
func (a Amount) Neg() Amount {
	return Amount{units: -a.units, scale: a.scale, currency: a.currency}
}

// IsNegative reports whether the amount is strictly below zero.
func (a Amount) IsNegative() bool { return a.units < 0 }

// IsZero reports whether the amount is exactly zero.
func (a Amount) IsZero() bool { return a.units == 0 }

// Equal reports whether two amounts denote the same value in the same
// currency, regardless of the scale they are held at: 1.5 equals 1.50.
func (a Amount) Equal(other Amount) bool {
	if a.currency != other.currency {
		return false
	}
	x, y, ok := align(a, other)
	if !ok {
		return false
	}
	return x == y
}

func align(a, b Amount) (int64, int64, bool) {
	for a.scale < b.scale {
		v, ok := mul10(a.units)
		if !ok {
			return 0, 0, false
		}
		a.units, a.scale = v, a.scale+1
	}
	for b.scale < a.scale {
		v, ok := mul10(b.units)
		if !ok {
			return 0, 0, false
		}
		b.units, b.scale = v, b.scale+1
	}
	return a.units, b.units, true
}

func mul10(v int64) (int64, bool) {
	if v > math.MaxInt64/10 || v < math.MinInt64/10 {
		return 0, false
	}
	return v * 10, true
}

var pow10 = [...]int64{
	1, 10, 100, 1_000, 10_000, 100_000, 1_000_000,
	10_000_000, 100_000_000, 1_000_000_000,
}

// rescaled returns the units at the requested scale, rounding half away
// from zero when precision must be dropped. Rounding only ever happens
// for display; Exact never rounds. Scaling up multiplies, so it can exceed
// int64: rescaled reports that as an error rather than returning a wrong
// value.
func (a Amount) rescaled(target uint8) (int64, error) {
	if a.scale == target {
		return a.units, nil
	}
	if a.scale < target {
		units := a.units
		for s := a.scale; s < target; s++ {
			v, ok := mul10(units)
			if !ok {
				return 0, fmt.Errorf("%w: %s does not fit at scale %d", ErrRange, render(a.units, a.scale), target)
			}
			units = v
		}
		return units, nil
	}

	// Scaling down: divide and round. maxScale bounds the shift to a valid
	// pow10 index, but check rather than trust it — an out-of-range index
	// would panic instead of failing cleanly.
	shift := int(a.scale) - int(target)
	if shift <= 0 || shift >= len(pow10) {
		return 0, fmt.Errorf("%w: scale shift %d has no power of ten", ErrRange, shift)
	}
	div := pow10[shift]
	negative := a.units < 0
	abs := a.units
	if negative {
		abs = -abs
	}

	q, r := abs/div, abs%div
	if r*2 >= div {
		q++
	}
	if negative {
		q = -q
	}
	return q, nil
}

// render writes the scaled integer with a decimal point inserted.
func render(units int64, scale uint8) string {
	abs := units
	if abs < 0 {
		abs = -abs
	}
	digits := strconv.FormatInt(abs, 10)

	s := int(scale)
	if len(digits) <= s {
		digits = strings.Repeat("0", s-len(digits)+1) + digits
	}

	var b strings.Builder
	if units < 0 {
		b.WriteByte('-')
	}
	if s == 0 {
		b.WriteString(digits)
		return b.String()
	}
	point := len(digits) - s
	b.WriteString(digits[:point])
	b.WriteByte('.')
	b.WriteString(digits[point:])
	return b.String()
}

// Exact renders every significant digit, with at least two decimal
// places, and never rounds: 12 becomes "12.00" and 23631.9805 stays
// "23631.9805". This is what belongs in an OFX document. Padding to the
// minimum precision multiplies, so a value too large to pad is an error
// rather than a silently truncated rendering.
func (a Amount) Exact() (string, error) {
	scale := a.scale
	units := a.units
	for scale < minExactScale {
		v, ok := mul10(units)
		if !ok {
			return "", fmt.Errorf("%w: %s does not fit with %d decimals", ErrRange, render(a.units, a.scale), minExactScale)
		}
		units, scale = v, scale+1
	}
	return render(units, scale), nil
}

// Quantity renders the value at the currency's conventional precision,
// two decimal places for USD, rounding half away from zero. It carries no
// symbol: "12.56", "-12.56". It errors when rendering would overflow int64.
func (a Amount) Quantity() (string, error) {
	d := a.currency.Decimals()
	units, err := a.rescaled(d)
	if err != nil {
		return "", err
	}
	return render(units, d), nil
}

// Display renders the value for a human, with the currency symbol and a
// leading minus for negatives: "$12.56", "-$12.56".
func (a Amount) Display() (string, error) {
	q, err := a.Quantity()
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(q, "-") {
		return "-" + a.currency.Symbol() + q[1:], nil
	}
	return a.currency.Symbol() + q, nil
}

// Accounting renders the value in accounting style, wrapping negatives in
// parentheses rather than signing them: "$12.56", "($12.56)".
func (a Amount) Accounting() (string, error) {
	q, err := a.Quantity()
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(q, "-") {
		return "(" + a.currency.Symbol() + q[1:] + ")", nil
	}
	return a.currency.Symbol() + q, nil
}

// String is the human display form. As a fmt.Stringer it cannot return an
// error, so for the pathological amount too large to render at the currency's
// precision it falls back to the exact stored digits, which never overflow.
func (a Amount) String() string {
	s, err := a.Display()
	if err != nil {
		raw := render(a.units, a.scale)
		if strings.HasPrefix(raw, "-") {
			return "-" + a.currency.Symbol() + raw[1:]
		}
		return a.currency.Symbol() + raw
	}
	return s
}
