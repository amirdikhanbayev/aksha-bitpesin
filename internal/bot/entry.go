package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/money"
	"aksha-bitpesin/internal/storage"
)

// kindTitle is how an operation kind is shown in prompts.
func kindTitle(kind string) string {
	if kind == storage.KindIncome {
		return "➕ Income"
	}
	return "➖ Expense"
}

// startEntry launches the guided flow. The user types only the amount — and the
// rate, when an operation crosses currencies; everything else is picked from
// buttons: account → amount → category → source.
func (b *Bot) startEntry(ctx context.Context, u storage.User, chatID int64, kind string) {
	accs, err := b.st.ListAccounts(ctx, u.ID, false)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	switch len(accs) {
	case 0:
		b.startNewAccount(ctx, u, chatID)
	case 1:
		b.askAmount(ctx, u, chatID, draft{Kind: kind}, accs[0])
	default:
		b.setState(ctx, u.ID, stateAmount, draft{Kind: kind})
		question := "paid from which account?"
		if kind == storage.KindIncome {
			question = "received on which account?"
		}
		b.send(ctx, chatID, kindTitle(kind)+" — "+question,
			accountsKeyboard(accs, "entacc", btn("✖️ Cancel", "cancel")))
	}
}

// askAmount asks for the amount on the chosen account. It is in the account's
// currency unless the user taps “Another currency”.
func (b *Bot) askAmount(ctx context.Context, u storage.User, chatID int64, d draft, acc storage.Account) {
	d.AccountID = acc.ID
	d.Currency = ""
	d.OriginalAmount = nil
	b.setState(ctx, u.ID, stateAmount, d)

	where := fmt.Sprintf("%s %s · %s", placeEmoji(acc.Name), esc(acc.Name), acc.Currency)
	if balance, err := b.st.AccountBalance(ctx, u.ID, acc.ID); err == nil {
		where += fmt.Sprintf(" · <i>%s</i>", money.FormatCode(balance, acc.Decimals, acc.Currency))
	}
	b.send(ctx, chatID, fmt.Sprintf("%s\n%s\n\nHow much? Send the amount in <b>%s</b>.",
		kindTitle(d.Kind), where, acc.Currency),
		inline([]models.InlineKeyboardButton{btn("💱 Another currency", "entcur")},
			[]models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")}))
}

// askAmountOtherCurrency asks for the amount in a currency other than the
// account's; the rate follows once the amount is in.
func (b *Bot) askAmountOtherCurrency(ctx context.Context, u storage.User, chatID int64, d draft, acc storage.Account) {
	b.setState(ctx, u.ID, stateAmount, d)
	b.send(ctx, chatID, fmt.Sprintf(
		"%s · account “%s” (%s)\nHow much in <b>%s</b>?\n<i>I'll ask for the rate next — every bank has its own.</i>",
		kindTitle(d.Kind), esc(acc.Name), acc.Currency, d.Currency),
		inline([]models.InlineKeyboardButton{btn(fmt.Sprintf("↩️ Use %s", acc.Currency), fmt.Sprintf("entacc:%d", acc.ID))},
			[]models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")}))
}

// inputGuidedAmount takes the typed amount of the guided flow: a bare number,
// nothing else. In another currency it goes on to ask for the rate.
func (b *Bot) inputGuidedAmount(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	acc, err := b.st.Account(ctx, u.ID, d.AccountID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	currency, decimals := acc.Currency, acc.Decimals
	if d.Currency != "" && d.Currency != acc.Currency {
		c, err := b.st.Currency(ctx, d.Currency)
		if err != nil {
			b.fail(ctx, chatID, err)
			return
		}
		currency, decimals = c.Code, c.Decimals
	}
	amount, err := money.Parse(text, decimals)
	if err != nil || amount <= 0 {
		b.send(ctx, chatID, fmt.Sprintf(
			"Send the amount in <b>%s</b> as a number, for example <code>5000</code>.", currency), cancelKeyboard())
		return
	}
	if currency != acc.Currency {
		d.AmountRaw = strings.TrimSpace(text)
		b.askConversion(ctx, u, chatID, d, acc)
		return
	}
	d.Amount = amount
	// A repeat comes with its category already chosen — straight to recording.
	if d.CategoryID != nil {
		b.finishEntry(ctx, u, chatID, d)
		return
	}
	b.askCategory(ctx, u, chatID, d, "Category?")
}

// askConversion handles an operation whose currency differs from its account's:
// a purchase priced in KZT paid from a USD card, cash changed at a kiosk.
// Every bank and exchange uses its own rate, so the bot asks the user for the
// facts — how much actually left the account, or the rate they got — and never
// substitutes a market rate of its own.
func (b *Bot) askConversion(ctx context.Context, u storage.User, chatID int64, d draft, acc storage.Account) {
	opCur, err := b.st.Currency(ctx, d.Currency)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	raw, err := money.Parse(d.AmountRaw, opCur.Decimals)
	if err != nil || raw <= 0 {
		b.send(ctx, chatID, "Couldn't read the amount: <code>"+esc(d.AmountRaw)+"</code>", nil)
		return
	}
	d.OriginalAmount = &raw
	d.AccountID = acc.ID
	b.setState(ctx, u.ID, stateConvAmount, d)

	b.send(ctx, chatID, fmt.Sprintf(
		"💱 The operation is in <b>%s</b>, but account “%s” is in <b>%s</b>.\n\n"+
			"How much <b>%s</b> was debited from the account?\n"+
			"<i>Every bank and exchange has its own rate — so I ask instead of working it out myself.</i>",
		money.FormatCode(raw, opCur.Decimals, opCur.Code), esc(acc.Name), acc.Currency,
		acc.Currency),
		inline([]models.InlineKeyboardButton{
			btn("Enter the rate instead", "convrate"),
			btn("✖️ Cancel", "cancel"),
		}))
}

// askCategory shows the category keyboard for the given kind.
func (b *Bot) askCategory(ctx context.Context, u storage.User, chatID int64, d draft, title string) {
	cats, err := b.st.ListCategories(ctx, u.ID, d.Kind)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.setState(ctx, u.ID, stateAmount, d)
	if len(cats) == 0 {
		// None yet: this operation names the first one.
		b.askNewCategory(ctx, u, chatID, d)
		return
	}
	kb := categoriesKeyboard(cats, "pickcat",
		btn("➕ New category", "newcat"),
		btn("⏭ No category", "pickcat:0"),
		btn("✖️ Cancel", "cancel"))
	if row := b.loanShortcut(ctx, u, d); row != nil {
		// Right under the categories, above New / No category / Cancel, where
		// it is seen rather than scrolled past.
		rows := kb.InlineKeyboard
		last := len(rows) - 1
		kb.InlineKeyboard = append(append(append([][]models.InlineKeyboardButton{}, rows[:last]...), row), rows[last])
	}
	b.send(ctx, chatID, title, kb)
}

// askSource suggests sources the user has already used.
func (b *Bot) askSource(ctx context.Context, u storage.User, chatID int64, d draft) {
	sources, err := b.st.SuggestSources(ctx, u.ID, d.Kind, d.CategoryID, 6)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	d.Sources = sources
	b.setState(ctx, u.ID, stateSource, d)

	title := "Where did the money come from? Pick one, or type a new name:"
	if d.Kind == storage.KindExpense {
		title = "Who was it paid to / for what? Pick one, or type a new name:"
	}
	if len(sources) == 0 {
		b.send(ctx, chatID, title+"\n<i>(you can skip this — /skip)</i>", inline(
			[]models.InlineKeyboardButton{
				btn("⏭ No source", "src:skip"),
				btn("✖️ Cancel", "cancel"),
			}))
		return
	}
	b.send(ctx, chatID, title, sourcesKeyboard(sources, "src"))
}

// finishEntry records the operation and shows the card.
func (b *Bot) finishEntry(ctx context.Context, u storage.User, chatID int64, d draft) {
	if d.Source == "" && d.Kind != storage.KindTransfer {
		b.askSource(ctx, u, chatID, d)
		return
	}
	b.saveEntry(ctx, u, chatID, d)
}

func (b *Bot) saveEntry(ctx context.Context, u storage.User, chatID int64, d draft) {
	at := b.clock().In(u.Location())
	if d.OccurredAt != nil {
		at = *d.OccurredAt
	}
	t := storage.Transaction{
		UserID:     u.ID,
		AccountID:  d.AccountID,
		CategoryID: d.CategoryID,
		Kind:       d.Kind,
		Amount:     d.Amount,
		Note:       d.Note,
		Source:     d.Source,
		Rate:       d.Rate,
		OccurredAt: at,
	}
	// Keep what the purchase actually cost, alongside what left the account.
	if d.OriginalAmount != nil && d.Rate != nil && d.Currency != "" {
		t.OriginalAmount = d.OriginalAmount
		t.OriginalCurrency = d.Currency
	}
	saved, err := b.st.CreateTransaction(ctx, t)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.clearState(ctx, u.ID)

	text, kb := b.cardOf(ctx, u, saved)
	if warn := b.budgetWarning(ctx, u, saved); warn != "" {
		text += "\n\n" + warn
	}
	b.send(ctx, chatID, text, kb)
}

// budgetWarning warns if an expense has eaten into the category's monthly limit.
func (b *Bot) budgetWarning(ctx context.Context, u storage.User, t storage.Transaction) string {
	if t.Kind != storage.KindExpense || t.CategoryID == nil {
		return ""
	}
	loc := u.Location()
	at := t.OccurredAt.In(loc)
	month := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, loc)
	buds, err := b.st.Budgets(ctx, u.ID, month, month, month.AddDate(0, 1, 0))
	if err != nil {
		return ""
	}
	for _, bd := range buds {
		if bd.CategoryID != *t.CategoryID || bd.Currency != t.AccountCurr || bd.LimitAmount <= 0 {
			continue
		}
		pct := int(bd.Spent * 100 / bd.LimitAmount)
		switch {
		case pct >= 100:
			return fmt.Sprintf("🔴 Limit for “%s” exceeded: %s of %s",
				esc(bd.CategoryName), money.Format(bd.Spent, bd.Decimals),
				money.FormatCode(bd.LimitAmount, bd.Decimals, bd.Currency))
		case pct >= 80:
			return fmt.Sprintf("🟡 Limit for “%s” almost used: %s of %s (%d%%)",
				esc(bd.CategoryName), money.Format(bd.Spent, bd.Decimals),
				money.FormatCode(bd.LimitAmount, bd.Decimals, bd.Currency), pct)
		}
	}
	return ""
}
