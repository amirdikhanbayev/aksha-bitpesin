package storage_test

import (
	"testing"
	"time"

	"aksha-bitpesin/internal/storage"
)

// A transfer's fee comes out of the source account on top of the amount, is the
// only part of it that is spending, and goes with the transfer when it is deleted.
func TestTransferFee(t *testing.T) {
	st, ctx := openStore(t)
	loc, _ := time.LoadLocation("Asia/Almaty")
	u, err := st.EnsureUser(ctx, time.Now().UnixNano()%1_000_000_000+41, "fees", "Asia/Almaty", "KZT")
	if err != nil {
		t.Fatal(err)
	}
	card, _ := st.CreateAccount(ctx, u.ID, "Card", "KZT", 100_000_00)
	cash, _ := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0)
	at := time.Date(2026, time.June, 10, 12, 0, 0, 0, loc)

	tr, err := st.CreateTransfer(ctx, u.ID, card.ID, cash.ID, 50_000_00, nil, nil, "", at, 500_00, "1%")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Fee != 500_00 {
		t.Errorf("transfer's fee = %d, want 500", tr.Fee)
	}
	if b, _ := st.AccountBalance(ctx, u.ID, card.ID); b != 49_500_00 {
		t.Errorf("card = %d, want 49,500: 50,000 out plus the 500 fee", b)
	}
	if b, _ := st.AccountBalance(ctx, u.ID, cash.ID); b != 50_000_00 {
		t.Errorf("cash = %d, want 50,000", b)
	}
	totals, _ := st.Totals(ctx, u.ID, at.AddDate(0, 0, -1), at.AddDate(0, 0, 1))
	if len(totals) != 1 || totals[0].Expense != 500_00 {
		t.Errorf("totals = %+v, want only the 500 fee as spending", totals)
	}
	cats, _ := st.ByCategory(ctx, u.ID, storage.KindExpense, at.AddDate(0, 0, -1), at.AddDate(0, 0, 1))
	if len(cats) != 1 || cats[0].Name != "Transfer fees" {
		t.Errorf("categories = %+v, want the fee under Transfer fees", cats)
	}

	if err := st.DeleteTransaction(ctx, u.ID, tr.ID); err != nil {
		t.Fatal(err)
	}
	if b, _ := st.AccountBalance(ctx, u.ID, card.ID); b != 100_000_00 {
		t.Errorf("card after deleting the transfer = %d, want 100,000: the fee goes with it", b)
	}
}

