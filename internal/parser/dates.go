// Package parser parses the periods and dates that reports are asked for.
package parser

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Period — a half-open interval [From, To) in the user's timezone plus a human-readable label.
type Period struct {
	From  time.Time
	To    time.Time
	Label string
	Month time.Time // the 1st of the month, if the period is exactly one month; zero otherwise
}

// Arg renders the period in a form ParsePeriod reads back — "2026-08" for a
// whole month, "2026-07-05..2026-08-20" for anything else. It goes into button
// data, so a statement's buttons can name the period they belong to.
func (p Period) Arg() string {
	if !p.Month.IsZero() {
		return p.Month.Format("2006-01")
	}
	return p.From.Format("2006-01-02") + ".." + p.To.AddDate(0, 0, -1).Format("2006-01-02")
}

var monthNames = map[string]time.Month{
	"январь": time.January, "января": time.January, "янв": time.January,
	"февраль": time.February, "февраля": time.February, "фев": time.February,
	"март": time.March, "марта": time.March, "мар": time.March,
	"апрель": time.April, "апреля": time.April, "апр": time.April,
	"май": time.May, "мая": time.May,
	"июнь": time.June, "июня": time.June, "июн": time.June,
	"июль": time.July, "июля": time.July, "июл": time.July,
	"август": time.August, "августа": time.August, "авг": time.August,
	"сентябрь": time.September, "сентября": time.September, "сен": time.September, "сент": time.September,
	"октябрь": time.October, "октября": time.October, "окт": time.October,
	"ноябрь": time.November, "ноября": time.November, "ноя": time.November,
	"декабрь": time.December, "декабря": time.December, "дек": time.December,

	"january": time.January, "jan": time.January,
	"february": time.February, "feb": time.February,
	"march": time.March, "mar": time.March,
	"april": time.April, "apr": time.April,
	"may":  time.May,
	"june": time.June, "jun": time.June,
	"july": time.July, "jul": time.July,
	"august": time.August, "aug": time.August,
	"september": time.September, "sep": time.September, "sept": time.September,
	"october": time.October, "oct": time.October,
	"november": time.November, "nov": time.November,
	"december": time.December, "dec": time.December,
}

var monthTitles = [...]string{"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December"}

// MonthTitle — "September 2025".
func MonthTitle(t time.Time) string {
	return fmt.Sprintf("%s %d", monthTitles[int(t.Month())-1], t.Year())
}

func isDateToken(tok string) bool {
	switch tok {
	case "сегодня", "вчера", "позавчера", "today", "yesterday":
		return true
	}
	if _, ok := monthNames[tok]; ok {
		return false // a bare month name is a period, not an operation date
	}
	digits, seps := 0, 0
	for _, r := range tok {
		switch {
		case unicode.IsDigit(r):
			digits++
		case r == '.' || r == '/' || r == '-':
			seps++
		default:
			return false
		}
	}
	return digits >= 2 && seps >= 1
}

// parseDate parses "вчера", "12.09", "12.09.2025", "2025-09-12" as noon of the local day.
func parseDate(tok string, now time.Time, loc *time.Location) (time.Time, error) {
	today := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 12, 0, 0, 0, loc)
	switch tok {
	case "сегодня", "today":
		return today, nil
	case "вчера", "yesterday":
		return today.AddDate(0, 0, -1), nil
	case "позавчера":
		return today.AddDate(0, 0, -2), nil
	}

	sep := "."
	if strings.Contains(tok, "/") {
		sep = "/"
	} else if strings.Contains(tok, "-") {
		sep = "-"
	}
	parts := strings.Split(tok, sep)
	nums := make([]int, 0, 3)
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return time.Time{}, fmt.Errorf("not a date: %q", tok)
		}
		nums = append(nums, n)
	}

	var day, month, year int
	switch {
	case len(nums) == 2:
		day, month, year = nums[0], nums[1], today.Year()
	case len(nums) == 3 && nums[0] > 31: // 2025-09-12
		year, month, day = nums[0], nums[1], nums[2]
	case len(nums) == 3:
		day, month, year = nums[0], nums[1], nums[2]
		if year < 100 {
			year += 2000
		}
	default:
		return time.Time{}, fmt.Errorf("not a date: %q", tok)
	}
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return time.Time{}, fmt.Errorf("not a date: %q", tok)
	}
	d := time.Date(year, time.Month(month), day, 12, 0, 0, 0, loc)
	if d.Day() != day {
		return time.Time{}, fmt.Errorf("no such date: %q", tok)
	}
	// "12.09" in the future almost certainly means last year
	if len(nums) == 2 && d.After(today.AddDate(0, 0, 1)) {
		d = d.AddDate(-1, 0, 0)
	}
	return d, nil
}

