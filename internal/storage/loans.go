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
	// The split bill this is a share of, when it is one (see SplitTransaction).
	SplitOf *int64

	// Worked out from the linked operations.
	Repaid       int64      // principal settled so far: paid back or forgiven
	PaidTotal    int64      // everything paid on it, interest included; forgiven not
	InterestPaid int64      // the interest part of the payments
	Forgiven     int64      // how much of it was let go
	Payments     int        // how many repayments
	LastPayment  *time.Time // when the last one was
}

// Outstanding is what is still owed.
func (l Loan) Outstanding() int64 { return l.Principal - l.Repaid }

// PaidOff reports whether nothing is owed any more.
func (l Loan) PaidOff() bool { return l.Outstanding() <= 0 }

// OwedToMe reports whether the money is owed to the user rather than by them.
func (l Loan) OwedToMe() bool { return l.Kind == LoanLent }

// Forgivable reports whether the debt can be let go: only one between people —
// a bank doesn't forgive.
func (l Loan) Forgivable() bool { return !l.FromBank() }

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
// money back to the user on a loan they gave, money paid on any other, and a
// part of it forgiven either way. The operation that opened the loan goes the
// other way and is not counted.
const repayment = `(t.forgiven OR CASE WHEN l.kind = 'lent' THEN t.kind = 'debt_in'
                                      ELSE t.kind IN ('debt_out','expense') END)`

// paid is a repayment where money actually moved.
const paid = `(` + repayment + ` AND NOT t.forgiven)`

const loanSelect = `
	SELECT l.id, l.user_id, l.kind, l.name, l.currency, cur.decimals, l.principal,
	       l.account_id, l.monthly_payment, l.payment_day, COALESCE(l.note,''),
	       l.opened_at, l.last_reminded, l.split_of,
	       COALESCE(SUM(t.amount - COALESCE(t.interest,0)) FILTER (WHERE ` + repayment + `), 0),
	       COALESCE(SUM(t.amount) FILTER (WHERE ` + paid + `), 0),
	       COALESCE(SUM(t.interest) FILTER (WHERE ` + paid + `), 0),
	       COALESCE(SUM(t.amount) FILTER (WHERE t.forgiven), 0),
	       COUNT(t.id) FILTER (WHERE ` + paid + ` AND COALESCE(t.interest,0) < t.amount),
	       MAX(t.occurred_at) FILTER (WHERE ` + paid + `)
	FROM loans l
	JOIN currencies cur ON cur.code = l.currency
	LEFT JOIN transactions t ON t.loan_id = l.id AND t.deleted_at IS NULL`

const loanGroup = ` GROUP BY l.id, cur.decimals`

