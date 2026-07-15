// Package payee implements pattern-based matching of raw bank
// transaction descriptions to clean payee names.
package payee

import (
	"sort"
	"strings"
)

// Payee represents a known clean payee name.
type Payee struct {
	ID   int64
	Name string
}

// MatchField names which of a transaction's two names a rule tests against.
type MatchField string

const (
	// MatchRaw tests the pattern against the raw bank descriptor.
	MatchRaw MatchField = "raw"
	// MatchMerchant tests it against the provider's normalized merchant name.
	MatchMerchant MatchField = "merchant"
)

// Rule maps a case-insensitive substring pattern to a Payee. Field selects
// which of the transaction's two names the pattern is tested against; an empty
// Field is treated as MatchRaw.
type Rule struct {
	ID       int64
	PayeeID  int64
	Pattern  string
	Field    MatchField
	Priority int
}

// Match holds the result of matching a transaction description.
type Match struct {
	Payee      Payee
	Confidence string // "rule" when a rule matched, "" when no match
}

// Matched reports whether the match found a payee.
func (m Match) Matched() bool {
	return m.Confidence != ""
}

// Matcher holds a set of rules and performs matching against
// transaction descriptions.
type Matcher struct {
	rules  []Rule
	payees map[int64]Payee
}

// NewMatcher creates a Matcher from a set of payees and rules.
// Rules are sorted by priority descending, then pattern length
// descending (longer patterns are more specific).
func NewMatcher(payees []Payee, rules []Rule) *Matcher {
	pm := make(map[int64]Payee, len(payees))
	for _, p := range payees {
		pm[p.ID] = p
	}

	sorted := make([]Rule, len(rules))
	copy(sorted, rules)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority > sorted[j].Priority
		}
		return len(sorted[i].Pattern) > len(sorted[j].Pattern)
	})

	return &Matcher{
		rules:  sorted,
		payees: pm,
	}
}

// Match resolves a transaction's payee from its raw descriptor and its
// provider merchant name (either may be empty).
//
// Rules are tried in two tiers, raw before merchant, because the raw
// descriptor is immutable ground truth while merchant_name is the provider's
// fallible inference:
//
//  1. rules keyed on the raw field, tested against raw
//  2. rules keyed on the merchant field, tested against merchant
//
// Within each tier the usual priority/length ordering applies. The first hit
// wins. A Match with Confidence="" means no rule matched — the caller decides
// the fallback (typically merchant_name, then raw).
func (m *Matcher) Match(raw, merchant string) Match {
	if hit, ok := m.matchField(MatchRaw, raw); ok {
		return hit
	}
	if hit, ok := m.matchField(MatchMerchant, merchant); ok {
		return hit
	}
	return Match{}
}

// matchField tries the rules keyed on one field against one value.
func (m *Matcher) matchField(field MatchField, value string) (Match, bool) {
	if value == "" {
		return Match{}, false
	}
	upper := strings.ToUpper(value)
	for _, r := range m.rules {
		if ruleField(r) != field {
			continue
		}
		if strings.Contains(upper, strings.ToUpper(r.Pattern)) {
			return Match{Payee: m.payees[r.PayeeID], Confidence: "rule"}, true
		}
	}
	return Match{}, false
}

// ruleField normalizes a rule's field, treating the empty default as raw.
func ruleField(r Rule) MatchField {
	if r.Field == MatchMerchant {
		return MatchMerchant
	}
	return MatchRaw
}
