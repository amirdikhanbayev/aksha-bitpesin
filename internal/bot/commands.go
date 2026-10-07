package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/money"
	"aksha-bitpesin/internal/parser"
	"aksha-bitpesin/internal/report"
	"aksha-bitpesin/internal/storage"
)

const helpText = `<b>How to keep the books</b>

You only type numbers — an amount, or a rate. Everything else you tap.

<b>Recording something</b>
1. <b>➖ Expense</b>, <b>➕ Income</b> or <b>🔁 Transfer</b>
2. Pick the account (skipped if you have just one)
3. Send the amount — just the number: <code>5000</code>, <code>1,500.50</code>, <code>12k</code>
4. Pick a category, then who it was paid to / came from

<b>Categories are yours</b>
None come with the bot. The first time, you name one in your own words — start with an emoji to give it an icon: <code>🛒 Groceries</code>. After that it's a button, and the ones you use most sit on top. Rename or put one away in ⚙️ Settings → 🏷 Categories.

<b>Another currency?</b>
Tap <b>💱 Another currency</b> before the amount. The bot then asks <b>how much actually left the account, or what rate you got</b> — every bank and exchange has its own rate, and the bot never substitutes one. A transfer between currencies asks the same.

<b>Got it wrong?</b>
Every operation's card can change anything: <b>💰 Amount</b>, <b>📅 Date</b>, <b>🏷 Category</b>, <b>💳 Account</b>, <b>🧑 Source</b>, <b>📝 Note</b>, <b>🗑 Delete</b>. For something recorded earlier, open /last or 🔍 Search and tap it. Moving an operation to an account in another currency asks for the amount again — the old number wouldn't mean the same there.

<b>🔁 Again</b> on a card repeats it: same category, account and source, only the amount to send.

<b>Statements</b>
📊 <b>Reports</b> — any period by tapping: a month, a window, or <b>📅 Custom range</b>. Under every statement: <b>🧾 Operations</b> for what's behind the numbers, <b>📄 CSV</b> for a file, and <b>🤖 Prompt for AI analysis</b> — the CSV plus a prompt explaining it, to paste into whatever AI you use.
The monthly statement also arrives by itself on the 1st.
/month · /stats · /last · /undo · /export

<b>🔍 Search</b>
/search magnum · /search &gt; 50000 · /search coffee 1000-5000 — a word, an amount, or both.

<b>🤝 Debts and loans</b>
Lent someone money, borrowed, bought something in installments, took a credit or a mortgage — add it under <b>🤝 Debts</b> and the bot keeps count of what's left. To pay: open it there and tap <b>💳 Make a payment</b> — or just record a usual <b>➖ Expense</b> and tap <b>🏦 It's a loan or debt payment</b> instead of a category (money paid back to you: <b>➕ Income</b> → <b>🤝 It's a debt paid back to me</b>). A bank loan also asks how much of the payment was interest. Lending and paying back aren't spending; installment and mortgage payments and any interest are, and the statement lists them by name. Set a payment day and you get a reminder that day if it isn't recorded.
/debts

<b>Accounts</b>
💳 <b>🤝 Debts and loans</b>
Lent someone money, borrowed, bought something in installments, took a credit or a mortgage — add it under <b>🤝 Debts</b> and the bot keeps count of what's left. To pay: open it there and tap <b>💳 Make a payment</b> — or just record a usual <b>➖ Expense</b> and tap <b>🏦 It's a loan or debt payment</b> instead of a category (money paid back to you: <b>➕ Income</b> → <b>🤝 It's a debt paid back to me</b>). A bank loan also asks how much of the payment was interest. Lending and paying back aren't spending; installment and mortgage payments and any interest are, and the statement lists them by name. Set a payment day and you get a reminder that day if it isn't recorded.
/debts

<b>Accounts</b> — balances; add, rename, archive. One place can hold several currencies: cash in tenge and cash in dollars live under the same <b>Cash</b>, each with its own balance.

<b>Rates</b>
/rates — what's known · <b>🔄 Update from NBRK</b> · <b>🕰 Fill in past dates</b> for older operations
/rate USD 540 — a rate for the report's summary line only; it never touches a real operation.

⚙️ <b>Settings</b> — accounts, categories, budgets, recurring operations, currency, timezone, the monthly statement, the evening reminder, and 🗑 erasing everything.
/cancel — cancel whatever you're in the middle of`

