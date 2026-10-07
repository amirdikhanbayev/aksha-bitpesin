// Package money works with amounts in minor units (cents, tiyn).
// No floats in the arithmetic: only int64 and integer math.
package money

import (
	"fmt"
	"math"
	"strings"
)

// Parse parses an amount typed by a human into minor units.
// Understands: "1500", "1,500.50", "1 500,50", "1500.5", "12k", "1.2kk", "3 500 kzt".
// Both "." and "," may be the decimal separator; see normalizeNumber.
func Parse(s string, decimals int) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("empty amount")
	}
	s = strings.NewReplacer(" ", "", " ", "", "_", "", "'", "").Replace(s)
	s = strings.TrimPrefix(s, "+")

	mult := int64(1)
	for {
		suf, mul := matchSuffix(s)
		if suf == "" {
			break
		}
		mult *= mul
		s = strings.TrimSuffix(s, suf)
	}

	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	if s == "" {
		return 0, fmt.Errorf("does not look like an amount")
	}
	s = normalizeNumber(s)

	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	if strings.Contains(fracPart, ".") {
		return 0, fmt.Errorf("does not look like an amount")
	}
	if intPart == "" {
		intPart = "0"
	}
	for _, r := range intPart + fracPart {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("does not look like an amount: %q", s)
		}
	}

	var whole int64
	for _, r := range intPart {
		whole = whole*10 + int64(r-'0')
		if whole > math.MaxInt64/1000 {
			return 0, fmt.Errorf("amount is too large")
		}
	}

	scale := pow10(decimals)
	total := whole * scale
	if hasFrac && fracPart != "" {
		// pad/truncate the fractional part to `decimals` digits
		if len(fracPart) > decimals {
			fracPart = fracPart[:decimals]
		}
		var frac int64
		for _, r := range fracPart {
			frac = frac*10 + int64(r-'0')
		}
		for i := len(fracPart); i < decimals; i++ {
			frac *= 10
		}
		total += frac
	}
	total *= mult
	if neg {
		total = -total
	}
	return total, nil
}

// Format prints minor units as "1,500.50" (without the currency code).
func Format(minor int64, decimals int) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	scale := pow10(decimals)
	whole := minor / scale
	frac := minor % scale

	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteString(group(whole))
	if decimals > 0 && frac != 0 {
		b.WriteByte('.')
		b.WriteString(fmt.Sprintf("%0*d", decimals, frac))
	}
	return b.String()
}

// FormatPlain prints minor units as a bare number for machine consumption
// (CSV): no grouping, dot decimal, always the full number of decimals.
func FormatPlain(minor int64, decimals int) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	scale := pow10(decimals)
	if decimals == 0 {
		return fmt.Sprintf("%s%d", sign, minor)
	}
	return fmt.Sprintf("%s%d.%0*d", sign, minor/scale, decimals, minor%scale)
}

// FormatCode prints the amount together with the currency code: "1,500.50 USD".
func FormatCode(minor int64, decimals int, code string) string {
	return Format(minor, decimals) + " " + code
}

// FormatSigned always prints the sign: "+1 500", "−300".
func FormatSigned(minor int64, decimals int, code string) string {
	sign := "+"
	if minor < 0 {
		sign = "−"
		minor = -minor
	}
	out := sign + Format(minor, decimals)
	if code != "" {
		out += " " + code
	}
	return out
}

// Convert converts an amount from one currency to another using rate,
// where rate = how many units of the toDecimals currency per 1 unit of the from currency.
func Convert(minor int64, fromDecimals, toDecimals int, rate float64) int64 {
	v := float64(minor) / float64(pow10(fromDecimals)) * rate
	return int64(math.Round(v * float64(pow10(toDecimals))))
}

func pow10(n int) int64 {
	p := int64(1)
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}

func group(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	lead := len(s) % 3
	if lead > 0 {
		b.WriteString(s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// matchSuffix recognizes a multiplier suffix: 12к = 12000, 1.5млн = 1500000.
func matchSuffix(s string) (string, int64) {
	suffixes := []struct {
		suf string
		mul int64
	}{
		{"кк", 1_000_000}, {"kk", 1_000_000},
		{"млн", 1_000_000}, {"лям", 1_000_000},
		{"м", 1_000_000}, {"m", 1_000_000},
		{"тыс", 1_000}, {"к", 1_000}, {"k", 1_000},
	}
	for _, x := range suffixes {
		if len(s) > len(x.suf) && strings.HasSuffix(s, x.suf) {
			return x.suf, x.mul
		}
	}
	return "", 0
}

// normalizeNumber turns the digits-and-separators part of a typed amount into a
// plain "123.45" so both conventions work: "1,500.50" and "1.500,50" alike.
//
//   - both separators present: the last one is the decimal point
//   - "1,234,567" / "1.234.567" (groups of exactly three): thousands separators
//   - "1,500": one comma followed by exactly three digits reads as thousands,
//     the way the bot itself prints amounts ("0,500" and "1,5" stay decimals)
//   - anything else with a single comma: a decimal comma ("1500,50", "0,01")
//
// A string that fits none of these is returned untouched and rejected later.
func normalizeNumber(s string) string {
	dots, commas := strings.Count(s, "."), strings.Count(s, ",")
	switch {
	case dots > 0 && commas > 0:
		if strings.LastIndex(s, ".") > strings.LastIndex(s, ",") {
			return strings.ReplaceAll(s, ",", "")
		}
		return strings.ReplaceAll(strings.ReplaceAll(s, ".", ""), ",", ".")
	case commas > 0:
		if isGrouped(s, ",") && !strings.HasPrefix(s, "0") {
			return strings.ReplaceAll(s, ",", "")
		}
		if commas == 1 {
			return strings.ReplaceAll(s, ",", ".")
		}
		return s // several commas that aren't thousands groups: invalid
	case dots > 1 && isGrouped(s, "."):
		return strings.ReplaceAll(s, ".", "")
	}
	return s
}

// isGrouped reports whether s is digits in thousands groups split by sep:
// one to three leading digits, then groups of exactly three.
func isGrouped(s, sep string) bool {
	parts := strings.Split(s, sep)
	if len(parts) < 2 || len(parts[0]) < 1 || len(parts[0]) > 3 {
		return false
	}
	for i, p := range parts {
		if i > 0 && len(p) != 3 {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}
