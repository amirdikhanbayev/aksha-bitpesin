package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/money"
	"aksha-bitpesin/internal/parser"
	"aksha-bitpesin/internal/storage"
)

// Debts and loans. Money lent to or borrowed from people, and what is owed to a
// bank: an installment plan, a credit, a mortgage. The bot keeps count of what
// is left; the user taps through the same few steps as for any operation and
// types only the amounts.
//
// How a payment is counted depends on where the loan's money went — see
// storage.PayLoan. In short: lending and repaying principal move money without
// spending it; an installment or mortgage payment is spending, since the
// purchase itself never went through an account; interest is always spending.

func loanEmoji(kind string) string { return storage.LoanEmoji(kind) }

func loanKindTitle(kind string) string {
	switch kind {
	case storage.LoanLent:
		return "lent"
	case storage.LoanBorrowed:
		return "borrowed"
	case storage.LoanInstallment:
		return "installment plan"
	case storage.LoanMortgage:
		return "mortgage"
	default:
		return "credit"
	}
}

func loanTitle(l storage.Loan) string { return loanEmoji(l.Kind) + " " + l.Name }

func ordinal(n int) string { return parser.Ordinal(n) }

// --- the list ---

// cmdDebts shows every open debt and loan, what is owed each way, and opens any
// of them. With msgID set it replaces that message instead of sending a new one.
func (b *Bot) cmdDebts(ctx context.Context, u storage.User, chatID int64, msgID int) {
	loans, err := b.st.Loans(ctx, u.ID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	var owedToMe, iOwe []storage.Loan
	paidOff := 0
	for _, l := range loans {
		switch {
		case l.PaidOff():
			paidOff++
		case l.OwedToMe():
			owedToMe = append(owedToMe, l)
		default:
			iOwe = append(iOwe, l)
		}
	}

	var sb strings.Builder
	sb.WriteString("🤝 <b>Debts and loans</b>\n")
	if len(owedToMe)+len(iOwe) == 0 {
		sb.WriteString("\nNothing owed either way.\n<i>Lent someone money, borrowed, bought something " +
			"in installments, took a credit or a mortgage? Add it and the bot keeps count of what's left.</i>")
	}
	writeGroup := func(title string, list []storage.Loan) {
		if len(list) == 0 {
			return
		}
		fmt.Fprintf(&sb, "\n<b>%s</b>\n", title)
		totals := map[string]int64{}
		decimals := map[string]int{}
		var order []string
		for _, l := range list {
			fmt.Fprintf(&sb, "• %s — <b>%s</b>", esc(loanTitle(l)),
				money.FormatCode(l.Outstanding(), l.Decimals, l.Currency))
			if l.MonthlyPayment != nil && l.PaymentDay != nil {
				fmt.Fprintf(&sb, " · %s on the %s", money.Format(*l.MonthlyPayment, l.Decimals), ordinal(*l.PaymentDay))
			}
			sb.WriteByte('\n')
			if _, ok := totals[l.Currency]; !ok {
				order = append(order, l.Currency)
				decimals[l.Currency] = l.Decimals
			}
			totals[l.Currency] += l.Outstanding()
		}
		if len(list) > 1 {
			parts := make([]string, 0, len(order))
			for _, c := range order {
				parts = append(parts, money.FormatCode(totals[c], decimals[c], c))
			}
			fmt.Fprintf(&sb, "<i>In total: %s</i>\n", strings.Join(parts, " + "))
		}
	}
	writeGroup("Owed to you", owedToMe)
	writeGroup("You owe", iOwe)
	if len(owedToMe)+len(iOwe) > 0 {
		sb.WriteString("\n<i>Tap one to record a payment, see its history or correct it.</i>")
	}

	var buttons []models.InlineKeyboardButton
	for _, l := range append(owedToMe, iOwe...) {
		buttons = append(buttons, btn(short(loanTitle(l), 28), fmt.Sprintf("loan:%d", l.ID)))
	}
	rows := grid(buttons, 2)
	rows = append(rows, []models.InlineKeyboardButton{btn("➕ New debt or loan", "loannew")})
	if paidOff > 0 {
		rows = append(rows, []models.InlineKeyboardButton{btn(fmt.Sprintf("✅ Paid off (%d)", paidOff), "loanpaid")})
	}
	if msgID > 0 {
		b.edit(ctx, chatID, msgID, sb.String(), inline(rows...))
		return
	}
	b.send(ctx, chatID, sb.String(), inline(rows...))
}

// showPaidOff lists the loans that are settled, so they can still be opened.
func (b *Bot) showPaidOff(ctx context.Context, u storage.User, chatID int64, msgID int) {
	loans, err := b.st.Loans(ctx, u.ID)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	var buttons []models.InlineKeyboardButton
	for _, l := range loans {
		if l.PaidOff() {
			buttons = append(buttons, btn(short(loanTitle(l), 28), fmt.Sprintf("loan:%d", l.ID)))
		}
	}
	rows := grid(buttons, 2)
	rows = append(rows, []models.InlineKeyboardButton{btn("◀️ All debts", "debts")})
	b.edit(ctx, chatID, msgID, "✅ <b>Paid off</b>\nNothing is owed on these any more.", inline(rows...))
}

func short(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// --- adding one ---

func (b *Bot) askLoanKind(ctx context.Context, chatID int64, msgID int) {
	kb := inline(
		[]models.InlineKeyboardButton{btn("🤝 I lent money", "loankind:lent"), btn("🙏 I borrowed", "loankind:borrowed")},
		[]models.InlineKeyboardButton{btn("🛍 Installment plan", "loankind:installment"), btn("🏦 Credit", "loankind:credit")},
		[]models.InlineKeyboardButton{btn("🏠 Mortgage", "loankind:mortgage")},
		[]models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")},
	)
	text := "What kind?"
	if msgID > 0 {
		b.edit(ctx, chatID, msgID, text, kb)
		return
	}
	b.send(ctx, chatID, text, kb)
}

// startLoan asks who or what the loan is with. For debts between people the
// names already used are offered, since it is usually the same few people.
func (b *Bot) startLoan(ctx context.Context, u storage.User, chatID int64, kind string) {
	d := draft{LoanKind: kind}
	question := map[string]string{
		storage.LoanLent:        "Who did you lend to?",
		storage.LoanBorrowed:    "Who did you borrow from?",
		storage.LoanInstallment: "What was bought, and where? For example: <code>Kaspi · iPhone</code>",
		storage.LoanCredit:      "Which bank, and what for? For example: <code>Halyk · car</code>",
		storage.LoanMortgage:    "Which bank? For example: <code>Otbasy</code>",
	}[kind]

	var rows [][]models.InlineKeyboardButton
	if kind == storage.LoanLent || kind == storage.LoanBorrowed {
		if loans, err := b.st.Loans(ctx, u.ID); err == nil {
			for _, l := range loans {
				if !l.FromBank() && !contains(d.Sources, l.Name) && len(d.Sources) < 6 {
					d.Sources = append(d.Sources, l.Name)
				}
			}
		}
		var names []models.InlineKeyboardButton
		for i, name := range d.Sources {
			names = append(names, btn(short(name, 24), fmt.Sprintf("loanname:i%d", i)))
		}
		rows = grid(names, 2)
		if len(names) > 0 {
			question += " Pick one, or type a name:"
		}
	}
	rows = append(rows, []models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")})
	b.setState(ctx, u.ID, stateLoanName, d)
	b.send(ctx, chatID, loanEmoji(kind)+" "+question, inline(rows...))
}

func (b *Bot) inputLoanName(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	name := strings.TrimSpace(text)
	if name == "" || len([]rune(name)) > 60 {
		b.send(ctx, chatID, "A name — up to 60 characters.", cancelKeyboard())
		return
	}
	d.LoanName = name
	d.Sources = nil
	b.askLoanAccount(ctx, u, chatID, d)
}

// askLoanAccount asks which account the money came out of or landed on. An
// installment plan or a mortgage pays the shop or the seller directly, so it
// is not asked; a credit may go either way.
func (b *Bot) askLoanAccount(ctx context.Context, u storage.User, chatID int64, d draft) {
	if d.LoanKind == storage.LoanInstallment || d.LoanKind == storage.LoanMortgage {
		b.askLoanCurrency(ctx, u, chatID, d)
		return
	}
	accs, err := b.st.ListAccounts(ctx, u.ID, false)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if len(accs) == 0 {
		b.askLoanCurrency(ctx, u, chatID, d)
		return
	}
	question, none := "Which account did the money come from?", "Not from my accounts"
	switch d.LoanKind {
	case storage.LoanBorrowed:
		question, none = "Which account did the money land on?", "Not onto my accounts"
	case storage.LoanCredit:
		question, none = "Did the money land on one of your accounts?\n"+
			"<i>A credit that paid for a purchase directly didn't.</i>", "No — it paid for a purchase"
	}
	b.setState(ctx, u.ID, stateLoanAccount, d)
	b.send(ctx, chatID, question, accountsKeyboard(accs, "loanacc",
		btn(none, "loanacc:0"), btn("✖️ Cancel", "cancel")))
}

// askLoanCurrency asks the loan's currency. Payments are recorded on an account
// in that currency, so only the currencies the accounts hold are offered — and
// when they all hold one, there is nothing to ask.
func (b *Bot) askLoanCurrency(ctx context.Context, u storage.User, chatID int64, d draft) {
	d.AccountID = 0
	var held []string
	if accs, err := b.st.ListAccounts(ctx, u.ID, false); err == nil {
		for _, a := range accs {
			if !contains(held, a.Currency) {
				held = append(held, a.Currency)
			}
		}
	}
	switch len(held) {
	case 0:
		b.setState(ctx, u.ID, stateLoanCurrency, d)
		b.send(ctx, chatID, "Which currency is it in?", currencyKeyboard(u.MainCurrency, "loancur"))
	case 1:
		d.Currency = held[0]
		b.askLoanAmount(ctx, u, chatID, d)
	default:
		buttons := make([]models.InlineKeyboardButton, 0, len(held))
		for _, c := range held {
			buttons = append(buttons, btn(c, "loancur:"+c))
		}
		rows := grid(buttons, 3)
		rows = append(rows, []models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")})
		b.setState(ctx, u.ID, stateLoanCurrency, d)
		b.send(ctx, chatID, "Which currency is it in?", inline(rows...))
	}
}

func (b *Bot) askLoanAmount(ctx context.Context, u storage.User, chatID int64, d draft) {
	b.setState(ctx, u.ID, stateLoanAmount, d)
	question := map[string]string{
		storage.LoanLent:     "How much did you lend?",
		storage.LoanBorrowed: "How much did you borrow?",
	}[d.LoanKind]
	if question == "" {
		question = "How much is owed now?\n<i>Paying it for a while already? Send what is left — the bank's app shows it.</i>"
	}
	b.send(ctx, chatID, fmt.Sprintf("%s Send the amount in <b>%s</b>.", question, d.Currency), cancelKeyboard())
}

func (b *Bot) inputLoanAmount(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	cur, err := b.st.Currency(ctx, d.Currency)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	amount, err := money.Parse(text, cur.Decimals)
	if err != nil || amount <= 0 {
		b.send(ctx, chatID, fmt.Sprintf("Send the amount in <b>%s</b> as a number, for example <code>50000</code>.",
			d.Currency), cancelKeyboard())
		return
	}
	d.Amount = amount
	if d.LoanKind == storage.LoanLent || d.LoanKind == storage.LoanBorrowed {
		b.createLoan(ctx, u, chatID, d)
		return
	}
	b.setState(ctx, u.ID, stateLoanMonthly, d)
	b.send(ctx, chatID, "What is the monthly payment? Send the amount, or skip it if it varies.",
		inline([]models.InlineKeyboardButton{btn("⏭ Skip", "loanmonthly:skip"), btn("✖️ Cancel", "cancel")}))
}

func (b *Bot) inputLoanMonthly(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	if isSkip(text) {
		b.createLoan(ctx, u, chatID, d)
		return
	}
	cur, err := b.st.Currency(ctx, d.Currency)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	monthly, err := money.Parse(text, cur.Decimals)
	if err != nil || monthly <= 0 {
		b.send(ctx, chatID, "Send the monthly payment as a number, or skip it.",
			inline([]models.InlineKeyboardButton{btn("⏭ Skip", "loanmonthly:skip"), btn("✖️ Cancel", "cancel")}))
		return
	}
	d.Monthly = &monthly
	b.setState(ctx, u.ID, stateLoanDay, d)
	b.send(ctx, chatID, "On which day of the month is it paid?\n<i>I'll remind you that day if it isn't recorded yet.</i>",
		loanDayKeyboard())
}

func loanDayKeyboard() *models.InlineKeyboardMarkup {
	buttons := make([]models.InlineKeyboardButton, 0, 31)
	for day := 1; day <= 31; day++ {
		buttons = append(buttons, btn(strconv.Itoa(day), "loanday:"+strconv.Itoa(day)))
	}
	rows := grid(buttons, 7)
	rows = append(rows, []models.InlineKeyboardButton{btn("⏭ No set day", "loanday:skip"), btn("✖️ Cancel", "cancel")})
	return inline(rows...)
}

func (b *Bot) inputLoanDay(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	if isSkip(text) {
		b.createLoan(ctx, u, chatID, d)
		return
	}
	day, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || day < 1 || day > 31 {
		b.send(ctx, chatID, "Pick a day with the buttons.", loanDayKeyboard())
		return
	}
	d.DayOfMonth = day
	b.createLoan(ctx, u, chatID, d)
}

func (b *Bot) createLoan(ctx context.Context, u storage.User, chatID int64, d draft) {
	l := storage.Loan{
		UserID:         u.ID,
		Kind:           d.LoanKind,
		Name:           d.LoanName,
		Currency:       d.Currency,
		Principal:      d.Amount,
		MonthlyPayment: d.Monthly,
		OpenedAt:       b.clock().In(u.Location()),
	}
	if d.AccountID != 0 {
		l.AccountID = &d.AccountID
	}
	if d.DayOfMonth > 0 {
		l.PaymentDay = &d.DayOfMonth
	}
	saved, err := b.st.CreateLoan(ctx, l)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.clearState(ctx, u.ID)
	text, kb := b.loanView(ctx, u, saved)
	b.send(ctx, chatID, "✅ <b>Added</b>\n\n"+text+"\n\n"+loanHowTo(saved), kb)
}

// loanHowTo says, once, how the loan is used from here on — the part that is
// easy to forget by the next payment.
func loanHowTo(l storage.Loan) string {
	if l.OwedToMe() {
		return "💡 <i>When money comes back, tap 💰 Got money back here — or record it as a usual " +
			"➕ Income and pick 🤝 It's a debt paid back to me.</i>"
	}
	text := "💡 <i>When you pay, open it in 🤝 Debts and tap 💳 Make a payment — or record a usual " +
		"➖ Expense and pick 🏦 It's a loan or debt payment instead of a category."
	if l.PaymentDay != nil {
		text += " I'll remind you on the " + ordinal(*l.PaymentDay) + " if it isn't recorded by then."
	}
	return text + "</i>"
}

// --- one loan ---

func (b *Bot) showLoan(ctx context.Context, u storage.User, chatID int64, msgID int, id int64) {
	l, err := b.st.Loan(ctx, u.ID, id)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	text, kb := b.loanView(ctx, u, l)
	if msgID > 0 {
		b.edit(ctx, chatID, msgID, text, kb)
		return
	}
	b.send(ctx, chatID, text, kb)
}

// loanView is a loan's card: what is owed, how much has been paid, the schedule,
// and the buttons to pay, look back, correct or delete.
func (b *Bot) loanView(ctx context.Context, u storage.User, l storage.Loan) (string, *models.InlineKeyboardMarkup) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s <b>%s</b> · %s\n\n", loanEmoji(l.Kind), esc(l.Name), loanKindTitle(l.Kind))

	owed, paidWord := "You owe", "Paid so far"
	switch l.Kind {
	case storage.LoanLent:
		owed, paidWord = "Owes you", "Paid back to you"
	case storage.LoanBorrowed:
		paidWord = "Paid back"
	}
	if l.PaidOff() {
		sb.WriteString("✅ <b>Paid off</b>\n")
	} else {
		fmt.Fprintf(&sb, "%s: <b>%s</b> of %s\n", owed,
			money.FormatCode(l.Outstanding(), l.Decimals, l.Currency), money.Format(l.Principal, l.Decimals))
		pct := int(l.Repaid * 100 / l.Principal)
		fmt.Fprintf(&sb, "%s %d%% paid\n", progress(pct), pct)
	}
	if l.Payments > 0 || l.PaidTotal > 0 {
		fmt.Fprintf(&sb, "%s: %s", paidWord, money.FormatCode(l.PaidTotal, l.Decimals, l.Currency))
		if l.InterestPaid > 0 {
			fmt.Fprintf(&sb, " · of it interest %s", money.Format(l.InterestPaid, l.Decimals))
		}
		sb.WriteByte('\n')
	}
	if l.MonthlyPayment != nil && !l.PaidOff() {
		fmt.Fprintf(&sb, "Payment: %s", money.FormatCode(*l.MonthlyPayment, l.Decimals, l.Currency))
		if l.PaymentDay != nil {
			fmt.Fprintf(&sb, " on the %s", ordinal(*l.PaymentDay))
		}
		// Dividing what is owed by the payment only counts the payments when
		// none of them goes to interest; with interest it would promise far
		// fewer than the bank's schedule has.
		if *l.MonthlyPayment > 0 && l.InterestPaid == 0 && l.Kind != storage.LoanMortgage {
			left := (l.Outstanding() + *l.MonthlyPayment - 1) / *l.MonthlyPayment
			fmt.Fprintf(&sb, " · about %d left", left)
		}
		sb.WriteByte('\n')
	}
	loc := u.Location()
	if l.LastPayment != nil {
		fmt.Fprintf(&sb, "Last payment: %s\n", l.LastPayment.In(loc).Format("02.01.2006"))
	}
	fmt.Fprintf(&sb, "<i>Since %s", l.OpenedAt.In(loc).Format("02.01.2006"))
	if l.AccountID != nil {
		if acc, err := b.st.Account(ctx, u.ID, *l.AccountID); err == nil {
			fmt.Fprintf(&sb, " · %s %s", map[bool]string{true: "from", false: "onto"}[l.OwedToMe()], esc(accountLabel(acc)))
		}
	}
	sb.WriteString("</i>")

	var rows [][]models.InlineKeyboardButton
	if !l.PaidOff() {
		pay := "💳 Make a payment"
		switch l.Kind {
		case storage.LoanLent:
			pay = "💰 Got money back"
		case storage.LoanBorrowed:
			pay = "💸 Pay back"
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(pay, fmt.Sprintf("loanpay:%d", l.ID))})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{
			btn("🧾 History", fmt.Sprintf("loanhist:%d", l.ID)),
			btn("✏️ Correct what's owed", fmt.Sprintf("loanfix:%d", l.ID)),
		},
		[]models.InlineKeyboardButton{
			btn("🗑 Delete", fmt.Sprintf("loandel:%d", l.ID)),
			btn("◀️ All debts", "debts"),
		})
	return sb.String(), inline(rows...)
}

func progress(pct int) string {
	pct = max(0, min(pct, 100))
	filled := pct / 10
	return strings.Repeat("▓", filled) + strings.Repeat("░", 10-filled)
}

// --- paying ---

// startLoanPayment asks which account the payment went through — only accounts
// in the loan's currency, since a loan's figures add up in one currency.
func (b *Bot) startLoanPayment(ctx context.Context, u storage.User, chatID int64, id int64) {
	l, err := b.st.Loan(ctx, u.ID, id)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	accs, err := b.st.ListAccounts(ctx, u.ID, false)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	// The account the last payment went through comes first: it is usually the same one.
	lastAcc := int64(0)
	if txs, err := b.st.LoanTransactions(ctx, u.ID, l.ID, 1); err == nil && len(txs) > 0 {
		lastAcc = txs[0].AccountID
	}
	var options []storage.Account
	for _, a := range accs {
		if a.Currency != l.Currency {
			continue
		}
		if a.ID == lastAcc {
			options = append([]storage.Account{a}, options...)
		} else {
			options = append(options, a)
		}
	}
	d := draft{LoanID: l.ID, Currency: l.Currency}
	switch len(options) {
	case 0:
		b.send(ctx, chatID, fmt.Sprintf("There is no account in <b>%s</b> to record it on. Add one: /newaccount",
			l.Currency), nil)
	case 1:
		b.askLoanPayAmount(ctx, u, chatID, d, l, options[0])
	default:
		question := "Which account was it paid from?"
		if l.OwedToMe() {
			question = "Which account did the money come to?"
		}
		b.setState(ctx, u.ID, stateLoanPayAcc, d)
		b.send(ctx, chatID, question, accountsKeyboard(options, "loanpayacc", btn("✖️ Cancel", "cancel")))
	}
}

func (b *Bot) askLoanPayAmount(ctx context.Context, u storage.User, chatID int64, d draft,
	l storage.Loan, acc storage.Account) {

	d.AccountID = acc.ID
	b.setState(ctx, u.ID, stateLoanPay, d)

	question := "How much was paid?"
	if l.OwedToMe() {
		question = "How much was paid back?"
	}
	var quick []models.InlineKeyboardButton
	if l.MonthlyPayment != nil {
		quick = append(quick, btn(money.FormatCode(*l.MonthlyPayment, l.Decimals, l.Currency), "loanamt:monthly"))
	}
	if l.Outstanding() > 0 && (l.MonthlyPayment == nil || *l.MonthlyPayment != l.Outstanding()) {
		quick = append(quick, btn("All: "+money.Format(l.Outstanding(), l.Decimals), "loanamt:all"))
	}
	rows := [][]models.InlineKeyboardButton{}
	if len(quick) > 0 {
		rows = append(rows, quick)
	}
	rows = append(rows, []models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")})
	b.send(ctx, chatID, fmt.Sprintf("%s · %s\n%s: %s\n\n%s Send the amount in <b>%s</b>.",
		esc(loanTitle(l)), esc(accountLabel(acc)),
		map[bool]string{true: "Owes you", false: "Owed"}[l.OwedToMe()],
		money.FormatCode(l.Outstanding(), l.Decimals, l.Currency), question, l.Currency), inline(rows...))
}

func (b *Bot) inputLoanPay(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	l, err := b.st.Loan(ctx, u.ID, d.LoanID)
	if err != nil {
		b.clearState(ctx, u.ID)
		b.fail(ctx, chatID, err)
		return
	}
	amount, err := money.Parse(text, l.Decimals)
	if err != nil || amount <= 0 {
		b.send(ctx, chatID, fmt.Sprintf("Send the amount in <b>%s</b> as a number.", l.Currency), cancelKeyboard())
		return
	}
	b.loanPayAmount(ctx, u, chatID, d, l, amount)
}

// loanPayAmount goes on from the amount: a bank payment asks how much of it was
// interest — the bank knows, the bot doesn't guess — and anything else is
// recorded straight away.
func (b *Bot) loanPayAmount(ctx context.Context, u storage.User, chatID int64, d draft, l storage.Loan, amount int64) {
	d.Amount = amount
	if !l.FromBank() {
		b.recordLoanPayment(ctx, u, chatID, d, 0)
		return
	}
	b.setState(ctx, u.ID, stateLoanInterest, d)
	row := []models.InlineKeyboardButton{btn("No interest", "loanint:0")}
	if last := b.lastInterest(ctx, u, l.ID); last > 0 && last <= amount {
		row = append(row, btn("Same as last: "+money.Format(last, l.Decimals), "loanint:last"))
	}
	b.send(ctx, chatID, fmt.Sprintf(
		"Paying <b>%s</b>. How much of it was interest?\n<i>The bank's app shows the split. The rest pays the debt down.</i>",
		money.FormatCode(amount, l.Decimals, l.Currency)),
		inline(row, []models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")}))
}

// lastInterest is the interest of the loan's latest payment, offered as a
// button: a mortgage's interest changes little from one month to the next.
func (b *Bot) lastInterest(ctx context.Context, u storage.User, loanID int64) int64 {
	txs, err := b.st.LoanTransactions(ctx, u.ID, loanID, 5)
	if err != nil {
		return 0
	}
	for _, t := range txs {
		if t.Interest != nil {
			return *t.Interest
		}
	}
	return 0
}

func (b *Bot) inputLoanInterest(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	if isSkip(text) {
		b.recordLoanPayment(ctx, u, chatID, d, 0)
		return
	}
	cur, err := b.st.Currency(ctx, d.Currency)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	interest, err := money.Parse(text, cur.Decimals)
	if err != nil || interest < 0 || interest > d.Amount {
		b.send(ctx, chatID, "Send the interest as a number, no more than the payment itself.",
			inline([]models.InlineKeyboardButton{btn("No interest", "loanint:0"), btn("✖️ Cancel", "cancel")}))
		return
	}
	b.recordLoanPayment(ctx, u, chatID, d, interest)
}

func (b *Bot) recordLoanPayment(ctx context.Context, u storage.User, chatID int64, d draft, interest int64) {
	t, err := b.st.PayLoan(ctx, u.ID, d.LoanID, d.AccountID, d.Amount, interest, b.clock().In(u.Location()))
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.clearState(ctx, u.ID)

	text, kb := b.cardOf(ctx, u, t)
	l, err := b.st.Loan(ctx, u.ID, d.LoanID)
	if err == nil && interest > 0 && l.ThroughAccount() {
		// Recorded as two operations — the principal and the interest — and the
		// card shows one, so say what the whole payment was.
		text = fmt.Sprintf("💳 Paid <b>%s</b>: %s off the debt, %s interest\n\n",
			money.FormatCode(d.Amount, l.Decimals, l.Currency),
			money.Format(d.Amount-interest, l.Decimals), money.Format(interest, l.Decimals)) + text
	}
	if err == nil {
		switch {
		case l.PaidOff() && l.OwedToMe():
			text += "\n\n🎉 <b>All paid back.</b>"
		case l.PaidOff():
			text += "\n\n🎉 <b>Paid off — nothing owed any more.</b>"
		default:
			text += fmt.Sprintf("\n\n%s: <b>%s</b>",
				map[bool]string{true: "Still owes you", false: "Still owed"}[l.OwedToMe()],
				money.FormatCode(l.Outstanding(), l.Decimals, l.Currency))
		}
	}
	b.send(ctx, chatID, text, kb)
}

// --- from the everyday entry ---

// Out of habit an installment gets paid through ➖ Expense and a debt paid back
// arrives through ➕ Income. Once the amount is in, the category step offers to
// file it on the loan instead, so the count of what's owed stays right without
// having to remember where loans live.

// loanCandidates are the open loans an amount being entered could be paying:
// owed by the user for an expense, owed to them for income, in the account's
// currency.
func (b *Bot) loanCandidates(ctx context.Context, u storage.User, d draft) []storage.Loan {
	if d.Amount <= 0 || d.AccountID == 0 || d.TxID != 0 {
		return nil
	}
	acc, err := b.st.Account(ctx, u.ID, d.AccountID)
	if err != nil || (d.Currency != "" && d.Currency != acc.Currency) {
		return nil
	}
	loans, err := b.st.Loans(ctx, u.ID)
	if err != nil {
		return nil
	}
	var out []storage.Loan
	for _, l := range loans {
		if l.PaidOff() || l.Currency != acc.Currency || l.OwedToMe() != (d.Kind == storage.KindIncome) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// loanShortcut is the button row offering that, or nil when there is nothing to pay.
func (b *Bot) loanShortcut(ctx context.Context, u storage.User, d draft) []models.InlineKeyboardButton {
	cands := b.loanCandidates(ctx, u, d)
	if len(cands) == 0 {
		return nil
	}
	label := "🏦 It's a loan or debt payment"
	if d.Kind == storage.KindIncome {
		label = "🤝 It's a debt paid back to me"
	}
	if len(cands) == 1 {
		label += " — " + short(cands[0].Name, 20)
	}
	return []models.InlineKeyboardButton{btn(label, "entloan")}
}

// payFromEntry turns the amount being entered into a payment on the loan.
func (b *Bot) payFromEntry(ctx context.Context, u storage.User, chatID int64, msgID int, d draft, l storage.Loan) {
	b.edit(ctx, chatID, msgID, "Paying: <b>"+esc(loanTitle(l))+"</b>", nil)
	b.loanPayAmount(ctx, u, chatID, draft{LoanID: l.ID, AccountID: d.AccountID, Currency: l.Currency}, l, d.Amount)
}

// --- history, correction, deletion ---

func (b *Bot) showLoanHistory(ctx context.Context, u storage.User, chatID int64, id int64) {
	l, err := b.st.Loan(ctx, u.ID, id)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	txs, err := b.st.LoanTransactions(ctx, u.ID, id, 50)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	back := []models.InlineKeyboardButton{btn("◀️ Back", fmt.Sprintf("loan:%d", id))}
	if len(txs) == 0 {
		b.send(ctx, chatID, esc(loanTitle(l))+"\nNothing recorded on it yet.", inline(back))
		return
	}
	loc := u.Location()
	var sb strings.Builder
	fmt.Fprintf(&sb, "🧾 <b>%s</b>\n\n", esc(loanTitle(l)))
	for _, t := range txs {
		sb.WriteString(txLine(t, loc))
		sb.WriteByte('\n')
	}
	kb := operationsKeyboard(txs, loc)
	kb.InlineKeyboard = append(kb.InlineKeyboard, back)
	b.sendLong(ctx, chatID, sb.String(), kb)
}

func (b *Bot) askLoanFix(ctx context.Context, u storage.User, chatID int64, id int64) {
	l, err := b.st.Loan(ctx, u.ID, id)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.setState(ctx, u.ID, stateLoanFix, draft{LoanID: id, Currency: l.Currency})
	b.send(ctx, chatID, fmt.Sprintf(
		"I count <b>%s</b> owed. How much is it really?\n<i>The payments stay as recorded; only the starting amount moves.</i>",
		money.FormatCode(l.Outstanding(), l.Decimals, l.Currency)), cancelKeyboard())
}

func (b *Bot) inputLoanFix(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	cur, err := b.st.Currency(ctx, d.Currency)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	amount, err := money.Parse(text, cur.Decimals)
	if err != nil || amount < 0 {
		b.send(ctx, chatID, "Send the amount as a number.", cancelKeyboard())
		return
	}
	if err := b.st.SetLoanOutstanding(ctx, u.ID, d.LoanID, amount); err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.clearState(ctx, u.ID)
	b.showLoan(ctx, u, chatID, 0, d.LoanID)
}

func (b *Bot) askLoanDelete(ctx context.Context, u storage.User, chatID int64, msgID int, id int64) {
	l, err := b.st.Loan(ctx, u.ID, id)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	txs, _ := b.st.LoanTransactions(ctx, u.ID, id, 1000)
	text := fmt.Sprintf("Delete <b>%s</b>?", esc(loanTitle(l)))
	if len(txs) > 0 {
		text += fmt.Sprintf("\nIts %d %s go with it, and the balances go back as if it was never recorded.",
			len(txs), plural(len(txs), "operation"))
	}
	b.edit(ctx, chatID, msgID, text, inline([]models.InlineKeyboardButton{
		btn("🗑 Yes, delete", fmt.Sprintf("loandelyes:%d", id)),
		btn("◀️ Back", fmt.Sprintf("loan:%d", id)),
	}))
}

func (b *Bot) deleteLoan(ctx context.Context, u storage.User, chatID int64, msgID int, id int64) {
	if err := b.st.DeleteLoan(ctx, u.ID, id); err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.edit(ctx, chatID, msgID, "🗑 Deleted.", nil)
	b.cmdDebts(ctx, u, chatID, 0)
}

// handleLoanCallback serves every loan button; it reports false for an action
// that isn't one of them.
func (b *Bot) handleLoanCallback(ctx context.Context, u storage.User, q *models.CallbackQuery,
	action, arg string, d draft) bool {

	chatID := q.Message.Message.Chat.ID
	msgID := q.Message.Message.ID
	id, idErr := parseID(arg)

	switch action {
	case "debts":
		b.answer(ctx, q, "")
		b.cmdDebts(ctx, u, chatID, msgID)
	case "loanpaid":
		b.answer(ctx, q, "")
		b.showPaidOff(ctx, u, chatID, msgID)
	case "loannew":
		b.answer(ctx, q, "")
		b.askLoanKind(ctx, chatID, msgID)
	case "loankind":
		if !contains(storage.LoanKinds, arg) {
			b.answer(ctx, q, "Unknown choice")
			return true
		}
		b.answer(ctx, q, "")
		b.edit(ctx, chatID, msgID, loanEmoji(arg)+" "+strings.ToUpper(loanKindTitle(arg)[:1])+loanKindTitle(arg)[1:], nil)
		b.startLoan(ctx, u, chatID, arg)
	case "loanname":
		idx, err := strconv.Atoi(strings.TrimPrefix(arg, "i"))
		if err != nil || idx < 0 || idx >= len(d.Sources) {
			b.answer(ctx, q, "Unknown choice")
			return true
		}
		b.answer(ctx, q, "")
		b.edit(ctx, chatID, msgID, "Name: <b>"+esc(d.Sources[idx])+"</b>", nil)
		b.inputLoanName(ctx, u, chatID, d, d.Sources[idx])
	case "loanacc":
		if idErr != nil {
			b.answer(ctx, q, "Unknown choice")
			return true
		}
		b.answer(ctx, q, "")
		if id == 0 {
			b.edit(ctx, chatID, msgID, "Account: <i>none</i>", nil)
			b.askLoanCurrency(ctx, u, chatID, d)
			return true
		}
		acc, err := b.st.Account(ctx, u.ID, id)
		if err != nil {
			b.fail(ctx, chatID, err)
			return true
		}
		b.edit(ctx, chatID, msgID, "Account: <b>"+esc(accountLabel(acc))+"</b>", nil)
		d.AccountID, d.Currency = acc.ID, acc.Currency
		b.askLoanAmount(ctx, u, chatID, d)
	case "loancur":
		if _, err := b.st.Currency(ctx, arg); err != nil {
			b.answer(ctx, q, "Unknown currency")
			return true
		}
		b.answer(ctx, q, arg)
		b.edit(ctx, chatID, msgID, "Currency: <b>"+esc(arg)+"</b>", nil)
		d.Currency = arg
		b.askLoanAmount(ctx, u, chatID, d)
	case "loanmonthly":
		b.answer(ctx, q, "")
		b.edit(ctx, chatID, msgID, "Monthly payment: <i>varies</i>", nil)
		b.createLoan(ctx, u, chatID, d)
	case "loanday":
		b.answer(ctx, q, "")
		if arg == "skip" {
			b.edit(ctx, chatID, msgID, "Payment day: <i>none</i>", nil)
			b.createLoan(ctx, u, chatID, d)
			return true
		}
		day, err := strconv.Atoi(arg)
		if err != nil || day < 1 || day > 31 {
			return true
		}
		b.edit(ctx, chatID, msgID, "Payment day: <b>"+ordinal(day)+"</b>", nil)
		d.DayOfMonth = day
		b.createLoan(ctx, u, chatID, d)
	case "entloan":
		cands := b.loanCandidates(ctx, u, d)
		if len(cands) == 0 {
			b.answer(ctx, q, "Nothing to pay")
			return true
		}
		b.answer(ctx, q, "")
		if len(cands) == 1 {
			b.payFromEntry(ctx, u, chatID, msgID, d, cands[0])
			return true
		}
		var buttons []models.InlineKeyboardButton
		for _, l := range cands {
			buttons = append(buttons, btn(short(loanTitle(l), 28), fmt.Sprintf("entloanpick:%d", l.ID)))
		}
		rows := grid(buttons, 2)
		rows = append(rows, []models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")})
		b.edit(ctx, chatID, msgID, "Which one does it pay?", inline(rows...))
	case "entloanpick":
		for _, l := range b.loanCandidates(ctx, u, d) {
			if l.ID == id {
				b.answer(ctx, q, "")
				b.payFromEntry(ctx, u, chatID, msgID, d, l)
				return true
			}
		}
		b.answer(ctx, q, "Loan not found")
	case "loan":
		if idErr != nil {
			b.answer(ctx, q, "Unknown choice")
			return true
		}
		b.answer(ctx, q, "")
		b.showLoan(ctx, u, chatID, msgID, id)
	case "loanpay":
		if idErr != nil {
			b.answer(ctx, q, "Unknown choice")
			return true
		}
		b.answer(ctx, q, "")
		b.startLoanPayment(ctx, u, chatID, id)
	case "loanpayacc":
		l, err := b.st.Loan(ctx, u.ID, d.LoanID)
		if err != nil {
			b.answer(ctx, q, "Loan not found")
			return true
		}
		acc, err := b.st.Account(ctx, u.ID, id)
		if idErr != nil || err != nil || acc.Currency != l.Currency {
			b.answer(ctx, q, "Account not found")
			return true
		}
		b.answer(ctx, q, "")
		b.edit(ctx, chatID, msgID, "Account: <b>"+esc(accountLabel(acc))+"</b>", nil)
		b.askLoanPayAmount(ctx, u, chatID, d, l, acc)
	case "loanamt":
		l, err := b.st.Loan(ctx, u.ID, d.LoanID)
		if err != nil {
			b.answer(ctx, q, "Loan not found")
			return true
		}
		amount := l.Outstanding()
		if arg == "monthly" && l.MonthlyPayment != nil {
			amount = *l.MonthlyPayment
		}
		if amount <= 0 {
			b.answer(ctx, q, "Nothing is owed")
			return true
		}
		b.answer(ctx, q, "")
		b.edit(ctx, chatID, msgID, "Amount: <b>"+money.FormatCode(amount, l.Decimals, l.Currency)+"</b>", nil)
		b.loanPayAmount(ctx, u, chatID, d, l, amount)
	case "loanint":
		b.answer(ctx, q, "")
		if arg == "last" {
			l, err := b.st.Loan(ctx, u.ID, d.LoanID)
			if last := b.lastInterest(ctx, u, d.LoanID); err == nil && last > 0 && last <= d.Amount {
				b.edit(ctx, chatID, msgID, "Interest: <b>"+money.FormatCode(last, l.Decimals, l.Currency)+"</b>", nil)
				b.recordLoanPayment(ctx, u, chatID, d, last)
				return true
			}
		}
		b.edit(ctx, chatID, msgID, "Interest: <i>none</i>", nil)
		b.recordLoanPayment(ctx, u, chatID, d, 0)
	case "loanhist":
		if idErr != nil {
			b.answer(ctx, q, "Unknown choice")
			return true
		}
		b.answer(ctx, q, "")
		b.showLoanHistory(ctx, u, chatID, id)
	case "loanfix":
		if idErr != nil {
			b.answer(ctx, q, "Unknown choice")
			return true
		}
		b.answer(ctx, q, "")
		b.askLoanFix(ctx, u, chatID, id)
	case "loandel":
		if idErr != nil {
			b.answer(ctx, q, "Unknown choice")
			return true
		}
		b.answer(ctx, q, "")
		b.askLoanDelete(ctx, u, chatID, msgID, id)
	case "loandelyes":
		if idErr != nil {
			b.answer(ctx, q, "Unknown choice")
			return true
		}
		b.answer(ctx, q, "Deleted")
		b.deleteLoan(ctx, u, chatID, msgID, id)
	default:
		return false
	}
	return true
}
