package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Loan kinds.
const (
	LoanLent        = "lent"        // I lent money: someone owes me
	LoanBorrowed    = "borrowed"    // I borrowed from a person
	LoanInstallment = "installment" // a purchase paid off in parts
	LoanCredit      = "credit"      // a bank credit
	LoanMortgage    = "mortgage"
)

// LoanEmoji is the icon a loan kind is shown with. The category breakdown in
// ByCategory uses the same ones.
func LoanEmoji(kind string) string {
	switch kind {
	case LoanLent:
		return "🤝"
	case LoanBorrowed:
		return "🙏"
	case LoanInstallment:
		return "🛍"
	case LoanMortgage:
		return "🏠"
	default:
		return "🏦"
	}
}

// LoanKinds lists the kinds in the order they are offered.
var LoanKinds = []string{LoanLent, LoanBorrowed, LoanInstallment, LoanCredit, LoanMortgage}

// Loan is a debt either way: money owed to the user, or by them.
//
// What is owed now is never stored. It is the principal minus the repayments,
// and the repayments are the operations linked to the loan, so deleting or
// correcting one of them is reflected at once.
type Loan struct {
	ID             int64
	UserID         int64
	Kind           string
	Name           string
	Currency       string
	Decimals       int
	Principal      int64  // owed at the start, in minor units
	AccountID      *int64 // where the money came in or went out at the start; nil if it never touched an account
	MonthlyPayment *int64
	PaymentDay     *int
	Note           string
	OpenedAt       time.Time
	LastReminded   *time.Time

	// Worked out from the linked operations.
	Repaid       int64      // principal paid back so far
	PaidTotal    int64      // everything paid on it, interest included
	InterestPaid int64      // the interest part of the payments
	Payments     int        // how many repayments
	LastPayment  *time.Time // when the last one was
}

// Outstanding is what is still owed.
func (l Loan) Outstanding() int64 { return l.Principal - l.Repaid }

// PaidOff reports whether nothing is owed any more.
func (l Loan) PaidOff() bool { return l.Outstanding() <= 0 }

// OwedToMe reports whether the money is owed to the user rather than by them.
func (l Loan) OwedToMe() bool { return l.Kind == LoanLent }

// FromBank reports whether this is owed to a bank or a shop, with a schedule
// and, possibly, interest — as opposed to a debt between people.
func (l Loan) FromBank() bool {
	return l.Kind == LoanInstallment || l.Kind == LoanCredit || l.Kind == LoanMortgage
}

// ThroughAccount reports whether the money passed through one of the user's
// accounts when the loan was opened. It decides what a repayment is: when the
// money landed on an account and was spent from there, the spending was already
// recorded, so paying back the principal moves money but spends nothing. When it
// went straight to a shop or a seller, each payment is the spending.
func (l Loan) ThroughAccount() bool { return l.AccountID != nil }

// repayment is the condition, in SQL, for an operation that pays a loan down:
// money back to the user on a loan they gave, money paid on any other. The
// operation that opened the loan goes the other way and is not counted.
const repayment = `(CASE WHEN l.kind = 'lent' THEN t.kind = 'debt_in'
                         ELSE t.kind IN ('debt_out','expense') END)`

const loanSelect = `
	SELECT l.id, l.user_id, l.kind, l.name, l.currency, cur.decimals, l.principal,
	       l.account_id, l.monthly_payment, l.payment_day, COALESCE(l.note,''),
	       l.opened_at, l.last_reminded,
	       COALESCE(SUM(t.amount - COALESCE(t.interest,0)) FILTER (WHERE ` + repayment + `), 0),
	       COALESCE(SUM(t.amount) FILTER (WHERE ` + repayment + `), 0),
	       COALESCE(SUM(t.interest) FILTER (WHERE ` + repayment + `), 0),
	       COUNT(t.id) FILTER (WHERE ` + repayment + ` AND COALESCE(t.interest,0) < t.amount),
	       MAX(t.occurred_at) FILTER (WHERE ` + repayment + `)
	FROM loans l
	JOIN currencies cur ON cur.code = l.currency
	LEFT JOIN transactions t ON t.loan_id = l.id AND t.deleted_at IS NULL`

const loanGroup = ` GROUP BY l.id, cur.decimals`

func scanLoan(row pgx.Row) (Loan, error) {
	var l Loan
	var day *int16
	err := row.Scan(&l.ID, &l.UserID, &l.Kind, &l.Name, &l.Currency, &l.Decimals, &l.Principal,
		&l.AccountID, &l.MonthlyPayment, &day, &l.Note, &l.OpenedAt, &l.LastReminded,
		&l.Repaid, &l.PaidTotal, &l.InterestPaid, &l.Payments, &l.LastPayment)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, ErrNotFound
	}
	if day != nil {
		d := int(*day)
		l.PaymentDay = &d
	}
	return l, err
}

