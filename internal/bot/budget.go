package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/money"
	"aksha-bitpesin/internal/parser"
	"aksha-bitpesin/internal/storage"
)

// cmdBudget shows the current month's limits and lets them be changed.
func (b *Bot) cmdBudget(ctx context.Context, u storage.User, chatID int64, args string) {
	loc := u.Location()
	p, err := parser.ParsePeriod(args, b.clock(), loc)
	if err != nil || p.Month.IsZero() {
		p, _ = parser.ParsePeriod("", b.clock(), loc)
	}
	buds, err := b.st.Budgets(ctx, u.ID, p.Month, p.From, p.To)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>🎯 Limits: %s</b>\n\n", esc(p.Label))
	if len(buds) == 0 {
		sb.WriteString("No limits yet. Set a limit on a category — the bot will warn you as it runs out.\n")
	}
	for _, bd := range buds {
		pct := 0
		if bd.LimitAmount > 0 {
			pct = int(bd.Spent * 100 / bd.LimitAmount)
		}
		mark := "✅"
		switch {
		case pct >= 100:
			mark = "🔴"
		case pct >= 80:
			mark = "🟡"
		}
		left := bd.LimitAmount - bd.Spent
		fmt.Fprintf(&sb, "%s <b>%s</b>\n   %s of %s · %s left (%d%%)\n",
			mark, esc(bd.CategoryName),
			money.Format(bd.Spent, bd.Decimals),
			money.FormatCode(bd.LimitAmount, bd.Decimals, bd.Currency),
			money.Format(left, bd.Decimals), pct)
	}
	b.send(ctx, chatID, sb.String(), inline([]models.InlineKeyboardButton{
		btn("➕ Set a limit", "budpick"),
	}))
}

// askBudgetCategory shows the expense categories for picking a limit.
func (b *Bot) askBudgetCategory(ctx context.Context, u storage.User, chatID int64) {
	cats, err := b.st.ListCategories(ctx, u.ID, storage.KindExpense)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if len(cats) == 0 {
		b.send(ctx, chatID, "Create expense categories first: /categories", nil)
		return
	}
	b.send(ctx, chatID, "Which category should get the limit?",
		categoriesKeyboard(cats, "budcat", btn("✖️ Cancel", "cancel")))
}

func (b *Bot) inputBudgetAmount(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	if d.CategoryID == nil {
		b.clearState(ctx, u.ID)
		b.send(ctx, chatID, "Lost the category, please start over: /budget", nil)
		return
	}
	loc := u.Location()
	month := monthOf(b.clock(), loc)

	if isSkip(text) {
		if err := b.st.DeleteBudget(ctx, u.ID, *d.CategoryID, month); err != nil {
			b.fail(ctx, chatID, err)
			return
		}
		b.clearState(ctx, u.ID)
		b.send(ctx, chatID, "🗑 Limit removed.", nil)
		return
	}

	cur, err := b.st.Currency(ctx, u.MainCurrency)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	limit, err := money.Parse(text, cur.Decimals)
	if err != nil || limit <= 0 {
		b.send(ctx, chatID, "Send the limit as a number, for example <code>150000</code>", cancelKeyboard())
		return
	}
	if err := b.st.SetBudget(ctx, u.ID, *d.CategoryID, month, u.MainCurrency, limit); err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.clearState(ctx, u.ID)
	cat, _ := b.st.Category(ctx, u.ID, *d.CategoryID)
	b.send(ctx, chatID, fmt.Sprintf("✅ Limit for “%s”, %s: %s",
		esc(cat.Title()), parser.MonthTitle(month),
		money.FormatCode(limit, cur.Decimals, u.MainCurrency)), nil)
}
