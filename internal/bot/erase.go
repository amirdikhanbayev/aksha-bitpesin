package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/storage"
)

// Erasing everything. It cannot be undone, so it takes two deliberate taps with
// different wording, the scale of what will go is spelled out first, and the CSV
// export is offered right there — the last chance to keep a copy.

// askErase shows what erasing would take with it.
func (b *Bot) askErase(ctx context.Context, u storage.User, chatID int64, msgID int) {
	d, err := b.st.UserDataSummary(ctx, u.ID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}

	var sb strings.Builder
	sb.WriteString("🗑 <b>Erase everything?</b>\n\n")
	if !d.Any() {
		sb.WriteString("There is nothing recorded yet — erasing would only reset your timezone and currency.")
	} else {
		sb.WriteString("This would delete, permanently:\n")
		for _, line := range []struct {
			n    int
			word string
		}{
			{d.Operations, "operation"},
			{d.Accounts, "account"},
			{d.Categories, "category"},
			{d.Budgets, "budget"},
			{d.Recurring, "recurring operation"},
			{d.Loans, "loan"},
		} {
			if line.n > 0 {
				fmt.Fprintf(&sb, "• %d %s\n", line.n, plural(line.n, line.word))
			}
		}
		sb.WriteString("\nYour settings go too, and the bot starts over as if you had never used it.\n")
		sb.WriteString("<b>This cannot be undone.</b> Take the CSV first if you want to keep anything.")
	}

	rows := [][]models.InlineKeyboardButton{}
	if d.Operations > 0 {
		rows = append(rows, []models.InlineKeyboardButton{btn("📄 Export everything first", "csv:all")})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{btn("🗑 Erase everything", "erase:confirm")},
		[]models.InlineKeyboardButton{btn("◀️ Keep my data", "settings")})

	if msgID > 0 {
		b.edit(ctx, chatID, msgID, sb.String(), inline(rows...))
		return
	}
	b.send(ctx, chatID, sb.String(), inline(rows...))
}

// confirmErase is the second, differently worded tap.
func (b *Bot) confirmErase(ctx context.Context, u storage.User, chatID int64, msgID int) {
	b.edit(ctx, chatID, msgID,
		"⚠️ <b>Last check</b>\n\nEverything you have recorded will be gone for good, "+
			"and there is no way back from here.",
		inline(
			[]models.InlineKeyboardButton{btn("🗑 Yes, erase it all", "erase:yes")},
			[]models.InlineKeyboardButton{btn("◀️ No, keep my data", "settings")},
		))
}

// eraseNow does it. After this the user row is gone, so nothing may touch
// u.ID again: the next message creates a new user and starts the setup.
func (b *Bot) eraseNow(ctx context.Context, u storage.User, chatID int64, msgID int) {
	if err := b.st.DeleteUserData(ctx, u.ID); err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.edit(ctx, chatID, msgID, "🗑 <b>Done — everything is erased.</b>", nil)
	b.send(ctx, chatID, "Send anything when you want to start over.", removeMenu())
}

// plural is the crude English plural that covers the words used above.
func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	if strings.HasSuffix(word, "y") {
		return strings.TrimSuffix(word, "y") + "ies"
	}
	return word + "s"
}
