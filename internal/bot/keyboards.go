package bot

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/money"
	"aksha-bitpesin/internal/storage"
)

// Bottom menu buttons.
const (
	btnExpense  = "➖ Expense"
	btnIncome   = "➕ Income"
	btnReport   = "📊 Reports"
	btnAccounts = "💳 Accounts"
	btnTransfer = "🔁 Transfer"
	btnSettings = "⚙️ Settings"
	btnDebts    = "🤝 Debts"
)

func mainMenu() *models.ReplyKeyboardMarkup {
	return &models.ReplyKeyboardMarkup{
		Keyboard: [][]models.KeyboardButton{
			{{Text: btnExpense}, {Text: btnIncome}, {Text: btnTransfer}},
			{{Text: btnReport}, {Text: btnAccounts}, {Text: btnSettings}},
			{{Text: btnDebts}},
		},
		ResizeKeyboard: true,
	}
}

// removeMenu takes the bottom keyboard away — after erasing everything, there is
// nothing to tap until the setup runs again.
func removeMenu() *models.ReplyKeyboardRemove {
	return &models.ReplyKeyboardRemove{RemoveKeyboard: true}
}

func inline(rows ...[]models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func btn(text, data string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: data}
}

// grid lays out buttons into cols columns.
func grid(buttons []models.InlineKeyboardButton, cols int) [][]models.InlineKeyboardButton {
	var rows [][]models.InlineKeyboardButton
	for i := 0; i < len(buttons); i += cols {
		end := min(i+cols, len(buttons))
		rows = append(rows, buttons[i:end])
	}
	return rows
}

func categoriesKeyboard(cats []storage.Category, prefix string, extra ...models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	buttons := make([]models.InlineKeyboardButton, 0, len(cats))
	for _, c := range cats {
		buttons = append(buttons, btn(c.Title(), fmt.Sprintf("%s:%d", prefix, c.ID)))
	}
	rows := grid(buttons, 2)
	if len(extra) > 0 {
		rows = append(rows, extra)
	}
	return inline(rows...)
}

// accountLabel names one account: the place it is, and the currency it holds.
// A place can hold several, so the currency is part of what identifies it.
func accountLabel(a storage.Account) string {
	return fmt.Sprintf("%s %s · %s", placeEmoji(a.Name), a.Name, a.Currency)
}

func accountsKeyboard(accs []storage.Account, prefix string, extra ...models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	buttons := make([]models.InlineKeyboardButton, 0, len(accs))
	for _, a := range accs {
		buttons = append(buttons, btn(accountLabel(a), fmt.Sprintf("%s:%d", prefix, a.ID)))
	}
	rows := grid(buttons, 2)
	if len(extra) > 0 {
		rows = append(rows, extra)
	}
	return inline(rows...)
}

func sourcesKeyboard(sources []string, prefix string) *models.InlineKeyboardMarkup {
	buttons := make([]models.InlineKeyboardButton, 0, len(sources)+2)
	for i, s := range sources {
		label := s
		if len([]rune(label)) > 24 {
			label = string([]rune(label)[:24]) + "…"
		}
		// callback_data can't hold long strings — pass an index instead
		buttons = append(buttons, btn(label, fmt.Sprintf("%s:i%d", prefix, i)))
	}
	rows := grid(buttons, 2)
	rows = append(rows, []models.InlineKeyboardButton{
		btn("⏭ No source", prefix+":skip"),
		btn("✖️ Cancel", "cancel"),
	})
	return inline(rows...)
}

