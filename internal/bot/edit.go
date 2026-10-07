package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/money"
	"aksha-bitpesin/internal/storage"
)

// Correcting a mistyped amount. Which figure is being corrected depends on the
// operation, so the bot asks for the one the user actually entered:
//
//   - a plain income or expense — the amount itself;
//   - one paid in another currency — what the purchase cost, since that is what
//     was typed; what left the account follows from the rate already recorded;
//   - a transfer — the amount debited, with the credited amount following from
//     its rate.
//
// The rate itself is never recomputed: it is what the bank applied, and a
// correction of a typo doesn't change it.

// askEditAmount asks for the corrected figure, naming the currency it is in.
func (b *Bot) askEditAmount(ctx context.Context, u storage.User, chatID int64, t storage.Transaction) {
	b.setState(ctx, u.ID, stateEditAmount, draft{TxID: t.ID})

	currency, decimals, current := t.AccountCurr, t.Decimals, t.Amount
	extra := ""
	switch {
	case t.Converted():
		currency, decimals, current = t.OriginalCurrency, t.OriginalDecimals, *t.OriginalAmount
		extra = fmt.Sprintf("\n<i>I'll recompute what left “%s” at your rate: %s</i>",
			esc(t.AccountName), rateLine(t.OriginalCurrency, t.AccountCurr, *t.Rate))
	case t.Kind == storage.KindTransfer && t.ToAmount != nil && t.Rate != nil:
		extra = fmt.Sprintf("\n<i>I'll recompute what arrives in “%s” at your rate: %s</i>",
			esc(t.ToAccountName), rateLine(t.AccountCurr, t.ToAccountCurr, *t.Rate))
	}

	b.send(ctx, chatID, fmt.Sprintf(
		"Currently <b>%s</b>. What should it be? Send the amount in <b>%s</b>.%s",
		money.FormatCode(current, decimals, currency), currency, extra),
		cancelKeyboard())
}

