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

func (b *Bot) cmdAccounts(ctx context.Context, u storage.User, chatID int64) {
	accs, err := b.st.ListAccountsWithBalance(ctx, u.ID, false)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	if len(accs) == 0 {
		b.startNewAccount(ctx, u, chatID)
		return
	}

	var sb strings.Builder
	sb.WriteString("<b>💳 Accounts and balances</b>\n\n")
	sb.WriteString(accountsList(accs))

	buttons := []models.InlineKeyboardButton{
		btn("➕ New account", "newacc"),
		btn("✏️ Edit", "accedit"),
	}
	b.send(ctx, chatID, sb.String(), inline(buttons))
}

// accountsList renders the balances grouped by place, since one place can hold
// several currencies, followed by the total per currency.
func accountsList(accs []storage.Account) string {
	var places []string                       // in the order they come back
	byPlace := map[string][]storage.Account{} // place → its currencies
	totals := map[string]int64{}              // currency → sum across places
	decimals := map[string]int{}
	var currencies []string

	for _, a := range accs {
		if _, seen := byPlace[a.Name]; !seen {
			places = append(places, a.Name)
		}
		byPlace[a.Name] = append(byPlace[a.Name], a)
		if _, seen := totals[a.Currency]; !seen {
			currencies = append(currencies, a.Currency)
			decimals[a.Currency] = a.Decimals
		}
		totals[a.Currency] += a.Balance
	}

	var sb strings.Builder
	for _, place := range places {
		held := byPlace[place]
		if len(held) == 1 {
			fmt.Fprintf(&sb, "%s <b>%s</b> — %s\n", placeEmoji(place), esc(place),
				money.FormatCode(held[0].Balance, held[0].Decimals, held[0].Currency))
			continue
		}
		fmt.Fprintf(&sb, "%s <b>%s</b>\n", placeEmoji(place), esc(place))
		for _, a := range held {
			fmt.Fprintf(&sb, "   • %s\n", money.FormatCode(a.Balance, a.Decimals, a.Currency))
		}
	}

	// The total only says something once more than one place is involved.
	if len(places) > 1 {
		parts := make([]string, 0, len(currencies))
		for _, cur := range currencies {
			parts = append(parts, money.FormatCode(totals[cur], decimals[cur], cur))
		}
		fmt.Fprintf(&sb, "\n<b>Total:</b> %s\n", strings.Join(parts, " + "))
	}
	return sb.String()
}

