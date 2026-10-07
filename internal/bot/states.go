package bot

import (
	"context"
	"fmt"

	"github.com/go-telegram/bot/models"
	"strconv"
	"strings"
	"time"

	"aksha-bitpesin/internal/money"
	"aksha-bitpesin/internal/report"
	"aksha-bitpesin/internal/storage"
)

// handleStateInput handles the text the bot was waiting for in the dialog.
func (b *Bot) handleStateInput(ctx context.Context, u storage.User, chatID int64, state string, data []byte, text string) {
	d := parseDraft(data)

	switch state {
	case stateAmount:
		b.inputAmount(ctx, u, chatID, d, text)
	case stateSource:
		if isSkip(text) {
			d.Source = ""
		} else {
			d.Source = text
		}
		b.applySource(ctx, u, chatID, d)
	case stateCategoryName:
		b.inputCategoryName(ctx, u, chatID, d, text)
	case stateSearch:
		b.inputSearch(ctx, u, chatID, text)
	case stateEditAmount:
		b.inputEditAmount(ctx, u, chatID, d, text)
	case stateNote:
		note := text
		if isSkip(text) {
			note = ""
		}
		if err := b.st.SetTransactionNote(ctx, u.ID, d.TxID, note); err != nil {
			b.fail(ctx, chatID, err)
			return
		}
		b.clearState(ctx, u.ID)
		b.showTx(ctx, u, chatID, d.TxID)
	case stateNewCategory:
		b.inputNewCategory(ctx, u, chatID, d, text)
	case stateAccountName:
		b.inputAccountName(ctx, u, chatID, d, text)
	case stateAccountInit:
		b.inputAccountInitial(ctx, u, chatID, d, text)
	case stateConvRate:
		b.inputConversionRate(ctx, u, chatID, d, text)
	case stateConvAmount:
		b.inputConvAmount(ctx, u, chatID, d, text)
	case stateTransferAmt:
		b.inputTransferAmount(ctx, u, chatID, d, text)
	case stateTransferTo:
		b.inputTransferToAmount(ctx, u, chatID, d, text)
	case stateTransferRate:
		b.inputTransferRate(ctx, u, chatID, d, text)
	case stateBudgetAmount:
		b.inputBudgetAmount(ctx, u, chatID, d, text)
	case stateRateInput:
		b.inputMarketRate(ctx, u, chatID, d, text)
	case stateReportPeriod:
		b.clearState(ctx, u.ID)
		b.sendReport(ctx, u, chatID, text, report.Full())
	case stateRecurAmount:
		b.inputRecurAmount(ctx, u, chatID, d, text)
	case stateRecurDay:
		b.inputRecurDay(ctx, u, chatID, d, text)
	case stateBalanceFix:
		b.inputBalanceFix(ctx, u, chatID, d, text)
	case stateRenameAcc:
		b.inputAccountRename(ctx, u, chatID, d, text)
	case stateOnboard:
		b.send(ctx, chatID, "Use the buttons above to continue — the setup only needs taps and a balance.", nil)
	case stateOnboardName:
		b.inputOnboardingPlace(ctx, u, chatID, d, text)
	case stateLoanName:
		b.inputLoanName(ctx, u, chatID, d, text)
	case stateLoanAmount:
		b.inputLoanAmount(ctx, u, chatID, d, text)
	case stateLoanMonthly:
		b.inputLoanMonthly(ctx, u, chatID, d, text)
	case stateLoanDay:
		b.inputLoanDay(ctx, u, chatID, d, text)
	case stateLoanPay:
		b.inputLoanPay(ctx, u, chatID, d, text)
	case stateLoanInterest:
		b.inputLoanInterest(ctx, u, chatID, d, text)
	case stateLoanFix:
		b.inputLoanFix(ctx, u, chatID, d, text)
	case stateLoanAccount, stateLoanCurrency, stateLoanPayAcc:
		b.send(ctx, chatID, "Pick one with the buttons above.", nil)
	default:
		b.clearState(ctx, u.ID)
		b.nudge(ctx, chatID)
	}
}

