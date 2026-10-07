// Package scheduler sends monthly statements, creates recurring operations,
// and refreshes exchange rates.
package scheduler

import (
	"context"
	"html"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"aksha-bitpesin/internal/config"
	"aksha-bitpesin/internal/money"
	"aksha-bitpesin/internal/parser"
	"aksha-bitpesin/internal/rates"
	"aksha-bitpesin/internal/report"
	"aksha-bitpesin/internal/storage"
)

// Sender — what the scheduler uses to message a user (this is *bot.Bot).
type Sender interface {
	Send(ctx context.Context, chatID int64, text string)
	// SendStatement delivers a statement with its buttons; period names what it covers.
	SendStatement(ctx context.Context, chatID int64, text, period string)
	// SendLoanReminder delivers a payment reminder with a button that records it.
	SendLoanReminder(ctx context.Context, chatID int64, text string, loanID int64)
}

type Scheduler struct {
	// mu makes passes take turns: the internal ticker and POST /tasks/run may
	// fire at the same moment, and one pass at a time is all the work needs.
	mu sync.Mutex

	st     *storage.Store
	out    Sender
	cfg    config.Config
	http   *http.Client
	clock  func() time.Time
	ratesD string // date the rates were last refreshed for (YYYY-MM-DD)
}

func New(st *storage.Store, out Sender, cfg config.Config) *Scheduler {
	return &Scheduler{
		st:    st,
		out:   out,
		cfg:   cfg,
		http:  &http.Client{Timeout: 20 * time.Second},
		clock: time.Now,
	}
}

// Run loops until the context is canceled. A non-positive interval disables the
// internal ticker: on platforms that sleep the instance between requests there
// is nothing to tick, and an external cron calls Tick over HTTP instead.
func (s *Scheduler) Run(ctx context.Context) {
	if s.cfg.SchedulerEvery <= 0 {
		slog.Info("internal scheduler disabled, expecting external triggers")
		<-ctx.Done()
		return
	}
	ticker := time.NewTicker(s.cfg.SchedulerEvery)
	defer ticker.Stop()

	s.Tick(ctx) // run immediately at startup instead of waiting for the first tick
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Tick(ctx)
		}
	}
}

// Tick does one pass: rates, recurring operations, monthly statements. Safe to
// call repeatedly — each step records what it already did.
func (s *Scheduler) Tick(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tick(ctx)
}

func (s *Scheduler) tick(ctx context.Context) {
	if s.cfg.FetchRates {
		s.refreshRates(ctx)
	}
	users, err := s.st.AllUsers(ctx)
	if err != nil {
		slog.Error("AllUsers", "err", err)
		return
	}
	for _, u := range users {
		s.runRecurring(ctx, u)
		s.sendMonthly(ctx, u)
		s.sendReminder(ctx, u)
		s.remindLoanPayments(ctx, u)
	}
}

// loanReminderHour is the local hour from which payment reminders go out.
const loanReminderHour = 10

// remindLoanPayments reminds about a loan payment on its day, once a month,
// unless a payment was already recorded this month. A missed day — the bot was
// down — is caught up on later in the month, saying when it was due.
func (s *Scheduler) remindLoanPayments(ctx context.Context, u storage.User) {
	loc := u.Location()
	now := s.clock().In(loc)
	if now.Hour() < loanReminderHour {
		return
	}
	loans, err := s.st.Loans(ctx, u.ID)
	if err != nil {
		slog.Error("Loans", "err", err)
		return
	}
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	lastDay := month.AddDate(0, 1, -1).Day()
	for _, l := range loans {
		if l.PaymentDay == nil || l.OwedToMe() || l.PaidOff() {
			continue
		}
		due := min(*l.PaymentDay, lastDay)
		if now.Day() < due {
			continue
		}
		if l.OpenedAt.In(loc).After(time.Date(now.Year(), now.Month(), due, 0, 0, 0, 0, loc)) {
			continue // opened after this month's payment day: the first one is next month
		}
		paid, err := s.st.LoanPaidSince(ctx, l.ID, month)
		if err != nil {
			slog.Error("LoanPaidSince", "err", err)
			continue
		}
		if paid {
			continue
		}
		claimed, err := s.st.ClaimLoanReminder(ctx, l.ID, month)
		if err != nil || !claimed {
			if err != nil {
				slog.Error("ClaimLoanReminder", "err", err)
			}
			continue
		}
		when := "due today"
		if now.Day() > due {
			when = "was due on the " + parser.Ordinal(due)
		}
		text := "🔔 <b>Payment " + when + ":</b> " + storage.LoanEmoji(l.Kind) + " " + html.EscapeString(l.Name)
		if l.MonthlyPayment != nil {
			text += " — " + money.FormatCode(*l.MonthlyPayment, l.Decimals, l.Currency)
		}
		text += "\n<i>Owed: " + money.FormatCode(l.Outstanding(), l.Decimals, l.Currency) + "</i>"
		s.out.SendLoanReminder(ctx, u.TelegramID, text, l.ID)
	}
}

