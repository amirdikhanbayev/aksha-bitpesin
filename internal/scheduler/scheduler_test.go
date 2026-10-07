package scheduler

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"aksha-bitpesin/internal/config"
	"aksha-bitpesin/internal/storage"
)

type fakeSender struct {
	msgs    []string
	periods []string // the period each statement was sent for
	loans   []int64  // the loans a payment reminder was sent for
}

func (f *fakeSender) SendLoanReminder(_ context.Context, _ int64, text string, loanID int64) {
	f.msgs = append(f.msgs, text)
	f.loans = append(f.loans, loanID)
}

func (f *fakeSender) Send(_ context.Context, _ int64, text string) {
	f.msgs = append(f.msgs, text)
}

func (f *fakeSender) SendStatement(_ context.Context, _ int64, text, period string) {
	f.msgs = append(f.msgs, text)
	f.periods = append(f.periods, period)
}

func TestMonthlyReportAndRecurring(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	loc, _ := time.LoadLocation("Asia/Almaty")
	u, err := st.EnsureUser(ctx, time.Now().UnixNano()%1_000_000_000, "sched", "Asia/Almaty", "KZT")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	acc, err := st.CreateAccount(ctx, u.ID, "Test account", "KZT", 0)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	food, err := st.CreateCategory(ctx, u.ID, "Groceries", storage.KindExpense, "🛒")
	if err != nil {
		t.Fatalf("CreateCategory: %v", err)
	}

	// "Today" — the 1st of the current month, 10am.
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), 1, 10, 0, 0, 0, loc)
	prevMonth := today.AddDate(0, -1, 0)

	// An operation last month — so the statement has something to show.
	if _, err := st.CreateTransaction(ctx, storage.Transaction{
		UserID: u.ID, AccountID: acc.ID, CategoryID: &food.ID,
		Kind: storage.KindExpense, Amount: 7_000_00, Source: "Small",
		OccurredAt: prevMonth.AddDate(0, 0, 4),
	}); err != nil {
		t.Fatalf("last month's operation: %v", err)
	}

	// Recurring income on the 1st.
	salary, err := st.CreateCategory(ctx, u.ID, "Salary", storage.KindIncome, "💼")
	if err != nil {
		t.Fatalf("CreateCategory salary: %v", err)
	}
	if _, err := st.CreateRecurring(ctx, storage.Recurring{
		UserID: u.ID, AccountID: acc.ID, CategoryID: salary.ID,
		Kind: storage.KindIncome, Amount: 500_000_00, DayOfMonth: 1, Note: "monthly pay",
	}); err != nil {
		t.Fatalf("CreateRecurring: %v", err)
	}

	out := &fakeSender{}
	s := New(st, out, config.Config{SchedulerEvery: time.Hour, FetchRates: false})
	s.clock = func() time.Time { return today }

	s.tick(ctx)

	var gotReport, gotRecurring bool
	for _, m := range out.msgs {
		if strings.Contains(m, "Monthly statement") && strings.Contains(m, "Small") {
			gotReport = true
		}
		if strings.Contains(m, "recurring operation") {
			gotRecurring = true
		}
	}
	if !gotReport {
		t.Errorf("last month's statement was not sent: %v", out.msgs)
	}
	// Sent as a statement, so it carries the same buttons as one asked for, and
	// they name last month.
	// (The database is shared with other tests, so other users' statements may be
	// in the list too — every one of them must still be for last month.)
	want := prevMonth.Format("2006-01")
	if len(out.periods) == 0 {
		t.Error("no statement was sent with its period")
	}
	for _, got := range out.periods {
		if got != want {
			t.Errorf("statement period = %q, want %q", got, want)
		}
	}
	if !gotRecurring {
		t.Errorf("recurring operation was not created: %v", out.msgs)
	}

	// A repeated tick must not duplicate either the statement or the recurring operation.
	before := len(out.msgs)
	s.tick(ctx)
	if len(out.msgs) != before {
		t.Errorf("a repeated tick duplicated messages: %v", out.msgs[before:])
	}

	// The salary was recorded exactly once.
	from := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, loc)
	txs, err := st.ListTransactions(ctx, u.ID, from, from.AddDate(0, 1, 0), 100)
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	n := 0
	for _, tx := range txs {
		if tx.Kind == storage.KindIncome && tx.Amount == 500_000_00 {
			n++
		}
	}
	if n != 1 {
		t.Errorf("recurring income was recorded %d times, want 1", n)
	}
}

