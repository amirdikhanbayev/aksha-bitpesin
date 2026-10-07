package storage_test

import (
	"testing"
	"time"

	"aksha-bitpesin/internal/storage"
)

// Every kind of debt, and what it does to the account, to what is owed, and to
// the period's income and expenses.
func TestLoansMoveMoneyTheRightWay(t *testing.T) {
	st, ctx := openStore(t)
	loc, _ := time.LoadLocation("Asia/Almaty")
	u, err := st.EnsureUser(ctx, time.Now().UnixNano()%1_000_000_000+31, "loans", "Asia/Almaty", "KZT")
	if err != nil {
		t.Fatal(err)
	}
	card, err := st.CreateAccount(ctx, u.ID, "Card", "KZT", 1_000_000_00)
	if err != nil {
		t.Fatal(err)
	}
	month := time.Date(2026, time.June, 1, 0, 0, 0, 0, loc)
	at := func(day int) time.Time { return month.AddDate(0, 0, day-1).Add(12 * time.Hour) }

	balance := func() int64 {
		t.Helper()
		b, err := st.AccountBalance(ctx, u.ID, card.ID)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	outstanding := func(id int64) int64 {
		t.Helper()
		l, err := st.Loan(ctx, u.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		return l.Outstanding()
	}
	pay := func(id, amount, interest int64, day int) storage.Transaction {
		t.Helper()
		tx, err := st.PayLoan(ctx, u.ID, id, card.ID, amount, interest, at(day))
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}

	// Lent 50,000 from the card: the money leaves, someone owes it back.
	lent, err := st.CreateLoan(ctx, storage.Loan{UserID: u.ID, Kind: storage.LoanLent, Name: "Aidar",
		Currency: "KZT", Principal: 50_000_00, AccountID: &card.ID, OpenedAt: at(1)})
	if err != nil {
		t.Fatal(err)
	}
	if got := balance(); got != 950_000_00 {
		t.Fatalf("after lending: balance = %d, want 950,000", got)
	}
	pay(lent.ID, 20_000_00, 0, 5) // 20,000 back
	if got := outstanding(lent.ID); got != 30_000_00 {
		t.Errorf("Aidar owes %d, want 30,000", got)
	}

	// A credit of 300,000 that landed on the card, repaid 30,000 of which 5,000
	// interest: only the interest is spending.
	credit, err := st.CreateLoan(ctx, storage.Loan{UserID: u.ID, Kind: storage.LoanCredit, Name: "Halyk",
		Currency: "KZT", Principal: 300_000_00, AccountID: &card.ID, OpenedAt: at(2)})
	if err != nil {
		t.Fatal(err)
	}
	pay(credit.ID, 30_000_00, 5_000_00, 10)
	if got := outstanding(credit.ID); got != 275_000_00 {
		t.Errorf("credit outstanding = %d, want 275,000", got)
	}

	// An installment paid straight to the shop: each payment is the spending.
	inst, err := st.CreateLoan(ctx, storage.Loan{UserID: u.ID, Kind: storage.LoanInstallment,
		Name: "Kaspi · iPhone", Currency: "KZT", Principal: 600_000_00, OpenedAt: at(3)})
	if err != nil {
		t.Fatal(err)
	}
	tx := pay(inst.ID, 50_000_00, 0, 15)
	if tx.Kind != storage.KindExpense || tx.LoanName != "Kaspi · iPhone" {
		t.Errorf("installment payment = %s on %q, want an expense on the installment", tx.Kind, tx.LoanName)
	}
	if got := outstanding(inst.ID); got != 550_000_00 {
		t.Errorf("installment outstanding = %d, want 550,000", got)
	}

	// The card: 1,000,000 − 50,000 lent + 20,000 back + 300,000 credit − 30,000 − 50,000.
	if got, want := balance(), int64(1_190_000_00); got != want {
		t.Errorf("balance = %d, want %d", got, want)
	}

	// Spending is the interest and the installment payment — not the lending, not
	// the principal of the credit, and the credit coming in isn't income.
	totals, err := st.Totals(ctx, u.ID, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(totals) != 1 || totals[0].Expense != 55_000_00 || totals[0].Income != 0 {
		t.Errorf("totals = %+v, want expenses 55,000 and no income", totals)
	}

	// The category breakdown lists the payments under the loan, not "No category".
	cats, err := st.ByCategory(ctx, u.ID, storage.KindExpense, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int64{}
	for _, c := range cats {
		names[c.Name] = c.Total
	}
	if names["Kaspi · iPhone"] != 50_000_00 || names["Halyk"] != 5_000_00 {
		t.Errorf("categories = %v, want the installment and the credit's interest by name", names)
	}

	// Correcting what is owed to match the bank.
	if err := st.SetLoanOutstanding(ctx, u.ID, credit.ID, 270_000_00); err != nil {
		t.Fatal(err)
	}
	if got := outstanding(credit.ID); got != 270_000_00 {
		t.Errorf("after correction: %d, want 270,000", got)
	}

	// Paying off the rest closes it.
	pay(lent.ID, 30_000_00, 0, 20)
	l, _ := st.Loan(ctx, u.ID, lent.ID)
	if !l.PaidOff() {
		t.Errorf("Aidar's debt should be paid off, outstanding %d", l.Outstanding())
	}

	// A loan in another currency can't be paid from a KZT account.
	usd, err := st.CreateLoan(ctx, storage.Loan{UserID: u.ID, Kind: storage.LoanBorrowed, Name: "Bob",
		Currency: "USD", Principal: 100_00, OpenedAt: at(4)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PayLoan(ctx, u.ID, usd.ID, card.ID, 10_00, 0, at(5)); err == nil {
		t.Error("paid a USD loan from a KZT account")
	}

	// Deleting a loan takes its operations with it.
	before := balance()
	if err := st.DeleteLoan(ctx, u.ID, credit.ID); err != nil {
		t.Fatal(err)
	}
	if got, want := balance(), before-300_000_00+30_000_00; got != want {
		t.Errorf("after deleting the credit: balance = %d, want %d", got, want)
	}

	// The payment reminder is claimed once per month.
	first, _ := st.ClaimLoanReminder(ctx, inst.ID, month)
	again, _ := st.ClaimLoanReminder(ctx, inst.ID, month)
	if !first || again {
		t.Errorf("reminder claims = %v, %v; want true, false", first, again)
	}

	// Erasing the user takes the loans too.
	if err := st.DeleteUserData(ctx, u.ID); err != nil {
		t.Fatalf("erase with loans: %v", err)
	}
}