// txCard — the card for a saved operation, with the buttons that correct it.
// balance is the account's balance after the operation, or nil when unknown.
func txCard(t storage.Transaction, balance *int64) (string, *models.InlineKeyboardMarkup) {
	if t.LoanID != nil {
		return loanTxCard(t, balance)
	}
	var b strings.Builder

	switch t.Kind {
	case storage.KindIncome:
		b.WriteString("✅ <b>Income recorded</b>\n\n")
	case storage.KindExpense:
		b.WriteString("✅ <b>Expense recorded</b>\n\n")
	default:
		b.WriteString("✅ <b>Transfer recorded</b>\n\n")
	}

	if t.Kind == storage.KindTransfer {
		to := money.FormatCode(t.Amount, t.Decimals, t.AccountCurr)
		if t.ToAmount != nil {
			to = money.FormatCode(*t.ToAmount, t.ToDecimals, t.ToAccountCurr)
		}
		fmt.Fprintf(&b, "🔁 <b>%s</b> → <b>%s</b>\n",
			money.FormatCode(t.Amount, t.Decimals, t.AccountCurr), to)
		fmt.Fprintf(&b, "From: %s %s · %s\nTo: %s %s · %s\n",
			placeEmoji(t.AccountName), esc(t.AccountName), t.AccountCurr,
			placeEmoji(t.ToAccountName), esc(t.ToAccountName), t.ToAccountCurr)
		if t.Rate != nil {
			fmt.Fprintf(&b, "Rate: %s\n", rateLine(t.AccountCurr, t.ToAccountCurr, *t.Rate))
		}
	} else {
		mark, sign := "💸", "−"
		if t.Kind == storage.KindIncome {
			mark, sign = "💰", "+"
		}
		fmt.Fprintf(&b, "%s <b>%s%s</b>\n", mark, sign,
			money.FormatCode(t.Amount, t.Decimals, t.AccountCurr))

		// A converted operation shows both sides, so the figures can be checked
		// against the bank statement.
		if t.Converted() {
			fmt.Fprintf(&b, "Purchase: %s · <i>your rate: %s</i>\n",
				money.FormatCode(*t.OriginalAmount, t.OriginalDecimals, t.OriginalCurrency),
				rateLine(t.OriginalCurrency, t.AccountCurr, *t.Rate))
		}

		cat := t.CategoryName
		if cat == "" {
			cat = "no category"
		} else if t.CategoryEmoji != "" {
			cat = t.CategoryEmoji + " " + cat
		}
		fmt.Fprintf(&b, "Category: %s\n", esc(cat))
		if t.Source != "" {
			fmt.Fprintf(&b, "Source: %s\n", esc(t.Source))
		}
		fmt.Fprintf(&b, "Account: %s %s · %s\n", placeEmoji(t.AccountName), esc(t.AccountName), t.AccountCurr)
	}

	if balance != nil {
		fmt.Fprintf(&b, "Balance: <b>%s</b>\n", money.FormatCode(*balance, t.Decimals, t.AccountCurr))
	}
	if t.Note != "" {
		fmt.Fprintf(&b, "Note: %s\n", esc(t.Note))
	}
	fmt.Fprintf(&b, "<i>%s</i>", t.OccurredAt.Format("02.01.2006 15:04"))

	rows := [][]models.InlineKeyboardButton{
		{
			btn("💰 Amount", fmt.Sprintf("txamt:%d", t.ID)),
			btn("📅 Date", fmt.Sprintf("txdate:%d", t.ID)),
		},
	}
	if t.Kind == storage.KindTransfer {
		rows = append(rows, []models.InlineKeyboardButton{
			btn("💳 From / To", fmt.Sprintf("txacc:%d", t.ID)),
			btn("📝 Note", fmt.Sprintf("txnote:%d", t.ID)),
		})
	} else {
		rows = append(rows,
			[]models.InlineKeyboardButton{
				btn("🏷 Category", fmt.Sprintf("txcat:%d", t.ID)),
				btn("💳 Account", fmt.Sprintf("txacc:%d", t.ID)),
			},
			[]models.InlineKeyboardButton{
				btn("🧑 Source", fmt.Sprintf("txsrc:%d", t.ID)),
				btn("📝 Note", fmt.Sprintf("txnote:%d", t.ID)),
			},
			[]models.InlineKeyboardButton{btn("🔁 Again", fmt.Sprintf("txagain:%d", t.ID))},
		)
	}
	rows = append(rows, []models.InlineKeyboardButton{btn("🗑 Delete", fmt.Sprintf("txdel:%d", t.ID))})
	return b.String(), inline(rows...)
}

// loanTxCard is the card of an operation that belongs to a debt or a loan: what
// moved, on which loan, and the way back to the loan itself. Its category and
// account belong to the loan, so they aren't offered for change here.
func loanTxCard(t storage.Transaction, balance *int64) (string, *models.InlineKeyboardMarkup) {
	var b strings.Builder
	fmt.Fprintf(&b, "✅ <b>%s</b>\n\n", loanOpTitle(t))
	sign, mark := "−", "📤"
	switch t.Kind {
	case storage.KindDebtIn:
		sign, mark = "+", "📥"
	case storage.KindExpense:
		mark = "💸" // a payment that is spending looks like any other expense
	}
	fmt.Fprintf(&b, "%s <b>%s%s</b>\n", mark, sign, money.FormatCode(t.Amount, t.Decimals, t.AccountCurr))
	if t.Interest != nil && *t.Interest > 0 {
		fmt.Fprintf(&b, "Of it interest: %s\n", money.FormatCode(*t.Interest, t.Decimals, t.AccountCurr))
	}
	fmt.Fprintf(&b, "%s %s\n", loanEmoji(t.LoanKind), esc(t.LoanName))
	fmt.Fprintf(&b, "Account: %s %s · %s\n", placeEmoji(t.AccountName), esc(t.AccountName), t.AccountCurr)
	if balance != nil {
		fmt.Fprintf(&b, "Balance: <b>%s</b>\n", money.FormatCode(*balance, t.Decimals, t.AccountCurr))
	}
	if t.Note != "" {
		fmt.Fprintf(&b, "Note: %s\n", esc(t.Note))
	}
	fmt.Fprintf(&b, "<i>%s</i>", t.OccurredAt.Format("02.01.2006 15:04"))

	return b.String(), inline(
		[]models.InlineKeyboardButton{
			btn("💰 Amount", fmt.Sprintf("txamt:%d", t.ID)),
			btn("📅 Date", fmt.Sprintf("txdate:%d", t.ID)),
		},
		[]models.InlineKeyboardButton{
			btn("📝 Note", fmt.Sprintf("txnote:%d", t.ID)),
			btn(loanEmoji(t.LoanKind)+" Open", fmt.Sprintf("loan:%d", *t.LoanID)),
		},
		[]models.InlineKeyboardButton{btn("🗑 Delete", fmt.Sprintf("txdel:%d", t.ID))},
	)
}

