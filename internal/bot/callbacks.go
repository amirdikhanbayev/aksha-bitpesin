package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"aksha-bitpesin/internal/parser"
	"aksha-bitpesin/internal/report"
	"aksha-bitpesin/internal/storage"
)

func (b *Bot) handleCallback(ctx context.Context, u storage.User, q *models.CallbackQuery) {
	chatID := q.Message.Message.Chat.ID
	msgID := q.Message.Message.ID
	data := q.Data
	action, arg, _ := strings.Cut(data, ":")

	state, raw, err := b.st.LoadState(ctx, u.ID)
	if err != nil {
		slogError("LoadState", err)
	}
	d := parseDraft(raw)

	// A wizard button only means something on the step that showed it. Tapped
	// again after the operation was saved, or on an old message while another
	// dialog is open, it would act on a draft that isn't its own.
	if want, ok := wizardSteps[action]; ok && !contains(want, state) {
		b.answer(ctx, q, "This button is out of date")
		return
	}

	if b.handleLoanCallback(ctx, u, q, action, arg, d) {
		return
	}

	switch action {
	case "cancel":
		b.clearState(ctx, u.ID)
		b.answer(ctx, q, "Canceled")
		b.edit(ctx, chatID, msgID, "✖️ Canceled", nil)

	case "kind":
		b.answer(ctx, q, "")
		switch arg {
		case "transfer":
			b.cmdTransfer(ctx, u, chatID)
		default:
			b.startEntry(ctx, u, chatID, arg)
		}

	// --- account selection in the operation wizard ---
	// --- erasing everything ---
	case "erase":
		b.answer(ctx, q, "")
		switch arg {
		case "confirm":
			b.confirmErase(ctx, u, chatID, msgID)
		case "yes":
			b.eraseNow(ctx, u, chatID, msgID)
		default:
			b.askErase(ctx, u, chatID, msgID)
		}

	// --- first-run setup ---
	case "obtz":
		if !validZone(arg) {
			b.answer(ctx, q, "Unknown timezone")
			return
		}
		if err := b.st.SetTimezone(ctx, u.ID, arg); err != nil {
			b.answer(ctx, q, "Didn't work")
			return
		}
		b.answer(ctx, q, arg)
		b.edit(ctx, chatID, msgID, "Timezone: <b>"+esc(arg)+"</b>", nil)
		b.askOnboardingCurrency(ctx, u, chatID)

	case "obcur":
		if _, err := b.st.Currency(ctx, arg); err != nil {
			b.answer(ctx, q, "Unknown currency")
			return
		}
		if err := b.st.SetMainCurrency(ctx, u.ID, arg); err != nil {
			b.answer(ctx, q, "Didn't work")
			return
		}
		u.MainCurrency = arg
		b.answer(ctx, q, arg)
		b.edit(ctx, chatID, msgID, "Main currency: <b>"+esc(arg)+"</b>", nil)
		b.askOnboardingPlaces(ctx, u, chatID)

	case "obplace":
		b.answer(ctx, q, "")
		switch {
		case arg == "done":
			if len(d.Queue) == 0 {
				b.answer(ctx, q, "Pick at least one place, or skip")
				return
			}
			d.Queue = orderedBy(d.Sources, d.Queue)
			d.Sources = nil
			b.edit(ctx, chatID, msgID, "Places: <b>"+esc(strings.Join(d.Queue, ", "))+"</b>", nil)
			b.askPlaceCurrencies(ctx, u, chatID, 0, d)
		case arg == "skip":
			b.edit(ctx, chatID, msgID, "Places: skipped", nil)
			b.finishOnboarding(ctx, u, chatID)
		case arg == "other":
			b.setState(ctx, u.ID, stateOnboardName, d)
			b.edit(ctx, chatID, msgID,
				"Type a name for the place — a bank, a wallet, whatever you call it (up to 40 characters):",
				inline([]models.InlineKeyboardButton{btn("◀️ Back", "obplace:back")}))
		case arg == "back":
			b.showPlaces(ctx, u, chatID, msgID, d)
		default:
			idx, err := strconv.Atoi(strings.TrimPrefix(arg, "i"))
			if err != nil || !togglePlace(&d, idx) {
				b.answer(ctx, q, "Unknown choice")
				return
			}
			b.showPlaces(ctx, u, chatID, msgID, d)
		}

	case "obacccur":
		if len(d.Queue) == 0 {
			b.answer(ctx, q, "Start over with /start")
			return
		}
		if arg == "done" {
			if len(d.Currencies) == 0 {
				b.answer(ctx, q, "Tick at least one currency, or skip")
				return
			}
			b.answer(ctx, q, "")
			d.Currencies = orderedBy(d.Sources, d.Currencies)
			d.Sources = nil
			b.edit(ctx, chatID, msgID, fmt.Sprintf("%s <b>%s</b>: %s", placeEmoji(d.Queue[0]),
				esc(d.Queue[0]), strings.Join(d.Currencies, ", ")), nil)
			b.accountWizardNext(ctx, u, chatID, d)
			return
		}
		idx, err := strconv.Atoi(strings.TrimPrefix(arg, "i"))
		if err != nil || !toggleCurrency(&d, idx) {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		b.answer(ctx, q, "")
		b.askPlaceCurrencies(ctx, u, chatID, msgID, d)

	case "obskip":
		b.answer(ctx, q, "Skipped")
		b.edit(ctx, chatID, msgID, "Skipped", nil)
		if len(d.Queue) > 0 {
			d.Queue = d.Queue[1:]
		}
		d.Currencies, d.Sources = nil, nil
		b.accountWizardNext(ctx, u, chatID, d)

	// --- guided flow: account and currency are picked, only the amount is typed ---
	case "entacc":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		acc, err := b.st.Account(ctx, u.ID, id)
		if err != nil {
			b.answer(ctx, q, "Account not found")
			return
		}
		b.answer(ctx, q, acc.Name)
		b.edit(ctx, chatID, msgID, "Account: <b>"+esc(acc.Name)+"</b>", nil)
		b.askAmount(ctx, u, chatID, d, acc)

	case "entcur":
		acc, err := b.st.Account(ctx, u.ID, d.AccountID)
		if err != nil {
			b.answer(ctx, q, "Start over with /add")
			return
		}
		b.answer(ctx, q, "")
		if arg == "" {
			b.edit(ctx, chatID, msgID, fmt.Sprintf("Which currency was the operation in?\n<i>Account “%s” is in %s.</i>",
				esc(acc.Name), acc.Currency),
				currencyKeyboardExcept(u.MainCurrency, "entcur", acc.Currency))
			return
		}
		d.Currency = arg
		b.edit(ctx, chatID, msgID, "Currency: <b>"+esc(arg)+"</b>", nil)
		b.askAmountOtherCurrency(ctx, u, chatID, d, acc)

	// --- category selection ---
	case "pickcat":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		if id == 0 {
			d.CategoryID = nil
			b.answer(ctx, q, "No category")
			b.edit(ctx, chatID, msgID, "Category: <i>none</i>", nil)
			b.finishEntry(ctx, u, chatID, d)
			return
		}
		cat, err := b.st.Category(ctx, u.ID, id)
		if err != nil {
			b.answer(ctx, q, "Category not found")
			return
		}
		d.CategoryID = &cat.ID
		b.answer(ctx, q, cat.Name)
		b.edit(ctx, chatID, msgID, "Category: <b>"+esc(cat.Title())+"</b>", nil)
		b.finishEntry(ctx, u, chatID, d)

	case "newcat": // in the middle of recording an operation
		b.answer(ctx, q, "")
		b.askNewCategory(ctx, u, chatID, d)

	case "catedit":
		b.answer(ctx, q, "")
		b.askCategoryToEdit(ctx, u, chatID, msgID, arg)

	case "catpick":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		c, err := b.st.Category(ctx, u.ID, id)
		if err != nil {
			b.answer(ctx, q, "Category not found")
			return
		}
		b.answer(ctx, q, "")
		b.showCategory(ctx, u, chatID, msgID, c)

	case "catren":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		b.answer(ctx, q, "")
		b.setState(ctx, u.ID, stateCategoryName, draft{CategoryID: &id})
		b.edit(ctx, chatID, msgID,
			"New name? An emoji first sets its icon: <code>🛒 Groceries</code>", cancelKeyboard())

	case "catarch":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		c, err := b.st.Category(ctx, u.ID, id)
		if err != nil {
			b.answer(ctx, q, "Category not found")
			return
		}
		if err := b.st.ArchiveCategory(ctx, u.ID, id); err != nil {
			b.fail(ctx, chatID, err)
			return
		}
		b.answer(ctx, q, "Put away")
		b.edit(ctx, chatID, msgID, fmt.Sprintf("📦 <b>%s</b> is put away. Its operations are unchanged.",
			esc(c.Title())), nil)
		b.cmdCategories(ctx, u, chatID)

	case "catdone": // finished adding categories one after another
		b.clearState(ctx, u.ID)
		b.answer(ctx, q, "")
		b.edit(ctx, chatID, msgID, "✅ Done.", nil)
		b.cmdCategories(ctx, u, chatID)

	case "newcatkind": // from the categories screen
		b.answer(ctx, q, "")
		b.askNewCategory(ctx, u, chatID, draft{Kind: arg})

	// --- source ---
	case "src":
		switch {
		case arg == "skip":
			d.Source = ""
			b.answer(ctx, q, "No source")
			b.edit(ctx, chatID, msgID, "Source: <i>not specified</i>", nil)
		case strings.HasPrefix(arg, "i"):
			idx, err := strconv.Atoi(strings.TrimPrefix(arg, "i"))
			if err != nil || idx < 0 || idx >= len(d.Sources) {
				b.answer(ctx, q, "Source not found")
				return
			}
			d.Source = d.Sources[idx]
			b.answer(ctx, q, d.Source)
			b.edit(ctx, chatID, msgID, "Source: <b>"+esc(d.Source)+"</b>", nil)
		}
		b.applySource(ctx, u, chatID, d)

	// --- operation card ---
	case "txdel":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Didn't get that")
			return
		}
		if err := b.st.DeleteTransaction(ctx, u.ID, id); err != nil {
			b.answer(ctx, q, "Already deleted")
			return
		}
		b.answer(ctx, q, "Deleted")
		b.edit(ctx, chatID, msgID, "🗑 Operation deleted", nil)

	case "txsrc":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Didn't get that")
			return
		}
		t, err := b.st.Transaction(ctx, u.ID, id)
		if err != nil {
			b.answer(ctx, q, "Operation not found")
			return
		}
		b.answer(ctx, q, "")
		sources, _ := b.st.SuggestSources(ctx, u.ID, t.Kind, t.CategoryID, 6)
		b.setState(ctx, u.ID, stateSource, draft{TxID: id, Sources: sources, Kind: t.Kind,
			AccountID: t.AccountID, Amount: t.Amount, CategoryID: t.CategoryID, Note: t.Note})
		if len(sources) > 0 {
			b.send(ctx, chatID, "Who is the source / recipient?", sourcesKeyboard(sources, "src"))
			return
		}
		b.send(ctx, chatID, "Who is the source / recipient? Type it:", cancelKeyboard())

	case "txdate":
		idStr, daysStr, picked := strings.Cut(arg, ":")
		id, err := parseID(idStr)
		if err != nil {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		if !picked {
			b.answer(ctx, q, "")
			b.edit(ctx, chatID, msgID, "When did it happen?", dateKeyboard(id))
			return
		}
		daysAgo, err := strconv.Atoi(daysStr)
		if err != nil || daysAgo < 0 || daysAgo > 366 {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		if err := b.st.SetTransactionDate(ctx, u.ID, id, dateDaysAgo(b.clock(), u.Location(), daysAgo)); err != nil {
			b.answer(ctx, q, "Operation not found")
			return
		}
		b.answer(ctx, q, "Date updated")
		b.showTxInPlace(ctx, u, chatID, msgID, id)

	case "txshow":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		b.answer(ctx, q, "")
		b.showTxInPlace(ctx, u, chatID, msgID, id)

	case "txamt":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		t, err := b.st.Transaction(ctx, u.ID, id)
		if err != nil {
			b.answer(ctx, q, "Operation not found")
			return
		}
		b.answer(ctx, q, "")
		b.askEditAmount(ctx, u, chatID, t)

	case "txcat":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		t, err := b.st.Transaction(ctx, u.ID, id)
		if err != nil {
			b.answer(ctx, q, "Operation not found")
			return
		}
		b.answer(ctx, q, "")
		b.askEditCategory(ctx, u, chatID, msgID, t)

	case "txsetcat":
		idStr, catStr, _ := strings.Cut(arg, ":")
		id, err := parseID(idStr)
		if err != nil {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		var categoryID *int64
		if catID, err := parseID(catStr); err == nil && catID > 0 {
			cat, err := b.st.Category(ctx, u.ID, catID)
			if err != nil {
				b.answer(ctx, q, "Category not found")
				return
			}
			categoryID = &cat.ID
		}
		if err := b.st.SetTransactionCategory(ctx, u.ID, id, categoryID); err != nil {
			b.fail(ctx, chatID, err)
			return
		}
		b.answer(ctx, q, "Moved")
		b.showTxInPlace(ctx, u, chatID, msgID, id)

	case "txacc":
		idStr, side, _ := strings.Cut(arg, ":")
		id, err := parseID(idStr)
		if err != nil {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		t, err := b.st.Transaction(ctx, u.ID, id)
		if err != nil {
			b.answer(ctx, q, "Operation not found")
			return
		}
		b.answer(ctx, q, "")
		b.askEditAccount(ctx, u, chatID, msgID, t, side)

	case "txsetacc":
		parts := strings.Split(arg, ":")
		if len(parts) < 2 {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		id, err1 := parseID(parts[0])
		accountID, err2 := parseID(parts[len(parts)-1])
		if err1 != nil || err2 != nil {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		side := ""
		if len(parts) == 3 {
			side = parts[1]
		}
		t, err := b.st.Transaction(ctx, u.ID, id)
		if err != nil {
			b.answer(ctx, q, "Operation not found")
			return
		}
		b.answer(ctx, q, "")
		b.moveToAccount(ctx, u, chatID, msgID, t, accountID, side)

	case "txagain":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		t, err := b.st.Transaction(ctx, u.ID, id)
		if err != nil {
			b.answer(ctx, q, "Operation not found")
			return
		}
		if t.LoanID != nil {
			b.answer(ctx, q, "Part of a loan — pay it from the loan")
			return
		}
		b.answer(ctx, q, "")
		b.repeatOperation(ctx, u, chatID, t)

	case "txnote":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Didn't get that")
			return
		}
		b.answer(ctx, q, "")
		b.setState(ctx, u.ID, stateNote, draft{TxID: id})
		b.send(ctx, chatID, "Note for the operation? (send - to clear it)", cancelKeyboard())

	// --- reports ---
	case "rep":
		b.answer(ctx, q, "")
		if arg == "custom" {
			b.setState(ctx, u.ID, stateReportPeriod, draft{})
			b.edit(ctx, chatID, msgID,
				"Type a period: <code>september</code>, <code>01.09-15.09</code>, <code>2025</code>", nil)
			return
		}
		b.sendReport(ctx, u, chatID, arg, report.Full())

	case "search":
		b.answer(ctx, q, "")
		b.askSearch(ctx, u, chatID)

	case "ops":
		b.answer(ctx, q, "")
		b.sendOperations(ctx, u, chatID, arg)

	case "aiprompt":
		b.answer(ctx, q, "Preparing…")
		b.sendAIPrompt(ctx, u, chatID, arg)

	case "csv":
		b.answer(ctx, q, "Preparing the file…")
		b.cmdExport(ctx, u, chatID, arg)

	case "rng", "rngf", "rngfd", "rngt", "rngtd":
		if !b.handleRange(ctx, u, chatID, msgID, action, arg) {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		b.answer(ctx, q, "")

	// --- accounts ---
	case "newacc":
		b.answer(ctx, q, "")
		b.startNewAccount(ctx, u, chatID)

	case "accname":
		if arg == "other" {
			b.answer(ctx, q, "")
			b.setState(ctx, u.ID, stateAccountName, d)
			b.edit(ctx, chatID, msgID,
				"Type a name for the place — a bank, a wallet, whatever you call it (up to 40 characters):",
				cancelKeyboard())
			return
		}
		idx, err := strconv.Atoi(strings.TrimPrefix(arg, "i"))
		if err != nil || idx < 0 || idx >= len(d.Sources) {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		place := d.Sources[idx]
		b.answer(ctx, q, place)
		b.edit(ctx, chatID, msgID, fmt.Sprintf("%s <b>%s</b>", placeEmoji(place), esc(place)), nil)
		d.Queue = []string{place} // the wizard works through a queue of places
		d.Sources, d.Currencies = nil, nil
		b.askPlaceCurrencies(ctx, u, chatID, 0, d)

	case "accinit":
		b.answer(ctx, q, "")
		b.edit(ctx, chatID, msgID, "Balance: <b>0</b>", nil)
		b.createAccountFromDraft(ctx, u, chatID, d, 0)

	case "accedit":
		b.answer(ctx, q, "")
		accs, err := b.st.ListAccounts(ctx, u.ID, true)
		if err != nil {
			b.fail(ctx, chatID, err)
			return
		}
		b.edit(ctx, chatID, msgID, "Which account?",
			accountsKeyboard(accs, "accpick", btn("✖️ Close", "cancel")))

	case "accpick":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Didn't get that")
			return
		}
		acc, err := b.st.Account(ctx, u.ID, id)
		if err != nil {
			b.answer(ctx, q, "Account not found")
			return
		}
		b.answer(ctx, q, acc.Name)
		b.edit(ctx, chatID, msgID, fmt.Sprintf("%s <b>%s</b> · %s", placeEmoji(acc.Name), esc(acc.Name), acc.Currency),
			accountEditKeyboard(acc))

	case "accren":
		id, _ := parseID(arg)
		b.answer(ctx, q, "")
		b.setState(ctx, u.ID, stateRenameAcc, draft{AccountID: id})
		b.send(ctx, chatID, "New name for the place? Every currency kept there is renamed with it.", cancelKeyboard())

	case "accbal":
		id, _ := parseID(arg)
		b.answer(ctx, q, "")
		b.setState(ctx, u.ID, stateBalanceFix, draft{AccountID: id})
		b.send(ctx, chatID, "What is the actual balance right now? I will correct the starting balance.", cancelKeyboard())

	case "accarch", "accunarch":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Didn't get that")
			return
		}
		archived := action == "accarch"
		if err := b.st.SetAccountArchived(ctx, u.ID, id, archived); err != nil {
			b.answer(ctx, q, "Didn't work")
			return
		}
		if archived {
			b.answer(ctx, q, "Archived")
			b.edit(ctx, chatID, msgID, "📦 Account archived. Its operations are kept.", nil)
		} else {
			b.answer(ctx, q, "Restored")
			b.edit(ctx, chatID, msgID, "♻️ Account is active again.", nil)
		}

	// --- conversion: the user's own rate, asked for, never guessed ---
	case "convrate":
		b.answer(ctx, q, "")
		acc, err := b.st.Account(ctx, u.ID, d.AccountID)
		if err != nil {
			b.fail(ctx, chatID, err)
			return
		}
		if d.Currency == "" {
			b.answer(ctx, q, "Lost the operation's currency")
			return
		}
		b.edit(ctx, chatID, msgID, fmt.Sprintf(
			"How do you want to enter the rate?\n<i>%s ↔ %s</i>", d.Currency, acc.Currency),
			rateDirectionKeyboard("convdir", d.Currency, acc.Currency))

	case "convdir":
		acc, err := b.st.Account(ctx, u.ID, d.AccountID)
		if err != nil {
			b.fail(ctx, chatID, err)
			return
		}
		b.answer(ctx, q, "")
		d.RateDir = arg
		b.setState(ctx, u.ID, stateConvRate, d)
		from, to := d.Currency, acc.Currency
		if arg == rateInverse {
			from, to = acc.Currency, d.Currency
		}
		b.edit(ctx, chatID, msgID, fmt.Sprintf(
			"What rate did the operation go through at?\nHow many <b>%s</b> per 1 <b>%s</b>?\n\n"+
				"<i>For example: <code>470</code> means 1 %s = 470 %s</i>",
			to, from, from, to), nil)

	// --- transfer ---
	case "trfrom":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Didn't get that")
			return
		}
		d.Kind = storage.KindTransfer
		d.AccountID = id
		b.answer(ctx, q, "")
		b.edit(ctx, chatID, msgID, "From: selected", nil)
		b.askTransferAmount(ctx, u, chatID, d)

	case "trto":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Didn't get that")
			return
		}
		d.ToAccountID = id
		b.answer(ctx, q, "")
		b.edit(ctx, chatID, msgID, "To: selected", nil)
		b.askTransferRate(ctx, u, chatID, d)

	case "trdir":
		b.answer(ctx, q, "")
		b.edit(ctx, chatID, msgID, "Rate: entering…", nil)
		b.askTransferRateInput(ctx, u, chatID, d, arg)

	case "tramount":
		b.answer(ctx, q, "")
		b.edit(ctx, chatID, msgID, "Entering the credited amount…", nil)
		b.askTransferAmountManual(ctx, u, chatID, d)

	// --- settings ---
	case "setmenu":
		b.answer(ctx, q, "")
		switch arg {
		case "accounts":
			b.cmdAccounts(ctx, u, chatID)
		case "categories":
			b.cmdCategories(ctx, u, chatID)
		case "budgets":
			b.cmdBudget(ctx, u, chatID, "")
		case "recurring":
			b.cmdRecurring(ctx, u, chatID, "")
		case "rates":
			b.cmdRates(ctx, u, chatID, "")
		case "currency":
			b.edit(ctx, chatID, msgID,
				"<b>Main currency</b>\nThe report total is converted into it at market rates.",
				currencyKeyboardWith(u.MainCurrency, "setcur", "", btn("◀️ Back", "settings")))
		case "tz":
			b.edit(ctx, chatID, msgID,
				"<b>Timezone</b>\nMonth boundaries are computed in it. Not listed? Use <code>/timezone Europe/Paris</code>.",
				timezoneKeyboard())
		}

	case "setcur":
		if err := b.st.SetMainCurrency(ctx, u.ID, arg); err != nil {
			b.answer(ctx, q, "Didn't work")
			return
		}
		b.answer(ctx, q, arg)
		u.MainCurrency = arg
		text, kb := settingsView(u)
		b.edit(ctx, chatID, msgID, text, kb)

	case "setnudge":
		if err := b.st.SetDailyReminder(ctx, u.ID, arg == "on"); err != nil {
			b.answer(ctx, q, "Didn't work")
			return
		}
		b.answer(ctx, q, "Done")
		u.DailyReminder = arg == "on"
		text, kb := settingsView(u)
		b.edit(ctx, chatID, msgID, text, kb)

	case "setreport":
		on := arg == "on"
		if err := b.st.SetMonthlyReport(ctx, u.ID, on); err != nil {
			b.answer(ctx, q, "Didn't work")
			return
		}
		b.answer(ctx, q, "Done")
		u.MonthlyReport = on
		text, kb := settingsView(u)
		b.edit(ctx, chatID, msgID, text, kb)

	case "settz":
		if err := b.st.SetTimezone(ctx, u.ID, arg); err != nil {
			b.answer(ctx, q, "Didn't work")
			return
		}
		b.answer(ctx, q, arg)
		u.Timezone = arg
		text, kb := settingsView(u)
		b.edit(ctx, chatID, msgID, text, kb)

	case "settings":
		b.answer(ctx, q, "")
		text, kb := settingsView(u)
		b.edit(ctx, chatID, msgID, text, kb)

	// --- budgets ---
	case "budpick":
		b.answer(ctx, q, "")
		b.askBudgetCategory(ctx, u, chatID)

	case "budcat":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Didn't get that")
			return
		}
		cat, err := b.st.Category(ctx, u.ID, id)
		if err != nil {
			b.answer(ctx, q, "Category not found")
			return
		}
		b.answer(ctx, q, cat.Name)
		d.CategoryID = &cat.ID
		d.Kind = storage.KindExpense
		b.setState(ctx, u.ID, stateBudgetAmount, d)
		b.edit(ctx, chatID, msgID, fmt.Sprintf(
			"Monthly limit for %s in %s? Send an amount, or - to remove the limit.",
			esc(cat.Title()), u.MainCurrency), nil)

	// --- recurring operations ---
	case "recnew":
		b.answer(ctx, q, "")
		b.startRecurring(ctx, u, chatID, arg)

	case "reccat":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Didn't get that")
			return
		}
		cat, err := b.st.Category(ctx, u.ID, id)
		if err != nil {
			b.answer(ctx, q, "Category not found")
			return
		}
		d.CategoryID = &cat.ID
		b.answer(ctx, q, cat.Name)
		b.setState(ctx, u.ID, stateRecurAmount, d)
		b.edit(ctx, chatID, msgID, "Amount of the recurring operation?", nil)

	case "recday":
		day, err := strconv.Atoi(arg)
		if err != nil || day < 1 || day > 31 {
			b.answer(ctx, q, "Unknown choice")
			return
		}
		b.answer(ctx, q, "")
		b.edit(ctx, chatID, msgID, fmt.Sprintf("Day of the month: <b>%d</b>", day), nil)
		b.createRecurring(ctx, u, chatID, d, day)

	case "recdel":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Didn't get that")
			return
		}
		if err := b.st.DeleteRecurring(ctx, u.ID, id); err != nil {
			b.answer(ctx, q, "Not found")
			return
		}
		b.answer(ctx, q, "Deleted")
		b.edit(ctx, chatID, msgID, "🗑 Recurring operation deleted", nil)

	case "recacc":
		id, err := parseID(arg)
		if err != nil {
			b.answer(ctx, q, "Didn't get that")
			return
		}
		d.AccountID = id
		b.answer(ctx, q, "")
		cats, err := b.st.ListCategories(ctx, u.ID, d.Kind)
		if err != nil {
			b.fail(ctx, chatID, err)
			return
		}
		b.setState(ctx, u.ID, stateRecurAmount, d)
		b.edit(ctx, chatID, msgID, "Category of the recurring operation?",
			categoriesKeyboard(cats, "reccat", btn("✖️ Cancel", "cancel")))

	// --- exchange rates ---
	case "ratesfill":
		b.answer(ctx, q, "Fetching…")
		period, err := parser.ParsePeriod("year", b.clock(), u.Location())
		if err != nil {
			b.fail(ctx, chatID, err)
			return
		}
		b.backfillRates(ctx, u, chatID, period)

	case "ratesupd":
		b.answer(ctx, q, "Updating…")
		b.updateRates(ctx, u, chatID)

	default:
		b.answer(ctx, q, "")
	}
}