func isSkip(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	return t == "-" || t == "/skip" || t == "skip" || t == "no" || t == "пропустить" || t == "нет"
}

// applySource fills in the source: either updates an already-saved operation
// or finishes the draft.
func (b *Bot) applySource(ctx context.Context, u storage.User, chatID int64, d draft) {
	if d.TxID != 0 {
		if err := b.st.SetTransactionSource(ctx, u.ID, d.TxID, d.Source); err != nil {
			b.fail(ctx, chatID, err)
			return
		}
		b.clearState(ctx, u.ID)
		b.showTx(ctx, u, chatID, d.TxID)
		return
	}
	b.saveEntry(ctx, u, chatID, d)
}

// inputAmount takes what is typed while the bot waits for an amount — or, once the
// amount is known, for a category button.
func (b *Bot) inputAmount(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	// The amount is already set, so the bot is waiting for a category button:
	// whatever is typed can only be a category name.
	if d.Amount > 0 && d.AccountID != 0 {
		if cat, err := b.st.FindCategory(ctx, u.ID, d.Kind, text); err == nil {
			d.CategoryID = &cat.ID
			b.finishEntry(ctx, u, chatID, d)
			return
		}
		b.send(ctx, chatID, "No such category. Pick one with a button or /cancel.", nil)
		return
	}
	// An account is chosen and only the amount is typed.
	if d.AccountID != 0 && d.Kind != "" {
		b.inputGuidedAmount(ctx, u, chatID, d, text)
		return
	}
	// Nothing to type yet: the bot is waiting for a button.
	b.send(ctx, chatID, "Use the buttons above, or /cancel.", nil)
}

// inputConvAmount: the user states how much actually left the account, so the
// figures match their bank statement exactly. The rate follows from the two
// real amounts — nothing is guessed.
func (b *Bot) inputConvAmount(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	acc, opCur, raw, ok := b.conversionContext(ctx, u, chatID, d)
	if !ok {
		return
	}
	accAmount, err := money.Parse(text, acc.Decimals)
	if err != nil || accAmount <= 0 {
		b.send(ctx, chatID, fmt.Sprintf(
			"Need an amount in <b>%s</b> — how much left the account. For example: <code>10.64</code>",
			acc.Currency), cancelKeyboard())
		return
	}
	rate := float64(accAmount) / float64(pow10i(acc.Decimals)) /
		(float64(raw) / float64(pow10i(opCur.Decimals)))
	b.applyConversion(ctx, u, chatID, d, acc, opCur, raw, accAmount, rate)
}

// inputConversionRate: the user states the rate their bank or exchange applied.
// d.RateDir says which way round they are typing it.
func (b *Bot) inputConversionRate(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	acc, opCur, raw, ok := b.conversionContext(ctx, u, chatID, d)
	if !ok {
		return
	}
	entered, err := parseRate(text)
	if err != nil {
		b.send(ctx, chatID, "The rate must be a number, for example <code>470</code> or <code>537.5</code>", cancelKeyboard())
		return
	}
	rate := normalizeRate(entered, d.RateDir)
	accAmount := money.Convert(raw, opCur.Decimals, acc.Decimals, rate)
	if accAmount <= 0 {
		b.send(ctx, chatID, "At that rate nothing would be debited — check the number.", cancelKeyboard())
		return
	}
	b.applyConversion(ctx, u, chatID, d, acc, opCur, raw, accAmount, rate)
}

// conversionContext reloads everything a conversion step needs: the account, the
// operation's currency and the amount as the user originally typed it.
func (b *Bot) conversionContext(ctx context.Context, u storage.User, chatID int64, d draft) (
	storage.Account, storage.Currency, int64, bool) {

	var acc storage.Account
	var opCur storage.Currency
	acc, err := b.st.Account(ctx, u.ID, d.AccountID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return acc, opCur, 0, false
	}
	opCur, err = b.st.Currency(ctx, d.Currency)
	if err != nil {
		b.fail(ctx, chatID, err)
		return acc, opCur, 0, false
	}
	raw := int64(0)
	if d.OriginalAmount != nil {
		raw = *d.OriginalAmount
	} else {
		raw, err = money.Parse(d.AmountRaw, opCur.Decimals)
		if err != nil {
			raw = 0
		}
	}
	if raw <= 0 {
		b.clearState(ctx, u.ID)
		b.send(ctx, chatID, "Lost the operation amount, please start over.", nil)
		return acc, opCur, 0, false
	}
	return acc, opCur, raw, true
}