// sendMonthly sends last month's statement on the 1st, after 9am in the user's timezone.
func (s *Scheduler) sendMonthly(ctx context.Context, u storage.User) {
	if !u.MonthlyReport {
		return
	}
	loc := u.Location()
	now := s.clock().In(loc)
	if now.Hour() < 9 {
		return
	}
	prev := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, -1, 0)
	if u.LastReportMonth != nil && !u.LastReportMonth.Before(prev) {
		return // already sent
	}

	p := parser.Period{From: prev, To: prev.AddDate(0, 1, 0), Label: parser.MonthTitle(prev), Month: prev}
	count, err := s.st.CountTransactions(ctx, u.ID, p.From, p.To)
	if err != nil {
		slog.Error("CountTransactions", "err", err)
		return
	}
	// Nothing to show — just mark the month as closed so we don't send empty statements.
	if count == 0 {
		if err := s.st.MarkReportSent(ctx, u.ID, prev); err != nil {
			slog.Error("MarkReportSent", "err", err)
		}
		return
	}

	text, err := report.Build(ctx, s.st, u, p, report.Full())
	if err != nil {
		slog.Error("report.Build", "err", err)
		return
	}
	s.out.SendStatement(ctx, u.TelegramID, "🗓 <b>Monthly statement</b>\n\n"+text, p.Arg())
	if err := s.st.MarkReportSent(ctx, u.ID, prev); err != nil {
		slog.Error("MarkReportSent", "err", err)
		return
	}
	slog.Info("statement sent", "user", u.ID, "month", prev.Format("2006-01"))
}

// reminderHour is the local hour the evening nudge goes out, once the day is
// mostly spent but not over.
const reminderHour = 21

// sendReminder nudges someone who recorded nothing today. Habits are what the
// bot lives on, and the day's spending is easiest to remember on the same day.
// Nothing is sent to someone who already recorded something, who turned it off,
// or who hasn't finished the setup.
func (s *Scheduler) sendReminder(ctx context.Context, u storage.User) {
	if !u.DailyReminder || !u.Onboarded() {
		return
	}
	loc := u.Location()
	now := s.clock().In(loc)
	if now.Hour() < reminderHour {
		return
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	if u.LastReminder != nil && !u.LastReminder.Before(today) {
		return // already nudged today
	}

	recorded, err := s.st.HasOperationsOn(ctx, u.ID, today, today.AddDate(0, 0, 1))
	if err != nil {
		slog.Error("HasOperationsOn", "err", err)
		return
	}
	if err := s.st.MarkReminderSent(ctx, u.ID, today); err != nil {
		slog.Error("MarkReminderSent", "err", err)
		return
	}
	if recorded {
		return // nothing to nudge about
	}
	s.out.Send(ctx, u.TelegramID,
		"🌙 Nothing recorded today. Spent anything? It's easiest to remember now.\n"+
			"<i>Turn this off in ⚙️ Settings.</i>")
}

// runRecurring creates operations on schedule (salary, rent, subscriptions).
func (s *Scheduler) runRecurring(ctx context.Context, u storage.User) {
	loc := u.Location()
	now := s.clock().In(loc)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	lastDay := month.AddDate(0, 1, -1).Day()

	due, err := s.st.DueRecurring(ctx, u.ID, month, now.Day(), lastDay)
	if err != nil {
		slog.Error("DueRecurring", "err", err)
		return
	}
	for _, r := range due {
		day := min(r.DayOfMonth, lastDay)
		at := time.Date(now.Year(), now.Month(), day, 12, 0, 0, 0, loc)
		t, created, err := s.st.RecordRecurring(ctx, r, month, at)
		if err != nil {
			slog.Error("recurring operation", "recurring", r.ID, "err", err)
		}
		if !created || err != nil {
			continue
		}
		sign := "−"
		if r.Kind == storage.KindIncome {
			sign = "+"
		}
		s.out.Send(ctx, u.TelegramID, "🔁 Recorded a recurring operation:\n<b>"+sign+
			money.FormatCode(t.Amount, t.Decimals, t.AccountCurr)+"</b> · "+html.EscapeString(t.CategoryName)+
			"\n<i>Doesn't look right? Fix it: /last</i>")
	}
}

// refreshRates pulls the official NBRK rates once a day.
func (s *Scheduler) refreshRates(ctx context.Context) {
	today := s.clock().Format("2006-01-02")
	if s.ratesD == today {
		return
	}
	quotes, err := rates.FetchNBRK(ctx, s.http, s.clock())
	if err != nil {
		slog.Warn("NBRK rates unavailable", "err", err)
		return
	}
	have, err := s.st.DecimalsMap(ctx)
	if err != nil {
		slog.Error("DecimalsMap", "err", err)
		return
	}
	saved := 0
	for _, q := range quotes {
		if _, ok := have[q.Code]; !ok {
			continue
		}
		if err := s.st.SetRate(ctx, storage.PivotCurrency, q.Code, q.Date, q.Rate); err != nil {
			slog.Error("SetRate", "err", err)
			continue
		}
		saved++
	}
	s.ratesD = today
	slog.Info("rates refreshed", "count", saved)
}
