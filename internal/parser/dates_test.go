package parser

import (
	"testing"
	"time"
)

func loc() *time.Location {
	l, _ := time.LoadLocation("Asia/Almaty")
	return l
}

func TestParsePeriod(t *testing.T) {
	now := time.Date(2026, time.September, 20, 15, 0, 0, 0, loc())
	cases := []struct {
		arg      string
		from, to string
	}{
		{"", "2026-09-01", "2026-10-01"},
		{"last", "2026-08-01", "2026-09-01"},
		{"last month", "2026-08-01", "2026-09-01"},
		{"september", "2026-09-01", "2026-10-01"},
		{"may", "2026-05-01", "2026-06-01"},
		{"december", "2025-12-01", "2026-01-01"},
		{"sep 2025", "2025-09-01", "2025-10-01"},
		{"this week", "2026-09-14", "2026-09-21"},
		{"year", "2026-01-01", "2027-01-01"},
		{"today", "2026-09-20", "2026-09-21"},
		{"yesterday", "2026-09-19", "2026-09-20"},
		{"прошлый", "2026-08-01", "2026-09-01"},
		{"сентябрь", "2026-09-01", "2026-10-01"},
		{"май", "2026-05-01", "2026-06-01"},
		{"декабрь", "2025-12-01", "2026-01-01"},
		{"09.2025", "2025-09-01", "2025-10-01"},
		{"2025-09", "2025-09-01", "2025-10-01"},
		{"2025", "2025-01-01", "2026-01-01"},
		{"01.09-15.09", "2026-09-01", "2026-09-16"},
		{"сегодня", "2026-09-20", "2026-09-21"},
		{"вчера", "2026-09-19", "2026-09-20"},
		{"неделя", "2026-09-14", "2026-09-21"},
		{"год", "2026-01-01", "2027-01-01"},
	}
	for _, c := range cases {
		p, err := ParsePeriod(c.arg, now, loc())
		if err != nil {
			t.Errorf("ParsePeriod(%q): %v", c.arg, err)
			continue
		}
		if got := p.From.Format("2006-01-02"); got != c.from {
			t.Errorf("ParsePeriod(%q).From = %s, want %s", c.arg, got, c.from)
		}
		if got := p.To.Format("2006-01-02"); got != c.to {
			t.Errorf("ParsePeriod(%q).To = %s, want %s", c.arg, got, c.to)
		}
	}
	if _, err := ParsePeriod("абракадабра", now, loc()); err == nil {
		t.Error("ParsePeriod(gibberish): expected an error")
	}
}

func TestMonthTitleIsEnglish(t *testing.T) {
	if got := MonthTitle(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)); got != "September 2026" {
		t.Errorf("MonthTitle = %q, want %q", got, "September 2026")
	}
}

// The error is shown to the user as-is, so it must be English.
func TestParsePeriodErrorIsEnglish(t *testing.T) {
	_, err := ParsePeriod("gibberish", time.Now(), loc())
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, r := range err.Error() {
		if r >= 'а' && r <= 'я' || r >= 'А' && r <= 'Я' {
			t.Errorf("error is not English: %q", err.Error())
			break
		}
	}
}

// Statement buttons carry the period as text; reading it back must give the
// same interval, or "Operations" would list a different period than the statement.
func TestPeriodArgRoundTrips(t *testing.T) {
	now := time.Date(2026, time.September, 20, 15, 0, 0, 0, loc())
	for _, arg := range []string{"", "last", "september", "2025", "week", "today", "year", "01.09-15.09", "05.07.2026-20.08.2026"} {
		p, err := ParsePeriod(arg, now, loc())
		if err != nil {
			t.Errorf("ParsePeriod(%q): %v", arg, err)
			continue
		}
		back, err := ParsePeriod(p.Arg(), now, loc())
		if err != nil {
			t.Errorf("%q → Arg %q could not be read back: %v", arg, p.Arg(), err)
			continue
		}
		if !back.From.Equal(p.From) || !back.To.Equal(p.To) {
			t.Errorf("%q → Arg %q → %v..%v, want %v..%v", arg, p.Arg(), back.From, back.To, p.From, p.To)
		}
	}
	// A whole month stays short; anything else is an explicit range.
	if got := mustPeriod(t, "september", now).Arg(); got != "2026-09" {
		t.Errorf("a month's Arg = %q, want 2026-09", got)
	}
	if got := mustPeriod(t, "01.09-15.09", now).Arg(); got != "2026-09-01..2026-09-15" {
		t.Errorf("a range's Arg = %q, want 2026-09-01..2026-09-15", got)
	}
}

func mustPeriod(t *testing.T, arg string, now time.Time) Period {
	t.Helper()
	p, err := ParsePeriod(arg, now, loc())
	if err != nil {
		t.Fatalf("ParsePeriod(%q): %v", arg, err)
	}
	return p
}