// startNewAccount begins adding an account, all by buttons: the place, then the
// currencies it holds, then the balance of each. A place already there can be
// picked again to give it another currency.
func (b *Bot) startNewAccount(ctx context.Context, u storage.User, chatID int64) {
	accs, err := b.st.ListAccounts(ctx, u.ID, true)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	existing := make([]string, 0, len(accs))
	for _, a := range accs {
		if !contains(existing, a.Name) {
			existing = append(existing, a.Name)
		}
	}

	d := draft{Sources: suggestPlaces(existing)}
	b.setState(ctx, u.ID, stateAccountName, d)

	buttons := make([]models.InlineKeyboardButton, 0, len(d.Sources))
	for i, name := range d.Sources {
		buttons = append(buttons, btn(placeEmoji(name)+" "+name, fmt.Sprintf("accname:i%d", i)))
	}
	rows := grid(buttons, 2)
	rows = append(rows,
		[]models.InlineKeyboardButton{btn("✏️ Other name", "accname:other")},
		[]models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")})
	b.send(ctx, chatID, "Where is the money kept?", inline(rows...))
}

// suggestPlaces offers the places to keep money: the ones the user already has
// first, since adding a currency to one is the common case, then the usual kinds.
func suggestPlaces(existing []string) []string {
	out := append([]string(nil), existing...)
	for _, p := range placeOptions {
		if !containsFold(out, p.name) {
			out = append(out, p.name)
		}
	}
	return out
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// askAccountBalance asks for the current balance — the one number the user types.
func (b *Bot) askAccountBalance(ctx context.Context, u storage.User, chatID int64, d draft) {
	b.setState(ctx, u.ID, stateAccountInit, d)
	b.send(ctx, chatID, fmt.Sprintf(
		"%s <b>%s</b> · %s — how much is there right now? Send the amount, or start from zero.",
		placeEmoji(d.Note), esc(d.Note), d.Currency),
		inline([]models.InlineKeyboardButton{btn("0️⃣ Start from zero", "accinit:zero")},
			[]models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")}))
}

// inputAccountName takes a place named by the user instead of picked, then asks
// which currencies it holds.
func (b *Bot) inputAccountName(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	name := strings.TrimSpace(text)
	if name == "" || len([]rune(name)) > 40 {
		b.send(ctx, chatID, "Name — up to 40 characters.", cancelKeyboard())
		return
	}
	d.Queue = []string{name}
	d.Sources, d.Currencies = nil, nil
	b.askPlaceCurrencies(ctx, u, chatID, 0, d)
}

func currencyKeyboard(main, prefix string) *models.InlineKeyboardMarkup {
	return currencyKeyboardExcept(main, prefix, "")
}

// currencyKeyboardExcept lists the common currencies, the user's main one
// first, leaving out exclude (an account's own currency, for instance).
func currencyKeyboardExcept(main, prefix, exclude string) *models.InlineKeyboardMarkup {
	return currencyKeyboardWith(main, prefix, exclude, btn("✖️ Cancel", "cancel"))
}

// currencyKeyboardWith is the same list with a caller-chosen last button.
func currencyKeyboardWith(main, prefix, exclude string, last models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	var codes []string
	if main != exclude {
		codes = append(codes, main)
	}
	for _, c := range []string{"KZT", "USD", "EUR", "RUB", "USDT", "TRY", "GEL", "AED"} {
		if c != main && c != exclude {
			codes = append(codes, c)
		}
	}
	buttons := make([]models.InlineKeyboardButton, 0, len(codes))
	for _, c := range codes {
		buttons = append(buttons, btn(c, prefix+":"+c))
	}
	rows := grid(buttons, 3)
	rows = append(rows, []models.InlineKeyboardButton{last})
	return inline(rows...)
}

func (b *Bot) inputAccountInitial(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	cur, err := b.st.Currency(ctx, d.Currency)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	var initial int64
	if !isSkip(text) && strings.TrimSpace(text) != "0" {
		initial, err = money.Parse(text, cur.Decimals)
		if err != nil {
			b.send(ctx, chatID, "Couldn't read the amount. Send a number, or start from zero.",
				inline([]models.InlineKeyboardButton{btn("0️⃣ Start from zero", "accinit:zero")},
					[]models.InlineKeyboardButton{btn("✖️ Cancel", "cancel")}))
			return
		}
	}
	b.createAccountFromDraft(ctx, u, chatID, d, initial)
}

// createAccountFromDraft creates the account the wizard has collected: currency
// and name in the draft, the balance passed in.
func (b *Bot) createAccountFromDraft(ctx context.Context, u storage.User, chatID int64, d draft, initial int64) {
	cur, err := b.st.Currency(ctx, d.Currency)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	acc, err := b.st.CreateAccount(ctx, u.ID, d.Note, d.Currency, initial)
	if err != nil {
		if strings.Contains(err.Error(), "accounts_user_id_name_currency_key") {
			// That place already holds this currency — nothing to add.
			b.send(ctx, chatID, fmt.Sprintf("“%s” already holds %s.", esc(d.Note), d.Currency), nil)
			if len(d.Currencies) > 0 {
				d.Currencies = d.Currencies[1:]
			}
			if len(d.Currencies) == 0 && len(d.Queue) > 0 {
				d.Queue = d.Queue[1:]
			}
			b.accountWizardNext(ctx, u, chatID, d)
			return
		}
		b.fail(ctx, chatID, err)
		return
	}
	// One place can hold several currencies, so this may be one of a few accounts
	// in a row: report it and let the wizard carry on.
	b.send(ctx, chatID, fmt.Sprintf("✅ %s <b>%s</b> · %s — %s", placeEmoji(acc.Name), esc(acc.Name),
		acc.Currency, money.FormatCode(initial, cur.Decimals, acc.Currency)), nil)
	if len(d.Currencies) > 0 {
		d.Currencies = d.Currencies[1:]
	}
	if len(d.Currencies) == 0 && len(d.Queue) > 0 { // this place is done
		d.Queue = d.Queue[1:]
	}
	d.Currency, d.Note = "", ""
	b.accountWizardNext(ctx, u, chatID, d)
}

func (b *Bot) inputAccountRename(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	name := strings.TrimSpace(text)
	if name == "" || len([]rune(name)) > 40 {
		b.send(ctx, chatID, "Name — up to 40 characters.", cancelKeyboard())
		return
	}
	if err := b.st.RenameAccount(ctx, u.ID, d.AccountID, name); err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.clearState(ctx, u.ID)
	b.send(ctx, chatID, "✅ Renamed to “"+esc(name)+"”.", nil)
	b.cmdAccounts(ctx, u, chatID)
}

// inputBalanceFix adjusts the account balance to match reality: corrects the starting balance.
func (b *Bot) inputBalanceFix(ctx context.Context, u storage.User, chatID int64, d draft, text string) {
	accs, err := b.st.ListAccountsWithBalance(ctx, u.ID, true)
	if err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	var acc storage.Account
	found := false
	for _, a := range accs {
		if a.ID == d.AccountID {
			acc, found = a, true
			break
		}
	}
	if !found {
		b.send(ctx, chatID, "Account not found.", nil)
		b.clearState(ctx, u.ID)
		return
	}
	want, err := money.Parse(text, acc.Decimals)
	if err != nil {
		b.send(ctx, chatID, "Couldn't read the amount. Send the actual balance as a number.", cancelKeyboard())
		return
	}
	diff := want - acc.Balance
	if err := b.st.SetInitialBalance(ctx, u.ID, acc.ID, acc.InitialBalance+diff); err != nil {
		b.fail(ctx, chatID, err)
		return
	}
	b.clearState(ctx, u.ID)
	b.send(ctx, chatID, fmt.Sprintf("✅ Balance of “%s” is now %s (adjusted by %s).",
		esc(acc.Name), money.FormatCode(want, acc.Decimals, acc.Currency),
		money.FormatSigned(diff, acc.Decimals, acc.Currency)), nil)
}

// accountEditKeyboard — the edit menu for a specific account.
func accountEditKeyboard(a storage.Account) *models.InlineKeyboardMarkup {
	archive := btn("📦 Archive", fmt.Sprintf("accarch:%d", a.ID))
	if a.Archived {
		archive = btn("♻️ Restore from archive", fmt.Sprintf("accunarch:%d", a.ID))
	}
	return inline(
		[]models.InlineKeyboardButton{
			btn("✏️ Rename place", fmt.Sprintf("accren:%d", a.ID)),
			btn("⚖️ Adjust balance", fmt.Sprintf("accbal:%d", a.ID)),
		},
		[]models.InlineKeyboardButton{archive, btn("◀️ Back", "accedit")},
	)
}

// categoryEditRow is the row that opens category editing for one kind.
func categoryEditRow(cats []storage.Category, kind string) []models.InlineKeyboardButton {
	label := "✏️ Edit expense categories"
	if kind == storage.KindIncome {
		label = "✏️ Edit income categories"
	}
	return []models.InlineKeyboardButton{btn(label, "catedit:"+kind)}
}

func (b *Bot) cmdCategories(ctx context.Context, u storage.User, chatID int64) {
	var sb strings.Builder
	var rows [][]models.InlineKeyboardButton
	for _, kind := range []string{storage.KindExpense, storage.KindIncome} {
		cats, err := b.st.ListCategories(ctx, u.ID, kind)
		if err != nil {
			b.fail(ctx, chatID, err)
			return
		}
		title := "💸 Expense categories"
		if kind == storage.KindIncome {
			title = "📈 Income categories"
		}
		fmt.Fprintf(&sb, "<b>%s</b>\n", title)
		if len(cats) == 0 {
			sb.WriteString("<i>none yet — they appear as you name them</i>\n")
		}
		for _, c := range cats {
			fmt.Fprintf(&sb, "• %s\n", esc(c.Title()))
		}
		if len(cats) > 0 {
			rows = append(rows, categoryEditRow(cats, kind))
		}
		sb.WriteByte('\n')
	}
	sb.WriteString("<i>Most-used first. Add one here, or when recording an operation.</i>")
	rows = append(rows, []models.InlineKeyboardButton{
		btn("➖ Expense", "newcatkind:expense"),
		btn("➕ Income", "newcatkind:income"),
	})
	b.send(ctx, chatID, sb.String(), inline(rows...))
}

func parseID(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}