// A split bill keeps the user's share as the expense, the others' as debts; a
// share forgiven becomes spending under the bill's category without touching the
// account, and a share deleted goes back into the bill.
func TestSplitBillAndForgive(t *testing.T) {
	st, ctx := openStore(t)
	loc, _ := time.LoadLocation("Asia/Almaty")
	u, err := st.EnsureUser(ctx, time.Now().UnixNano()%1_000_000_000+42, "split", "Asia/Almaty", "KZT")
	if err != nil {
		t.Fatal(err)
	}
	card, _ := st.CreateAccount(ctx, u.ID, "Card", "KZT", 100_000_00)
	cash, _ := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0)
	food, _ := st.CreateCategory(ctx, u.ID, "Restaurants", storage.KindExpense, "🍽")
	at := time.Date(2026, time.June, 10, 20, 0, 0, 0, loc)
	from, to := at.AddDate(0, 0, -1), at.AddDate(0, 0, 1)

	bill, err := st.CreateTransaction(ctx, storage.Transaction{UserID: u.ID, AccountID: card.ID,
		CategoryID: &food.ID, Kind: storage.KindExpense, Amount: 30_000_00, Source: "Navat", OccurredAt: at})
	if err != nil {
		t.Fatal(err)
	}
	loans, err := st.SplitTransaction(ctx, u.ID, bill.ID, []storage.Share{
		{Name: "Aidar", Amount: 10_000_00}, {Name: "Dana", Amount: 10_000_00}})
	if err != nil {
		t.Fatal(err)
	}
	if len(loans) != 2 || loans[0].Name != "Aidar · Navat" || loans[0].Outstanding() != 10_000_00 {
		t.Fatalf("loans = %+v, want two 10,000 debts named after the person and the bill", loans)
	}
	if _, err := st.SplitTransaction(ctx, u.ID, bill.ID, []storage.Share{{Name: "X", Amount: 1}}); err == nil {
		t.Error("split the same bill twice")
	}
	balance := func(id int64) int64 {
		t.Helper()
		b, err := st.AccountBalance(ctx, u.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	spent := func() int64 {
		t.Helper()
		totals, err := st.Totals(ctx, u.ID, from, to)
		if err != nil {
			t.Fatal(err)
		}
		if len(totals) == 0 {
			return 0
		}
		return totals[0].Expense
	}
	if got := balance(card.ID); got != 70_000_00 {
		t.Errorf("card = %d, want 70,000: the whole bill left it", got)
	}
	if got := spent(); got != 10_000_00 {
		t.Errorf("spent = %d, want 10,000: only the user's share", got)
	}

	// Aidar sends his share to another account.
	if _, err := st.PayLoan(ctx, u.ID, loans[0].ID, cash.ID, 10_000_00, 0, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := balance(cash.ID); got != 10_000_00 {
		t.Errorf("cash = %d, want 10,000 from Aidar", got)
	}

	// Dana's share is forgiven: spending, under the bill's category; no balance moves.
	tx, err := st.ForgiveLoan(ctx, u.ID, loans[1].ID, 10_000_00, at.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !tx.Forgiven || tx.Kind != storage.KindExpense || tx.CategoryID == nil || *tx.CategoryID != food.ID {
		t.Errorf("forgiven = %+v, want a forgiven expense under Restaurants", tx)
	}
	if got := balance(card.ID); got != 70_000_00 {
		t.Errorf("card after forgiving = %d, want 70,000 still", got)
	}
	if got := spent(); got != 20_000_00 {
		t.Errorf("spent after forgiving = %d, want 20,000: the user's share and Dana's", got)
	}
	dana, _ := st.Loan(ctx, u.ID, loans[1].ID)
	if !dana.PaidOff() || dana.Forgiven != 10_000_00 || dana.PaidTotal != 0 {
		t.Errorf("Dana's debt = %+v, want settled by forgiving, nothing paid", dana)
	}
	if _, err := st.ForgiveLoan(ctx, u.ID, loans[1].ID, 1, at); err == nil {
		t.Error("forgave more than was owed")
	}

	// Deleting Aidar's share puts it back into the bill.
	if err := st.DeleteLoan(ctx, u.ID, loans[0].ID); err != nil {
		t.Fatal(err)
	}
	b, _ := st.Transaction(ctx, u.ID, bill.ID)
	if b.Amount != 20_000_00 {
		t.Errorf("bill after deleting a share = %d, want 20,000", b.Amount)
	}
	if got := balance(card.ID); got != 70_000_00 {
		t.Errorf("card after deleting a share = %d, want 70,000: the bill still left it", got)
	}

	// Deleting the bill takes the remaining shares with it.
	if err := st.DeleteTransaction(ctx, u.ID, bill.ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := st.Loans(ctx, u.ID); len(left) != 0 {
		t.Errorf("loans after deleting the bill = %+v, want none", left)
	}
	if got := balance(card.ID); got != 100_000_00 {
		t.Errorf("card after deleting the bill = %d, want 100,000", got)
	}
}

// A bill paid entirely for others is all lending: the expense is put away, and
// comes back when a share is deleted.
func TestSplitEntirelyAmongOthers(t *testing.T) {
	st, ctx := openStore(t)
	loc, _ := time.LoadLocation("Asia/Almaty")
	u, err := st.EnsureUser(ctx, time.Now().UnixNano()%1_000_000_000+43, "split2", "Asia/Almaty", "KZT")
	if err != nil {
		t.Fatal(err)
	}
	card, _ := st.CreateAccount(ctx, u.ID, "Card", "KZT", 100_000_00)
	at := time.Date(2026, time.June, 10, 20, 0, 0, 0, loc)
	bill, _ := st.CreateTransaction(ctx, storage.Transaction{UserID: u.ID, AccountID: card.ID,
		Kind: storage.KindExpense, Amount: 9_000_00, OccurredAt: at})

	loans, err := st.SplitTransaction(ctx, u.ID, bill.ID, []storage.Share{{Name: "Aidar", Amount: 9_000_00}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Transaction(ctx, u.ID, bill.ID); err == nil {
		t.Error("the bill is still an expense with nothing of it the user's")
	}
	if b, _ := st.AccountBalance(ctx, u.ID, card.ID); b != 91_000_00 {
		t.Errorf("card = %d, want 91,000", b)
	}
	if err := st.DeleteLoan(ctx, u.ID, loans[0].ID); err != nil {
		t.Fatal(err)
	}
	back, err := st.Transaction(ctx, u.ID, bill.ID)
	if err != nil || back.Amount != 9_000_00 {
		t.Errorf("bill after deleting its only share = %+v, %v; want 9,000 back as an expense", back, err)
	}
	if b, _ := st.AccountBalance(ctx, u.ID, card.ID); b != 91_000_00 {
		t.Errorf("card = %d, want 91,000 still", b)
	}
}

// Being let off a debt is income, and moves no money.
func TestForgivenToMeIsIncome(t *testing.T) {
	st, ctx := openStore(t)
	loc, _ := time.LoadLocation("Asia/Almaty")
	u, err := st.EnsureUser(ctx, time.Now().UnixNano()%1_000_000_000+44, "forgive", "Asia/Almaty", "KZT")
	if err != nil {
		t.Fatal(err)
	}
	card, _ := st.CreateAccount(ctx, u.ID, "Card", "KZT", 0)
	at := time.Date(2026, time.June, 10, 12, 0, 0, 0, loc)
	l, err := st.CreateLoan(ctx, storage.Loan{UserID: u.ID, Kind: storage.LoanBorrowed, Name: "Mom",
		Currency: "KZT", Principal: 50_000_00, AccountID: &card.ID, OpenedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ForgiveLoan(ctx, u.ID, l.ID, 20_000_00, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if b, _ := st.AccountBalance(ctx, u.ID, card.ID); b != 50_000_00 {
		t.Errorf("card = %d, want 50,000: what was borrowed, nothing more", b)
	}
	totals, _ := st.Totals(ctx, u.ID, at.AddDate(0, 0, -1), at.AddDate(0, 0, 1))
	if len(totals) != 1 || totals[0].Income != 20_000_00 {
		t.Errorf("totals = %+v, want 20,000 income", totals)
	}
	l, _ = st.Loan(ctx, u.ID, l.ID)
	if l.Outstanding() != 30_000_00 {
		t.Errorf("owed = %d, want 30,000", l.Outstanding())
	}

	bank, _ := st.CreateLoan(ctx, storage.Loan{UserID: u.ID, Kind: storage.LoanCredit, Name: "Bank",
		Currency: "KZT", Principal: 1_000_00, OpenedAt: at})
	if _, err := st.ForgiveLoan(ctx, u.ID, bank.ID, 1_000_00, at); err == nil {
		t.Error("a bank loan was forgiven")
	}
}
