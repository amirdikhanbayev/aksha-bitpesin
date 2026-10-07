package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const txCols = `t.id, t.user_id, t.account_id, t.category_id, t.kind, t.amount,
	t.to_account_id, t.to_amount, t.rate, COALESCE(t.note,''), COALESCE(t.source,''), t.occurred_at,
	t.original_amount, COALESCE(t.original_currency,''),
	a.name, a.currency, cur.decimals,
	COALESCE(ta.name,''), COALESCE(ta.currency,''), COALESCE(tcur.decimals,0),
	COALESCE(c.name,''), COALESCE(c.emoji,''), COALESCE(ocur.decimals,0),
	t.loan_id, t.interest, COALESCE(l.name,''), COALESCE(l.kind,'')`

const txFrom = `FROM transactions t
	JOIN accounts a ON a.id = t.account_id
	JOIN currencies cur ON cur.code = a.currency
	LEFT JOIN accounts ta ON ta.id = t.to_account_id
	LEFT JOIN currencies tcur ON tcur.code = ta.currency
	LEFT JOIN categories c ON c.id = t.category_id
	LEFT JOIN currencies ocur ON ocur.code = t.original_currency
	LEFT JOIN loans l ON l.id = t.loan_id`

func scanTx(row pgx.Row) (Transaction, error) {
	var t Transaction
	err := row.Scan(&t.ID, &t.UserID, &t.AccountID, &t.CategoryID, &t.Kind, &t.Amount,
		&t.ToAccountID, &t.ToAmount, &t.Rate, &t.Note, &t.Source, &t.OccurredAt,
		&t.OriginalAmount, &t.OriginalCurrency,
		&t.AccountName, &t.AccountCurr, &t.Decimals,
		&t.ToAccountName, &t.ToAccountCurr, &t.ToDecimals,
		&t.CategoryName, &t.CategoryEmoji, &t.OriginalDecimals,
		&t.LoanID, &t.Interest, &t.LoanName, &t.LoanKind)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// CreateTransaction records an income or expense.
func (s *Store) CreateTransaction(ctx context.Context, t Transaction) (Transaction, error) {
	if t.Kind != KindIncome && t.Kind != KindExpense {
		return t, fmt.Errorf("CreateTransaction: invalid kind %q", t.Kind)
	}
	if t.Amount <= 0 {
		return t, fmt.Errorf("amount must be greater than zero")
	}
	// A converted operation must carry the rate the user entered: the bot never
	// substitutes a market rate for a real operation.
	if t.OriginalCurrency != "" && (t.OriginalAmount == nil || t.Rate == nil) {
		return t, fmt.Errorf("converted operation needs both the original amount and the user's rate")
	}
	id, err := insertTransaction(ctx, s.pool, t)
	if err != nil {
		return t, err
	}
	return s.Transaction(ctx, t.UserID, id)
}

// querier is what both the pool and a transaction can run a query with.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func insertTransaction(ctx context.Context, q querier, t Transaction) (int64, error) {
	var id int64
	err := q.QueryRow(ctx,
		`INSERT INTO transactions
		     (user_id, account_id, category_id, kind, amount, note, source, occurred_at,
		      rate, original_amount, original_currency, loan_id, interest)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6,''), NULLIF($7,''), $8, $9, $10, NULLIF($11,''), $12, $13)
		 RETURNING id`,
		t.UserID, t.AccountID, t.CategoryID, t.Kind, t.Amount, t.Note, t.Source, t.OccurredAt,
		t.Rate, t.OriginalAmount, t.OriginalCurrency, t.LoanID, t.Interest).Scan(&id)
	return id, err
}

