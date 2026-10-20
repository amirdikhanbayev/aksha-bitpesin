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

// Splitting a bill. The user paid all of it — dinner, a taxi, a gift — and the
// others are to send their part back. Each part becomes a debt of its own
// (🤝 lent), so it shows in 🤝 Debts, is paid back to whichever account the
// money arrives on, and can be forgiven on its own. The expense keeps only the
// user's own share: that is what they spent. See storage.SplitTransaction.

// maxSplitPeople caps how many people one bill is split between.
const maxSplitPeople = 12

// splittable reports whether an operation's card offers to split it: an
// ordinary expense in its account's currency.
func splittable(t storage.Transaction) bool {
	return t.Kind == storage.KindExpense && t.LoanID == nil && t.FeeOf == nil && !t.Converted()
}

// billLabel is what a bill was for, as the debts it is split into are named.
func billLabel(t storage.Transaction) string {
	switch {
	case t.Source != "":
		return t.Source
	case t.CategoryName != "":
		return t.CategoryName
	}
	return ""
}

// startSplit begins splitting an expense: who shares it.
func (b *Bot) startSplit(ctx context.Context, u storage.User, chatID int64, t storage.Transaction) {
	if !splittable(t) {
		b.send(ctx, chatID, "Only an ordinary expense in its account's currency can be split.", nil)
		return
	}
	if shares, err := b.st.SplitLoans(ctx, u.ID, t.ID); err == nil && len(shares) > 0 {
		b.send(ctx, chatID, "This bill is already split.", nil)
		return
	}
	people, _ := b.st.SplitPeople(ctx, u.ID, 8)
	d := draft{TxID: t.ID, Amount: t.Amount, AccountID: t.AccountID, Currency: t.AccountCurr, Sources: people}
	b.showSplitPeople(ctx, u, chatID, 0, d)
}

