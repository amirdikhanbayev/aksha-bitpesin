package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/money"
	"aksha-bitpesin/internal/storage"
)

// cmdRecurring — salary, rent, subscriptions: the bot creates them itself on the right day of the month.
func (b *Bot) cmdRecurring(ctx context.Context, u storage.User, chatID int64, _ string) {
	list, err := b.st.ListRecurring(ctx, u.ID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	var sb strings.Builder
	sb.WriteString("<b>🔁 Recurring operations</b>\n\n")
	if len(list) == 0 {
		sb.WriteString("Nothing yet. Add a salary or rent — the bot will record them itself.\n")
	}
	var rows [][]models.InlineKeyboardButton
	for _, r := range list {
		sign := "−"
		if r.Kind == storage.KindIncome {
			sign = "+"
		}
		status := ""
		if !r.Active {
			status = " (off)"
		}
		fmt.Fprintf(&sb, "• day %s · <b>%s%s</b> · %s · %s%s\n",
			strconv.Itoa(r.DayOfMonth), sign,
			money.FormatCode(r.Amount, r.Decimals, r.AccountCurr),
			esc(r.CategoryName), esc(r.AccountName), status)
		rows = append(rows, []models.InlineKeyboardButton{
			btn("🗑 "+r.CategoryName+" · day "+strconv.Itoa(r.DayOfMonth), fmt.Sprintf("recdel:%d", r.ID)),
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		btn("➕ Income", "recnew:income"),
		btn("➖ Expense", "recnew:expense"),
	})
	b.send(ctx, chatID, sb.String(), inline(rows...))
}

func (b *Bot) startRecurring(ctx context.Context, u storage.User, chatID int64, kind string) {
	if kind != storage.KindIncome {
		kind = storage.KindExpense
	}
	accs, err := b.st.ListAccounts(ctx, u.ID, false)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if len(accs) == 0 {
		b.send(ctx, chatID, "Set up an account first: /newaccount", nil)
		return
	}
	d := draft{Kind: kind}
	if len(accs) == 1 {
		d.AccountID = accs[0].ID
		cats, err := b.st.ListCategories(ctx, u.ID, kind)
		if err != nil {
			b.fail(ctx, chatID, err)
			return
		}
		b.setState(ctx, u.ID, stateRecurAmount, d)
		b.send(ctx, chatID, "Category of the recurring operation?",
			categoriesKeyboard(cats, "reccat", btn("✖️ Cancel", "cancel")))
		return
	}
	b.setState(ctx, u.ID, stateRecurAmount, d)
	b.send(ctx, chatID, "Which account?", accountsKeyboard(accs, "recacc", btn("✖️ Cancel", "cancel")))
}

func (b *Bot) inputRecurAmount(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	if d.CategoryID == nil || d.AccountID == 0 {
		b.clearState(ctx, u.ID)
		b.send(ctx, chatID, "Lost the draft, please start over: /recurring", nil)
		return
	}
	acc, err := b.st.Account(ctx, u.ID, d.AccountID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	amount, err := money.Parse(text, acc.Decimals)
	if err != nil || amount <= 0 {
		b.send(ctx, chatID, "Send the amount as a number.", cancelKeyboard())
		return
	}
	d.Amount = amount
	b.setState(ctx, u.ID, stateRecurDay, d)
	b.send(ctx, chatID, "On which day of the month?\n<i>Days past the end of a shorter month land on its last day.</i>",
		dayKeyboard())
}

// dayKeyboard is a calendar grid, so the day is tapped rather than typed.
func dayKeyboard() *models.InlineKeyboardMarkup {
	buttons := make([]models.InlineKeyboardButton, 0, 31)
	for day := 1; day <= 31; day++ {
		label := strconv.Itoa(day)
		buttons = append(buttons, btn(label, "recday:"+label))
	}
	rows := grid(buttons, 7)
	rows = append(rows, []models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")})
	return inline(rows...)
}

func (b *Bot) inputRecurDay(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	day, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || day < 1 || day > 31 {
		b.send(ctx, chatID, "Pick a day with the buttons.", dayKeyboard())
		return
	}
	b.createRecurring(ctx, u, chatID, d, day)
}

// createRecurring saves the scheduled operation the wizard has collected.
func (b *Bot) createRecurring(ctx context.Context, u storage.User, chatID int64, d draft, day int) {
	if d.CategoryID == nil || d.AccountID == 0 || d.Amount <= 0 {
		b.clearState(ctx, u.ID)
		b.send(ctx, chatID, "Lost the draft, please start over: /recurring", nil)
		return
	}
	_, err := b.st.CreateRecurring(ctx, storage.Recurring{
		UserID:     u.ID,
		AccountID:  d.AccountID,
		CategoryID: *d.CategoryID,
		Kind:       d.Kind,
		Amount:     d.Amount,
		DayOfMonth: day,
		Note:       d.Note,
	})
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.clearState(ctx, u.ID)
	acc, _ := b.st.Account(ctx, u.ID, d.AccountID)
	b.send(ctx, chatID, fmt.Sprintf("✅ I will record %s on day %d of every month on account “%s”.",
		money.FormatCode(d.Amount, acc.Decimals, acc.Currency), day, esc(acc.Name)), nil)
}
