package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/report"
	"aksha-bitpesin/internal/storage"
)

const (
	isoDay      = "2006-01-02"
	isoMonth    = "2006-01"
	rangeMonths = 12 // how far back the range picker reaches
)

// A custom range is four taps and no typing: the first month, its day, the last
// month, its day. Each step's button data carries what was chosen so far, so no
// dialog state is needed:
//
//	rng                       start: pick the first month
//	rngf:2026-07              first month chosen: pick the day
//	rngfd:2026-07-05          first day chosen: pick the last month
//	rngt:2026-07-05:2026-08   last month chosen: pick the day
//	rngtd:2026-07-05:2026-08-20   done: send the statement

// rangeStart offers the recent months to start from.
func (b *Bot) rangeStart(ctx context.Context, u storage.User, chatID int64, msgID int) {
	loc := u.Location()
	first := monthStart(b.clock().In(loc))
	buttons := make([]models.InlineKeyboardButton, 0, rangeMonths)
	for i := 0; i < rangeMonths; i++ {
		m := first.AddDate(0, -i, 0)
		buttons = append(buttons, btn(m.Format("Jan 2006"), "rngf:"+m.Format(isoMonth)))
	}
	rows := grid(buttons, 3)
	rows = append(rows, []models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")})
	b.edit(ctx, chatID, msgID, "<b>Custom range</b>\nFrom which month?", inline(rows...))
}

// rangeDay offers the days of a month; on picks the callback prefix, min and max
// bound the choice (zero means unbounded).
func rangeDay(month time.Time, prefix, carry string, min, max time.Time) *models.InlineKeyboardMarkup {
	last := month.AddDate(0, 1, -1).Day()
	var buttons []models.InlineKeyboardButton
	for day := 1; day <= last; day++ {
		d := time.Date(month.Year(), month.Month(), day, 0, 0, 0, 0, month.Location())
		if (!min.IsZero() && d.Before(min)) || (!max.IsZero() && d.After(max)) {
			continue
		}
		buttons = append(buttons, btn(fmt.Sprint(day), prefix+carry+d.Format(isoDay)))
	}
	rows := grid(buttons, 7)
	rows = append(rows, []models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")})
	return inline(rows...)
}

// handleRange runs one step of the picker. action is one of rng, rngf, rngfd,
// rngt, rngtd; arg is what that step carries. It reports false on data it can't
// read, so the caller can say so.
func (b *Bot) handleRange(ctx context.Context, u storage.User, chatID int64, msgID int, action, arg string) bool {
	loc := u.Location()
	today := dayStart(b.clock().In(loc))

	switch action {
	case "rng":
		b.rangeStart(ctx, u, chatID, msgID)

	case "rngf": // first month → its days
		month, err := time.ParseInLocation(isoMonth, arg, loc)
		if err != nil {
			return false
		}
		b.edit(ctx, chatID, msgID, fmt.Sprintf("From which day in <b>%s</b>?", month.Format("January 2006")),
			rangeDay(month, "rngfd:", "", time.Time{}, today))

	case "rngfd": // first day → the last month
		from, err := time.ParseInLocation(isoDay, arg, loc)
		if err != nil {
			return false
		}
		var buttons []models.InlineKeyboardButton
		for m := monthStart(from); !m.After(today); m = m.AddDate(0, 1, 0) {
			buttons = append(buttons, btn(m.Format("Jan 2006"), "rngt:"+arg+":"+m.Format(isoMonth)))
		}
		rows := [][]models.InlineKeyboardButton{{btn("📌 Just "+from.Format("2 Jan"), "rep:"+arg+".."+arg)}}
		rows = append(rows, grid(buttons, 3)...)
		rows = append(rows, []models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")})
		b.edit(ctx, chatID, msgID, fmt.Sprintf("From <b>%s</b>. To which month?", from.Format("02.01.2006")), inline(rows...))

	case "rngt": // last month → its days, none before the first day
		fromStr, monthStr, ok := strings.Cut(arg, ":")
		if !ok {
			return false
		}
		from, err1 := time.ParseInLocation(isoDay, fromStr, loc)
		month, err2 := time.ParseInLocation(isoMonth, monthStr, loc)
		if err1 != nil || err2 != nil {
			return false
		}
		b.edit(ctx, chatID, msgID,
			fmt.Sprintf("From <b>%s</b>. To which day in <b>%s</b>?", from.Format("02.01.2006"), month.Format("January 2006")),
			rangeDay(month, "rngtd:", fromStr+":", from, today))

	case "rngtd": // done
		fromStr, toStr, ok := strings.Cut(arg, ":")
		if !ok {
			return false
		}
		from, err1 := time.ParseInLocation(isoDay, fromStr, loc)
		to, err2 := time.ParseInLocation(isoDay, toStr, loc)
		if err1 != nil || err2 != nil || to.Before(from) {
			return false
		}
		b.sendReport(ctx, u, chatID, fromStr+".."+toStr, report.Full())
	}
	return true
}

func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}

func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
