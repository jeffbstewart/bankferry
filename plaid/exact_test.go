package plaid

import (
	"testing"

	"github.com/jeffbstewart/bankferry/money"
)

// mustExact renders an amount for a test, failing on the error only a
// pathologically large value would produce. It keeps the many balance/amount
// assertions readable now that money.Exact is fallible.
func mustExact(t *testing.T, a money.Amount) string {
	t.Helper()
	s, err := a.Exact()
	if err != nil {
		t.Fatalf("Exact(): %v", err)
	}
	return s
}
