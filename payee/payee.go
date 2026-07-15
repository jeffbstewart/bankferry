// Package payee implements pattern-based matching of raw bank
// transaction descriptions to clean payee names.
package payee

import (
	"fmt"
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

// Match holds the result of matching a transaction description. The zero
// Match carries no payee and reports Matched() == false.
type Match struct {
	Payee   Payee
	matched bool
}

// Matched reports whether a rule matched, and so whether Payee is meaningful.
func (m Match) Matched() bool {
	return m.matched
}

// compiledRule is a Rule prepared for matching: its field normalized, its
// pattern upper-cased once at construction rather than on every Match, and its
// payee resolved so a match needs no map lookup.
type compiledRule struct {
	field        MatchField
	patternUpper string
	payee        Payee
}

// Matcher holds a set of compiled rules and performs matching against
// transaction descriptions.
type Matcher struct {
	rules []compiledRule
}

// NewMatcher creates a Matcher from a set of payees and rules. Rules are
// sorted by priority descending, then pattern length descending (longer
// patterns are more specific).
//
// Every rule's PayeeID must resolve to one of payees. A rule pointing at an
// unknown payee is a data-integrity error, not a rule that quietly matches to
// an empty name, so NewMatcher refuses it rather than let a nameless match flow
// downstream into the OFX NAME field.
func NewMatcher(payees []Payee, rules []Rule) (*Matcher, error) {
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

	compiled := make([]compiledRule, len(sorted))
	for i, r := range sorted {
		p, ok := pm[r.PayeeID]
		if !ok {
			return nil, fmt.Errorf("payee: rule %d references unknown payee %d", r.ID, r.PayeeID)
		}
		compiled[i] = compiledRule{
			field:        ruleField(r),
			patternUpper: strings.ToUpper(r.Pattern),
			payee:        p,
		}
	}

	return &Matcher{rules: compiled}, nil
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
// wins. A Match reporting Matched() == false means no rule matched — the caller
// decides the fallback (typically merchant_name, then raw).
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
		if r.field != field {
			continue
		}
		if strings.Contains(upper, r.patternUpper) {
			return Match{Payee: r.payee, matched: true}, true
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