func (b *Bot) handleCommand(ctx context.Context, u storage.User, chatID int64, cmd, args string) {
	switch cmd {
	case "/start":
		b.cmdStart(ctx, u, chatID)
	case "/help":
		b.send(ctx, chatID, helpText, mainMenu())
	case "/skip":
		state, data, err := b.st.LoadState(ctx, u.ID)
		if err != nil || state == "" {
			b.send(ctx, chatID, "Nothing to skip right now.", nil)
			return
		}
		b.handleStateInput(ctx, u, chatID, state, data, "-")
	case "/cancel":
		b.clearState(ctx, u.ID)
		b.send(ctx, chatID, "Canceled. What's next?", mainMenu())
	case "/add":
		b.askKind(ctx, chatID)
	case "/expense", "/rashod":
		b.startEntry(ctx, u, chatID, storage.KindExpense)
	case "/income", "/dohod":
		b.startEntry(ctx, u, chatID, storage.KindIncome)
	case "/transfer":
		b.cmdTransfer(ctx, u, chatID)
	case "/month":
		b.sendReport(ctx, u, chatID, "", report.Full())
	case "/report":
		if args == "" {
			b.send(ctx, chatID, "For which period?", periodKeyboard(b.clock(), u.Location()))
			return
		}
		b.sendReport(ctx, u, chatID, args, report.Full())
	case "/stats":
		b.sendReport(ctx, u, chatID, args, report.Short())
	case "/last":
		b.cmdLast(ctx, u, chatID, args)
	case "/accounts":
		b.cmdAccounts(ctx, u, chatID)
	case "/newaccount", "/account_new":
		b.startNewAccount(ctx, u, chatID)
	case "/categories":
		b.cmdCategories(ctx, u, chatID)
	case "/budget":
		b.cmdBudget(ctx, u, chatID, args)
	case "/recurring":
		b.cmdRecurring(ctx, u, chatID, args)
	case "/rate":
		b.cmdRate(ctx, u, chatID, args)
	case "/rates":
		b.cmdRates(ctx, u, chatID, args)
	case "/currency":
		b.cmdCurrency(ctx, u, chatID, args)
	case "/timezone", "/tz":
		b.cmdTimezone(ctx, u, chatID, args)
	case "/settings":
		b.cmdSettings(ctx, u, chatID)
	case "/export":
		b.cmdExport(ctx, u, chatID, args)
	case "/search", "/find":
		if args != "" {
			b.inputSearch(ctx, u, chatID, args)
			return
		}
		b.askSearch(ctx, u, chatID)
	case "/debts", "/loans":
		b.cmdDebts(ctx, u, chatID, 0)
	case "/erase":
		b.askErase(ctx, u, chatID, 0)
	case "/undo":
		b.cmdUndo(ctx, u, chatID)
	default:
		b.send(ctx, chatID, "Unknown command. /help", nil)
	}
}

func (b *Bot) handleMenuButton(ctx context.Context, u storage.User, chatID int64, text string) bool {
	switch text {
	case btnExpense:
		b.startEntry(ctx, u, chatID, storage.KindExpense)
	case btnIncome:
		b.startEntry(ctx, u, chatID, storage.KindIncome)
	case btnReport:
		b.send(ctx, chatID, "For which period?", periodKeyboard(b.clock(), u.Location()))
	case btnAccounts:
		b.cmdAccounts(ctx, u, chatID)
	case btnTransfer:
		b.cmdTransfer(ctx, u, chatID)
	case btnSettings:
		b.cmdSettings(ctx, u, chatID)
	case btnDebts:
		b.cmdDebts(ctx, u, chatID, 0)
	default:
		return false
	}
	return true
}

func (b *Bot) cmdStart(ctx context.Context, u storage.User, chatID int64) {
	if b.needsOnboarding(ctx, u) {
		b.startOnboarding(ctx, u, chatID)
		return
	}
	b.send(ctx, chatID, "Welcome back! Tap <b>➖ Expense</b>, <b>➕ Income</b> or <b>🔁 Transfer</b> to record something, "+
		"or <b>📊 Reports</b> to see where you stand.\n\n/help — everything the bot can do", mainMenu())
}

func (b *Bot) askKind(ctx context.Context, chatID int64) {
	b.send(ctx, chatID, "What are we recording?", inline([]models.InlineKeyboardButton{
		btn("➖ Expense", "kind:expense"),
		btn("➕ Income", "kind:income"),
		btn("🔁 Transfer", "kind:transfer"),
	}))
}

