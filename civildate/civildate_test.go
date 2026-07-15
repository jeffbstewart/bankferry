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
	if New(2025, time.March, 15).IsZero() {
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
		{"equal", New(2025, time.June, 3), New(2025, time.June, 3), 0},
		{"earlier day", New(2025, time.June, 3), New(2025, time.June, 4), -1},
		{"later day", New(2025, time.June, 4), New(2025, time.June, 3), 1},
		{"earlier month", New(2025, time.May, 31), New(2025, time.June, 1), -1},
		{"later month", New(2025, time.July, 1), New(2025, time.June, 30), 1},
		{"earlier year", New(2024, time.December, 31), New(2025, time.January, 1), -1},
		{"later year", New(2026, time.January, 1), New(2025, time.December, 31), 1},
		{"year beats day", New(2024, time.December, 31), New(2025, time.January, 1), -1},
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
	d := New(2025, time.March, 15)
	if d.Year() != 2025 || d.Month() != time.March || d.Day() != 15 {
		t.Errorf("New(2025, March, 15) = {%d, %v, %d}, want {2025, March, 15}",
			d.Year(), d.Month(), d.Day())
	}
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
		{New(2025, time.January, 5), "2025-01-05"},
		{New(2025, time.December, 31), "2025-12-31"},
		{New(2000, time.February, 1), "2000-02-01"},
	}
	for _, tc := range tests {
		got := tc.date.String()
		if got != tc.want {
			t.Errorf("%v.String() = %q, want %q", tc.date, got, tc.want)
		}
	}
}

func TestFormat(t *testing.T) {
	d := New(2025, time.June, 3)
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
		`123`,
	}
	for _, input := range cases {
		var d ISO8601Date
		if err := json.Unmarshal([]byte(input), &d); err == nil {
			t.Errorf("Unmarshal(%s) expected error, got nil", input)
		}
	}
}
