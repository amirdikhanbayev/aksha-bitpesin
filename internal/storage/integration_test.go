package storage_test

import (
	"context"
	"os"
	"testing"
	"time"

	"aksha-bitpesin/internal/money"
	"aksha-bitpesin/internal/parser"
	"aksha-bitpesin/internal/report"
	"aksha-bitpesin/internal/storage"
)

// This test only runs if TEST_DATABASE_URL is set.
func openStore(t *testing.T) (*storage.Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return st, ctx
}

func TestEndToEnd(t *testing.T) {
	st, ctx := openStore(t)
	loc, _ := time.LoadLocation("Asia/Almaty")
	tgID := time.Now().UnixNano() % 1_000_000_000

	u, err := st.EnsureUser(ctx, tgID, "tester", "Asia/Almaty", "KZT")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	if u2, err := st.EnsureUser(ctx, tgID, "tester", "Asia/Almaty", "KZT"); err != nil || u2.ID != u.ID {
		t.Fatalf("a repeated EnsureUser created a new user: %v %+v", err, u2)
	}

	kaspi, err := st.CreateAccount(ctx, u.ID, "Kaspi Gold", "KZT", 100_000_00)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	usd, err := st.CreateAccount(ctx, u.ID, "Dollars", "USD", 500_00)
	if err != nil {
		t.Fatalf("CreateAccount USD: %v", err)
	}

	food, err := st.CreateCategory(ctx, u.ID, "Groceries", storage.KindExpense, "🛒")
	if err != nil {
		t.Fatalf("CreateCategory groceries: %v", err)
	}
	salary, err := st.CreateCategory(ctx, u.ID, "Salary", storage.KindIncome, "💼")
	if err != nil {
		t.Fatalf("CreateCategory salary: %v", err)
	}
	// A category is found by name afterwards, however it was cased.
	if found, err := st.FindCategory(ctx, u.ID, storage.KindExpense, "groceries"); err != nil || found.ID != food.ID {
		t.Errorf("FindCategory groceries = %v, %v; want the category just created", found.ID, err)
	}

	now := time.Now().In(loc)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	at := month.AddDate(0, 0, 2).Add(12 * time.Hour)

	if _, err := st.CreateTransaction(ctx, storage.Transaction{
		UserID: u.ID, AccountID: kaspi.ID, CategoryID: &food.ID,
		Kind: storage.KindExpense, Amount: 12_500_00, Source: "Magnum", OccurredAt: at,
	}); err != nil {
		t.Fatalf("expense: %v", err)
	}
	if _, err := st.CreateTransaction(ctx, storage.Transaction{
		UserID: u.ID, AccountID: kaspi.ID, CategoryID: &salary.ID,
		Kind: storage.KindIncome, Amount: 400_000_00, Source: "Acme LLC", OccurredAt: at,
	}); err != nil {
		t.Fatalf("income: %v", err)
	}

	// A transfer with exchange: 54,000 KZT → 100 USD at the user's rate of 540
	toAmount := int64(100_00)
	rate := 1.0 / 540.0
	if _, err := st.CreateTransfer(ctx, u.ID, kaspi.ID, usd.ID, 54_000_00, &toAmount, &rate, "exchange", at); err != nil {
		t.Fatalf("transfer: %v", err)
	}

	// Balances: 100000 + 400000 − 12500 − 54000 = 433,500 KZT; 500 + 100 = 600 USD
	accs, err := st.ListAccountsWithBalance(ctx, u.ID, false)
	if err != nil {
		t.Fatalf("balances: %v", err)
	}
	want := map[string]int64{"Kaspi Gold": 433_500_00, "Dollars": 600_00}
	for _, a := range accs {
		if w, ok := want[a.Name]; ok && a.Balance != w {
			t.Errorf("balance %s = %s, want %s", a.Name,
				money.Format(a.Balance, a.Decimals), money.Format(w, a.Decimals))
		}
	}

	// Totals: transfers don't count as income/expense
	totals, err := st.Totals(ctx, u.ID, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Totals: %v", err)
	}
	var kzt storage.CurrencyTotal
	for _, x := range totals {
		if x.Currency == "KZT" {
			kzt = x
		}
	}
	if kzt.Income != 400_000_00 || kzt.Expense != 12_500_00 {
		t.Errorf("KZT totals: income %d, expense %d", kzt.Income, kzt.Expense)
	}

	// Breakdown by source
	srcs, err := st.BySource(ctx, u.ID, storage.KindIncome, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("BySource: %v", err)
	}
	found := false
	for _, s := range srcs {
		if s.Source == "Acme LLC" && s.Total == 400_000_00 {
			found = true
		}
	}
	if !found {
		t.Errorf("income source is missing from the report: %+v", srcs)
	}

	// Rate for the summary line
	if err := st.SetRate(ctx, "KZT", "USD", now, 540); err != nil {
		t.Fatalf("SetRate: %v", err)
	}
	if r, err := st.Rate(ctx, "KZT", "USD", now); err != nil || r != 540 {
		t.Errorf("Rate = %v, %v", r, err)
	}
	if r, err := st.Rate(ctx, "USD", "KZT", now); err != nil || r < 0.00185 || r > 0.00186 {
		t.Errorf("reverse Rate = %v, %v", r, err)
	}

	// Budget
	if err := st.SetBudget(ctx, u.ID, food.ID, month, "KZT", 20_000_00); err != nil {
		t.Fatalf("SetBudget: %v", err)
	}
	buds, err := st.Budgets(ctx, u.ID, month, month, month.AddDate(0, 1, 0))
	if err != nil || len(buds) != 1 {
		t.Fatalf("Budgets: %v %+v", err, buds)
	}
	if buds[0].Spent != 12_500_00 {
		t.Errorf("budget spent = %d, want 1250000", buds[0].Spent)
	}

	// Statement text
	u, _ = st.UserByID(ctx, u.ID)
	p := parser.Period{From: month, To: month.AddDate(0, 1, 0), Label: parser.MonthTitle(month), Month: month}
	text, err := report.Build(ctx, st, u, p, report.Full())
	if err != nil {
		t.Fatalf("report.Build: %v", err)
	}
	for _, must := range []string{"Total", "Groceries", "Acme LLC", "Account balances", "Budgets"} {
		if !contains(text, must) {
			t.Errorf("statement is missing %q:\n%s", must, text)
		}
	}
	t.Log("\n" + text)

	// Soft delete
	last, err := st.LastTransactions(ctx, u.ID, 1)
	if err != nil || len(last) == 0 {
		t.Fatalf("LastTransactions: %v", err)
	}
	if err := st.DeleteTransaction(ctx, u.ID, last[0].ID); err != nil {
		t.Fatalf("DeleteTransaction: %v", err)
	}
	if err := st.DeleteTransaction(ctx, u.ID, last[0].ID); err == nil {
		t.Error("a repeated delete should return an error")
	}
}

