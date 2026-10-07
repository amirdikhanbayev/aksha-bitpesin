package bot

import (
	"context"
	"fmt"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/money"
	"aksha-bitpesin/internal/storage"
)

// cmdTransfer launches the transfer wizard: source account → amount → destination account →
// (if the currencies differ) the rate, which the user enters themselves.
func (b *Bot) cmdTransfer(ctx context.Context, u storage.User, chatID int64) {
	accs, err := b.st.ListAccounts(ctx, u.ID, false)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if len(accs) < 2 {
		b.send(ctx, chatID, "A transfer needs at least two accounts. Create another one: /newaccount", nil)
		return
	}
	b.setState(ctx, u.ID, stateTransferAmt, draft{Kind: storage.KindTransfer})
	b.send(ctx, chatID, "🔁 Transfer — from which account?", accountsKeyboard(accs, "trfrom", btn("✖️ Cancel", "cancel")))
}

// askTransferAmount — the source account has been picked.
func (b *Bot) askTransferAmount(ctx context.Context, u storage.User, chatID int64, d draft) {
	acc, err := b.st.Account(ctx, u.ID, d.AccountID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.setState(ctx, u.ID, stateTransferAmt, d)
	b.send(ctx, chatID, fmt.Sprintf("How much to debit from “%s” (%s)?", esc(acc.Name), acc.Currency), cancelKeyboard())
}

func (b *Bot) inputTransferAmount(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	if d.AccountID == 0 {
		b.send(ctx, chatID, "Pick the source account with a button first.", nil)
		return
	}
	from, err := b.st.Account(ctx, u.ID, d.AccountID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	amount, err := money.Parse(text, from.Decimals)
	if err != nil || amount <= 0 {
		b.send(ctx, chatID, "Send the amount as a number, for example <code>50000</code>", cancelKeyboard())
		return
	}
	d.Amount = amount

	accs, err := b.st.ListAccounts(ctx, u.ID, false)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	var others []storage.Account
	for _, a := range accs {
		if a.ID != d.AccountID {
			others = append(others, a)
		}
	}
	b.setState(ctx, u.ID, stateTransferAmt, d)
	b.send(ctx, chatID, fmt.Sprintf("Debiting %s. Which account receives it?",
		money.FormatCode(amount, from.Decimals, from.Currency)),
		accountsKeyboard(others, "trto", btn("✖️ Cancel", "cancel")))
}

// askTransferRate — the destination account has been picked. When the currencies
// differ, the rate is the first thing asked: every bank and exchange booth uses
// its own, so the user states it and the bot never assumes one.
func (b *Bot) askTransferRate(ctx context.Context, u storage.User, chatID int64, d draft) {
	from, to, ok := b.transferAccounts(ctx, u, chatID, d)
	if !ok {
		return
	}
	if from.Currency == to.Currency {
		b.saveTransfer(ctx, u, chatID, d)
		return
	}
	b.setState(ctx, u.ID, stateTransferRate, d)
	b.send(ctx, chatID, fmt.Sprintf(
		"💱 Exchange %s → %s. Debiting <b>%s</b>.\n\n"+
			"What rate did you exchange at? Choose how you enter the rate:",
		from.Currency, to.Currency,
		money.FormatCode(d.Amount, from.Decimals, from.Currency)),
		rateDirectionKeyboard("trdir", from.Currency, to.Currency,
			[]models.InlineKeyboardButton{btn("Enter the credited amount", "tramount")}...))
}

// askTransferRateInput — the direction is chosen, now the number itself.
func (b *Bot) askTransferRateInput(ctx context.Context, u storage.User, chatID int64, d draft, dir string) {
	from, to, ok := b.transferAccounts(ctx, u, chatID, d)
	if !ok {
		return
	}
	d.RateDir = dir
	b.setState(ctx, u.ID, stateTransferRate, d)

	source, target := from.Currency, to.Currency
	if dir == rateInverse {
		source, target = to.Currency, from.Currency
	}
	b.send(ctx, chatID, fmt.Sprintf(
		"How many <b>%s</b> per 1 <b>%s</b>?\n\n<i>For example: <code>470</code> means 1 %s = 470 %s</i>",
		target, source, source, target), cancelKeyboard())
}

// askTransferAmountManual — the user would rather state how much actually
// arrived; the rate then follows from the two real amounts.
func (b *Bot) askTransferAmountManual(ctx context.Context, u storage.User, chatID int64, d draft) {
	from, to, ok := b.transferAccounts(ctx, u, chatID, d)
	if !ok {
		return
	}
	b.setState(ctx, u.ID, stateTransferTo, d)
	b.send(ctx, chatID, fmt.Sprintf(
		"Debiting <b>%s</b>. How much <b>%s</b> arrived in “%s”?",
		money.FormatCode(d.Amount, from.Decimals, from.Currency),
		to.Currency, esc(to.Name)), cancelKeyboard())
}

// transferAccounts reloads both sides of the transfer being built.
func (b *Bot) transferAccounts(ctx context.Context, u storage.User, chatID int64, d draft) (
	storage.Account, storage.Account, bool) {

	from, err := b.st.Account(ctx, u.ID, d.AccountID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return from, storage.Account{}, false
	}
	to, err := b.st.Account(ctx, u.ID, d.ToAccountID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return from, to, false
	}
	return from, to, true
}

// inputTransferRate: the user entered a rate — compute the credited amount.
func (b *Bot) inputTransferRate(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	entered, err := parseRate(text)
	if err != nil {
		b.send(ctx, chatID, "The rate must be a number, for example <code>470</code> or <code>0.0019</code>", cancelKeyboard())
		return
	}
	from, to, ok := b.transferAccounts(ctx, u, chatID, d)
	if !ok {
		return
	}
	if d.RateDir == "" {
		// The direction wasn't chosen (an old dialog) — ask before doing anything.
		b.askTransferRate(ctx, u, chatID, d)
		return
	}
	rate := normalizeRate(entered, d.RateDir)
	toAmount := money.Convert(d.Amount, from.Decimals, to.Decimals, rate)
	if toAmount <= 0 {
		b.send(ctx, chatID, "That rate gives zero. Check the number.", cancelKeyboard())
		return
	}
	d.ToAmount = &toAmount
	d.Rate = &rate
	b.send(ctx, chatID, fmt.Sprintf("Crediting <b>%s</b>\n<i>your rate: %s</i>",
		money.FormatCode(toAmount, to.Decimals, to.Currency),
		rateLine(from.Currency, to.Currency, rate)), nil)
	b.saveTransfer(ctx, u, chatID, d)
}

// inputTransferToAmount: the user enters the credited amount directly, we derive the rate.
func (b *Bot) inputTransferToAmount(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	from, err := b.st.Account(ctx, u.ID, d.AccountID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	to, err := b.st.Account(ctx, u.ID, d.ToAccountID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	toAmount, err := money.Parse(text, to.Decimals)
	if err != nil || toAmount <= 0 {
		b.send(ctx, chatID, "Send the credited amount as a number.", cancelKeyboard())
		return
	}
	d.ToAmount = &toAmount
	rate := float64(toAmount) / float64(pow10i(to.Decimals)) /
		(float64(d.Amount) / float64(pow10i(from.Decimals)))
	d.Rate = &rate
	b.send(ctx, chatID, fmt.Sprintf("<i>Your rate from these amounts: %s</i>",
		rateLine(from.Currency, to.Currency, rate)), nil)
	b.saveTransfer(ctx, u, chatID, d)
}

func (b *Bot) saveTransfer(ctx context.Context, u storage.User, chatID int64, d draft) {
	at := b.clock().In(u.Location())
	if d.OccurredAt != nil {
		at = *d.OccurredAt
	}
	t, err := b.st.CreateTransfer(ctx, u.ID, d.AccountID, d.ToAccountID, d.Amount,
		d.ToAmount, d.Rate, d.Note, at)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.clearState(ctx, u.ID)
	text, kb := b.cardOf(ctx, u, t)
	b.send(ctx, chatID, text, kb)
}

func pow10i(n int) int64 {
	p := int64(1)
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}
