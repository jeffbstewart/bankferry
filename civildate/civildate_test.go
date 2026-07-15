package civildate

import (
	"encoding/json"
	"testing"
	"time"
)

func TestIsZero(t *testing.T) {
	var zero ISO8601Date
	if !zero.IsZero() {
		t.Error("zero value should report IsZero")
	}
	if MustNew(2025, time.March, 15).IsZero() {
		t.Error("a real date should not report IsZero")
	}
	if FromTime(time.Now()).IsZero() {
		t.Error("FromTime should never produce the zero date")
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		name string
		a, b ISO8601Date
		want int
	}{
		{"equal", MustNew(2025, time.June, 3), MustNew(2025, time.June, 3), 0},
		{"earlier day", MustNew(2025, time.June, 3), MustNew(2025, time.June, 4), -1},
		{"later day", MustNew(2025, time.June, 4), MustNew(2025, time.June, 3), 1},
		{"earlier month", MustNew(2025, time.May, 31), MustNew(2025, time.June, 1), -1},
		{"later month", MustNew(2025, time.July, 1), MustNew(2025, time.June, 30), 1},
		{"earlier year", MustNew(2024, time.December, 31), MustNew(2025, time.January, 1), -1},
		{"later year", MustNew(2026, time.January, 1), MustNew(2025, time.December, 31), 1},
		{"year beats day", MustNew(2024, time.December, 31), MustNew(2025, time.January, 1), -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Compare(tc.b); got != tc.want {
				t.Errorf("%v.Compare(%v) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestNew(t *testing.T) {
	d, err := New(2025, time.March, 15)
	if err != nil {
		t.Fatalf("New(2025, March, 15): %v", err)
	}
	if d.Year() != 2025 || d.Month() != time.March || d.Day() != 15 {
		t.Errorf("New(2025, March, 15) = {%d, %v, %d}, want {2025, March, 15}",
			d.Year(), d.Month(), d.Day())
	}
}

// Out-of-range fields are nonsense, not dates. New must refuse them rather
// than silently normalize (January 32 is not February 1 here).
func TestNew_RejectsOutOfRange(t *testing.T) {
	cases := []struct {
		name      string
		year, day int
		month     time.Month
	}{
		{"month zero", 2025, 1, time.Month(0)},
		{"month thirteen", 2025, 1, time.Month(13)},
		{"month negative", 2025, 1, time.Month(-7)},
		{"day zero", 2025, 0, time.January},
		{"day negative", 2025, -7, time.January},
		{"day billion", 2025, -1_000_000_000, time.January},
		{"day past month end", 2025, 32, time.January},
		{"feb 29 non-leap", 2025, 29, time.February},
		{"feb 30 leap", 2024, 30, time.February},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.year, tc.month, tc.day); err == nil {
				t.Errorf("New(%d, %v, %d) should have failed", tc.year, tc.month, tc.day)
			}
		})
	}
}

// A genuine leap day is a valid date and must be accepted.
func TestNew_AcceptsLeapDay(t *testing.T) {
	if _, err := New(2024, time.February, 29); err != nil {
		t.Errorf("New(2024, February, 29): %v", err)
	}
}

func TestMustNew_PanicsOnInvalid(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustNew should panic on an invalid date")
		}
	}()
	MustNew(2025, time.February, 30)
}

func TestFromTime(t *testing.T) {
	loc := time.FixedZone("UTC-8", -8*60*60)
	tm := time.Date(2025, time.December, 31, 23, 59, 59, 0, loc)
	d := FromTime(tm)
	if d.Year() != 2025 || d.Month() != time.December || d.Day() != 31 {
		t.Errorf("FromTime gave {%d, %v, %d}, want {2025, December, 31}",
			d.Year(), d.Month(), d.Day())
	}
}

func TestToday(t *testing.T) {
	d := Today()
	now := time.Now()
	if d.Year() != now.Year() || d.Month() != now.Month() || d.Day() != now.Day() {
		t.Errorf("Today() = {%d, %v, %d}, want {%d, %v, %d}",
			d.Year(), d.Month(), d.Day(),
			now.Year(), now.Month(), now.Day())
	}
}