// wizardSteps lists, for each button that continues a wizard, the dialog
// states in which it can be pressed. Buttons not listed work at any time.
var wizardSteps = map[string][]string{
	"obplace":  {stateOnboard, stateOnboardName},
	"obacccur": {stateOnboard},
	"obskip":   {stateOnboard},
	"entacc":   {stateAmount},
	"entcur":   {stateAmount},
	"newcat":   {stateAmount},
	"pickcat":  {stateAmount, stateNewCategory},
	"src":      {stateSource},
	"accname":  {stateAccountName},
	"accinit":  {stateAccountInit},
	"convrate": {stateConvAmount},
	"convdir":  {stateConvAmount, stateConvRate},
	"trfrom":   {stateTransferAmt},
	"trto":     {stateTransferAmt},
	"trdir":    {stateTransferRate},
	"tramount": {stateTransferRate},
	"recacc":   {stateRecurAmount},
	"reccat":   {stateRecurAmount},
	"recday":   {stateRecurDay},

	"loanname":    {stateLoanName},
	"loanacc":     {stateLoanAccount},
	"loancur":     {stateLoanCurrency},
	"loanmonthly": {stateLoanMonthly},
	"loanday":     {stateLoanDay},
	"loanpayacc":  {stateLoanPayAcc},
	"loanamt":     {stateLoanPay},
	"loanint":     {stateLoanInterest},
	"entloan":     {stateAmount, stateNewCategory},
	"entloanpick": {stateAmount, stateNewCategory},
}

func slogError(msg string, err error) {
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		slog.Error(msg, "err", err)
	}
}
