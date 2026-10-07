package bot

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/money"
	"aksha-bitpesin/internal/storage"
)

// Searching the books. The query is one line of what you remember: a word, an
// amount, or both — "magnum", "> 50000", "coffee < 2000".

const maxSearchResults = 30

// searchQuery is a parsed search line.
type searchQuery struct {
	Text string
	Min  int64 // 0 = no lower bound
	Max  int64 // 0 = no upper bound
}

// parseSearch reads the search line. Amount bounds are written as > 50000,
// < 2000, or 1000-5000; anything else is text to match.
func parseSearch(line string, decimals int) searchQuery {
	var q searchQuery
	var words []string

	tokens := strings.Fields(line)
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		// "> 50000" and ">50000" mean the same thing, so a bare operator takes
		// the number that follows it.
		if (tok == ">" || tok == "<") && i+1 < len(tokens) {
			if v, err := money.Parse(tokens[i+1], decimals); err == nil {
				if tok == ">" {
					q.Min = v
				} else {
					q.Max = v
				}
				i++
				continue
			}
		}
		switch {
		case strings.HasPrefix(tok, ">"):
			if v, err := money.Parse(strings.TrimPrefix(tok, ">"), decimals); err == nil {
				q.Min = v
				continue
			}
		case strings.HasPrefix(tok, "<"):
			if v, err := money.Parse(strings.TrimPrefix(tok, "<"), decimals); err == nil {
				q.Max = v
				continue
			}
		case strings.Contains(tok, "-") && !strings.HasPrefix(tok, "-"):
			lo, hi, _ := strings.Cut(tok, "-")
			loV, errLo := money.Parse(lo, decimals)
			hiV, errHi := money.Parse(hi, decimals)
			if errLo == nil && errHi == nil && loV <= hiV {
				q.Min, q.Max = loV, hiV
				continue
			}
		}
		words = append(words, tok)
	}
	q.Text = strings.Join(words, " ")
	return q
}

// askSearch invites a search line.
func (b *Bot) askSearch(ctx context.Context, u storage.User, chatID int64) {
	b.setState(ctx, u.ID, stateSearch, draft{})
	b.send(ctx, chatID,
		"🔍 <b>Search</b>\n\nWhat do you remember about it?\n"+
			"<i>A word — a shop, a category, an account, a note. An amount — </i><code>&gt; 50000</code><i>, </i>"+
			"<code>&lt; 2000</code><i>, </i><code>1000-5000</code><i>. Or both: </i><code>magnum &gt; 10000</code>",
		cancelKeyboard())
}

// inputSearch runs the search and shows what it found.
func (b *Bot) inputSearch(ctx context.Context, u storage.User, chatID int64, text string) {
	main, err := b.st.Currency(ctx, u.MainCurrency)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	q := parseSearch(text, main.Decimals)
	if q.Text == "" && q.Min == 0 && q.Max == 0 {
		b.send(ctx, chatID, "Nothing to search for — send a word, an amount, or both.", cancelKeyboard())
		return
	}

	txs, err := b.st.SearchTransactions(ctx, u.ID, q.Text, q.Min, q.Max, maxSearchResults+1)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.clearState(ctx, u.ID)

	if len(txs) == 0 {
		b.send(ctx, chatID, "Nothing matched “"+esc(text)+"”.",
			inline([]models.InlineKeyboardButton{btn("🔍 Search again", "search")}))
		return
	}
	truncated := len(txs) > maxSearchResults
	if truncated {
		txs = txs[:maxSearchResults]
	}

	loc := u.Location()
	var sb strings.Builder
	fmt.Fprintf(&sb, "🔍 <b>%s</b> — %d found\n\n", esc(text), len(txs))

	// Totals per currency, so the answer says how much, not just which ones.
	totals := map[string]int64{}
	decimals := map[string]int{}
	var order []string
	for _, t := range txs {
		sb.WriteString(txLine(t, loc))
		sb.WriteByte('\n')
		if t.Kind == storage.KindTransfer {
			continue
		}
		if _, seen := totals[t.AccountCurr]; !seen {
			order = append(order, t.AccountCurr)
			decimals[t.AccountCurr] = t.Decimals
		}
		if t.Kind == storage.KindExpense || t.Kind == storage.KindDebtOut {
			totals[t.AccountCurr] -= t.Amount
		} else {
			totals[t.AccountCurr] += t.Amount
		}
	}
	if len(order) > 0 {
		parts := make([]string, 0, len(order))
		for _, cur := range order {
			parts = append(parts, money.FormatSigned(totals[cur], decimals[cur], cur))
		}
		fmt.Fprintf(&sb, "\n<b>Net:</b> %s", strings.Join(parts, " · "))
	}
	if truncated {
		fmt.Fprintf(&sb, "\n<i>Showing the newest %d.</i>", maxSearchResults)
	}

	b.sendLong(ctx, chatID, sb.String(), operationsKeyboard(txs, loc))
}