// CreateLoan records a loan and, when its money passed through an account, the
// operation that moved it: money out for a loan given, money in for one taken.
func (s *Store) CreateLoan(ctx context.Context, l Loan) (Loan, error) {
	if l.Principal <= 0 {
		return l, fmt.Errorf("the amount must be greater than zero")
	}
	var id int64
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		if l.AccountID != nil {
			if err := accountHolds(ctx, tx, l.UserID, *l.AccountID, l.Currency); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO loans (user_id, kind, name, currency, principal, account_id,
			                    monthly_payment, payment_day, note, opened_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9,''), $10)
			 RETURNING id`,
			l.UserID, l.Kind, l.Name, l.Currency, l.Principal, l.AccountID,
			l.MonthlyPayment, l.PaymentDay, l.Note, l.OpenedAt).Scan(&id); err != nil {
			return err
		}
		if l.AccountID == nil {
			return nil
		}
		kind := KindDebtIn
		if l.Kind == LoanLent {
			kind = KindDebtOut
		}
		_, err := insertTransaction(ctx, tx, Transaction{
			UserID: l.UserID, AccountID: *l.AccountID, Kind: kind, Amount: l.Principal,
			Source: l.Name, OccurredAt: l.OpenedAt, LoanID: &id,
		})
		return err
	})
	if err != nil {
		return l, err
	}
	return s.Loan(ctx, l.UserID, id)
}

// accountHolds checks that the account is the user's and in the loan's currency:
// a loan's figures only add up in one currency.
func accountHolds(ctx context.Context, tx pgx.Tx, userID, accountID int64, currency string) error {
	var cur string
	err := tx.QueryRow(ctx, `SELECT currency FROM accounts WHERE id = $1 AND user_id = $2`,
		accountID, userID).Scan(&cur)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if cur != currency {
		return fmt.Errorf("the account is in %s, the loan in %s", cur, currency)
	}
	return nil
}

func (s *Store) Loan(ctx context.Context, userID, id int64) (Loan, error) {
	return scanLoan(s.pool.QueryRow(ctx,
		loanSelect+` WHERE l.id = $1 AND l.user_id = $2 AND l.deleted_at IS NULL`+loanGroup, id, userID))
}

// Loans lists the user's loans, open and paid off, oldest first.
func (s *Store) Loans(ctx context.Context, userID int64) ([]Loan, error) {
	rows, err := s.pool.Query(ctx,
		loanSelect+` WHERE l.user_id = $1 AND l.deleted_at IS NULL`+loanGroup+
			` ORDER BY l.opened_at, l.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Loan
	for rows.Next() {
		l, err := scanLoan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PayLoan records a payment on a loan from (or, for money owed to the user,
// into) an account, and returns the operation to show. interest is how much of
// amount was interest, as the user stated it; zero for a debt between people.
//
// What gets recorded follows from where the loan's money went (see
// Loan.ThroughAccount):
//
//   - a loan given: money comes back in — debt_in;
//   - a debt to a person: money goes out — debt_out;
//   - a bank loan whose money landed on an account: the principal goes out as
//     debt_out, the interest as an expense — the only part that is spending;
//   - a bank loan paid straight to a shop or seller: the whole payment is an
//     expense, with its interest part noted on it.
func (s *Store) PayLoan(ctx context.Context, userID, loanID, accountID, amount, interest int64,
	at time.Time) (Transaction, error) {

	if amount <= 0 {
		return Transaction{}, fmt.Errorf("the amount must be greater than zero")
	}
	if interest < 0 || interest > amount {
		return Transaction{}, fmt.Errorf("the interest can't be more than the payment")
	}
	l, err := s.Loan(ctx, userID, loanID)
	if err != nil {
		return Transaction{}, err
	}
	if !l.FromBank() {
		interest = 0
	}

	var shown int64
	err = s.InTx(ctx, func(tx pgx.Tx) error {
		if err := accountHolds(ctx, tx, userID, accountID, l.Currency); err != nil {
			return err
		}
		base := Transaction{UserID: userID, AccountID: accountID, Source: l.Name,
			OccurredAt: at, LoanID: &l.ID}
		insert := func(kind string, amount int64, interest *int64, note string) error {
			t := base
			t.Kind, t.Amount, t.Interest, t.Note = kind, amount, interest, note
			id, err := insertTransaction(ctx, tx, t)
			if shown == 0 {
				shown = id
			}
			return err
		}
		switch {
		case l.Kind == LoanLent:
			return insert(KindDebtIn, amount, nil, "")
		case !l.FromBank():
			return insert(KindDebtOut, amount, nil, "")
		case l.ThroughAccount():
			if principal := amount - interest; principal > 0 {
				if err := insert(KindDebtOut, principal, nil, ""); err != nil {
					return err
				}
			}
			if interest > 0 {
				return insert(KindExpense, interest, &interest, "interest")
			}
			return nil
		default:
			return insert(KindExpense, amount, &interest, "")
		}
	})
	if err != nil {
		return Transaction{}, err
	}
	return s.Transaction(ctx, userID, shown)
}

// SetLoanOutstanding corrects what is owed to match the bank or the person —
// the way an account's balance is corrected: the starting amount moves, the
// recorded payments stay as they were.
func (s *Store) SetLoanOutstanding(ctx context.Context, userID, id, outstanding int64) error {
	l, err := s.Loan(ctx, userID, id)
	if err != nil {
		return err
	}
	principal := l.Principal + (outstanding - l.Outstanding())
	if principal <= 0 {
		return fmt.Errorf("with what is already repaid, that would make the loan negative")
	}
	_, err = s.pool.Exec(ctx, `UPDATE loans SET principal = $3 WHERE id = $1 AND user_id = $2`,
		id, userID, principal)
	return err
}

// DeleteLoan removes a loan together with its operations — soft-deleted, like
// any operation, so balances go back to what they were without it.
func (s *Store) DeleteLoan(ctx context.Context, userID, id int64) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`UPDATE loans SET deleted_at = now() WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
			id, userID)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx,
			`UPDATE transactions SET deleted_at = now()
			 WHERE loan_id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID)
		return err
	})
}

