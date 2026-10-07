package bot

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/money"
	"aksha-bitpesin/internal/storage"
)

// First-run setup. A new user is asked, in order, for
//
//	1. their timezone        — month boundaries and the monthly statement follow it
//	2. the currency they count in — report totals are converted into it
//	3. where they keep money — several places at once, then for each one its
//	                           currency and how much is in it now
//
// Everything is tapped except the balances. All of it can be changed later in
// Settings, and the last step can be skipped.

// placeOptions are the usual places to keep money, offered as buttons.
var placeOptions = []struct{ emoji, name string }{
	{"💵", "Cash"}, {"💳", "Card"}, {"🏦", "Deposit"}, {"💰", "Savings"}, {"₿", "Crypto wallet"},
}

// placeEmoji is the icon shown next to a place's name.
func placeEmoji(name string) string {
	for _, p := range placeOptions {
		if p.name == name {
			return p.emoji
		}
	}
	return "📌" // a place the user named themselves
}

// needsOnboarding reports whether this user still has to go through setup.
// Someone who already has an account was set up the old way (or by hand): they
// are marked done and never asked.
func (b *Bot) needsOnboarding(ctx context.Context, u storage.User) bool {
	if u.Onboarded() {
		return false
	}
	accs, err := b.st.ListAccounts(ctx, u.ID, true)
	if err != nil {
		return false // don't trap anyone behind a database hiccup
	}
	if len(accs) > 0 {
		_ = b.st.MarkOnboarded(ctx, u.ID)
		return false
	}
	return true
}

// startOnboarding greets a new user and asks the first question.
func (b *Bot) startOnboarding(ctx context.Context, u storage.User, chatID int64) {
	b.setState(ctx, u.ID, stateOnboard, draft{Onboarding: true})
	b.send(ctx, chatID,
		"👋 <b>Welcome!</b> Three quick questions and you're ready. Anything here can be changed later in ⚙️ Settings.\n\n"+
			"<b>Step 1 of 3 · Timezone</b>\nWhich one are you in? Month boundaries and the monthly statement follow it.",
		timezoneKeyboardWith("obtz:", btn("Keep "+u.Timezone, "obtz:"+u.Timezone)))
}

// askOnboardingCurrency is the second question.
func (b *Bot) askOnboardingCurrency(ctx context.Context, u storage.User, chatID int64) {
	b.send(ctx, chatID,
		"<b>Step 2 of 3 · Your currency</b>\nWhich one do you count in? Report totals are converted into it — "+
			"your accounts can still hold any currency.",
		currencyKeyboardWith(u.MainCurrency, "obcur", "", btn("Keep "+u.MainCurrency, "obcur:"+u.MainCurrency)))
}

// askOnboardingPlaces is the third: where the money is kept.
func (b *Bot) askOnboardingPlaces(ctx context.Context, u storage.User, chatID int64) {
	d := draft{Onboarding: true}
	for _, p := range placeOptions {
		d.Sources = append(d.Sources, p.name)
	}
	b.showPlaces(ctx, u, chatID, 0, d)
}

// showPlaces draws the multi-select of places: tap to tick, tap again to untick.
// It edits message msgID in place, or sends a fresh message when msgID is 0.
func (b *Bot) showPlaces(ctx context.Context, u storage.User, chatID int64, msgID int, d draft) {
	b.setState(ctx, u.ID, stateOnboard, d)

	chosen := make(map[string]bool, len(d.Queue))
	for _, n := range d.Queue {
		chosen[n] = true
	}
	buttons := make([]models.InlineKeyboardButton, 0, len(d.Sources))
	for i, name := range d.Sources {
		label := placeEmoji(name) + " " + name
		if chosen[name] {
			label = "✅ " + label
		}
		buttons = append(buttons, btn(label, fmt.Sprintf("obplace:i%d", i)))
	}
	rows := grid(buttons, 2)
	rows = append(rows,
		[]models.InlineKeyboardButton{btn("✏️ Other place", "obplace:other")},
		[]models.InlineKeyboardButton{btn("Done ▶️", "obplace:done")},
		[]models.InlineKeyboardButton{btn("⏭ Skip for now", "obplace:skip")})

	text := "<b>Step 3 of 3 · Where the money is</b>\nTick everything that applies — then I'll ask what each holds and how much."
	if len(d.Queue) > 0 {
		text += "\n\n<i>Selected: " + esc(strings.Join(orderedBy(d.Sources, d.Queue), ", ")) + "</i>"
	}
	if msgID > 0 {
		b.edit(ctx, chatID, msgID, text, inline(rows...))
		return
	}
	b.send(ctx, chatID, text, inline(rows...))
}

