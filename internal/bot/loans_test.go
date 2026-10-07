package bot

import (
	"testing"

	"aksha-bitpesin/internal/storage"
)

// An installment plan: added in a few taps, paid with the monthly amount as a
// button, and each payment is spending filed under the plan.
func TestInstallmentPlanFlow(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 301)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Card", "KZT", 1_000_000_00); err != nil {
		t.Fatal(err)
	}

	p.say("🤝 Debts")
	p.sees("Nothing owed either way")
	p.tap("➕ New debt or loan")
	p.tap("🛍 Installment plan")
	p.say("Kaspi · iPhone")
	p.say("600000") // the only currency the accounts hold: not asked
	p.say("50000")
	p.tap("15")
	p.sees("You owe: 600,000 KZT of 600,000")
	p.sees("Payment: 50,000 KZT on the 15th · about 12 left")

	p.tap("💳 Make a payment") // one KZT account: not asked which
	p.tap("50,000 KZT")
	p.sees("How much of it was interest?")
	p.tap("No interest")
	p.sees("Payment recorded")
	p.sees("Still owed: 550,000 KZT")

	txs, _ := st.LastTransactions(ctx, u.ID, 5)
	if len(txs) != 1 || txs[0].Kind != storage.KindExpense || txs[0].Amount != 50_000_00 ||
		txs[0].LoanName != "Kaspi · iPhone" {
		t.Fatalf("operations = %+v, want one 50,000 expense on the plan", txs)
	}

	// The statement shows what was paid on it.
	p.say("📊 Reports")
	p.tap("This month")
	p.sees("Kaspi · iPhone — paid 50,000 KZT")
	p.sees("Now: you owe 550,000 KZT")
}

// Lending from one account and being paid back: no spending, no income, and the
// debt closes when it is all back.
func TestLentAndPaidBack(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 302)
	u := userOf(t, st, ctx, p)
	cash, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 100_000_00)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccount(ctx, u.ID, "Card", "USD", 1_000_00); err != nil {
		t.Fatal(err)
	}

	p.say("/debts")
	p.tap("➕ New debt or loan")
	p.tap("🤝 I lent money")
	p.say("Aidar")
	p.sees("Which account did the money come from?")
	p.tap("💵 Cash · KZT")
	p.say("40000")
	p.sees("Owes you: 40,000 KZT")
	if b, _ := st.AccountBalance(ctx, u.ID, cash.ID); b != 60_000_00 {
		t.Errorf("cash after lending = %d, want 60,000", b)
	}

	p.tap("💰 Got money back") // only one KZT account
	p.tap("All: 40,000")
	p.sees("All paid back")
	if b, _ := st.AccountBalance(ctx, u.ID, cash.ID); b != 100_000_00 {
		t.Errorf("cash after being paid back = %d, want 100,000", b)
	}

	// The name is offered the next time.
	p.say("/debts")
	p.tap("➕ New debt or loan")
	p.tap("🙏 I borrowed")
	p.tap("Aidar")
	p.sees("Which account did the money land on?")
}

// A credit that landed on an account: the payment is split into principal and
// interest, and only the interest is spending.
func TestCreditPaymentWithInterest(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 303)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Card", "KZT", 0); err != nil {
		t.Fatal(err)
	}

	p.say("🤝 Debts")
	p.tap("➕ New debt or loan")
	p.tap("🏦 Credit")
	p.say("Halyk · car")
	p.tap("💳 Card · KZT")
	p.say("300000")
	p.tap("⏭ Skip")
	p.sees("You owe: 300,000 KZT")

	p.tap("💳 Make a payment")
	p.say("30000")
	p.say("5000")
	p.sees("Paid 30,000 KZT: 25,000 off the debt, 5,000 interest")
	p.sees("Still owed: 275,000 KZT")

	loans, _ := st.Loans(ctx, u.ID)
	if len(loans) != 1 || loans[0].InterestPaid != 5_000_00 || loans[0].Outstanding() != 275_000_00 {
		t.Fatalf("loan = %+v, want 275,000 owed and 5,000 interest paid", loans)
	}
}

// Buttons of the loan wizard don't act once the wizard is over.
func TestLoanWizardButtonsGoStale(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 304)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Card", "KZT", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAccount(ctx, u.ID, "Card", "USD", 0); err != nil {
		t.Fatal(err) // two currencies, so the currency is asked
	}
	p.say("🤝 Debts")
	p.tap("➕ New debt or loan")
	p.tap("🛍 Installment plan")
	p.say("Shop")
	msg := p.messageWith("loancur:KZT")
	p.tap("KZT")
	p.say("1000")
	p.tap("⏭ Skip")
	p.pressAt(msg, "loancur:KZT") // the old currency button
	p.say("1000")                 // and an amount, which now means nothing

	loans, _ := st.Loans(ctx, u.ID)
	if len(loans) != 1 {
		t.Fatalf("loans = %d, want 1", len(loans))
	}
}

// Paid out of habit through ➖ Expense: the category step offers the loan, and
// the payment lands on it instead of in a category.
func TestLoanPaymentFromTheExpenseFlow(t *testing.T) {
	st, ctx := openBotStore(t)
	p := newPerson(t, st, 305)
	u := userOf(t, st, ctx, p)
	if _, err := st.CreateAccount(ctx, u.ID, "Card", "KZT", 100_000_00); err != nil {
		t.Fatal(err)
	}
	withCategories(t, st, ctx, u, storage.KindExpense, "Groceries")
	if _, err := st.CreateLoan(ctx, storage.Loan{UserID: u.ID, Kind: storage.LoanInstallment,
		Name: "TV", Currency: "KZT", Principal: 90_000_00, OpenedAt: p.b.clock()}); err != nil {
		t.Fatal(err)
	}

	p.say("➖ Expense")
	p.say("30000")
	p.tap("🏦 It's a loan or debt payment — TV")
	p.tap("No interest")
	p.sees("Still owed: 60,000 KZT")

	// Income offers nothing: nobody owes this user.
	p.say("➕ Income")
	p.say("1000")
	if p.hasButton("🤝 It's a debt paid back to me") {
		t.Error("offered a debt paid back with none owed")
	}
}
