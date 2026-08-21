package money

import (
	"errors"
	"testing"
)

// CheckLiteral is the grammar Parse uses, with no currency and no range. A
// share quantity with more digits than an int64 scale allows is fine here;
// anything that is not a decimal literal is not.
func TestCheckLiteral(t *testing.T) {
	for _, ok := range []string{"0", "-1", "+2.5", "123.456789123456789012345", "1e3", "2.5E-7", ".5", "5."} {
		if err := CheckLiteral(ok); err != nil {
			t.Errorf("CheckLiteral(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-", ".", "1,000", "$5", "1.2.3", "1e", "abc", " 1", "1 ", "NaN", "Infinity", "0x10"} {
		if err := CheckLiteral(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("CheckLiteral(%q) = %v, want ErrInvalid", bad, err)
		}
	}
}
