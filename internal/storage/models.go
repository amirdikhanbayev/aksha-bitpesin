package storage

import "time"

const (
	KindIncome   = "income"
	KindExpense  = "expense"
	KindTransfer = "transfer"

	// Money in or out of an account that is neither income nor an expense:
	// lending, being paid back, borrowing, repaying the principal of a debt.
	KindDebtIn  = "debt_in"
	KindDebtOut = "debt_out"
)

type User struct {
	ID              int64
	TelegramID      int64
	Username        string
	Timezone        string
	MainCurrency    string
	MonthlyReport   bool
	LastReportMonth *time.Time
	CreatedAt       time.Time
	OnboardedAt     *time.Time // when the first-run setup was finished or skipped
	DailyReminder   bool       // send the evening nudge to record the day
	LastReminder    *time.Time // the day the nudge was last sent
}

// Onboarded reports whether the first-run setup is behind this user.
func (u User) Onboarded() bool { return u.OnboardedAt != nil }

// Location — the user's timezone; falls back to UTC if the value is broken.
func (u User) Location() *time.Location {
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

type Currency struct {
	Code     string
	Name     string
	Decimals int
}

type Account struct {
	ID             int64
	UserID         int64
	Name           string
	Currency       string
	InitialBalance int64
	Archived       bool
	Decimals       int   // from currencies, pulled in via join
	Balance        int64 // current balance, filled in by ListAccountsWithBalance
}

type Category struct {
	ID       int64
	UserID   int64
	Name     string
	Kind     string
	Emoji    string
	Archived bool
}

// Title — "🛒 Groceries" or just the name if there is no emoji.
func (c Category) Title() string {
	if c.Emoji == "" {
		return c.Name
	}
	return c.Emoji + " " + c.Name
}

type Transaction struct {
	ID          int64
	UserID      int64
	AccountID   int64
	CategoryID  *int64
	Kind        string
	Amount      int64
	ToAccountID *int64
	ToAmount    *int64
	Rate        *float64
	Note        string
	Source      string
	OccurredAt  time.Time

	// The operation's own currency, when it differs from the account's:
	// a purchase priced in KZT paid from a USD account. Amount stays in the
	// account's currency; these hold what the purchase actually cost.
	OriginalAmount   *int64
	OriginalCurrency string

	// The loan or debt the operation belongs to, and for a payment on one, how
	// much of it was interest.
	LoanID   *int64
	Interest *int64

	// Denormalized fields for display
	AccountName      string
	AccountCurr      string
	Decimals         int
	ToAccountName    string
	ToAccountCurr    string
	ToDecimals       int
	CategoryName     string
	CategoryEmoji    string
	OriginalDecimals int
	LoanName         string
	LoanKind         string
}

// Converted reports whether the operation was entered in a currency other than
// its account's, so the user's own exchange rate applies to it.
func (t Transaction) Converted() bool {
	return t.OriginalCurrency != "" && t.OriginalAmount != nil && t.Rate != nil
}

type Budget struct {
	CategoryID   int64
	CategoryName string
	Month        time.Time
	Currency     string
	LimitAmount  int64
	Spent        int64
	Decimals     int
}

type Recurring struct {
	ID               int64
	UserID           int64
	AccountID        int64
	CategoryID       int64
	Kind             string
	Amount           int64
	DayOfMonth       int
	Note             string
	Active           bool
	LastCreatedMonth *time.Time

	AccountName  string
	AccountCurr  string
	Decimals     int
	CategoryName string
}
