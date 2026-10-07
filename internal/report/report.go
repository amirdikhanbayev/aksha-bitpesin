// Package report builds the statement text for a period.
package report

import (
	"context"
	"errors"
	"fmt"
	"html"
	"sort"
	"strings"

	"aksha-bitpesin/internal/money"
	"aksha-bitpesin/internal/parser"
	"aksha-bitpesin/internal/storage"
)

// Options — what to include in the statement.
type Options struct {
	Categories bool
	Sources    bool
	Accounts   bool
	Balances   bool
	Budgets    bool
	Loans      bool
	TopN       int // how many rows to show in breakdowns, 0 = all
}

// Full — the full statement (monthly and /report).
func Full() Options {
	return Options{Categories: true, Sources: true, Accounts: true, Balances: true, Budgets: true, Loans: true, TopN: 12}
}

// Short — a compact summary for /stats.
func Short() Options {
	return Options{Categories: true, TopN: 5}
}

// Build renders the HTML statement text for a period.
func Build(ctx context.Context, st *storage.Store, u storage.User, p parser.Period, opt Options) (string, error) {
	var b strings.Builder

	fmt.Fprintf(&b, "<b>📊 Statement: %s</b>\n", html.EscapeString(p.Label))
	fmt.Fprintf(&b, "<i>%s — %s</i>\n",
		p.From.Format("02.01.2006"), p.To.AddDate(0, 0, -1).Format("02.01.2006"))

	totals, err := st.Totals(ctx, u.ID, p.From, p.To)
	if err != nil {
		return "", err
	}
	count, err := st.CountTransactions(ctx, u.ID, p.From, p.To)
	if err != nil {
		return "", err
	}
	if count == 0 {
		b.WriteString("\nNo operations in this period.")
		return b.String(), nil
	}

	// --- Totals by currency ---
	b.WriteString("\n<b>💰 Total</b>\n")
	for _, t := range totals {
		fmt.Fprintf(&b, "%s: <b>%s</b>  income %s · expenses %s\n",
			t.Currency,
			money.FormatSigned(t.Net(), t.Decimals, ""),
			money.Format(t.Income, t.Decimals),
			money.Format(t.Expense, t.Decimals))
	}

	// --- Summary in the main currency ---
	if len(totals) > 1 {
		if line := convertedLine(ctx, st, u, p); line != "" {
			b.WriteString(line)
		}
	}
	fmt.Fprintf(&b, "Operations: %d\n", count)

	// --- Expenses by category ---
	if opt.Categories {
		cats, err := st.ByCategory(ctx, u.ID, storage.KindExpense, p.From, p.To)
		if err != nil {
			return "", err
		}
		writeCategoryBlock(&b, "💸 Expenses by category", cats, totals, opt.TopN, storage.KindExpense)

		incCats, err := st.ByCategory(ctx, u.ID, storage.KindIncome, p.From, p.To)
		if err != nil {
			return "", err
		}
		writeCategoryBlock(&b, "📈 Income by category", incCats, totals, opt.TopN, storage.KindIncome)
	}

	// --- Sources ---
	if opt.Sources {
		for _, kind := range []string{storage.KindIncome, storage.KindExpense} {
			srcs, err := st.BySource(ctx, u.ID, kind, p.From, p.To)
			if err != nil {
				return "", err
			}
			title := "🏷 Income sources"
			if kind == storage.KindExpense {
				title = "🏪 Where the money goes"
			}
			writeSourceBlock(&b, title, srcs, opt.TopN)
		}
	}

	// --- Account turnover ---
	if opt.Accounts {
		accs, err := st.ByAccount(ctx, u.ID, p.From, p.To)
		if err != nil {
			return "", err
		}
		if len(accs) > 0 {
			b.WriteString("\n<b>💳 Account turnover</b>\n")
			for _, a := range accs {
				fmt.Fprintf(&b, "• %s: +%s / −%s\n", html.EscapeString(a.Name),
					money.Format(a.Income, a.Decimals), money.Format(a.Expense, a.Decimals))
			}
		}
	}

	// --- Budgets ---
	if opt.Budgets && !p.Month.IsZero() {
		buds, err := st.Budgets(ctx, u.ID, p.Month, p.From, p.To)
		if err != nil {
			return "", err
		}
		if len(buds) > 0 {
			b.WriteString("\n<b>🎯 Budgets</b>\n")
			for _, bd := range buds {
				pct := 0
				if bd.LimitAmount > 0 {
					pct = int(bd.Spent * 100 / bd.LimitAmount)
				}
				mark := "✅"
				switch {
				case pct >= 100:
					mark = "🔴"
				case pct >= 80:
					mark = "🟡"
				}
				fmt.Fprintf(&b, "%s %s: %s / %s (%d%%)\n", mark, html.EscapeString(bd.CategoryName),
					money.Format(bd.Spent, bd.Decimals),
					money.FormatCode(bd.LimitAmount, bd.Decimals, bd.Currency), pct)
			}
		}
	}

	// --- Debts and loans ---
	if opt.Loans {
		if err := writeLoans(ctx, &b, st, u, p); err != nil {
			return "", err
		}
	}

	// --- Balances ---
	if opt.Balances {
		accs, err := st.ListAccountsWithBalance(ctx, u.ID, false)
		if err != nil {
			return "", err
		}
		if len(accs) > 0 {
			b.WriteString("\n<b>🏦 Account balances</b>\n")
			for _, a := range accs {
				fmt.Fprintf(&b, "• %s: <b>%s</b>\n", html.EscapeString(a.Name),
					money.FormatCode(a.Balance, a.Decimals, a.Currency))
			}
		}
	}

	return b.String(), nil
}

