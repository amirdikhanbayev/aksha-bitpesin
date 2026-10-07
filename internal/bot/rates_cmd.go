package bot

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/parser"
	"aksha-bitpesin/internal/rates"
	"aksha-bitpesin/internal/storage"
)

// cmdRate — manual entry of a market rate for the report's summary line.
// It does not apply to the user's operations: there the rate is entered per-operation.
func (b *Bot) cmdRate(ctx context.Context, u storage.User, chatID int64, args string) {
	fields := strings.Fields(args)
	if len(fields) < 2 {
		b.send(ctx, chatID, fmt.Sprintf(
			"A rate for the report's summary line (an estimate of “how much is this in %s”).\n\n"+
				"<code>/rate USD 540</code> — 1 USD = 540 %s\n"+
				"<code>/rates update</code> — pull the National Bank of Kazakhstan rates\n\n"+
				"<i>This rate doesn't affect your operations: for transfers and exchanges you enter your own rate.</i>",
			u.MainCurrency, u.MainCurrency), nil)
		return
	}
	code := strings.ToUpper(fields[0])
	if _, err := b.st.Currency(ctx, code); err != nil {
		b.send(ctx, chatID, "Unknown currency "+esc(code), nil)
		return
	}
	rate, err := parseRate(fields[1])
	if err != nil {
		b.send(ctx, chatID, "The rate must be a number: <code>/rate USD 540</code>", nil)
		return
	}
	day := b.clock().In(u.Location())
	if err := b.st.SetRate(ctx, u.MainCurrency, code, day, rate); err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.send(ctx, chatID, fmt.Sprintf("✅ 1 %s = %g %s (on %s)",
		code, rate, u.MainCurrency, day.Format("02.01.2006")), nil)
}

func (b *Bot) cmdRates(ctx context.Context, u storage.User, chatID int64, args string) {
	if strings.EqualFold(strings.TrimSpace(args), "update") {
		b.updateRates(ctx, u, chatID)
		return
	}
	known, day, err := b.st.LatestRates(ctx, u.MainCurrency)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>💱 Rates against %s</b>\n", u.MainCurrency)
	if len(known) == 0 {
		sb.WriteString("\nNo rates yet.\nSet your own: <code>/rate USD 540</code>\n" +
			"or pull the National Bank of Kazakhstan rates with the button below.\n")
	} else {
		fmt.Fprintf(&sb, "<i>as of %s</i>\n\n", day.Format("02.01.2006"))
		codes := make([]string, 0, len(known))
		for c := range known {
			codes = append(codes, c)
		}
		sort.Strings(codes)
		for _, c := range codes {
			fmt.Fprintf(&sb, "1 %s = %s %s\n", c, trimFloat(known[c]), u.MainCurrency)
		}
	}
	sb.WriteString("\n<i>These rates are used only for the report's summary line.\n" +
		"For transfers and exchanges you enter the rate yourself.</i>")

	b.send(ctx, chatID, sb.String(), inline(
		[]models.InlineKeyboardButton{btn("🔄 Update from NBRK", "ratesupd")},
		[]models.InlineKeyboardButton{btn("🕰 Fill in past dates", "ratesfill")},
	))
}

// updateRates pulls the official NBRK rates and stores them against the user's main currency.
func (b *Bot) updateRates(ctx context.Context, u storage.User, chatID int64) {
	day := b.clock().In(u.Location())
	quotes, err := rates.FetchNBRK(ctx, b.http, day)
	if err != nil {
		// on weekends today's rates may not be published yet — try yesterday
		quotes, err = rates.FetchNBRK(ctx, b.http, day.AddDate(0, 0, -1))
		if err != nil {
			b.send(ctx, chatID, "Couldn't fetch the NBRK rates: "+esc(err.Error())+
				"\nSet one manually: <code>/rate USD 540</code>", nil)
			return
		}
		day = day.AddDate(0, 0, -1)
	}

	have, err := b.st.DecimalsMap(ctx)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	saved := 0
	for _, q := range quotes {
		if _, ok := have[q.Code]; !ok {
			continue // currency isn't in the reference table — skip it
		}
		if err := b.st.SetRate(ctx, storage.PivotCurrency, q.Code, day, q.Rate); err != nil {
			slogError("SetRate", err)
			continue
		}
		saved++
	}
	if saved == 0 {
		b.send(ctx, chatID, "NBRK returned no rates for your currencies.", nil)
		return
	}
	b.send(ctx, chatID, fmt.Sprintf("✅ Updated %d rates against %s as of %s.",
		saved, storage.PivotCurrency, day.Format("02.01.2006")), nil)
	b.cmdRates(ctx, u, chatID, "")
}

func trimFloat(v float64) string {
	s := fmt.Sprintf("%.4f", v)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// inputMarketRate — the rate entered in response to the bot's prompt.
func (b *Bot) inputMarketRate(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	rate, err := parseRate(text)
	if err != nil {
		b.send(ctx, chatID, "The rate must be a number, for example <code>540</code>", cancelKeyboard())
		return
	}
	code := d.CurrencyTo
	if code == "" {
		code = d.Currency
	}
	day := b.clock().In(u.Location())
	if err := b.st.SetRate(ctx, u.MainCurrency, code, day, rate); err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.clearState(ctx, u.ID)
	b.send(ctx, chatID, fmt.Sprintf("✅ 1 %s = %g %s", code, rate, u.MainCurrency), nil)
}

var _ = time.Now

// maxBackfillDays caps one backfill run: the National Bank is asked once per day
// that needs it, and a runaway loop over years of history helps nobody.
const maxBackfillDays = 60

// backfillRates fetches the rates that past operations need but nobody saved at
// the time — the bot only ever stored today's. Without this, the report's
// "≈ in your currency" line quietly skips anything older than the first rate.
func (b *Bot) backfillRates(ctx context.Context, u storage.User, chatID int64, p parser.Period) {
	days, err := b.st.MissingRateDays(ctx, u.ID, u.MainCurrency, p.From, p.To)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if len(days) == 0 {
		b.send(ctx, chatID, "Every operation in "+esc(p.Label)+" already has a rate for its date.", nil)
		return
	}
	capped := false
	if len(days) > maxBackfillDays {
		days, capped = days[len(days)-maxBackfillDays:], true
	}

	b.send(ctx, chatID, fmt.Sprintf("📈 Fetching rates for %d %s…",
		len(days), plural(len(days), "day")), nil)

	have, err := b.st.DecimalsMap(ctx)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	var saved, failed int
	for _, day := range days {
		quotes, err := rates.FetchNBRK(ctx, b.http, day)
		if err != nil {
			failed++
			slog.Warn("backfill: no rates for a day", "day", day.Format("2006-01-02"), "err", err)
			continue
		}
		for _, q := range quotes {
			if _, ok := have[q.Code]; !ok {
				continue
			}
			if err := b.st.SetRate(ctx, storage.PivotCurrency, q.Code, day, q.Rate); err != nil {
				slogError("SetRate", err)
				continue
			}
			saved++
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "✅ Saved %d rates for %d %s.", saved, len(days)-failed, plural(len(days)-failed, "day"))
	if failed > 0 {
		fmt.Fprintf(&sb, "\n<i>%d %s had none published — weekends and holidays usually don't.</i>",
			failed, plural(failed, "day"))
	}
	if capped {
		fmt.Fprintf(&sb, "\n<i>Stopped at %d days; run it again for anything older.</i>", maxBackfillDays)
	}
	b.send(ctx, chatID, sb.String(), nil)
}