// inputEditAmount takes the corrected amount and writes it, together with the
// amounts that follow from it.
func (b *Bot) inputEditAmount(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	t, err := b.st.Transaction(ctx, u.ID, d.TxID)
	if err != nil {
		b.clearState(ctx, u.ID)
		b.fail(ctx, chatID, err)
		return
	}

	// The figure is read in the currency it was entered in.
	currency, decimals := t.AccountCurr, t.Decimals
	if t.Converted() {
		currency, decimals = t.OriginalCurrency, t.OriginalDecimals
	}
	entered, err := money.Parse(text, decimals)
	if err != nil || entered <= 0 {
		b.send(ctx, chatID, fmt.Sprintf(
			"Send the amount in <b>%s</b> as a number, for example <code>5000</code>.", currency), cancelKeyboard())
		return
	}

	amount := entered
	var original, to *int64
	switch {
	case t.Converted():
		// What was typed is the purchase; the debit follows from the rate.
		original = &entered
		acc, err := b.st.Account(ctx, u.ID, t.AccountID)
		if err != nil {
			b.fail(ctx, chatID, err)
			return
		}
		amount = money.Convert(entered, t.OriginalDecimals, acc.Decimals, *t.Rate)
		if amount <= 0 {
			b.send(ctx, chatID, "At that amount nothing would be debited — check the number.", cancelKeyboard())
			return
		}
	case t.Kind == storage.KindTransfer && t.ToAmount != nil && t.Rate != nil:
		credited := money.Convert(entered, t.Decimals, t.ToDecimals, *t.Rate)
		if credited <= 0 {
			b.send(ctx, chatID, "At that amount nothing would arrive — check the number.", cancelKeyboard())
			return
		}
		to = &credited
	}

	if err := b.st.SetTransactionAmounts(ctx, u.ID, t.ID, amount, original, to); err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.clearState(ctx, u.ID)

	updated, err := b.st.Transaction(ctx, u.ID, t.ID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	text2, kb := b.cardOf(ctx, u, updated)
	text2 = "✏️ <b>Amount corrected</b>\n\n" + text2
	if warn := b.budgetWarning(ctx, u, updated); warn != "" {
		text2 += "\n\n" + warn
	}
	b.send(ctx, chatID, text2, kb)
}

// Changing the rest of an operation: which category it belongs to, which account
// it went through, and — for a transfer — which two. The amount moves with the
// account when the currency changes, since an amount only means anything in one.

// askEditCategory offers the categories to move an operation to.
func (b *Bot) askEditCategory(ctx context.Context, u storage.User, chatID int64, msgID int, t storage.Transaction) {
	cats, err := b.st.ListCategories(ctx, u.ID, t.Kind)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if len(cats) == 0 {
		b.send(ctx, chatID, "No categories yet — name one while recording something.", nil)
		return
	}
	b.edit(ctx, chatID, msgID, "Which category does this belong to?",
		categoriesKeyboard(cats, fmt.Sprintf("txsetcat:%d", t.ID),
			btn("⏭ No category", fmt.Sprintf("txsetcat:%d:0", t.ID)),
			btn("◀️ Back", fmt.Sprintf("txshow:%d", t.ID))))
}

// askEditAccount offers the accounts an operation could have gone through. For a
// transfer both ends can be changed, one at a time.
func (b *Bot) askEditAccount(ctx context.Context, u storage.User, chatID int64, msgID int,
	t storage.Transaction, side string) {

	accs, err := b.st.ListAccounts(ctx, u.ID, false)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if t.Kind == storage.KindTransfer && side == "" {
		b.edit(ctx, chatID, msgID, "Which end of the transfer?", inline(
			[]models.InlineKeyboardButton{
				btn("From: "+t.AccountName, fmt.Sprintf("txacc:%d:from", t.ID)),
				btn("To: "+t.ToAccountName, fmt.Sprintf("txacc:%d:to", t.ID)),
			},
			[]models.InlineKeyboardButton{btn("◀️ Back", fmt.Sprintf("txshow:%d", t.ID))}))
		return
	}

	// Leave out the account already on that side, and the other end of a transfer.
	var options []storage.Account
	for _, a := range accs {
		if a.ID == t.AccountID && side != "to" {
			continue
		}
		if t.ToAccountID != nil && a.ID == *t.ToAccountID && side != "from" {
			continue
		}
		options = append(options, a)
	}
	if len(options) == 0 {
		b.send(ctx, chatID, "There is no other account to move it to.", nil)
		return
	}

	prefix := fmt.Sprintf("txsetacc:%d", t.ID)
	if side != "" {
		prefix += ":" + side
	}
	b.edit(ctx, chatID, msgID, "Which account did it go through?",
		accountsKeyboard(options, prefix, btn("◀️ Back", fmt.Sprintf("txshow:%d", t.ID))))
}

// moveToAccount moves an operation to another account. When the currencies
// differ the amount cannot simply follow — 5 000 tenge is not 5 000 dollars — so
// the amount is asked for again in the new currency.
func (b *Bot) moveToAccount(ctx context.Context, u storage.User, chatID int64, msgID int,
	t storage.Transaction, accountID int64, side string) {

	acc, err := b.st.Account(ctx, u.ID, accountID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if err := b.st.MoveTransaction(ctx, u.ID, t.ID, accountID, side == "to"); err != nil {
		b.fail(ctx, chatID, err)
		return
	}

	// The currency changed, so whatever the amount was, it no longer means what it did.
	oldCurrency := t.AccountCurr
	if side == "to" {
		oldCurrency = t.ToAccountCurr
	}
	if oldCurrency != acc.Currency {
		updated, err := b.st.Transaction(ctx, u.ID, t.ID)
		if err != nil {
			b.fail(ctx, chatID, err)
			return
		}
		b.edit(ctx, chatID, msgID, fmt.Sprintf("Moved to %s %s · %s", placeEmoji(acc.Name), esc(acc.Name), acc.Currency), nil)
		b.send(ctx, chatID, fmt.Sprintf(
			"The account is in <b>%s</b> now, so the amount needs restating.", acc.Currency), nil)
		b.askEditAmount(ctx, u, chatID, updated)
		return
	}
	b.showTxInPlace(ctx, u, chatID, msgID, t.ID)
}

// repeatOperation starts a new operation from an old one: the same category,
// account and source, so only the amount is left to type. The everyday case —
// the same coffee, the same taxi — in two taps.
func (b *Bot) repeatOperation(ctx context.Context, u storage.User, chatID int64, t storage.Transaction) {
	acc, err := b.st.Account(ctx, u.ID, t.AccountID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	d := draft{
		Kind:       t.Kind,
		AccountID:  acc.ID,
		CategoryID: t.CategoryID,
		Source:     t.Source,
	}
	b.setState(ctx, u.ID, stateAmount, d)

	what := []string{}
	if t.CategoryName != "" {
		what = append(what, strings.TrimSpace(t.CategoryEmoji+" "+t.CategoryName))
	}
	if t.Source != "" {
		what = append(what, t.Source)
	}
	again := "the same again"
	if len(what) > 0 {
		again = strings.Join(what, " · ")
	}
	b.send(ctx, chatID, fmt.Sprintf("%s · %s\n<i>%s</i>\n\nHow much this time? Send the amount in <b>%s</b>.",
		kindTitle(t.Kind), fmt.Sprintf("%s %s", placeEmoji(acc.Name), esc(acc.Name)), esc(again), acc.Currency),
		cancelKeyboard())
}

// Category housekeeping: rename one, or put it away. Archiving keeps the
// operations filed under it, so past statements stay as they were — the category
// just stops being offered.

// askCategoryToEdit lists the categories of a kind for editing.
func (b *Bot) askCategoryToEdit(ctx context.Context, u storage.User, chatID int64, msgID int, kind string) {
	cats, err := b.st.ListCategories(ctx, u.ID, kind)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if len(cats) == 0 {
		b.send(ctx, chatID, "Nothing to edit yet.", nil)
		return
	}
	b.edit(ctx, chatID, msgID, "Which category?",
		categoriesKeyboard(cats, "catpick", btn("◀️ Back", "setmenu:categories")))
}

// showCategory offers what can be done with one category.
func (b *Bot) showCategory(ctx context.Context, u storage.User, chatID int64, msgID int, c storage.Category) {
	used, err := b.st.CategoryUsage(ctx, u.ID, c.ID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	text := fmt.Sprintf("<b>%s</b>\n%d %s filed under it.", esc(c.Title()), used, plural(used, "operation"))
	if used > 0 {
		text += "\n<i>Putting it away keeps them — it just stops being offered.</i>"
	}
	b.edit(ctx, chatID, msgID, text, inline(
		[]models.InlineKeyboardButton{
			btn("✏️ Rename", fmt.Sprintf("catren:%d", c.ID)),
			btn("📦 Put away", fmt.Sprintf("catarch:%d", c.ID)),
		},
		[]models.InlineKeyboardButton{btn("◀️ Back", "catedit:"+c.Kind)},
	))
}

// inputCategoryName takes the new name of a category being renamed.
func (b *Bot) inputCategoryName(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	name := strings.TrimSpace(text)
	if name == "" || len([]rune(name)) > 40 {
		b.send(ctx, chatID, "Name — up to 40 characters.", cancelKeyboard())
		return
	}
	emoji := ""
	if fields := strings.Fields(name); len(fields) > 1 && isEmoji(fields[0]) {
		emoji, name = fields[0], strings.Join(fields[1:], " ")
	}
	if d.CategoryID == nil {
		b.clearState(ctx, u.ID)
		b.send(ctx, chatID, "Lost which category that was — open ⚙️ Settings → 🏷 Categories again.", nil)
		return
	}
	if err := b.st.RenameCategory(ctx, u.ID, *d.CategoryID, name, emoji); err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.clearState(ctx, u.ID)
	b.send(ctx, chatID, "✅ Renamed to “"+esc(strings.TrimSpace(emoji+" "+name))+"”.", nil)
	b.cmdCategories(ctx, u, chatID)
}
