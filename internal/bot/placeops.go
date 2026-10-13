package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/storage"
)

// The incomes and expenses of one place — a bank, cash — page by page, every
// one of them a button that opens its card. This is how something recorded a
// week ago is found again and corrected.

const placeOpsPerPage = 15

// askPlaceOps asks which place to list. With only one there is nothing to ask.
// msgID 0 sends a new message; otherwise the one at msgID is redrawn.
func (b *Bot) askPlaceOps(ctx context.Context, u storage.User, chatID int64, msgID int) {
	accs, err := b.st.ListAccounts(ctx, u.ID, false)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	// One button per place, whatever currencies it holds: the place's first
	// account stands for all of them.
	var places []storage.Account
	for _, a := range accs {
		seen := false
		for _, p := range places {
			if p.Name == a.Name {
				seen = true
				break
			}
		}
		if !seen {
			places = append(places, a)
		}
	}
	switch len(places) {
	case 0:
		b.send(ctx, chatID, "No accounts yet.", nil)
		return
	case 1:
		b.showPlaceOps(ctx, u, chatID, msgID, places[0].ID, 0)
		return
	}

	buttons := make([]models.InlineKeyboardButton, 0, len(places))
	for _, p := range places {
		buttons = append(buttons, btn(placeEmoji(p.Name)+" "+p.Name, fmt.Sprintf("accops:%d:0", p.ID)))
	}
	rows := grid(buttons, 2)
	rows = append(rows, []models.InlineKeyboardButton{btn("✖️ Close", "cancel")})
	text := "🧾 Operations of which account?"
	if msgID == 0 {
		b.send(ctx, chatID, text, inline(rows...))
		return
	}
	b.edit(ctx, chatID, msgID, text, inline(rows...))
}

// showPlaceOps shows one page of a place's incomes and expenses, newest first.
func (b *Bot) showPlaceOps(ctx context.Context, u storage.User, chatID int64, msgID int, accountID int64, page int) {
	acc, err := b.st.Account(ctx, u.ID, accountID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	page = max(page, 0)
	txs, total, err := b.st.PlaceTransactions(ctx, u.ID, accountID, page*placeOpsPerPage, placeOpsPerPage)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	pages := max((total+placeOpsPerPage-1)/placeOpsPerPage, 1)
	if page >= pages && total > 0 {
		// Deletions shrank the list since this page was shown: go to its new end.
		b.showPlaceOps(ctx, u, chatID, msgID, accountID, pages-1)
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🧾 <b>%s %s</b> — incomes and expenses\n", placeEmoji(acc.Name), esc(acc.Name))
	if total == 0 {
		sb.WriteString("\n<i>Nothing recorded here yet.</i>")
	} else {
		fmt.Fprintf(&sb, "<i>%d in all · page %d of %d</i>\n\n", total, page+1, pages)
		sb.WriteString(numberedLines(txs, page*placeOpsPerPage, u.Location()))
		sb.WriteString("\n<i>Tap a number to open it and change anything.</i>")
	}

	var rows [][]models.InlineKeyboardButton
	rows = append(rows, numberedButtons(txs, page*placeOpsPerPage)...)
	if pages > 1 {
		var nav []models.InlineKeyboardButton
		if page > 0 {
			nav = append(nav, btn("◀️", fmt.Sprintf("accops:%d:%d", accountID, page-1)))
		}
		nav = append(nav, btn(fmt.Sprintf("%d / %d", page+1, pages), fmt.Sprintf("accops:%d:%d", accountID, page)))
		if page < pages-1 {
			nav = append(nav, btn("▶️", fmt.Sprintf("accops:%d:%d", accountID, page+1)))
		}
		rows = append(rows, nav)
	}
	rows = append(rows, []models.InlineKeyboardButton{
		btn("🏦 Other account", "accops:list"),
		btn("🔍 Search", "search"),
	})

	if msgID == 0 {
		b.send(ctx, chatID, sb.String(), inline(rows...))
		return
	}
	b.edit(ctx, chatID, msgID, sb.String(), inline(rows...))
}

// handlePlaceOps routes the accops buttons: "" picks a place in a new message,
// "list" picks one in place, "<account>:<page>" shows a page.
func (b *Bot) handlePlaceOps(ctx context.Context, u storage.User, chatID int64, msgID int, arg string) bool {
	switch arg {
	case "":
		b.askPlaceOps(ctx, u, chatID, 0)
		return true
	case "list":
		b.askPlaceOps(ctx, u, chatID, msgID)
		return true
	}
	idStr, pageStr, _ := strings.Cut(arg, ":")
	id, err := parseID(idStr)
	if err != nil {
		return false
	}
	page, err := strconv.Atoi(pageStr)
	if err != nil || page < 0 {
		return false
	}
	b.showPlaceOps(ctx, u, chatID, msgID, id, page)
	return true
}

// numberedLines lists operations one per line, numbered from first+1 to match
// the buttons of numberedButtons.
func numberedLines(txs []storage.Transaction, first int, loc *time.Location) string {
	var sb strings.Builder
	for i, t := range txs {
		fmt.Fprintf(&sb, "<b>%d.</b> %s\n", first+i+1, txLine(t, loc))
	}
	return sb.String()
}

// numberedButtons opens each listed operation's card in a message of its own, so
// the list stays where it is for the next one.
func numberedButtons(txs []storage.Transaction, first int) [][]models.InlineKeyboardButton {
	buttons := make([]models.InlineKeyboardButton, 0, len(txs))
	for i, t := range txs {
		buttons = append(buttons, btn(strconv.Itoa(first+i+1), fmt.Sprintf("txopen:%d", t.ID)))
	}
	return grid(buttons, 5)
}