// CreateTransfer records a transfer between accounts.
// If the currencies differ, toAmount and rate are required — the user enters the rate.
func (s *Store) CreateTransfer(ctx context.Context, userID, fromID, toID, amount int64,
	toAmount *int64, rate *float64, note string, at time.Time) (Transaction, error) {

	if fromID == toID {
		return Transaction{}, fmt.Errorf("source and destination accounts are the same")
	}
	if amount <= 0 {
		return Transaction{}, fmt.Errorf("amount must be greater than zero")
	}
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO transactions (user_id, account_id, to_account_id, kind, amount, to_amount, rate, note, occurred_at)
		 VALUES ($1, $2, $3, 'transfer', $4, $5, $6, NULLIF($7,''), $8)
		 RETURNING id`,
		userID, fromID, toID, amount, toAmount, rate, note, at).Scan(&id)
	if err != nil {
		return Transaction{}, err
	}
	return s.Transaction(ctx, userID, id)
}

func (s *Store) Transaction(ctx context.Context, userID, id int64) (Transaction, error) {
	return scanTx(s.pool.QueryRow(ctx,
		`SELECT `+txCols+` `+txFrom+`
		 WHERE t.id = $1 AND t.user_id = $2 AND t.deleted_at IS NULL`, id, userID))
}

// ListTransactions returns operations for a period (half-open interval [from, to)), newest first.
func (s *Store) ListTransactions(ctx context.Context, userID int64, from, to time.Time, limit int) ([]Transaction, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+txCols+` `+txFrom+`
		 WHERE t.user_id = $1 AND t.deleted_at IS NULL
		   AND t.occurred_at >= $2 AND t.occurred_at < $3
		 ORDER BY t.occurred_at DESC, t.id DESC
		 LIMIT $4`, userID, from, to, limit)
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

// LastTransactions — the most recent operations, not tied to any period.
func (s *Store) LastTransactions(ctx context.Context, userID int64, limit int) ([]Transaction, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+txCols+` `+txFrom+`
		 WHERE t.user_id = $1 AND t.deleted_at IS NULL
		 ORDER BY t.occurred_at DESC, t.id DESC LIMIT $2`, userID, limit)
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

// DeleteTransaction — soft delete.
func (s *Store) DeleteTransaction(ctx context.Context, userID, id int64) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE transactions SET deleted_at = now()
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SuggestSources — sources the user has already used for this category/operation kind.
func (s *Store) SuggestSources(ctx context.Context, userID int64, kind string, categoryID *int64, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT t.source
		 FROM transactions t
		 WHERE t.user_id = $1 AND t.deleted_at IS NULL AND t.source IS NOT NULL
		   AND t.kind = $2 AND ($3::bigint IS NULL OR t.category_id = $3)
		 GROUP BY t.source
		 ORDER BY COUNT(*) DESC, MAX(t.occurred_at) DESC
		 LIMIT $4`, userID, kind, categoryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var src string
		if err := rows.Scan(&src); err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// SetTransactionSource sets the source on an already-recorded operation.
func (s *Store) SetTransactionSource(ctx context.Context, userID, id int64, source string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE transactions SET source = NULLIF($3,'')
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL AND loan_id IS NULL`, id, userID, source)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTransactionNote sets the note on an already-recorded operation.
func (s *Store) SetTransactionNote(ctx context.Context, userID, id int64, note string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE transactions SET note = NULLIF($3,'')
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID, note)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTransactionDate moves an already-recorded operation to another moment —
// used by the date buttons on the operation card.
func (s *Store) SetTransactionDate(ctx context.Context, userID, id int64, at time.Time) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE transactions SET occurred_at = $3
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID, at)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTransactionAmounts corrects the amounts of an operation — a mistyped figure.
// The rate is left as it was: it is what the bank applied, not something a
// correction changes. amount is always in the account's currency; originalAmount
// and toAmount are passed for the operations that have them, nil otherwise.
func (s *Store) SetTransactionAmounts(ctx context.Context, userID, id, amount int64,
	originalAmount, toAmount *int64) error {

	if amount <= 0 {
		return fmt.Errorf("amount must be greater than zero")
	}
	ct, err := s.pool.Exec(ctx,
		`UPDATE transactions
		 SET amount = $3, original_amount = $4, to_amount = $5,
		     interest = LEAST(interest, $3)  -- the interest of a loan payment can't exceed it
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		id, userID, amount, originalAmount, toAmount)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SearchTransactions finds operations by what the user remembers about them: a
// word from the source, note or category, and an amount range. Any of the three
// may be left out. Newest first.
func (s *Store) SearchTransactions(ctx context.Context, userID int64, query string,
	min, max int64, limit int) ([]Transaction, error) {

	rows, err := s.pool.Query(ctx,
		`SELECT `+txCols+` `+txFrom+`
		 WHERE t.user_id = $1 AND t.deleted_at IS NULL
		   AND ($2 = '' OR t.source ILIKE '%' || $2 || '%'
		                OR t.note ILIKE '%' || $2 || '%'
		                OR c.name ILIKE '%' || $2 || '%'
		                OR a.name ILIKE '%' || $2 || '%')
		   AND ($3::bigint IS NULL OR t.amount >= $3)
		   AND ($4::bigint IS NULL OR t.amount <= $4)
		 ORDER BY t.occurred_at DESC, t.id DESC
		 LIMIT $5`,
		userID, query, nullIf(min), nullIf(max), limit)
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

// nullIf turns "no bound given" into SQL NULL.
func nullIf(v int64) *int64 {
	if v <= 0 {
		return nil
	}
	return &v
}

// SetTransactionCategory files an operation under another category, or under none
// when categoryID is nil.
func (s *Store) SetTransactionCategory(ctx context.Context, userID, id int64, categoryID *int64) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE transactions SET category_id = $3
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
		   AND loan_id IS NULL  -- a loan's operations are filed under the loan`, id, userID, categoryID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveTransaction moves an operation to another account — the destination side of
// a transfer when toSide is set, the account it came out of otherwise.
//
// The amount is left as the number it was: whether it still means the same thing
// depends on the currency, which is the caller's business to sort out with the user.
func (s *Store) MoveTransaction(ctx context.Context, userID, id, accountID int64, toSide bool) error {
	column := "account_id"
	if toSide {
		column = "to_account_id"
	}
	ct, err := s.pool.Exec(ctx,
		`UPDATE transactions SET `+column+` = $3
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
		   AND loan_id IS NULL  -- a loan's figures only add up in its own currency
		   AND EXISTS (SELECT 1 FROM accounts WHERE id = $3 AND user_id = $2)`,
		id, userID, accountID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