func scanLoan(row pgx.Row) (Loan, error) {
	var l Loan
	var day *int16
	err := row.Scan(&l.ID, &l.UserID, &l.Kind, &l.Name, &l.Currency, &l.Decimals, &l.Principal,
		&l.AccountID, &l.MonthlyPayment, &day, &l.Note, &l.OpenedAt, &l.LastReminded, &l.SplitOf,
		&l.Repaid, &l.PaidTotal, &l.InterestPaid, &l.Forgiven, &l.Payments, &l.LastPayment)
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
//
// A share of a split bill goes back into the bill: the whole bill still left the
// account, and with nobody owing that part, it was the user's own spending. A
// bill split entirely among others was put away when it was split, and comes
// back with the first share returned to it.
func (s *Store) DeleteLoan(ctx context.Context, userID, id int64) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		var splitOf *int64
		var principal int64
		err := tx.QueryRow(ctx,
			`UPDATE loans SET deleted_at = now() WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
			 RETURNING split_of, principal`, id, userID).Scan(&splitOf, &principal)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if splitOf != nil {
			if _, err := tx.Exec(ctx,
				`UPDATE transactions
				 SET amount = CASE WHEN deleted_at IS NULL THEN amount + $3 ELSE $3 END,
				     deleted_at = NULL
				 WHERE id = $1 AND user_id = $2`, *splitOf, userID, principal); err != nil {
				return err
			}
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
	Forgiven int64 // let go, with no money moving
}

// LoansInPeriod sums each loan's operations over a period.
func (s *Store) LoansInPeriod(ctx context.Context, userID int64, from, to time.Time) ([]LoanPeriodSum, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT l.id, l.kind, l.name, l.currency, cur.decimals,
		       COALESCE(SUM(t.amount) FILTER (WHERE t.kind IN ('debt_out','expense') AND NOT t.forgiven), 0),
		       COALESCE(SUM(t.amount) FILTER (WHERE t.kind = 'debt_in'), 0),
		       COALESCE(SUM(t.interest), 0),
		       COALESCE(SUM(t.amount) FILTER (WHERE t.forgiven), 0)
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
			&x.Out, &x.In, &x.Interest, &x.Forgiven); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ForgiveLoan lets go of amount of a debt between people, and returns the
// operation that records it. No money moves, so no balance changes; what it is
// for the books depends on who forgave whom:
//
//   - forgiving money lent: it is not coming back, so it is spending — filed
//     under the bill's category when the debt is a share of a split bill;
//   - being let off a debt: money kept that was going to be paid, so income.
//
// The operation is counted in the loan's currency, through its account — or,
// for a loan that never touched one, the first account in that currency.
func (s *Store) ForgiveLoan(ctx context.Context, userID, loanID, amount int64, at time.Time) (Transaction, error) {
	l, err := s.Loan(ctx, userID, loanID)
	if err != nil {
		return Transaction{}, err
	}
	if !l.Forgivable() {
		return Transaction{}, fmt.Errorf("only a debt between people can be forgiven")
	}
	if amount <= 0 {
		return Transaction{}, fmt.Errorf("the amount must be greater than zero")
	}
	if amount > l.Outstanding() {
		return Transaction{}, fmt.Errorf("that is more than is owed")
	}
	t := Transaction{UserID: userID, Kind: KindIncome, Amount: amount, Source: l.Name,
		OccurredAt: at, LoanID: &l.ID, Forgiven: true}
	if l.OwedToMe() {
		t.Kind = KindExpense
	}
	if l.AccountID != nil {
		t.AccountID = *l.AccountID
	} else {
		err := s.pool.QueryRow(ctx,
			`SELECT id FROM accounts WHERE user_id = $1 AND currency = $2
			 ORDER BY archived, id LIMIT 1`, userID, l.Currency).Scan(&t.AccountID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Transaction{}, fmt.Errorf("there is no account in %s to count it in", l.Currency)
		}
		if err != nil {
			return Transaction{}, err
		}
	}
	if l.SplitOf != nil && l.OwedToMe() {
		// The share was part of what the bill paid for, so it is spent on the same thing.
		err := s.pool.QueryRow(ctx,
			`SELECT category_id FROM transactions WHERE id = $1 AND user_id = $2`,
			*l.SplitOf, userID).Scan(&t.CategoryID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Transaction{}, err
		}
	}
	id, err := insertTransaction(ctx, s.pool, t)
	if err != nil {
		return Transaction{}, err
	}
	return s.Transaction(ctx, userID, id)
}

// Share is one person's part of a split bill.
type Share struct {
	Name   string
	Amount int64 // in the bill's account's currency
}

// SplitTransaction divides an expense the user paid in full: each share is owed
// back by someone, and what is left is the user's own.
//
// Each share becomes a debt of its own — money lent from the bill's account, at
// the bill's moment, named after the person and what the bill was for — so it
// is paid back, or forgiven, like any other. The expense keeps only the user's
// share. When nothing is left of it (the user paid entirely for others), it is
// put away: it was all lending. Either way the account loses the whole bill, as
// it did.
func (s *Store) SplitTransaction(ctx context.Context, userID, txID int64, shares []Share) ([]Loan, error) {
	if len(shares) == 0 {
		return nil, fmt.Errorf("nobody to split with")
	}
	var total int64
	for _, sh := range shares {
		if sh.Amount <= 0 {
			return nil, fmt.Errorf("every share must be greater than zero")
		}
		if sh.Name == "" {
			return nil, fmt.Errorf("every share needs a name")
		}
		total += sh.Amount
	}
	var ids []int64
	err := s.InTx(ctx, func(tx pgx.Tx) error {
		var (
			kind, currency, label string
			amount, accountID     int64
			at                    time.Time
			loanID, feeOf         *int64
			converted             bool
		)
		err := tx.QueryRow(ctx,
			`SELECT t.kind, t.amount, t.account_id, a.currency, t.occurred_at, t.loan_id, t.fee_of,
			        t.original_currency IS NOT NULL,
			        COALESCE(t.source, c.name, '')
			 FROM transactions t
			 JOIN accounts a ON a.id = t.account_id
			 LEFT JOIN categories c ON c.id = t.category_id
			 WHERE t.id = $1 AND t.user_id = $2 AND t.deleted_at IS NULL
			 FOR UPDATE OF t`, txID, userID).
			Scan(&kind, &amount, &accountID, &currency, &at, &loanID, &feeOf, &converted, &label)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		switch {
		case kind != KindExpense || loanID != nil || feeOf != nil:
			return fmt.Errorf("only an ordinary expense can be split")
		case converted:
			return fmt.Errorf("an expense in another currency can't be split")
		case total > amount:
			return fmt.Errorf("the shares add up to more than the bill")
		}
		var already bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM loans WHERE split_of = $1 AND deleted_at IS NULL)`,
			txID).Scan(&already); err != nil {
			return err
		}
		if already {
			return fmt.Errorf("this bill is already split")
		}

		if total == amount {
			_, err = tx.Exec(ctx, `UPDATE transactions SET deleted_at = now() WHERE id = $1`, txID)
		} else {
			_, err = tx.Exec(ctx, `UPDATE transactions SET amount = amount - $2 WHERE id = $1`, txID, total)
		}
		if err != nil {
			return err
		}

		for _, sh := range shares {
			name := sh.Name
			if label != "" {
				name += " · " + label
			}
			if r := []rune(name); len(r) > 60 {
				name = string(r[:59]) + "…"
			}
			var id int64
			if err := tx.QueryRow(ctx,
				`INSERT INTO loans (user_id, kind, name, currency, principal, account_id, opened_at, split_of)
				 VALUES ($1, 'lent', $2, $3, $4, $5, $6, $7)
				 RETURNING id`,
				userID, name, currency, sh.Amount, accountID, at, txID).Scan(&id); err != nil {
				return err
			}
			if _, err := insertTransaction(ctx, tx, Transaction{
				UserID: userID, AccountID: accountID, Kind: KindDebtOut, Amount: sh.Amount,
				Source: name, OccurredAt: at, LoanID: &id,
			}); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]Loan, 0, len(ids))
	for _, id := range ids {
		l, err := s.Loan(ctx, userID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

// SplitLoans lists the debts a bill was split into, in the order they were made.
func (s *Store) SplitLoans(ctx context.Context, userID, txID int64) ([]Loan, error) {
	rows, err := s.pool.Query(ctx,
		loanSelect+` WHERE l.user_id = $1 AND l.split_of = $2 AND l.deleted_at IS NULL`+loanGroup+
			` ORDER BY l.id`, userID, txID)
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

// SplitPeople are the names the user has split bills with or lent to and
// borrowed from, most recent first — offered as buttons when splitting.
func (s *Store) SplitPeople(ctx context.Context, userID int64, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name FROM (
		     SELECT CASE WHEN split_of IS NULL THEN name ELSE split_part(name, ' · ', 1) END AS name,
		            opened_at
		     FROM loans
		     WHERE user_id = $1 AND deleted_at IS NULL AND kind IN ('lent','borrowed')
		 ) p
		 GROUP BY name ORDER BY MAX(opened_at) DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
