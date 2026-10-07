package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/parser"
	"aksha-bitpesin/internal/report"
	"aksha-bitpesin/internal/storage"
)

// maxListedOperations caps the itemized list; /export has everything.
const maxListedOperations = 500

// statementKeyboard sits under every statement, monthly or on request: the
// summary is the headline, these open what's behind it.
func statementKeyboard(period string) *models.InlineKeyboardMarkup {
	return inline(
		[]models.InlineKeyboardButton{
			btn("🧾 Operations", "ops:"+period),
			btn("📄 CSV", "csv:"+period),
		},
		[]models.InlineKeyboardButton{btn("🤖 Prompt for AI analysis", "aiprompt:"+period)},
	)
}

// SendStatement delivers a statement together with its buttons. The scheduler
// uses it, so the monthly statement is no different from one asked for.
func (b *Bot) SendStatement(ctx context.Context, chatID int64, text, period string) {
	b.sendLong(ctx, chatID, text, statementKeyboard(period))
}

// SendLoanReminder delivers a loan's payment reminder with the button that
// records the payment — needed by the scheduler.
func (b *Bot) SendLoanReminder(ctx context.Context, chatID int64, text string, loanID int64) {
	b.send(ctx, chatID, text, inline([]models.InlineKeyboardButton{
		btn("💳 Record the payment", fmt.Sprintf("loanpay:%d", loanID)),
		btn("Open", fmt.Sprintf("loan:%d", loanID)),
	}))
}

// sendOperations lists every operation in the period, oldest first — the
// statement in the sense of a bank statement.
func (b *Bot) sendOperations(ctx context.Context, u storage.User, chatID int64, period string) {
	loc := u.Location()
	p, err := parser.ParsePeriod(period, b.clock(), loc)
	if err != nil {
		b.send(ctx, chatID, "❓ "+esc(err.Error()), nil)
		return
	}
	txs, err := b.st.ListTransactions(ctx, u.ID, p.From, p.To, maxListedOperations+1)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if len(txs) == 0 {
		b.send(ctx, chatID, "No operations in "+esc(p.Label)+".", nil)
		return
	}
	truncated := len(txs) > maxListedOperations
	if truncated {
		txs = txs[:maxListedOperations] // the newest ones: the query is newest-first
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "<b>🧾 Operations: %s</b> (%d)\n\n", esc(p.Label), len(txs))
	for i := len(txs) - 1; i >= 0; i-- { // chronological, like a statement
		sb.WriteString(txLine(txs[i], loc))
		sb.WriteByte('\n')
	}
	if truncated {
		fmt.Fprintf(&sb, "\n<i>Showing the latest %d — the CSV has everything.</i>", maxListedOperations)
	}
	b.sendLong(ctx, chatID, sb.String(), statementKeyboard(p.Arg()))
}

// sendAIPrompt hands over a prompt to paste into whichever AI the person uses,
// along with the CSV. Sent as its own message with nothing else in it, so the
// whole thing can be copied in one go — and as plain text, since a prompt full of
// markup is useless once pasted.
func (b *Bot) sendAIPrompt(ctx context.Context, u storage.User, chatID int64, period string) {
	p, err := parser.ParsePeriod(period, b.clock(), u.Location())
	if err != nil {
		b.send(ctx, chatID, "❓ "+esc(err.Error()), nil)
		return
	}
	ops, err := b.st.CountTransactions(ctx, u.ID, p.From, p.To)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}

	b.send(ctx, chatID,
		"🤖 <b>For your AI</b>\n\nTake the CSV below and the prompt in the next message — "+
			"paste both into whatever you use. The prompt explains the columns, so the answer "+
			"doesn't have to guess what the data means.", nil)
	b.cmdExport(ctx, u, chatID, period)

	// Plain text, no HTML: this message exists to be copied verbatim.
	_, err = b.api.SendMessage(ctx, &tg.SendMessageParams{
		ChatID: chatID,
		Text:   report.AIPrompt(u, p, ops),
		LinkPreviewOptions: &models.LinkPreviewOptions{
			IsDisabled: ptr(true),
		},
	})
	if err != nil {
		slog.Error("sending the AI prompt", "err", err)
		b.send(ctx, chatID, "Couldn't send the prompt: "+esc(err.Error()), nil)
	}
}
