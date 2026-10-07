package storage

import (
	"context"
	"time"
)

// CurrencyTotal — totals for one currency over a period.
type CurrencyTotal struct {
	Currency string
	Decimals int
	Income   int64
	Expense  int64
}

func (c CurrencyTotal) Net() int64 { return c.Income - c.Expense }

// CategorySum — how much was spent/received in a category, in a specific currency.
type CategorySum struct {
	CategoryID *int64
	Name       string
	Emoji      string
	Kind       string
	Currency   string
	Decimals   int
	Total      int64
	Count      int
}

// SourceSum — the same, by income source / expense recipient.
type SourceSum struct {
	Source   string
	Kind     string
	Currency string
	Decimals int
	Total    int64
	Count    int
}

// AccountSum — account turnover over a period.
type AccountSum struct {
	AccountID int64
	Name      string
	Currency  string
	Decimals  int
	Income    int64
	Expense   int64
}

// Totals — income and expenses for a period, broken down by currency.
// Transfers count as neither income nor expense.
func (s *Store) Totals(ctx context.Context, userID int64, from, to time.Time) ([]CurrencyTotal, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.currency, cur.decimals,
		       COALESCE(SUM(t.amount) FILTER (WHERE t.kind = 'income'), 0),
		       COALESCE(SUM(t.amount) FILTER (WHERE t.kind = 'expense'), 0)
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id
		JOIN currencies cur ON cur.code = a.currency
		WHERE t.user_id = $1 AND t.deleted_at IS NULL AND t.kind IN ('income','expense')
		  AND t.occurred_at >= $2 AND t.occurred_at < $3
		GROUP BY a.currency, cur.decimals
		ORDER BY a.currency`, userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CurrencyTotal
	for rows.Next() {
		var c CurrencyTotal
		if err := rows.Scan(&c.Currency, &c.Decimals, &c.Income, &c.Expense); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ByCategory — breakdown by category over a period. kind: 'income' | 'expense'.
func (s *Store) ByCategory(ctx context.Context, userID int64, kind string, from, to time.Time) ([]CategorySum, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.category_id, COALESCE(c.name, l.name, 'No category'),
		       COALESCE(c.emoji, CASE l.kind WHEN 'installment' THEN '🛍'
		                                     WHEN 'mortgage'    THEN '🏠'
		                                     WHEN 'credit'      THEN '🏦' END, ''),
		       t.kind, a.currency, cur.decimals, SUM(t.amount), COUNT(*)
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id
		JOIN currencies cur ON cur.code = a.currency
		LEFT JOIN categories c ON c.id = t.category_id
		-- A loan payment has no category of its own: it is listed under the loan.
		LEFT JOIN loans l ON l.id = t.loan_id AND t.category_id IS NULL
		WHERE t.user_id = $1 AND t.deleted_at IS NULL AND t.kind = $2
		  AND t.occurred_at >= $3 AND t.occurred_at < $4
		GROUP BY t.category_id, c.name, c.emoji, l.id, l.name, l.kind, t.kind, a.currency, cur.decimals
		ORDER BY a.currency, SUM(t.amount) DESC`, userID, kind, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CategorySum
	for rows.Next() {
		var c CategorySum
		if err := rows.Scan(&c.CategoryID, &c.Name, &c.Emoji, &c.Kind,
			&c.Currency, &c.Decimals, &c.Total, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// BySource — breakdown by income source / expense recipient.
func (s *Store) BySource(ctx context.Context, userID int64, kind string, from, to time.Time) ([]SourceSum, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(t.source, 'Not specified'), t.kind, a.currency, cur.decimals, SUM(t.amount), COUNT(*)
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id
		JOIN currencies cur ON cur.code = a.currency
		WHERE t.user_id = $1 AND t.deleted_at IS NULL AND t.kind = $2
		  AND t.occurred_at >= $3 AND t.occurred_at < $4
		GROUP BY COALESCE(t.source, 'Not specified'), t.kind, a.currency, cur.decimals
		ORDER BY a.currency, SUM(t.amount) DESC`, userID, kind, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceSum
	for rows.Next() {
		var x SourceSum
		if err := rows.Scan(&x.Source, &x.Kind, &x.Currency, &x.Decimals, &x.Total, &x.Count); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ByAccount — account turnover over a period.
func (s *Store) ByAccount(ctx context.Context, userID int64, from, to time.Time) ([]AccountSum, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.name, a.currency, cur.decimals,
		       COALESCE(SUM(t.amount) FILTER (WHERE t.kind = 'income'), 0),
		       COALESCE(SUM(t.amount) FILTER (WHERE t.kind = 'expense'), 0)
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id
		JOIN currencies cur ON cur.code = a.currency
		WHERE t.user_id = $1 AND t.deleted_at IS NULL AND t.kind IN ('income','expense')
		  AND t.occurred_at >= $2 AND t.occurred_at < $3
		GROUP BY a.id, a.name, a.currency, cur.decimals
		ORDER BY a.name`, userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountSum
	for rows.Next() {
		var x AccountSum
		if err := rows.Scan(&x.AccountID, &x.Name, &x.Currency, &x.Decimals, &x.Income, &x.Expense); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ConversionRow — the minimal slice of an operation needed to price it in
// another currency: what left the account, what the operation itself cost in its
// own currency (when the user entered a rate), and the date it happened on.
type ConversionRow struct {
	Kind             string
	Amount           int64
	Currency         string
	Decimals         int
	OriginalAmount   *int64
	OriginalCurrency string
	OriginalDecimals int
	OccurredAt       time.Time
}

// ConversionRows returns income/expense operations for a period with just enough
// data to convert each one into another currency — one query instead of a
// per-transaction lookup, and lighter than ListTransactions (no category/source joins).
func (s *Store) ConversionRows(ctx context.Context, userID int64, from, to time.Time) ([]ConversionRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.kind, t.amount, a.currency, cur.decimals,
		       t.original_amount, COALESCE(t.original_currency,''), COALESCE(ocur.decimals,0),
		       t.occurred_at
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id
		JOIN currencies cur ON cur.code = a.currency
		LEFT JOIN currencies ocur ON ocur.code = t.original_currency
		WHERE t.user_id = $1 AND t.deleted_at IS NULL AND t.kind IN ('income','expense')
		  AND t.occurred_at >= $2 AND t.occurred_at < $3`, userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConversionRow
	for rows.Next() {
		var r ConversionRow
		if err := rows.Scan(&r.Kind, &r.Amount, &r.Currency, &r.Decimals,
			&r.OriginalAmount, &r.OriginalCurrency, &r.OriginalDecimals, &r.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountTransactions — how many operations fall within the period (excluding transfers).
func (s *Store) CountTransactions(ctx context.Context, userID int64, from, to time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM transactions t
		WHERE t.user_id = $1 AND t.deleted_at IS NULL AND t.kind <> 'transfer'
		  AND t.occurred_at >= $2 AND t.occurred_at < $3`, userID, from, to).Scan(&n)
	return n, err
}

// --- budgets ---

func (s *Store) SetBudget(ctx context.Context, userID, categoryID int64, month time.Time, currency string, limit int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO budgets (user_id, category_id, month, currency, limit_amount)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, category_id, month)
		DO UPDATE SET currency = EXCLUDED.currency, limit_amount = EXCLUDED.limit_amount`,
		userID, categoryID, month, currency, limit)
	return err
}

func (s *Store) DeleteBudget(ctx context.Context, userID, categoryID int64, month time.Time) error {
	ct, err := s.pool.Exec(ctx,
		`DELETE FROM budgets WHERE user_id = $1 AND category_id = $2 AND month = $3`,
		userID, categoryID, month)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Budgets returns the month's limits together with actual spending in those categories.
func (s *Store) Budgets(ctx context.Context, userID int64, month time.Time, from, to time.Time) ([]Budget, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT b.category_id, COALESCE(c.name, '?'), b.month, b.currency, cur.decimals, b.limit_amount,
		       COALESCE((SELECT SUM(t.amount)
		                 FROM transactions t
		                 JOIN accounts a ON a.id = t.account_id
		                 WHERE t.user_id = b.user_id AND t.category_id = b.category_id
		                   AND t.deleted_at IS NULL AND t.kind = 'expense'
		                   AND a.currency = b.currency
		                   AND t.occurred_at >= $3 AND t.occurred_at < $4), 0)
		FROM budgets b
		JOIN currencies cur ON cur.code = b.currency
		LEFT JOIN categories c ON c.id = b.category_id
		WHERE b.user_id = $1 AND b.month = $2
		ORDER BY c.name`, userID, month, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Budget
	for rows.Next() {
		var b Budget
		if err := rows.Scan(&b.CategoryID, &b.CategoryName, &b.Month, &b.Currency,
			&b.Decimals, &b.LimitAmount, &b.Spent); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// HasOperationsOn reports whether anything was recorded on a given day — the
// evening nudge is only worth sending to someone who hasn't.
func (s *Store) HasOperationsOn(ctx context.Context, userID int64, from, to time.Time) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM transactions
		    WHERE user_id = $1 AND deleted_at IS NULL
		      AND occurred_at >= $2 AND occurred_at < $3)`, userID, from, to).Scan(&exists)
	return exists, err
}