// The evening nudge goes to someone who recorded nothing today, once, and skips
// everyone else.
func TestEveningReminder(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	loc, _ := time.LoadLocation("Asia/Almaty")
	evening := time.Date(time.Now().In(loc).Year(), time.Now().In(loc).Month(),
		time.Now().In(loc).Day(), 21, 30, 0, 0, loc)

	newUser := func(name string) storage.User {
		t.Helper()
		u, err := st.EnsureUser(ctx, time.Now().UnixNano()%1_000_000_000, name, "Asia/Almaty", "KZT")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.MarkOnboarded(ctx, u.ID); err != nil {
			t.Fatal(err)
		}
		u, _ = st.UserByID(ctx, u.ID)
		return u
	}

	// Recorded nothing today: gets nudged.
	idle := newUser("idle")

	// Recorded something today: left alone.
	busy := newUser("busy")
	acc, err := st.CreateAccount(ctx, busy.ID, "Cash", "KZT", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTransaction(ctx, storage.Transaction{
		UserID: busy.ID, AccountID: acc.ID, Kind: storage.KindExpense,
		Amount: 100_00, OccurredAt: evening.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// Turned it off: left alone.
	quiet := newUser("quiet")
	if err := st.SetDailyReminder(ctx, quiet.ID, false); err != nil {
		t.Fatal(err)
	}
	quiet, _ = st.UserByID(ctx, quiet.ID) // re-read: the setting lives on the row

	out := &fakeSender{}
	s := New(st, out, config.Config{SchedulerEvery: time.Hour, FetchRates: false})
	s.clock = func() time.Time { return evening }

	nudges := func() int {
		n := 0
		for _, m := range out.msgs {
			if strings.Contains(m, "Nothing recorded today") {
				n++
			}
		}
		return n
	}

	// Too early in the day: nothing goes out.
	s.clock = func() time.Time { return evening.Add(-6 * time.Hour) }
	s.sendReminder(ctx, idle)
	if nudges() != 0 {
		t.Errorf("nudged in the afternoon: %v", out.msgs)
	}

	s.clock = func() time.Time { return evening }
	s.sendReminder(ctx, idle)
	s.sendReminder(ctx, busy)
	s.sendReminder(ctx, quiet)
	if nudges() != 1 {
		t.Errorf("nudges sent = %d, want exactly one (the idle user): %v", nudges(), out.msgs)
	}

	// And not twice in one evening.
	fresh, _ := st.UserByID(ctx, idle.ID)
	s.sendReminder(ctx, fresh)
	if nudges() != 1 {
		t.Errorf("nudged twice in one day: %v", out.msgs)
	}
}

// Two passes over the same recurring operation at once — a trigger arriving
// while the ticker runs — still record it once: the month is claimed in the
// same transaction that records the operation.
func TestRecurringIsRecordedOnceUnderConcurrency(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	loc, _ := time.LoadLocation("Asia/Almaty")
	u, err := st.EnsureUser(ctx, time.Now().UnixNano()%1_000_000_000+7, "conc", "Asia/Almaty", "KZT")
	if err != nil {
		t.Fatal(err)
	}
	acc, err := st.CreateAccount(ctx, u.ID, "Card", "KZT", 0)
	if err != nil {
		t.Fatal(err)
	}
	rent, err := st.CreateCategory(ctx, u.ID, "Rent", storage.KindExpense, "")
	if err != nil {
		t.Fatal(err)
	}
	r := storage.Recurring{UserID: u.ID, AccountID: acc.ID, CategoryID: rent.ID,
		Kind: storage.KindExpense, Amount: 200_000_00, DayOfMonth: 1}
	if r.ID, err = st.CreateRecurring(ctx, r); err != nil {
		t.Fatal(err)
	}

	month := time.Date(2026, time.May, 1, 0, 0, 0, 0, loc)
	at := month.Add(12 * time.Hour)
	results := make(chan bool, 8)
	for range 8 {
		go func() {
			_, created, err := st.RecordRecurring(ctx, r, month, at)
			if err != nil {
				t.Error(err)
			}
			results <- created
		}()
	}
	created := 0
	for range 8 {
		if <-results {
			created++
		}
	}
	if created != 1 {
		t.Errorf("created %d times, want 1", created)
	}
	txs, err := st.ListTransactions(ctx, u.ID, month, month.AddDate(0, 1, 0), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 1 {
		t.Errorf("operations in the month = %d, want 1", len(txs))
	}
}

// A loan payment is reminded about on its day, once, and not at all once the
// month's payment is recorded.
func TestLoanPaymentReminder(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	st, err := storage.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	loc, _ := time.LoadLocation("Asia/Almaty")
	u, err := st.EnsureUser(ctx, time.Now().UnixNano()%1_000_000_000+11, "loanrem", "Asia/Almaty", "KZT")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkOnboarded(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	card, err := st.CreateAccount(ctx, u.ID, "Card", "KZT", 0)
	if err != nil {
		t.Fatal(err)
	}
	day, monthly := 31, int64(50_000_00) // the 31st lands on the 30th in June
	l, err := st.CreateLoan(ctx, storage.Loan{UserID: u.ID, Kind: storage.LoanInstallment, Name: "Phone",
		Currency: "KZT", Principal: 600_000_00, MonthlyPayment: &monthly, PaymentDay: &day,
		OpenedAt: time.Date(2026, time.May, 1, 12, 0, 0, 0, loc)})
	if err != nil {
		t.Fatal(err)
	}

	out := &fakeSender{}
	s := New(st, out, config.Config{SchedulerEvery: time.Hour})
	reminded := func() int {
		n := 0
		for _, id := range out.loans {
			if id == l.ID {
				n++
			}
		}
		return n
	}

	s.clock = func() time.Time { return time.Date(2026, time.June, 29, 11, 0, 0, 0, loc) }
	s.remindLoanPayments(ctx, u)
	if reminded() != 0 {
		t.Fatal("reminded before the payment day")
	}
	s.clock = func() time.Time { return time.Date(2026, time.June, 30, 11, 0, 0, 0, loc) }
	s.remindLoanPayments(ctx, u)
	s.remindLoanPayments(ctx, u)
	if reminded() != 1 {
		t.Fatalf("reminders on the day = %d, want 1", reminded())
	}

	// Paid in July before the day: no reminder that month.
	if _, err := st.PayLoan(ctx, u.ID, l.ID, card.ID, monthly, 0,
		time.Date(2026, time.July, 20, 12, 0, 0, 0, loc)); err != nil {
		t.Fatal(err)
	}
	s.clock = func() time.Time { return time.Date(2026, time.July, 31, 11, 0, 0, 0, loc) }
	s.remindLoanPayments(ctx, u)
	if reminded() != 1 {
		t.Errorf("reminded about a month already paid")
	}
}
