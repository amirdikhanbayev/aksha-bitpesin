package report

import (
	"fmt"
	"strings"

	"aksha-bitpesin/internal/parser"
	"aksha-bitpesin/internal/storage"
)

// A prompt to hand to whichever AI the person already uses, together with their
// own numbers. It is written for them to copy, so it explains the data instead of
// assuming the reader knows this bot: what the columns mean, that amounts are in
// minor units, that a place can hold several currencies, and which figures are
// exact rather than estimated. The questions ask for specifics that can be acted
// on, and it says plainly what to do when the data doesn't support an answer —
// an analysis of two weeks of records should say so, not invent a trend.

// AIPrompt returns the analysis prompt for a period, to be sent alongside the CSV.
func AIPrompt(u storage.User, p parser.Period, ops int) string {
	var b strings.Builder

	b.WriteString("Below is my personal spending data. Please analyse it.\n\n")

	b.WriteString("## What you're looking at\n\n")
	fmt.Fprintf(&b, "- Period: %s (%s to %s)\n", p.Label,
		p.From.Format("2006-01-02"), p.To.AddDate(0, 0, -1).Format("2006-01-02"))
	fmt.Fprintf(&b, "- Operations: %d\n", ops)
	fmt.Fprintf(&b, "- I count in %s, and my timezone is %s\n", u.MainCurrency, u.Timezone)
	b.WriteString("- The file is a CSV exported from my own tracker, one row per operation\n\n")

	b.WriteString("Columns:\n")
	b.WriteString("- `Date` — when it happened (dd.mm.yyyy hh:mm)\n")
	b.WriteString("- `Type` — income, expense, transfer, debt in or debt out\n")
	b.WriteString("- `Account amount` / `Account currency` — what moved on the account\n")
	b.WriteString("- `Operation amount` / `Operation currency` / `Rate` — filled in only when the\n" +
		"  operation happened in a different currency from the account, e.g. a purchase\n" +
		"  priced in tenge paid with a dollar card. Then the *operation* amount is what the\n" +
		"  thing actually cost, and `Rate` is the rate my bank applied\n")
	b.WriteString("- `Category`, `Source` — my own words; `Source` is the shop, employer or person\n")
	b.WriteString("- `Account` — where the money is kept. One place can hold several currencies,\n" +
		"  so \"Cash\" in KZT and \"Cash\" in USD are two balances in the same place\n")
	b.WriteString("- `To account` — only for transfers, the other end\n")
	b.WriteString("- `Debt / loan` — the debt or loan the row belongs to, and its kind (lent,\n" +
		"  borrowed, installment plan, credit, mortgage); `Interest` — how much of a loan\n" +
		"  payment was interest, as my bank stated it\n\n")

	b.WriteString("## How to read it\n\n")
	b.WriteString("- **Transfers are not income or expense.** They move my own money between my\n" +
		"  own accounts. Exclude them from spending totals, or you'll double-count\n")
	b.WriteString("- **Debt in / debt out are not income or expense either.** They are money lent,\n" +
		"  paid back, borrowed or repaid. Installment and mortgage payments, and loan\n" +
		"  interest, *are* expenses: the purchase they pay for was never recorded as one\n")
	b.WriteString("- **Keep currencies apart.** Don't add tenge to dollars. Report each currency on\n" +
		"  its own; convert only if you say which rate you used and that it's an estimate\n")
	b.WriteString("- **An empty category means I didn't label it**, not that it's uncategorised\n" +
		"  spending of a special kind\n")
	b.WriteString("- Amounts in the CSV are plain decimal numbers, already in major units\n\n")

	b.WriteString("## What I want to know\n\n")
	b.WriteString("1. **Where the money actually went.** The largest categories and sources, with\n" +
		"   amounts and shares. Name the specific ones, not \"food and transport\"\n")
	b.WriteString("2. **What looks unusual.** Anything out of line with the rest of the period: a\n" +
		"   single large purchase, an unusually frequent small one, a category that grew\n")
	b.WriteString("3. **Recurring commitments.** Payments that look like subscriptions, rent or\n" +
		"   instalments — what they add up to per month, and which ones I might not need\n")
	b.WriteString("4. **Where I could plausibly cut**, with the amount each change would save and\n" +
		"   how much of my spending it represents. Prefer two or three changes that matter\n" +
		"   over ten that don't\n")
	b.WriteString("5. **Anything the data itself suggests I should look at** that I haven't asked\n" +
		"   about\n\n")

	b.WriteString("## How to answer\n\n")
	b.WriteString("- Use my numbers. Every claim should be traceable to rows in the file\n")
	b.WriteString("- Be specific: \"1,200 USD on X across 14 payments\", not \"significant spending\"\n")
	b.WriteString("- If the period is too short or the data too sparse for a conclusion, say so\n" +
		"  plainly instead of guessing. Don't invent trends from a handful of rows\n")
	b.WriteString("- Skip the moralising. I want to see what's there, not be told to spend less\n")
	b.WriteString("- Start with the three things most worth my attention, then the detail\n")

	if ops < 20 {
		b.WriteString("\n<!-- Note: this period has few operations. Expect the answer to be thin, " +
			"and prefer a longer period for anything about trends. -->\n")
	}
	return b.String()
}