// applyConversion records both sides of a converted operation: what the purchase
// cost in its own currency and what left the account at the user's rate.
func (b *Bot) applyConversion(ctx context.Context, u storage.User, chatID int64, d draft,
	acc storage.Account, opCur storage.Currency, raw, accAmount int64, rate float64) {

	d.Amount = accAmount
	d.OriginalAmount = &raw
	d.Rate = &rate

	b.send(ctx, chatID, fmt.Sprintf(
		"Recording: <b>%s</b> — account “%s” debited <b>%s</b>\n<i>your rate: %s</i>",
		money.FormatCode(raw, opCur.Decimals, opCur.Code), esc(acc.Name),
		money.FormatCode(accAmount, acc.Decimals, acc.Currency),
		rateLine(opCur.Code, acc.Currency, rate)), nil)

	if d.CategoryID == nil {
		b.askCategory(ctx, u, chatID, d, "Category?")
		return
	}
	b.finishEntry(ctx, u, chatID, d)
}

// askNewCategory asks for a category by name. Nothing is offered up front: the
// categories are the user's own words for where their money goes, and a list
// somebody else wrote would only be noise. Once named, a category is a button
// forever after.
func (b *Bot) askNewCategory(ctx context.Context, u storage.User, chatID int64, d draft) {
	if d.Kind == "" {
		d.Kind = storage.KindExpense
	}
	b.setState(ctx, u.ID, stateNewCategory, d)

	kind := "expense"
	example := "🛒 Groceries"
	if d.Kind == storage.KindIncome {
		kind = "income"
		example = "💼 Salary"
	}
	cats, err := b.st.ListCategories(ctx, u.ID, d.Kind)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}

	var sb strings.Builder
	if len(cats) == 0 {
		fmt.Fprintf(&sb, "<b>Your first %s category</b>\n\nWhat should it be called?", kind)
	} else {
		fmt.Fprintf(&sb, "<b>New %s category</b>\n\nWhat should it be called?", kind)
	}
	fmt.Fprintf(&sb, "\n<i>Send it as text. Start with an emoji to give it an icon: </i><code>%s</code>", example)

	kb := cancelKeyboard()
	if row := b.loanShortcut(ctx, u, d); row != nil {
		kb.InlineKeyboard = append([][]models.InlineKeyboardButton{row}, kb.InlineKeyboard...)
	}
	b.send(ctx, chatID, sb.String(), kb)
}

// inputNewCategory takes a category name typed instead of picked. A leading
// emoji is used as the category's icon.
func (b *Bot) inputNewCategory(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	name := strings.TrimSpace(text)
	if name == "" || len([]rune(name)) > 40 {
		b.send(ctx, chatID, "Category name — up to 40 characters.", cancelKeyboard())
		return
	}
	emoji := ""
	fields := strings.Fields(name)
	if len(fields) > 1 && isEmoji(fields[0]) {
		emoji, name = fields[0], strings.Join(fields[1:], " ")
	}
	b.createCategory(ctx, u, chatID, d, name, emoji)
}

// createCategory saves the category and, if it was asked for in the middle of
// recording an operation, carries on with that operation.
func (b *Bot) createCategory(ctx context.Context, u storage.User, chatID int64, d draft, name, emoji string) {
	kind := d.Kind
	if kind == "" {
		kind = storage.KindExpense
	}
	cat, err := b.st.CreateCategory(ctx, u.ID, name, kind, emoji)
	if err != nil {
		if strings.Contains(err.Error(), "categories_user_uniq") {
			b.send(ctx, chatID, "You already have a category called “"+esc(name)+"”.", nil)
			return
		}
		b.fail(ctx, chatID, err)
		return
	}
	if d.Amount > 0 && d.AccountID > 0 {
		d.CategoryID = &cat.ID
		b.finishEntry(ctx, u, chatID, d)
		return
	}
	// Named from the categories screen: setting up categories is usually
	// several in a row, so stay ready for the next name until Done.
	b.setState(ctx, u.ID, stateNewCategory, draft{Kind: kind})
	b.send(ctx, chatID, "✅ Category “"+esc(cat.Title())+"” created.\n\n"+
		"<i>Send the next name to add another, or tap Done.</i>",
		inline([]models.InlineKeyboardButton{btn("✅ Done", "catdone")}))
}