// TestConvertToMainUsesPerOperationRate checks that the report's main-currency
// summary prices each operation at the rate in effect on its own date, not a
// single rate for the whole period — and skips currencies with no rate at all
// instead of failing the whole conversion.
func TestConvertToMainUsesPerOperationRate(t *testing.T) {
	st, ctx := openStore(t)
	loc, _ := time.LoadLocation("Asia/Almaty")
	tgID := time.Now().UnixNano()%1_000_000_000 + 1

	u, err := st.EnsureUser(ctx, tgID, "rates-tester", "Asia/Almaty", "KZT")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	kztAcc, err := st.CreateAccount(ctx, u.ID, "Cash KZT", "KZT", 0)
	if err != nil {
		t.Fatalf("CreateAccount KZT: %v", err)
	}
	usdAcc, err := st.CreateAccount(ctx, u.ID, "Card USD", "USD", 0)
	if err != nil {
		t.Fatalf("CreateAccount USD: %v", err)
	}
	eurAcc, err := st.CreateAccount(ctx, u.ID, "Card EUR", "EUR", 0)
	if err != nil {
		t.Fatalf("CreateAccount EUR: %v", err)
	}

	now := time.Now().In(loc)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	day1 := month.AddDate(0, 0, 2).Add(12 * time.Hour)  // rate changes here: 500
	day2 := month.AddDate(0, 0, 12).Add(12 * time.Hour) // rate changes here: 520

	// Two different USD/KZT rates on two different dates within the period.
	if err := st.SetRate(ctx, "KZT", "USD", day1, 500); err != nil {
		t.Fatalf("SetRate day1: %v", err)
	}
	if err := st.SetRate(ctx, "KZT", "USD", day2, 520); err != nil {
		t.Fatalf("SetRate day2: %v", err)
	}

	// Same-currency expense — should pass through with no rate lookup at all.
	if _, err := st.CreateTransaction(ctx, storage.Transaction{
		UserID: u.ID, AccountID: kztAcc.ID, Kind: storage.KindExpense,
		Amount: 5_000_00, OccurredAt: day1,
	}); err != nil {
		t.Fatalf("KZT expense: %v", err)
	}
	// Happens exactly on day1 — must use the 500 rate, not day2's 520.
	if _, err := st.CreateTransaction(ctx, storage.Transaction{
		UserID: u.ID, AccountID: usdAcc.ID, Kind: storage.KindExpense,
		Amount: 100_00, OccurredAt: day1,
	}); err != nil {
		t.Fatalf("USD expense on day1: %v", err)
	}
	// Happens exactly on day2 — must use the 520 rate.
	if _, err := st.CreateTransaction(ctx, storage.Transaction{
		UserID: u.ID, AccountID: usdAcc.ID, Kind: storage.KindExpense,
		Amount: 100_00, OccurredAt: day2,
	}); err != nil {
		t.Fatalf("USD expense on day2: %v", err)
	}
	// No EUR rate exists at all — must be skipped and reported as missing.
	if _, err := st.CreateTransaction(ctx, storage.Transaction{
		UserID: u.ID, AccountID: eurAcc.ID, Kind: storage.KindExpense,
		Amount: 50_00, OccurredAt: day1,
	}); err != nil {
		t.Fatalf("EUR expense: %v", err)
	}

	p := parser.Period{From: month, To: month.AddDate(0, 1, 0)}
	u, err = st.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	c, err := report.ConvertToMain(ctx, st, u, p)
	if err != nil {
		t.Fatalf("ConvertToMain: %v", err)
	}

	// 5000 KZT as-is + 100 USD @500 (=50000) + 100 USD @520 (=52000) = 107000 KZT.
	const wantExpense = 5_000_00 + 50_000_00 + 52_000_00
	if c.Expense != wantExpense {
		t.Errorf("Expense = %d, want %d (per-date rate not applied correctly)", c.Expense, wantExpense)
	}
	if c.Income != 0 {
		t.Errorf("Income = %d, want 0", c.Income)
	}
	if len(c.Missing) != 1 || c.Missing[0] != "EUR" {
		t.Errorf("Missing = %v, want [EUR]", c.Missing)
	}
}