// orderedBy returns the chosen names in the order of the options list, so
// accounts are set up in a predictable order however the buttons were tapped.
func orderedBy(options, chosen []string) []string {
	set := make(map[string]bool, len(chosen))
	for _, n := range chosen {
		set[n] = true
	}
	out := make([]string, 0, len(chosen))
	for _, o := range options {
		if set[o] {
			out = append(out, o)
		}
	}
	return out
}

// togglePlace ticks or unticks the option at idx.
func togglePlace(d *draft, idx int) bool {
	if idx < 0 || idx >= len(d.Sources) {
		return false
	}
	name := d.Sources[idx]
	for i, n := range d.Queue {
		if n == name {
			d.Queue = append(d.Queue[:i], d.Queue[i+1:]...)
			return true
		}
	}
	d.Queue = append(d.Queue, name)
	return true
}

// inputOnboardingPlace takes a place typed instead of picked — a bank's name, say.
func (b *Bot) inputOnboardingPlace(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	name := strings.TrimSpace(text)
	if name == "" || len([]rune(name)) > 40 {
		b.send(ctx, chatID, "Name — up to 40 characters.", nil)
		return
	}
	for _, existing := range d.Sources {
		if strings.EqualFold(existing, name) { // already on the list: just tick it
			name = existing
			break
		}
	}
	known := false
	for _, existing := range d.Sources {
		known = known || existing == name
	}
	if !known {
		d.Sources = append(d.Sources, name)
	}
	if !contains(d.Queue, name) {
		d.Queue = append(d.Queue, name)
	}
	b.showPlaces(ctx, u, chatID, 0, d)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// askPlaceCurrencies asks which currencies the next place holds — several are
// allowed: cash in tenge and cash in dollars are one place with two balances.
// Currencies already kept there are not offered again.
func (b *Bot) askPlaceCurrencies(ctx context.Context, u storage.User, chatID int64, msgID int, d draft) {
	place := d.Queue[0]
	held, err := b.st.PlaceCurrencies(ctx, u.ID, place)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	options := currencyOptions(u.MainCurrency, held)
	if len(options) == 0 {
		b.send(ctx, chatID, fmt.Sprintf("“%s” already holds every currency I know.", esc(place)), nil)
		d.Queue = d.Queue[1:]
		b.accountWizardNext(ctx, u, chatID, d)
		return
	}

	d.Sources = options
	b.setState(ctx, u.ID, stateOnboard, d)

	chosen := make(map[string]bool, len(d.Currencies))
	for _, c := range d.Currencies {
		chosen[c] = true
	}
	buttons := make([]models.InlineKeyboardButton, 0, len(options))
	for i, code := range options {
		label := code
		if chosen[code] {
			label = "✅ " + code
		}
		buttons = append(buttons, btn(label, fmt.Sprintf("obacccur:i%d", i)))
	}
	rows := grid(buttons, 3)
	if len(d.Currencies) > 0 {
		rows = append(rows, []models.InlineKeyboardButton{btn("Done ▶️", "obacccur:done")})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn("⏭ Skip this one", "obskip")})

	text := fmt.Sprintf("%s <b>%s</b> — which currencies does it hold?\nTick every one; each gets its own balance.",
		placeEmoji(place), esc(place))
	if len(held) > 0 {
		text += "\n<i>Already there: " + strings.Join(held, ", ") + "</i>"
	}
	if len(d.Currencies) > 0 {
		text += "\n\n<i>Selected: " + strings.Join(d.Currencies, ", ") + "</i>"
	}
	if msgID > 0 {
		b.edit(ctx, chatID, msgID, text, inline(rows...))
		return
	}
	b.send(ctx, chatID, text, inline(rows...))
}