// writeLoans shows what happened on each debt and loan over the period — what
// was paid on an installment plan or a credit, how much of it was interest, what
// was lent and paid back — and where things stand now.
func writeLoans(ctx context.Context, b *strings.Builder, st *storage.Store, u storage.User, p parser.Period) error {
	sums, err := st.LoansInPeriod(ctx, u.ID, p.From, p.To)
	if err != nil {
		return err
	}
	loans, err := st.Loans(ctx, u.ID)
	if err != nil {
		return err
	}
	owe, owed := map[string]int64{}, map[string]int64{}
	decimals := map[string]int{}
	var order []string
	for _, l := range loans {
		if l.PaidOff() {
			continue
		}
		if _, ok := decimals[l.Currency]; !ok {
			order = append(order, l.Currency)
			decimals[l.Currency] = l.Decimals
		}
		if l.OwedToMe() {
			owed[l.Currency] += l.Outstanding()
		} else {
			owe[l.Currency] += l.Outstanding()
		}
	}
	if len(sums) == 0 && len(order) == 0 {
		return nil
	}

	b.WriteString("\n<b>🤝 Debts and loans</b>\n")
	for _, x := range sums {
		f := func(v int64) string { return money.FormatCode(v, x.Decimals, x.Currency) }
		var parts []string
		switch x.Kind {
		case storage.LoanLent:
			if x.Out > 0 {
				parts = append(parts, "lent "+f(x.Out))
			}
			if x.In > 0 {
				parts = append(parts, "got back "+f(x.In))
			}
		case storage.LoanBorrowed:
			if x.In > 0 {
				parts = append(parts, "borrowed "+f(x.In))
			}
			if x.Out > 0 {
				parts = append(parts, "paid back "+f(x.Out))
			}
		default:
			if x.In > 0 {
				parts = append(parts, "received "+f(x.In))
			}
			if x.Out > 0 {
				paid := "paid " + f(x.Out)
				if x.Interest > 0 {
					paid += " · of it interest " + money.Format(x.Interest, x.Decimals)
				}
				parts = append(parts, paid)
			}
		}
		if len(parts) > 0 {
			fmt.Fprintf(b, "• %s %s — %s\n", loanEmoji(x.Kind), html.EscapeString(x.Name), strings.Join(parts, " · "))
		}
	}
	if len(order) > 0 {
		var now []string
		join := func(m map[string]int64) string {
			var out []string
			for _, c := range order {
				if m[c] > 0 {
					out = append(out, money.FormatCode(m[c], decimals[c], c))
				}
			}
			return strings.Join(out, " + ")
		}
		if s := join(owe); s != "" {
			now = append(now, "you owe "+s)
		}
		if s := join(owed); s != "" {
			now = append(now, "owed to you "+s)
		}
		fmt.Fprintf(b, "<i>Now: %s</i>\n", strings.Join(now, " · "))
	}
	return nil
}

