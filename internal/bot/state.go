package bot

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// Dialog states. Stored in user_states, so they survive a bot restart.
const (
	stateAmount       = "tx:amount"     // waiting for the amount
	stateSource       = "tx:source"     // waiting for the source
	stateNote         = "tx:note"       // waiting for a note on the operation
	stateNewCategory  = "cat:new"       // waiting for the category name
	stateAccountName  = "acc:name"      // waiting for the account name
	stateAccountInit  = "acc:init"      // waiting for the starting balance
	stateTransferAmt  = "tr:amount"     // waiting for the transfer amount
	stateTransferTo   = "tr:toamount"   // waiting for the credited amount (different currencies)
	stateBudgetAmount = "bud:amount"    // waiting for the budget limit
	stateRateInput    = "rate:input"    // waiting for the exchange rate
	stateReportPeriod = "rep:period"    // waiting for the report period
	stateRecurAmount  = "rec:amount"    // waiting for the recurring operation amount
	stateRecurDay     = "rec:day"       // waiting for the day of month
	stateBalanceFix   = "acc:balance"   // waiting for the actual account balance
	stateRenameAcc    = "acc:rename"    // waiting for the new account name
	stateConvRate     = "tx:rate"       // waiting for the rate: operation currency ≠ account currency
	stateConvAmount   = "tx:conv"       // waiting for how much was actually debited from the account
	stateEditAmount   = "tx:editamt"    // waiting for the corrected amount of a saved operation
	stateSearch       = "search"        // waiting for a search line
	stateCategoryName = "cat:rename"    // waiting for a category's new name
	stateOnboard      = "ob:step"       // first-run setup: waiting for a button
	stateOnboardName  = "ob:name"       // first-run setup: waiting for a place's name typed by the user
	stateTransferRate = "tr:rate"       // waiting for the transfer rate between different currencies
	stateLoanName     = "loan:name"     // new debt or loan: who, or what for
	stateLoanAccount  = "loan:acc"      // new debt or loan: the account the money went through (button)
	stateLoanCurrency = "loan:cur"      // new debt or loan: its currency (button)
	stateLoanAmount   = "loan:amount"   // new debt or loan: how much
	stateLoanMonthly  = "loan:monthly"  // new loan: the monthly payment
	stateLoanDay      = "loan:day"      // new loan: the payment day
	stateLoanPayAcc   = "loan:payacc"   // a payment: which account (button)
	stateLoanPay      = "loan:pay"      // a payment: how much
	stateLoanInterest = "loan:interest" // a payment: how much of it was interest
	stateLoanFix      = "loan:fix"      // what is really owed, to correct the count
)

// draft — the operation draft the wizard assembles.
type draft struct {
	Kind        string     `json:"kind,omitempty"`
	AmountRaw   string     `json:"amount_raw,omitempty"`
	Amount      int64      `json:"amount,omitempty"`
	Currency    string     `json:"currency,omitempty"`
	AccountID   int64      `json:"account_id,omitempty"`
	ToAccountID int64      `json:"to_account_id,omitempty"`
	ToAmount    *int64     `json:"to_amount,omitempty"`
	Rate        *float64   `json:"rate,omitempty"`
	CategoryID  *int64     `json:"category_id,omitempty"`
	Source      string     `json:"source,omitempty"`
	Note        string     `json:"note,omitempty"`
	OccurredAt  *time.Time `json:"occurred_at,omitempty"`

	// First-run setup: the places to set up accounts for, still to be done.
	Onboarding bool     `json:"onboarding,omitempty"`
	Queue      []string `json:"queue,omitempty"`      // places left to set up
	Currencies []string `json:"currencies,omitempty"` // currencies of the current place, left to price

	// Set when the operation's currency differs from the account's: what the
	// purchase cost in its own currency, and which way round the user is typing
	// the rate (rateDirect / rateInverse).
	OriginalAmount *int64 `json:"original_amount,omitempty"`

	// A category word typed in a quick-entry line, kept while the bot first asks
	// which account the operation went through.
	Hint    string `json:"hint,omitempty"`
	RateDir string `json:"rate_dir,omitempty"`

	// context for states that aren't tied to an operation
	TxID       int64    `json:"tx_id,omitempty"`
	Sources    []string `json:"sources,omitempty"`
	Month      string   `json:"month,omitempty"`
	CurrencyTo string   `json:"currency_to,omitempty"`
	DayOfMonth int      `json:"day,omitempty"`

	// Debts and loans: the one being added or paid.
	LoanID   int64  `json:"loan_id,omitempty"`
	LoanKind string `json:"loan_kind,omitempty"`
	LoanName string `json:"loan_name,omitempty"`
	Monthly  *int64 `json:"monthly,omitempty"`
}

func (b *Bot) setState(ctx context.Context, userID int64, state string, d draft) {
	raw, err := json.Marshal(d)
	if err != nil {
		slog.Error("marshal draft", "err", err)
		return
	}
	if err := b.st.SaveState(ctx, userID, state, raw); err != nil {
		slog.Error("SaveState", "err", err)
	}
}

func (b *Bot) clearState(ctx context.Context, userID int64) {
	if err := b.st.ClearState(ctx, userID); err != nil {
		slog.Error("ClearState", "err", err)
	}
}

func parseDraft(data []byte) draft {
	var d draft
	if len(data) > 0 {
		if err := json.Unmarshal(data, &d); err != nil {
			slog.Warn("corrupt draft", "err", err)
		}
	}
	return d
}