func (b *Bot) cmdLast(ctx context.Context, u storage.User, chatID int64, args string) {
	limit := 15
	if n, err := strconv.Atoi(strings.TrimSpace(args)); err == nil && n > 0 && n <= 100 {
		limit = n
	}
	txs, err := b.st.LastTransactions(ctx, u.ID, limit)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if len(txs) == 0 {
		b.send(ctx, chatID, "Nothing recorded yet. Tap <b>➖ Expense</b> or <b>➕ Income</b> to start.", mainMenu())
		return
	}
	loc := u.Location()
	var sb strings.Builder
	fmt.Fprintf(&sb, "🧾 <b>Last %d operations</b>\n\n", len(txs))
	for _, t := range txs {
		sb.WriteString(txLine(t, loc))
		sb.WriteByte('\n')
	}
	sb.WriteString("\n<i>Tap one to correct its amount, date, source or note.</i>")
	b.sendLong(ctx, chatID, sb.String(), operationsKeyboard(txs, loc))
}

// txLine — one line in the operations list.
func txLine(t storage.Transaction, loc *time.Location) string {
	date := t.OccurredAt.In(loc).Format("02.01")
	switch t.Kind {
	case storage.KindTransfer:
		to := money.FormatCode(t.Amount, t.Decimals, t.AccountCurr)
		if t.ToAmount != nil {
			to = money.FormatCode(*t.ToAmount, t.ToDecimals, t.ToAccountCurr)
		}
		return fmt.Sprintf("%s 🔁 %s → %s · %s → %s",
			date, esc(t.AccountName), esc(t.ToAccountName),
			money.FormatCode(t.Amount, t.Decimals, t.AccountCurr), to)
	case storage.KindDebtIn:
		return fmt.Sprintf("%s 📥 +%s · %s %s",
			date, money.FormatCode(t.Amount, t.Decimals, t.AccountCurr), loanEmoji(t.LoanKind), esc(t.LoanName))
	case storage.KindDebtOut:
		return fmt.Sprintf("%s 📤 −%s · %s %s",
			date, money.FormatCode(t.Amount, t.Decimals, t.AccountCurr), loanEmoji(t.LoanKind), esc(t.LoanName))
	case storage.KindIncome:
		return fmt.Sprintf("%s 🟢 <b>+%s</b> · %s%s",
			date, money.FormatCode(t.Amount, t.Decimals, t.AccountCurr),
			esc(categoryLabel(t)), sourceSuffix(t))
	default:
		return fmt.Sprintf("%s 🔴 <b>−%s</b> · %s%s",
			date, money.FormatCode(t.Amount, t.Decimals, t.AccountCurr),
			esc(categoryLabel(t)), sourceSuffix(t))
	}
}

func categoryLabel(t storage.Transaction) string {
	if t.CategoryName == "" && t.LoanName != "" {
		return loanEmoji(t.LoanKind) + " " + t.LoanName // a loan payment is filed under its loan
	}
	if t.CategoryName == "" {
		return "no category"
	}
	if t.CategoryEmoji != "" {
		return t.CategoryEmoji + " " + t.CategoryName
	}
	return t.CategoryName
}

func sourceSuffix(t storage.Transaction) string {
	if t.Source == "" || t.Source == t.LoanName { // a loan's name is shown already
		return ""
	}
	return " · <i>" + esc(t.Source) + "</i>"
}

func (b *Bot) cmdUndo(ctx context.Context, u storage.User, chatID int64) {
	txs, err := b.st.LastTransactions(ctx, u.ID, 1)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if len(txs) == 0 {
		b.send(ctx, chatID, "Nothing to undo.", nil)
		return
	}
	t := txs[0]
	if err := b.st.DeleteTransaction(ctx, u.ID, t.ID); err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.send(ctx, chatID, "🗑 Deleted:\n"+txLine(t, u.Location()), nil)
}

func (b *Bot) sendReport(ctx context.Context, u storage.User, chatID int64, arg string, opt report.Options) {
	p, err := parser.ParsePeriod(arg, b.clock(), u.Location())
	if err != nil {
		b.send(ctx, chatID, "❓ "+esc(err.Error())+
			"\n\nExamples: <code>/report september</code>, <code>/report last</code>, <code>/report 01.09-15.09</code>", nil)
		return
	}
	text, err := report.Build(ctx, b.st, u, p, opt)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.sendLong(ctx, chatID, text, statementKeyboard(p.Arg()))
}

func (b *Bot) fail(ctx context.Context, chatID int64, err error) {
	if errors.Is(err, storage.ErrNotFound) {
		b.send(ctx, chatID, "Not found. It may already be deleted.", nil)
		return
	}
	b.send(ctx, chatID, "⚠️ Error: "+esc(err.Error()), nil)
}