// showSplitPeople shows who is picked, with the names used before as toggles.
// With msgID set it redraws that message instead of sending a new one.
func (b *Bot) showSplitPeople(ctx context.Context, u storage.User, chatID int64, msgID int, d draft) {
	b.setState(ctx, u.ID, stateSplitPeople, d)
	t, err := b.st.Transaction(ctx, u.ID, d.TxID)
	if err != nil {
		b.clearState(ctx, u.ID)
		b.fail(ctx, chatID, err)
		return
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "👥 <b>Split %s</b>", money.FormatCode(t.Amount, t.Decimals, t.AccountCurr))
	if label := billLabel(t); label != "" {
		fmt.Fprintf(&sb, " · %s", esc(label))
	}
	sb.WriteString("\n\nWho shares it with you? ")
	if len(d.Sources) > 0 {
		sb.WriteString("Tap the names, or type a new one.")
	} else {
		sb.WriteString("Type a name — several at once with commas: <code>Aidar, Dana</code>")
	}
	if len(d.Queue) > 0 {
		fmt.Fprintf(&sb, "\n\n<i>With you: %s</i>", esc(strings.Join(d.Queue, ", ")))
	}

	var buttons []models.InlineKeyboardButton
	for i, name := range d.Sources {
		label := short(name, 22)
		if contains(d.Queue, name) {
			label = "✅ " + label
		}
		buttons = append(buttons, btn(label, fmt.Sprintf("splitwho:i%d", i)))
	}
	rows := grid(buttons, 2)
	if len(d.Queue) > 0 {
		rows = append(rows, []models.InlineKeyboardButton{btn("Done ▶️", "splitwho:done")})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")})
	if msgID > 0 {
		b.edit(ctx, chatID, msgID, sb.String(), inline(rows...))
		return
	}
	b.send(ctx, chatID, sb.String(), inline(rows...))
}

// inputSplitPerson takes typed names: one, or several separated by commas.
func (b *Bot) inputSplitPerson(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	added := false
	for _, part := range strings.Split(text, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if len([]rune(name)) > 40 {
			b.send(ctx, chatID, "A name — up to 40 characters.", cancelKeyboard())
			return
		}
		if len(d.Queue) >= maxSplitPeople {
			b.send(ctx, chatID, fmt.Sprintf("That's %d people already — tap Done.", maxSplitPeople), nil)
			break
		}
		if !contains(d.Sources, name) {
			d.Sources = append(d.Sources, name)
		}
		if !contains(d.Queue, name) {
			d.Queue = append(d.Queue, name)
			added = true
		}
	}
	if !added && len(d.Queue) == 0 {
		b.send(ctx, chatID, "Type a name, or tap one above.", cancelKeyboard())
		return
	}
	b.showSplitPeople(ctx, u, chatID, 0, d)
}

// toggleSplitPerson picks or unpicks one of the names on offer.
func toggleSplitPerson(d *draft, idx int) bool {
	if idx < 0 || idx >= len(d.Sources) {
		return false
	}
	name := d.Sources[idx]
	for i, n := range d.Queue {
		if n == name {
			d.Queue = append(d.Queue[:i:i], d.Queue[i+1:]...)
			return true
		}
	}
	if len(d.Queue) >= maxSplitPeople {
		return false
	}
	d.Queue = append(d.Queue, name)
	return true
}

// askSplitHow asks how the bill is divided, with the equal shares worked out.
func (b *Bot) askSplitHow(ctx context.Context, u storage.User, chatID int64, msgID int, d draft) {
	cur, err := b.st.Currency(ctx, d.Currency)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	n := int64(len(d.Queue))
	f := func(v int64) string { return money.Format(v, cur.Decimals) }
	others := "1 other"
	if n > 1 {
		others = fmt.Sprintf("%d others", n)
	}
	b.setState(ctx, u.ID, stateSplitPeople, d)
	b.edit(ctx, chatID, msgID, fmt.Sprintf("👥 Split <b>%s</b> between you and %s (%s).\n\nHow?",
		money.FormatCode(d.Amount, cur.Decimals, cur.Code), others, esc(strings.Join(d.Queue, ", "))),
		inline(
			[]models.InlineKeyboardButton{btn(fmt.Sprintf("➗ Equally — %s each", f(d.Amount/(n+1))), "splithow:eq")},
			[]models.InlineKeyboardButton{btn(fmt.Sprintf("🙅 Only them — %s each", f(d.Amount/n)), "splithow:them")},
			[]models.InlineKeyboardButton{btn("✏️ Set each share", "splithow:set")},
			[]models.InlineKeyboardButton{btn("◀️ Back", "splithow:back"), btn("✖️ Cancel", "cancel")},
		))
}

// equalShares divides total between n people and, unless withoutMe, the user.
// What doesn't divide evenly stays with the user — or, when the user takes no
// share, with the last person — so nobody owes a fraction of a coin.
func equalShares(total int64, n int, withoutMe bool) []int64 {
	parts := int64(n)
	if !withoutMe {
		parts++
	}
	shares := make([]int64, n)
	for i := range shares {
		shares[i] = total / parts
	}
	if withoutMe {
		shares[n-1] += total - total/parts*parts
	}
	return shares
}

// askSplitShare asks for the next person's share, saying what is left.
func (b *Bot) askSplitShare(ctx context.Context, u storage.User, chatID int64, d draft) {
	cur, err := b.st.Currency(ctx, d.Currency)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	left := d.Amount
	for _, v := range d.Shares {
		left -= v
	}
	b.setState(ctx, u.ID, stateSplitShare, d)
	name := d.Queue[len(d.Shares)]
	rows := [][]models.InlineKeyboardButton{}
	if len(d.Shares) == len(d.Queue)-1 && left > 0 {
		rows = append(rows, []models.InlineKeyboardButton{btn("All the rest: "+money.Format(left, cur.Decimals), "splitrest")})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")})
	b.send(ctx, chatID, fmt.Sprintf("How much is <b>%s</b>'s share?\n<i>Left of the bill: %s</i>",
		esc(name), money.FormatCode(left, cur.Decimals, cur.Code)), inline(rows...))
}

func (b *Bot) inputSplitShare(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	cur, err := b.st.Currency(ctx, d.Currency)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if len(d.Shares) >= len(d.Queue) {
		b.clearState(ctx, u.ID)
		b.nudge(ctx, chatID)
		return
	}
	left := d.Amount
	for _, v := range d.Shares {
		left -= v
	}
	share, err := money.Parse(text, cur.Decimals)
	if err != nil || share <= 0 || share > left {
		b.send(ctx, chatID, fmt.Sprintf("Send the share as a number, no more than the %s left.",
			money.FormatCode(left, cur.Decimals, cur.Code)), cancelKeyboard())
		return
	}
	b.addSplitShare(ctx, u, chatID, d, share)
}

// addSplitShare takes one share and goes on to the next person, or records the split.
func (b *Bot) addSplitShare(ctx context.Context, u storage.User, chatID int64, d draft, share int64) {
	d.Shares = append(d.Shares, share)
	if len(d.Shares) < len(d.Queue) {
		b.askSplitShare(ctx, u, chatID, d)
		return
	}
	b.recordSplit(ctx, u, chatID, d)
}

// recordSplit splits the bill and says who owes what.
func (b *Bot) recordSplit(ctx context.Context, u storage.User, chatID int64, d draft) {
	if len(d.Shares) != len(d.Queue) {
		b.clearState(ctx, u.ID)
		b.send(ctx, chatID, "Lost track of the shares, please start over.", nil)
		return
	}
	shares := make([]storage.Share, len(d.Queue))
	var total int64
	for i, name := range d.Queue {
		shares[i] = storage.Share{Name: name, Amount: d.Shares[i]}
		total += d.Shares[i]
	}
	loans, err := b.st.SplitTransaction(ctx, u.ID, d.TxID, shares)
	if err != nil {
		b.clearState(ctx, u.ID)
		b.fail(ctx, chatID, err)
		return
	}
	b.clearState(ctx, u.ID)

	cur, err := b.st.Currency(ctx, d.Currency)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	f := func(v int64) string { return money.FormatCode(v, cur.Decimals, cur.Code) }
	var sb strings.Builder
	fmt.Fprintf(&sb, "👥 <b>Bill split</b> · %s\n\n", f(d.Amount))
	fmt.Fprintf(&sb, "Your share: <b>%s</b>\n", f(d.Amount-total))
	var buttons []models.InlineKeyboardButton
	for i, l := range loans {
		fmt.Fprintf(&sb, "• %s owes you <b>%s</b>\n", esc(d.Queue[i]), f(l.Principal))
		buttons = append(buttons, btn("🤝 "+short(d.Queue[i], 24), fmt.Sprintf("loan:%d", l.ID)))
	}
	sb.WriteString("\n<i>Each share is in 🤝 Debts now. When someone sends theirs, open it and tap " +
		"💰 Got money back — whichever account it lands on. Or 🕊 Forgive it.</i>")
	rows := grid(buttons, 2)
	if d.Amount > total {
		rows = append(rows, []models.InlineKeyboardButton{btn("🧾 The bill", fmt.Sprintf("txshow:%d", d.TxID))})
	}
	b.send(ctx, chatID, sb.String(), inline(rows...))
}

// handleSplitCallback serves the buttons of the split dialog; it reports false
// for an action that isn't one of them.
func (b *Bot) handleSplitCallback(ctx context.Context, u storage.User, q *models.CallbackQuery,
	action, arg string, d draft) bool {

	chatID := q.Message.Message.Chat.ID
	msgID := q.Message.Message.ID

	switch action {
	case "split":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Unknown choice")
			return true
		}
		t, err := b.st.Transaction(ctx, u.ID, id)
		if err != nil {
			b.answer(ctx, q, "Operation not found")
			return true
		}
		b.answer(ctx, q, "")
		b.startSplit(ctx, u, chatID, t)
	case "splitwho":
		if arg == "done" {
			if len(d.Queue) == 0 {
				b.answer(ctx, q, "Pick at least one person")
				return true
			}
			b.answer(ctx, q, "")
			b.askSplitHow(ctx, u, chatID, msgID, d)
			return true
		}
		idx, err := strconv.Atoi(strings.TrimPrefix(arg, "i"))
		if err != nil || !toggleSplitPerson(&d, idx) {
			b.answer(ctx, q, "Unknown choice")
			return true
		}
		b.answer(ctx, q, "")
		b.showSplitPeople(ctx, u, chatID, msgID, d)
	case "splithow":
		if len(d.Queue) == 0 {
			b.answer(ctx, q, "Start over from the expense")
			return true
		}
		b.answer(ctx, q, "")
		switch arg {
		case "eq", "them":
			b.edit(ctx, chatID, msgID, "👥 Split: <b>"+map[string]string{
				"eq": "equally", "them": "only between them"}[arg]+"</b>", nil)
			d.Shares = equalShares(d.Amount, len(d.Queue), arg == "them")
			b.recordSplit(ctx, u, chatID, d)
		case "set":
			b.edit(ctx, chatID, msgID, "👥 Split: <b>share by share</b>", nil)
			d.Shares = nil
			b.askSplitShare(ctx, u, chatID, d)
		default:
			b.showSplitPeople(ctx, u, chatID, msgID, d)
		}
	case "splitrest":
		left := d.Amount
		for _, v := range d.Shares {
			left -= v
		}
		if left <= 0 || len(d.Shares) != len(d.Queue)-1 {
			b.answer(ctx, q, "Nothing left to give")
			return true
		}
		b.answer(ctx, q, "")
		b.edit(ctx, chatID, msgID, "Share: <b>the rest</b>", nil)
		b.addSplitShare(ctx, u, chatID, d, left)
	default:
		return false
	}
	return true
}