// loanOpTitle names what an operation did to its loan.
func loanOpTitle(t storage.Transaction) string {
	switch {
	case t.LoanKind == storage.LoanLent && t.Kind == storage.KindDebtOut:
		return "Lent"
	case t.LoanKind == storage.LoanLent:
		return "Paid back to you"
	case t.Kind == storage.KindDebtIn:
		return "Borrowed"
	case t.Kind == storage.KindExpense && t.Interest != nil && *t.Interest == t.Amount && t.Amount > 0:
		return "Interest paid"
	default:
		return "Payment recorded"
	}
}

// maxOperationButtons caps how many operations get a button of their own.
const maxOperationButtons = 8

// operationsKeyboard opens an operation's card from a list, so anything already
// recorded can still be corrected — not just the one just entered.
func operationsKeyboard(txs []storage.Transaction, loc *time.Location) *models.InlineKeyboardMarkup {
	shown := txs
	if len(shown) > maxOperationButtons {
		shown = shown[:maxOperationButtons]
	}
	buttons := make([]models.InlineKeyboardButton, 0, len(shown))
	for _, t := range shown {
		sign := "−"
		if t.Kind == storage.KindIncome || t.Kind == storage.KindDebtIn {
			sign = "+"
		} else if t.Kind == storage.KindTransfer {
			sign = "🔁"
		}
		label := fmt.Sprintf("%s %s%s", t.OccurredAt.In(loc).Format("02.01"), sign,
			money.FormatCode(t.Amount, t.Decimals, t.AccountCurr))
		buttons = append(buttons, btn(label, fmt.Sprintf("txshow:%d", t.ID)))
	}
	if len(buttons) == 0 {
		return nil
	}
	return inline(grid(buttons, 2)...)
}

func cancelKeyboard() *models.InlineKeyboardMarkup {
	return inline([]models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")})
}

// dateChoices are the offsets, in days back, offered when a recorded operation
// is moved to another date.
var dateChoices = []struct {
	label string
	days  int
}{
	{"Today", 0}, {"Yesterday", 1}, {"2 days ago", 2},
	{"3 days ago", 3}, {"4 days ago", 4}, {"A week ago", 7},
}

// dateKeyboard lets the user move an operation to a recent day without typing one.
func dateKeyboard(txID int64) *models.InlineKeyboardMarkup {
	buttons := make([]models.InlineKeyboardButton, 0, len(dateChoices))
	for _, c := range dateChoices {
		buttons = append(buttons, btn(c.label, fmt.Sprintf("txdate:%d:%d", txID, c.days)))
	}
	rows := grid(buttons, 3)
	rows = append(rows, []models.InlineKeyboardButton{btn("↩️ Back", fmt.Sprintf("txshow:%d", txID))})
	return inline(rows...)
}

// dateDaysAgo is the moment an operation gets when moved n days back. Today keeps
// the current time; earlier days are set to noon, which stays on the right date
// whatever timezone the timestamp is later shown in.
func dateDaysAgo(now time.Time, loc *time.Location, n int) time.Time {
	local := now.In(loc)
	if n == 0 {
		return local
	}
	return time.Date(local.Year(), local.Month(), local.Day()-n, 12, 0, 0, 0, loc)
}

// periodKeyboard offers the recent months and the usual windows as buttons.
func periodKeyboard(now time.Time, loc *time.Location) *models.InlineKeyboardMarkup {
	local := now.In(loc)
	first := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)

	var older []models.InlineKeyboardButton
	for i := 2; i <= 5; i++ {
		m := first.AddDate(0, -i, 0)
		older = append(older, btn(m.Format("Jan 2006"), "rep:"+m.Format("2006-01")))
	}
	rows := [][]models.InlineKeyboardButton{
		{btn("This month", "rep:month"), btn("Last month", "rep:last")},
	}
	rows = append(rows, grid(older, 2)...)
	rows = append(rows, []models.InlineKeyboardButton{
		btn("This week", "rep:week"), btn("Today", "rep:today"), btn("This year", "rep:year"),
	})
	rows = append(rows, []models.InlineKeyboardButton{btn("📅 Custom range", "rng")})
	return inline(rows...)
}