// TestConvertedOperationKeepsUserRate covers a purchase priced in one currency
// but paid from an account in another: the amount debited, what the purchase
// actually cost and the user's own rate are all kept, and the report prices it
// from those facts rather than from any market rate.
func TestConvertedOperationKeepsUserRate(t *testing.T) {
	st, ctx := openStore(t)
	loc, _ := time.LoadLocation("Asia/Almaty")
	tgID := time.Now().UnixNano()%1_000_000_000 + 2

	u, err := st.EnsureUser(ctx, tgID, "conv-tester", "Asia/Almaty", "KZT")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	usdAcc, err := st.CreateAccount(ctx, u.ID, "Card USD", "USD", 0)
	if err != nil {
		t.Fatalf("CreateAccount USD: %v", err)
	}

	now := time.Now().In(loc)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	at := month.AddDate(0, 0, 3).Add(12 * time.Hour)

	// A 5 000 KZT purchase paid with the dollar card at the bank's rate of
	// 1 USD = 470 KZT, i.e. 10.64 USD left the account.
	original := int64(5_000_00)
	rate := 1.0 / 470.0
	saved, err := st.CreateTransaction(ctx, storage.Transaction{
		UserID: u.ID, AccountID: usdAcc.ID, Kind: storage.KindExpense,
		Amount: 10_64, OriginalAmount: &original, OriginalCurrency: "KZT",
		Rate: &rate, Source: "Magnum", OccurredAt: at,
	})
	if err != nil {
		t.Fatalf("converted expense: %v", err)
	}
	if !saved.Converted() {
		t.Fatalf("saved operation is not marked as converted: %+v", saved)
	}
	if saved.OriginalCurrency != "KZT" || saved.OriginalAmount == nil || *saved.OriginalAmount != original {
		t.Errorf("original amount/currency not kept: %v %v", saved.OriginalAmount, saved.OriginalCurrency)
	}
	if saved.Amount != 10_64 || saved.AccountCurr != "USD" {
		t.Errorf("account side = %d %s, want 1064 USD", saved.Amount, saved.AccountCurr)
	}
	if saved.OriginalDecimals != 2 {
		t.Errorf("OriginalDecimals = %d, want 2", saved.OriginalDecimals)
	}

	// No USD/KZT market rate exists at all — the report must still price this
	// operation exactly, from the user's own figures.
	p := parser.Period{From: month, To: month.AddDate(0, 1, 0)}
	u, err = st.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	c, err := report.ConvertToMain(ctx, st, u, p)
	if err != nil {
		t.Fatalf("ConvertToMain: %v", err)
	}
	if c.Expense != original {
		t.Errorf("Expense = %d, want %d (the purchase's own KZT amount)", c.Expense, original)
	}
	if len(c.Missing) != 0 {
		t.Errorf("Missing = %v, want none — the operation carries its own rate", c.Missing)
	}
}