func loanEmoji(kind string) string { return storage.LoanEmoji(kind) }

func writeCategoryBlock(b *strings.Builder, title string, cats []storage.CategorySum,
	totals []storage.CurrencyTotal, topN int, kind string) {

	if len(cats) == 0 {
		return
	}
	// total per currency — for percentages
	base := map[string]int64{}
	for _, t := range totals {
		if kind == storage.KindExpense {
			base[t.Currency] = t.Expense
		} else {
			base[t.Currency] = t.Income
		}
	}

	byCurrency := map[string][]storage.CategorySum{}
	var order []string
	for _, c := range cats {
		if _, ok := byCurrency[c.Currency]; !ok {
			order = append(order, c.Currency)
		}
		byCurrency[c.Currency] = append(byCurrency[c.Currency], c)
	}
	sort.Slice(order, func(i, j int) bool { return base[order[i]] > base[order[j]] })

	fmt.Fprintf(b, "\n<b>%s</b>\n", title)
	for _, cur := range order {
		list := byCurrency[cur]
		sort.Slice(list, func(i, j int) bool { return list[i].Total > list[j].Total })
		if len(order) > 1 {
			fmt.Fprintf(b, "<u>%s</u>\n", cur)
		}
		shown := list
		var restTotal int64
		var restCount int
		if topN > 0 && len(list) > topN {
			shown = list[:topN]
			for _, c := range list[topN:] {
				restTotal += c.Total
				restCount++
			}
		}
		for _, c := range shown {
			pct := 0
			if base[cur] > 0 {
				pct = int(c.Total * 100 / base[cur])
			}
			name := c.Name
			if c.Emoji != "" {
				name = c.Emoji + " " + c.Name
			}
			fmt.Fprintf(b, "%s %s — %s (%d%%, %d ops)\n",
				bar(pct), html.EscapeString(name),
				money.Format(c.Total, c.Decimals), pct, c.Count)
		}
		if restCount > 0 {
			fmt.Fprintf(b, "… %d more categories — %s\n", restCount, money.Format(restTotal, shown[0].Decimals))
		}
	}
}

func writeSourceBlock(b *strings.Builder, title string, srcs []storage.SourceSum, topN int) {
	if len(srcs) == 0 {
		return
	}
	sort.SliceStable(srcs, func(i, j int) bool { return srcs[i].Total > srcs[j].Total })
	fmt.Fprintf(b, "\n<b>%s</b>\n", title)
	shown := srcs
	if topN > 0 && len(srcs) > topN {
		shown = srcs[:topN]
	}
	for _, s := range shown {
		fmt.Fprintf(b, "• %s — %s (%d)\n", html.EscapeString(s.Source),
			money.FormatCode(s.Total, s.Decimals, s.Currency), s.Count)
	}
	if len(srcs) > len(shown) {
		fmt.Fprintf(b, "… %d more\n", len(srcs)-len(shown))
	}
}

// rateLookup caches one exchange-rate answer (found or not) for a currency/day pair.
type rateLookup struct {
	rate  float64
	found bool
}

// Converted — the period's operations priced in the user's main currency.
type Converted struct {
	MainCurrency string
	Decimals     int
	Income       int64
	Expense      int64
	Missing      []string // currencies with at least one operation that had no rate for its date
}

