package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/storage"
)

func (b *Bot) cmdSettings(ctx context.Context, u storage.User, chatID int64) {
	text, kb := settingsView(u)
	b.send(ctx, chatID, text, kb)
}

// settingsView is the hub: current values, and a button for everything that can
// be set up — accounts, categories, budgets, recurring operations, rates, and
// the user's own preferences.
func settingsView(u storage.User) (string, *models.InlineKeyboardMarkup) {
	statement := "off"
	toggle := btn("🔔 Monthly statement: off", "setreport:on")
	if u.MonthlyReport {
		statement = "on (sent on the 1st)"
		toggle = btn("🔕 Monthly statement: on", "setreport:off")
	}
	nudge := "off"
	nudgeToggle := btn("🔔 Evening reminder: off", "setnudge:on")
	if u.DailyReminder {
		nudge = "on (21:00, if nothing recorded)"
		nudgeToggle = btn("🔕 Evening reminder: on", "setnudge:off")
	}
	text := fmt.Sprintf(
		"<b>⚙️ Settings</b>\n\n"+
			"Main currency: <b>%s</b>\n"+
			"Timezone: <b>%s</b>\n"+
			"Monthly statement: <b>%s</b>\n"+
			"Evening reminder: <b>%s</b>",
		u.MainCurrency, esc(u.Timezone), statement, nudge)

	return text, inline(
		[]models.InlineKeyboardButton{btn("💳 Accounts", "setmenu:accounts"), btn("🏷 Categories", "setmenu:categories")},
		[]models.InlineKeyboardButton{btn("🎯 Budgets", "setmenu:budgets"), btn("🔁 Recurring", "setmenu:recurring")},
		[]models.InlineKeyboardButton{btn("💱 Main currency", "setmenu:currency"), btn("🕒 Timezone", "setmenu:tz")},
		[]models.InlineKeyboardButton{btn("📈 Exchange rates", "setmenu:rates"), btn("🔍 Search", "search")},
		[]models.InlineKeyboardButton{toggle},
		[]models.InlineKeyboardButton{nudgeToggle},
		[]models.InlineKeyboardButton{btn("🗑 Erase all my data", "erase")},
	)
}

// timezoneChoices are the zones offered as buttons; /timezone takes any other.
var timezoneChoices = []struct{ label, zone string }{
	{"Almaty / Astana", "Asia/Almaty"}, {"Moscow", "Europe/Moscow"},
	{"Dubai", "Asia/Dubai"}, {"Istanbul", "Europe/Istanbul"},
	{"Tbilisi", "Asia/Tbilisi"}, {"Tashkent", "Asia/Tashkent"},
	{"Bishkek", "Asia/Bishkek"}, {"London", "Europe/London"},
	{"Berlin", "Europe/Berlin"}, {"New York", "America/New_York"},
	{"Baku", "Asia/Baku"}, {"Yerevan", "Asia/Yerevan"},
	{"Kyiv", "Europe/Kyiv"}, {"Minsk", "Europe/Minsk"},
}

// timezoneKeyboard offers the common zones, with a way back to Settings.
func timezoneKeyboard() *models.InlineKeyboardMarkup {
	return timezoneKeyboardWith("settz:", btn("◀️ Back", "settings"))
}

// timezoneKeyboardWith offers the common zones under a caller-chosen callback
// prefix and last button — Settings and the first-run setup share it.
func timezoneKeyboardWith(prefix string, last models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	buttons := make([]models.InlineKeyboardButton, 0, len(timezoneChoices))
	for _, z := range timezoneChoices {
		buttons = append(buttons, btn(z.label, prefix+z.zone))
	}
	rows := grid(buttons, 2)
	rows = append(rows, []models.InlineKeyboardButton{last})
	return inline(rows...)
}

func (b *Bot) cmdCurrency(ctx context.Context, u storage.User, chatID int64, args string) {
	code := strings.ToUpper(strings.TrimSpace(args))
	if code == "" {
		b.send(ctx, chatID, "Main currency is <b>"+u.MainCurrency+"</b>. Change it: /currency USD",
			currencyKeyboard(u.MainCurrency, "setcur"))
		return
	}
	if _, err := b.st.Currency(ctx, code); err != nil {
		b.send(ctx, chatID, "Unknown currency "+esc(code)+". Available: /rates", nil)
		return
	}
	if err := b.st.SetMainCurrency(ctx, u.ID, code); err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.send(ctx, chatID, "✅ Main currency: <b>"+code+"</b>", nil)
}

func (b *Bot) cmdTimezone(ctx context.Context, u storage.User, chatID int64, args string) {
	tz := strings.TrimSpace(args)
	if tz == "" {
		b.send(ctx, chatID, fmt.Sprintf(
			"Timezone is <b>%s</b>, local time %s.\n\nChange it: <code>/timezone Europe/Moscow</code>",
			esc(u.Timezone), b.clock().In(u.Location()).Format("15:04")), nil)
		return
	}
	if _, err := time.LoadLocation(tz); err != nil {
		b.send(ctx, chatID, "Unknown timezone. Examples: Asia/Almaty, Europe/Moscow, Asia/Dubai", nil)
		return
	}
	if err := b.st.SetTimezone(ctx, u.ID, tz); err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.send(ctx, chatID, "✅ Timezone: <b>"+esc(tz)+"</b>", nil)
}