func isEmoji(s string) bool {
	for _, r := range s {
		if r < 0x1F000 && r != '⚡' && r < 0x2190 {
			return false
		}
	}
	return s != ""
}

func joinNote(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "; " + b
	}
}

func parseRate(text string) (float64, error) {
	t := strings.TrimSpace(strings.ReplaceAll(text, ",", "."))
	t = strings.ReplaceAll(t, " ", "")
	rate, err := strconv.ParseFloat(t, 64)
	if err != nil || rate <= 0 {
		return 0, fmt.Errorf("not a rate")
	}
	return rate, nil
}

// cardOf renders an operation's card together with the balance of the account it
// touched — after recording or correcting something, that is the figure the
// person actually wants to see.
func (b *Bot) cardOf(ctx context.Context, u storage.User, t storage.Transaction) (string, *models.InlineKeyboardMarkup) {
	if balance, err := b.st.AccountBalance(ctx, u.ID, t.AccountID); err == nil {
		return txCard(t, &balance)
	}
	return txCard(t, nil)
}

// showTxInPlace redraws an operation's card in the message it already lives in —
// after a date change, or when backing out of the date picker.
func (b *Bot) showTxInPlace(ctx context.Context, u storage.User, chatID int64, msgID int, txID int64) {
	t, err := b.st.Transaction(ctx, u.ID, txID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	text, kb := b.cardOf(ctx, u, t)
	b.edit(ctx, chatID, msgID, text, kb)
}

func (b *Bot) showTx(ctx context.Context, u storage.User, chatID, txID int64) {
	t, err := b.st.Transaction(ctx, u.ID, txID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	text, kb := b.cardOf(ctx, u, t)
	b.send(ctx, chatID, text, kb)
}

// monthOf — the 1st of the month for a date, in the user's timezone.
func monthOf(t time.Time, loc *time.Location) time.Time {
	x := t.In(loc)
	return time.Date(x.Year(), x.Month(), 1, 0, 0, 0, 0, loc)
}

// Rate directions the user can choose when typing a rate.
const (
	rateDirect  = "direct"  // "1 <source currency> = N <target currency>" — the stored form
	rateInverse = "inverse" // "1 <target currency> = N <source currency>"
)

// normalizeRate turns the rate the user typed into the stored form: how much of
// the target currency per 1 unit of the source currency.
func normalizeRate(entered float64, dir string) float64 {
	if dir == rateInverse {
		return 1 / entered
	}
	return entered
}

// rateLine prints a stored rate the way people quote it — with the bigger unit
// first, so 0.00213 USD per tenge reads as "1 USD = 470 KZT".
func rateLine(sourceCurrency, targetCurrency string, rate float64) string {
	if rate >= 1 {
		return fmt.Sprintf("1 %s = %s %s", sourceCurrency, trimFloat(rate), targetCurrency)
	}
	return fmt.Sprintf("1 %s = %s %s", targetCurrency, trimFloat(1/rate), sourceCurrency)
}

// rateDirectionKeyboard asks which way round the user is about to type a rate,
// so nothing has to be inferred from the number itself.
func rateDirectionKeyboard(prefix, source, target string, extra ...models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	rows := [][]models.InlineKeyboardButton{{
		btn(fmt.Sprintf("1 %s = ? %s", target, source), prefix+":"+rateInverse),
		btn(fmt.Sprintf("1 %s = ? %s", source, target), prefix+":"+rateDirect),
	}}
	if len(extra) > 0 {
		rows = append(rows, extra)
	}
	rows = append(rows, []models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")})
	return inline(rows...)
}