// LoanTransactions lists a loan's operations, newest first.
func (s *Store) LoanTransactions(ctx context.Context, userID, loanID int64, limit int) ([]Transaction, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+txCols+` `+txFrom+`
		 WHERE t.user_id = $1 AND t.loan_id = $2 AND t.deleted_at IS NULL
		 ORDER BY t.occurred_at DESC, t.id DESC LIMIT $3`, userID, loanID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Transaction
	for rows.Next() {
		t, err := scanTx(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LoanPaidSince reports whether a repayment was recorded on or after from — the
// payment reminder isn't sent for a month that is already paid.
func (s *Store) LoanPaidSince(ctx context.Context, loanID int64, from time.Time) (bool, error) {
	var paid bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (
		     SELECT 1 FROM transactions t JOIN loans l ON l.id = t.loan_id
		     WHERE t.loan_id = $1 AND t.deleted_at IS NULL AND t.occurred_at >= $2
		       AND `+repayment+`)`, loanID, from).Scan(&paid)
	return paid, err
}

// ClaimLoanReminder marks the month's payment reminder as sent, and reports
// whether it wasn't already — so it goes out once however often the scheduler
// runs.
func (s *Store) ClaimLoanReminder(ctx context.Context, loanID int64, month time.Time) (bool, error) {
	ct, err := s.pool.Exec(ctx,
		`UPDATE loans SET last_reminded = $2
		 WHERE id = $1 AND (last_reminded IS NULL OR last_reminded < $2)`, loanID, month)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// LoanPeriodSum is what happened on one loan over a period, for the statement.
type LoanPeriodSum struct {
	LoanID   int64
	Kind     string
	Name     string
	Currency string
	Decimals int
	Out      int64 // money that left the user's accounts: lent, repaid, paid
	In       int64 // money that came in: borrowed, paid back
	Interest int64 // the interest part of the payments
}

// LoansInPeriod sums each loan's operations over a period.
func (s *Store) LoansInPeriod(ctx context.Context, userID int64, from, to time.Time) ([]LoanPeriodSum, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT l.id, l.kind, l.name, l.currency, cur.decimals,
		       COALESCE(SUM(t.amount) FILTER (WHERE t.kind IN ('debt_out','expense')), 0),
		       COALESCE(SUM(t.amount) FILTER (WHERE t.kind = 'debt_in'), 0),
		       COALESCE(SUM(t.interest), 0)
		FROM transactions t
		JOIN loans l ON l.id = t.loan_id
		JOIN currencies cur ON cur.code = l.currency
		WHERE t.user_id = $1 AND t.deleted_at IS NULL
		  AND t.occurred_at >= $2 AND t.occurred_at < $3
		GROUP BY l.id, cur.decimals
		ORDER BY l.opened_at, l.id`, userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LoanPeriodSum
	for rows.Next() {
		var x LoanPeriodSum
		if err := rows.Scan(&x.LoanID, &x.Kind, &x.Name, &x.Currency, &x.Decimals,
			&x.Out, &x.In, &x.Interest); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