// currencyOptions lists the currencies offered for a place: the user's main one
// first, then the usual suspects, minus what the place already holds.
func currencyOptions(main string, held []string) []string {
	have := make(map[string]bool, len(held))
	for _, c := range held {
		have[c] = true
	}
	var out []string
	for _, c := range append([]string{main}, "KZT", "USD", "EUR", "RUB", "USDT", "TRY", "GEL", "AED") {
		if !have[c] && !contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// toggleCurrency ticks or unticks the option at idx.
func toggleCurrency(d *draft, idx int) bool {
	if idx < 0 || idx >= len(d.Sources) {
		return false
	}
	code := d.Sources[idx]
	for i, c := range d.Currencies {
		if c == code {
			d.Currencies = append(d.Currencies[:i], d.Currencies[i+1:]...)
			return true
		}
	}
	d.Currencies = append(d.Currencies, code)
	return true
}

// accountWizardNext drives what is left of the account wizard: the balance of the
// next currency of this place, then the next place, then the end. Shared by the
// first-run setup and by adding an account later.
func (b *Bot) accountWizardNext(ctx context.Context, u storage.User, chatID int64, d draft) {
	if len(d.Currencies) > 0 && len(d.Queue) > 0 {
		d.Currency = d.Currencies[0]
		d.Note = d.Queue[0] // the place's name, until the account is created
		b.askAccountBalance(ctx, u, chatID, d)
		return
	}
	if len(d.Queue) > 0 {
		b.askPlaceCurrencies(ctx, u, chatID, 0, d)
		return
	}
	if d.Onboarding {
		b.finishOnboarding(ctx, u, chatID)
		return
	}
	b.clearState(ctx, u.ID)
	b.cmdAccounts(ctx, u, chatID)
}

// finishOnboarding closes the setup and shows what was set up.
func (b *Bot) finishOnboarding(ctx context.Context, u storage.User, chatID int64) {
	b.clearState(ctx, u.ID)
	if err := b.st.MarkOnboarded(ctx, u.ID); err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if fresh, err := b.st.UserByID(ctx, u.ID); err == nil {
		u = fresh
	}
	accs, err := b.st.ListAccountsWithBalance(ctx, u.ID, false)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}

	if len(accs) == 0 {
		b.send(ctx, chatID, "Setup skipped. Add an account any time from <b>💳 Accounts</b> — "+
			"there is nothing to record against until you do.", mainMenu())
		return
	}

	var sb strings.Builder
	sb.WriteString("🎉 <b>You're all set!</b>\n\n")
	totals := map[string]int64{}
	var order []string
	for _, a := range accs {
		fmt.Fprintf(&sb, "• <b>%s</b> — %s\n", esc(a.Name), money.FormatCode(a.Balance, a.Decimals, a.Currency))
		if _, seen := totals[a.Currency]; !seen {
			order = append(order, a.Currency)
		}
		totals[a.Currency] += a.Balance
	}
	if len(order) > 1 || len(accs) > 1 {
		sort.Strings(order) // by currency code, so the line reads the same every time
		parts := make([]string, 0, len(order))
		for _, cur := range order {
			dec := 2
			for _, a := range accs {
				if a.Currency == cur {
					dec = a.Decimals
					break
				}
			}
			parts = append(parts, money.FormatCode(totals[cur], dec, cur))
		}
		sb.WriteString("\nIn total: <b>" + strings.Join(parts, " + ") + "</b>\n")
	}
	fmt.Fprintf(&sb, "\nTimezone <b>%s</b> · main currency <b>%s</b> — change either in ⚙️ Settings.",
		esc(u.Timezone), u.MainCurrency)
	b.send(ctx, chatID, sb.String(), inline([]models.InlineKeyboardButton{btn("➕ Add another account", "newacc")}))
	b.send(ctx, chatID, "Tap <b>➖ Expense</b> or <b>➕ Income</b> to record your first operation.", mainMenu())
}

// validZone reports whether name is a timezone the system knows.
func validZone(name string) bool {
	_, err := time.LoadLocation(name)
	return err == nil
}