// ConvertToMain prices every operation in the period in the user's main currency,
// preferring what is actually known over any estimate:
//
//  1. the operation was entered in the main currency (a KZT purchase paid from a
//     USD card, with the user's own rate) — that amount is exact, nothing to estimate;
//  2. the account itself is in the main currency — the amount is already right;
//  3. otherwise — an estimate at the market rate in effect on that operation's
//     own date, not one rate for the whole period, so a currency that moved
//     during the month is summed correctly instead of being priced entirely at
//     the period's closing rate.
//
// Only case 3 is an approximation, and it is marked as such in the report.
// It does at most one exchange_rates lookup per distinct (currency, day) pair,
// cached in memory, rather than one per transaction.
func ConvertToMain(ctx context.Context, st *storage.Store, u storage.User, p parser.Period) (Converted, error) {
	var out Converted
	main, err := st.Currency(ctx, u.MainCurrency)
	if err != nil {
		return out, err
	}
	out.MainCurrency, out.Decimals = main.Code, main.Decimals

	rows, err := st.ConversionRows(ctx, u.ID, p.From, p.To)
	if err != nil {
		return out, err
	}

	cache := map[string]rateLookup{}
	missing := map[string]bool{}

	for _, r := range rows {
		var mainAmount int64
		switch {
		case r.OriginalCurrency == main.Code && r.OriginalAmount != nil:
			// The user stated this operation in the main currency and gave the
			// rate their bank applied — the exact figure, no estimate needed.
			mainAmount = money.Convert(*r.OriginalAmount, r.OriginalDecimals, main.Decimals, 1)
		case r.Currency == main.Code:
			mainAmount = money.Convert(r.Amount, r.Decimals, main.Decimals, 1)
		default:
			key := r.Currency + "|" + r.OccurredAt.Format("2006-01-02")
			lookup, ok := cache[key]
			if !ok {
				rate, err := st.Rate(ctx, main.Code, r.Currency, r.OccurredAt)
				if err != nil && !errors.Is(err, storage.ErrNotFound) {
					return out, err
				}
				lookup = rateLookup{rate: rate, found: err == nil}
				cache[key] = lookup
			}
			if !lookup.found {
				missing[r.Currency] = true
				continue
			}
			mainAmount = money.Convert(r.Amount, r.Decimals, main.Decimals, lookup.rate)
		}
		switch r.Kind {
		case storage.KindIncome:
			out.Income += mainAmount
		case storage.KindExpense:
			out.Expense += mainAmount
		}
	}

	for c := range missing {
		out.Missing = append(out.Missing, c)
	}
	sort.Strings(out.Missing)
	return out, nil
}

// convertedLine renders ConvertToMain's result as the report's summary line.
func convertedLine(ctx context.Context, st *storage.Store, u storage.User, p parser.Period) string {
	c, err := ConvertToMain(ctx, st, u, p)
	if err != nil || (c.Income == 0 && c.Expense == 0 && len(c.Missing) == 0) {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "≈ in %s: <b>%s</b>  (income %s · expenses %s)\n",
		c.MainCurrency,
		money.FormatSigned(c.Income-c.Expense, c.Decimals, ""),
		money.Format(c.Income, c.Decimals),
		money.Format(c.Expense, c.Decimals))
	if len(c.Missing) > 0 {
		fmt.Fprintf(&b, "<i>no rate for: %s — set one with /rate</i>\n", strings.Join(c.Missing, ", "))
	}
	return b.String()
}

// bar draws a mini gauge for a category's share.
func bar(pct int) string {
	switch {
	case pct >= 40:
		return "█████"
	case pct >= 25:
		return "████░"
	case pct >= 15:
		return "███░░"
	case pct >= 7:
		return "██░░░"
	case pct >= 3:
		return "█░░░░"
	default:
		return "░░░░░"
	}
}