// ParsePeriod parses a report argument: "", "месяц", "прошлый", "неделя",
// "сентябрь", "09.2025", "01.09-30.09", "2025".
func ParsePeriod(arg string, now time.Time, loc *time.Location) (Period, error) {
	arg = strings.ToLower(strings.TrimSpace(arg))
	today := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc)

	monthPeriod := func(year int, m time.Month) Period {
		from := time.Date(year, m, 1, 0, 0, 0, 0, loc)
		return Period{From: from, To: from.AddDate(0, 1, 0), Label: MonthTitle(from), Month: from}
	}

	switch arg {
	case "", "месяц", "month", "current", "this month", "текущий", "этот месяц":
		return monthPeriod(today.Year(), today.Month()), nil
	case "прошлый", "прошлый месяц", "пред", "last", "last month", "previous", "прошлом":
		prev := today.AddDate(0, 0, -today.Day()+1).AddDate(0, -1, 0)
		return monthPeriod(prev.Year(), prev.Month()), nil
	case "сегодня", "today":
		return Period{From: today, To: today.AddDate(0, 0, 1), Label: "today"}, nil
	case "вчера", "yesterday":
		y := today.AddDate(0, 0, -1)
		return Period{From: y, To: today, Label: "yesterday"}, nil
	case "неделя", "week", "this week":
		// week starting Monday
		off := (int(today.Weekday()) + 6) % 7
		from := today.AddDate(0, 0, -off)
		return Period{From: from, To: from.AddDate(0, 0, 7), Label: "this week"}, nil
	case "год", "year", "this year":
		from := time.Date(today.Year(), time.January, 1, 0, 0, 0, 0, loc)
		return Period{From: from, To: from.AddDate(1, 0, 0), Label: fmt.Sprintf("%d", today.Year())}, nil
	case "всё", "все", "all", "all time":
		from := time.Date(2000, time.January, 1, 0, 0, 0, 0, loc)
		return Period{From: from, To: today.AddDate(0, 0, 1), Label: "all time"}, nil
	}

	// Range "01.09-30.09" or "01.09.2025 - 30.09.2025"
	if from, to, ok := splitRange(arg); ok {
		f, err := parseDate(from, now, loc)
		if err != nil {
			return Period{}, fmt.Errorf("couldn't read the start of the period: %s", from)
		}
		t, err := parseDate(to, now, loc)
		if err != nil {
			return Period{}, fmt.Errorf("couldn't read the end of the period: %s", to)
		}
		fd := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, loc)
		td := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
		if !td.After(fd) {
			return Period{}, fmt.Errorf("the end of the period is before its start")
		}
		return Period{From: fd, To: td,
			Label: fd.Format("02.01.2006") + " — " + td.AddDate(0, 0, -1).Format("02.01.2006")}, nil
	}

	fields := strings.Fields(arg)

	// "сентябрь" / "сентябрь 2024" / "сен 24"
	if m, ok := monthNames[fields[0]]; ok {
		year := today.Year()
		if len(fields) > 1 {
			if y, err := strconv.Atoi(fields[1]); err == nil {
				if y < 100 {
					y += 2000
				}
				year = y
			}
		} else if m > today.Month() {
			year-- // "декабрь" (December) in March means last December
		}
		return monthPeriod(year, m), nil
	}

	// "09.2025" / "2025-09"
	if strings.ContainsAny(arg, ".-/") {
		sep := "."
		if strings.Contains(arg, "-") {
			sep = "-"
		} else if strings.Contains(arg, "/") {
			sep = "/"
		}
		parts := strings.Split(arg, sep)
		if len(parts) == 2 {
			a, errA := strconv.Atoi(strings.TrimSpace(parts[0]))
			b, errB := strconv.Atoi(strings.TrimSpace(parts[1]))
			if errA == nil && errB == nil {
				if a > 12 && b >= 1 && b <= 12 {
					return monthPeriod(a, time.Month(b)), nil // 2025-09
				}
				if a >= 1 && a <= 12 {
					if b < 100 {
						b += 2000
					}
					return monthPeriod(b, time.Month(a)), nil // 09.2025
				}
			}
		}
	}

	// "2025"
	if y, err := strconv.Atoi(arg); err == nil && y >= 2000 && y <= 2100 {
		from := time.Date(y, time.January, 1, 0, 0, 0, 0, loc)
		return Period{From: from, To: from.AddDate(1, 0, 0), Label: fmt.Sprintf("%d", y)}, nil
	}

	// A single date means that one day
	if isDateToken(arg) {
		d, err := parseDate(arg, now, loc)
		if err == nil {
			day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
			return Period{From: day, To: day.AddDate(0, 0, 1), Label: day.Format("02.01.2006")}, nil
		}
	}

	return Period{}, fmt.Errorf("couldn't read the period %q", arg)
}

func splitRange(s string) (from, to string, ok bool) {
	for _, sep := range []string{"—", "–", " - ", ".."} {
		if a, b, found := strings.Cut(s, sep); found {
			return strings.TrimSpace(a), strings.TrimSpace(b), true
		}
	}
	// "01.09-30.09": hyphen as separator, but not inside a single date like 2025-09-01
	if strings.Count(s, "-") == 1 && (strings.Contains(s, ".") || strings.Contains(s, "/")) {
		a, b, _ := strings.Cut(s, "-")
		return strings.TrimSpace(a), strings.TrimSpace(b), true
	}
	return "", "", false
}

// Ordinal is a day of the month as it is said: 1st, 2nd, 15th.
func Ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}
