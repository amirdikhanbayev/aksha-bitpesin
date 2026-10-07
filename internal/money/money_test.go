package money

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in       string
		decimals int
		want     int64
		wantErr  bool
	}{
		{"1500", 2, 150000, false},
		{"1 500,50", 2, 150050, false},
		{"1,500.50", 2, 150050, false},
		{"1.500,50", 2, 150050, false},
		{"1,500", 2, 150000, false}, // one comma + three digits: thousands, as the bot prints it
		{"1,234,567", 2, 123456700, false},
		{"1.234.567", 2, 123456700, false},
		{"0,500", 2, 50, false}, // leading zero: a decimal comma
		{"1,5", 2, 150, false},
		{"1,5k", 2, 150000, false},
		{"1500.5", 2, 150050, false},
		{"1500.567", 2, 150056, false},
		{"12к", 2, 1200000, false},
		{"12k", 2, 1200000, false},
		{"1.5млн", 2, 150000000, false},
		{"1.2kk", 2, 120000000, false},
		{"-300", 2, -30000, false},
		{"+300", 2, 30000, false},
		{"0,01", 2, 1, false},
		{"100", 0, 100, false},
		{"", 2, 0, true},
		{"абв", 2, 0, true},
		{"1.2.3", 2, 0, true},
		{"1,2,3", 2, 0, true},
		{"12,34,567", 2, 0, true},
	}
	for _, c := range cases {
		got, err := Parse(c.in, c.decimals)
		if c.wantErr {
			if err == nil {
				t.Errorf("Parse(%q): expected an error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestFormat(t *testing.T) {
	cases := []struct {
		minor    int64
		decimals int
		want     string
	}{
		{150000, 2, "1,500"},
		{150050, 2, "1,500.50"},
		{1, 2, "0.01"},
		{-150050, 2, "-1,500.50"},
		{123456789, 2, "1,234,567.89"},
		{100, 0, "100"},
	}
	for _, c := range cases {
		if got := Format(c.minor, c.decimals); got != c.want {
			t.Errorf("Format(%d, %d) = %q, want %q", c.minor, c.decimals, got, c.want)
		}
	}
}

func TestConvert(t *testing.T) {
	// 10 USD at a rate of 540 = 5400 KZT
	if got := Convert(1000, 2, 2, 540); got != 540000 {
		t.Errorf("Convert = %d, want 540000", got)
	}
}

func TestFormatPlain(t *testing.T) {
	cases := []struct {
		minor    int64
		decimals int
		want     string
	}{
		{150050, 2, "1500.50"},
		{150000, 2, "1500.00"}, // CSV keeps the full precision, unlike Format
		{1, 2, "0.01"},
		{-150050, 2, "-1500.50"},
		{100, 0, "100"},
	}
	for _, c := range cases {
		if got := FormatPlain(c.minor, c.decimals); got != c.want {
			t.Errorf("FormatPlain(%d, %d) = %q, want %q", c.minor, c.decimals, got, c.want)
		}
	}
}

// What the bot prints must be readable back by the parser.
func TestFormatParseRoundTrip(t *testing.T) {
	for _, minor := range []int64{1, 99, 100, 150000, 150050, 123456789, 5_000_00, 999_999_99} {
		text := Format(minor, 2)
		got, err := Parse(text, 2)
		if err != nil || got != minor {
			t.Errorf("Parse(Format(%d)) = %d, %v (text %q)", minor, got, err, text)
		}
	}
}