func TestString(t *testing.T) {
	tests := []struct {
		date ISO8601Date
		want string
	}{
		{MustNew(2025, time.January, 5), "2025-01-05"},
		{MustNew(2025, time.December, 31), "2025-12-31"},
		{MustNew(2000, time.February, 1), "2000-02-01"},
	}
	for _, tc := range tests {
		got := tc.date.String()
		if got != tc.want {
			t.Errorf("%v.String() = %q, want %q", tc.date, got, tc.want)
		}
	}
}

func TestFormat(t *testing.T) {
	d := MustNew(2025, time.June, 3)
	if got := d.Format("20060102"); got != "20250603" {
		t.Errorf("Format(20060102) = %q, want %q", got, "20250603")
	}
	if got := d.Format("2006-01-02"); got != "2025-06-03" {
		t.Errorf("Format(2006-01-02) = %q, want %q", got, "2025-06-03")
	}
	if got := d.Format("Jan 2, 2006"); got != "Jun 3, 2025" {
		t.Errorf("Format(Jan 2, 2006) = %q, want %q", got, "Jun 3, 2025")
	}
}

func TestParse(t *testing.T) {
	d, err := Parse("2006-01-02", "2025-03-15")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Year() != 2025 || d.Month() != time.March || d.Day() != 15 {
		t.Errorf("Parse gave {%d, %v, %d}, want {2025, March, 15}",
			d.Year(), d.Month(), d.Day())
	}
}

func TestParse_Invalid(t *testing.T) {
	_, err := Parse("2006-01-02", "not-a-date")
	if err == nil {
		t.Error("expected error for invalid date string")
	}
}

// An out-of-range but well-formed date must be rejected, not normalized.
func TestParse_OutOfRange(t *testing.T) {
	for _, value := range []string{"2025-13-01", "2025-00-01", "2025-02-30", "2025-01-32", "2025-01-00"} {
		if _, err := Parse("2006-01-02", value); err == nil {
			t.Errorf("Parse(%q) should have failed", value)
		}
	}
}

func TestMarshalJSON(t *testing.T) {
	b, err := json.Marshal(MustNew(2025, time.March, 15))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got, want := string(b), `"2025-03-15"`; got != want {
		t.Errorf("Marshal = %s, want %s", got, want)
	}
}

// Marshal then Unmarshal must reproduce the original date exactly.
func TestJSON_RoundTrip(t *testing.T) {
	want := MustNew(2000, time.February, 29)
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got ISO8601Date
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Compare(want) != 0 {
		t.Errorf("round trip = %v, want %v", got, want)
	}
}

// The zero date must survive a JSON round trip: it marshals to null and
// unmarshals back to the zero value, rather than to an unparseable
// "0000-00-00".
func TestJSON_ZeroRoundTrip(t *testing.T) {
	var zero ISO8601Date
	b, err := json.Marshal(zero)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(b) != "null" {
		t.Errorf("Marshal(zero) = %s, want null", b)
	}
	var got ISO8601Date
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal(null): %v", err)
	}
	if !got.IsZero() {
		t.Errorf("round trip of zero = %v, want the zero date", got)
	}
}

// A zero date nested in a struct also round-trips through null.
func TestJSON_ZeroRoundTripInStruct(t *testing.T) {
	type wrapper struct {
		When ISO8601Date `json:"when"`
	}
	b, err := json.Marshal(wrapper{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(b) != `{"when":null}` {
		t.Errorf("Marshal = %s, want {\"when\":null}", b)
	}
	var got wrapper
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !got.When.IsZero() {
		t.Errorf("round trip = %v, want the zero date", got.When)
	}
}

func TestUnmarshalJSON_Valid(t *testing.T) {
	var d ISO8601Date
	if err := json.Unmarshal([]byte(`"2025-03-15"`), &d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Year() != 2025 || d.Month() != time.March || d.Day() != 15 {
		t.Errorf("got {%d, %v, %d}, want {2025, March, 15}",
			d.Year(), d.Month(), d.Day())
	}
}

func TestUnmarshalJSON_Invalid(t *testing.T) {
	cases := []string{
		`"not-a-date"`,
		`"2025/03/15"`,
		`"03-15-2025"`,
		`"2025-13-01"`,
		`123`,
	}
	for _, input := range cases {
		var d ISO8601Date
		if err := json.Unmarshal([]byte(input), &d); err == nil {
			t.Errorf("Unmarshal(%s) expected error, got nil", input)
		}
	}
}