// TestConvertedOperationNeedsRate: a converted operation without the user's rate
// must be refused rather than stored with a guessed one.
func TestConvertedOperationNeedsRate(t *testing.T) {
	st, ctx := openStore(t)
	tgID := time.Now().UnixNano()%1_000_000_000 + 3

	u, err := st.EnsureUser(ctx, tgID, "conv-guard", "Asia/Almaty", "KZT")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	acc, err := st.CreateAccount(ctx, u.ID, "Card USD", "USD", 0)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	original := int64(5_000_00)
	_, err = st.CreateTransaction(ctx, storage.Transaction{
		UserID: u.ID, AccountID: acc.ID, Kind: storage.KindExpense,
		Amount: 10_64, OriginalAmount: &original, OriginalCurrency: "KZT",
		OccurredAt: time.Now(),
	})
	if err == nil {
		t.Error("a converted operation without a rate was accepted; it must be refused")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (len(needle) == 0 ||
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}())
}

// A backfill has to know which days lack a rate: the days with operations in a
// currency that has no rate on or before them.
func TestMissingRateDays(t *testing.T) {
	st, ctx := openStore(t)
	loc, _ := time.LoadLocation("Asia/Almaty")
	u, err := st.EnsureUser(ctx, time.Now().UnixNano()%1_000_000_000+9, "rates", "Asia/Almaty", "KZT")
	if err != nil {
		t.Fatal(err)
	}
	usd, err := st.CreateAccount(ctx, u.ID, "Card", "USD", 0)
	if err != nil {
		t.Fatal(err)
	}
	kzt, err := st.CreateAccount(ctx, u.ID, "Cash", "KZT", 0)
	if err != nil {
		t.Fatal(err)
	}

	month := time.Date(2026, time.March, 1, 0, 0, 0, 0, loc)
	day := func(n int) time.Time { return month.AddDate(0, 0, n-1).Add(12 * time.Hour) }

	// Three USD operations on three days, plus one in the main currency.
	for _, n := range []int{3, 10, 20} {
		if _, err := st.CreateTransaction(ctx, storage.Transaction{
			UserID: u.ID, AccountID: usd.ID, Kind: storage.KindExpense,
			Amount: 10_00, OccurredAt: day(n),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateTransaction(ctx, storage.Transaction{
		UserID: u.ID, AccountID: kzt.ID, Kind: storage.KindExpense,
		Amount: 5_000_00, OccurredAt: day(5),
	}); err != nil {
		t.Fatal(err)
	}

	from, to := month, month.AddDate(0, 1, 0)
	days, err := st.MissingRateDays(ctx, u.ID, "KZT", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 3 {
		t.Fatalf("missing days = %d (%v), want 3 — the KZT operation needs no rate", len(days), days)
	}

	// A rate on the 8th covers the 10th and the 20th, but not the 3rd.
	if err := st.SetRate(ctx, "KZT", "USD", day(8), 470); err != nil {
		t.Fatal(err)
	}
	days, err = st.MissingRateDays(ctx, u.ID, "KZT", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].Format("2006-01-02") != "2026-03-03" {
		t.Errorf("missing days = %v, want only 2026-03-03", days)
	}
}
