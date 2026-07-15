// Package civildate provides a timezone-free calendar date type.
package civildate

import (
	"cmp"
	"encoding/json"
	"fmt"
	"time"
)

// ISO8601Date represents a calendar date (year, month, day) with no
// time-of-day or timezone component. This prevents timezone-induced
// date shifts when exchanging dates between systems.
type ISO8601Date struct {
	year  int
	month time.Month
	day   int
}

// New creates an ISO8601Date from explicit year, month, and day values.
func New(year int, month time.Month, day int) ISO8601Date {
	return ISO8601Date{year: year, month: month, day: day}
}

// FromTime extracts the calendar date from a time.Time, discarding
// the time-of-day and timezone.
func FromTime(t time.Time) ISO8601Date {
	y, m, d := t.Date()
	return ISO8601Date{year: y, month: m, day: d}
}

// Today returns today's date in the local timezone.
func Today() ISO8601Date {
	return FromTime(time.Now())
}

// Parse parses a date string using the given Go time layout.
func Parse(layout, value string) (ISO8601Date, error) {
	t, err := time.Parse(layout, value)
	if err != nil {
		return ISO8601Date{}, err
	}
	return FromTime(t), nil
}

// Year returns the year.
func (d ISO8601Date) Year() int { return d.year }

// Month returns the month.
func (d ISO8601Date) Month() time.Month { return d.month }

// Day returns the day of the month.
func (d ISO8601Date) Day() int { return d.day }

// IsZero reports whether d is the zero date, which New, FromTime, and
// Parse never produce.
func (d ISO8601Date) IsZero() bool {
	return d == ISO8601Date{}
}

// Compare returns -1 if d falls before other, +1 if after, and 0 if
// they are the same date.
func (d ISO8601Date) Compare(other ISO8601Date) int {
	if c := cmp.Compare(d.year, other.year); c != 0 {
		return c
	}
	if c := cmp.Compare(d.month, other.month); c != 0 {
		return c
	}
	return cmp.Compare(d.day, other.day)
}

// String returns the date formatted as ISO 8601 "YYYY-MM-DD".
func (d ISO8601Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.year, int(d.month), d.day)
}

// Format returns the date formatted using the given Go time layout.
func (d ISO8601Date) Format(layout string) string {
	return time.Date(d.year, d.month, d.day, 0, 0, 0, 0, time.UTC).Format(layout)
}

// UnmarshalJSON parses a JSON string in "YYYY-MM-DD" format.
func (d *ISO8601Date) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := Parse("2006-01-02", s)
	if err != nil {
		return fmt.Errorf("invalid date %q: %w", s, err)
	}
	*d = parsed
	return nil
}
