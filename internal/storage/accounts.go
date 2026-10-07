package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateAccount(ctx context.Context, userID int64, name, currency string, initial int64) (Account, error) {
	var a Account
	err := s.pool.QueryRow(ctx,
		`INSERT INTO accounts (user_id, name, currency, initial_balance)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, user_id, name, currency, initial_balance, archived`,
		userID, name, currency, initial).
		Scan(&a.ID, &a.UserID, &a.Name, &a.Currency, &a.InitialBalance, &a.Archived)
	return a, err
}

func (s *Store) Account(ctx context.Context, userID, id int64) (Account, error) {
	var a Account
	err := s.pool.QueryRow(ctx,
		`SELECT a.id, a.user_id, a.name, a.currency, a.initial_balance, a.archived, c.decimals
		 FROM accounts a JOIN currencies c ON c.code = a.currency
		 WHERE a.id = $1 AND a.user_id = $2`, id, userID).
		Scan(&a.ID, &a.UserID, &a.Name, &a.Currency, &a.InitialBalance, &a.Archived, &a.Decimals)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

func (s *Store) ListAccounts(ctx context.Context, userID int64, withArchived bool) ([]Account, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT a.id, a.user_id, a.name, a.currency, a.initial_balance, a.archived, c.decimals
		 FROM accounts a JOIN currencies c ON c.code = a.currency
		 WHERE a.user_id = $1 AND ($2 OR NOT a.archived)
		 ORDER BY a.archived, a.name`, userID, withArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.UserID, &a.Name, &a.Currency, &a.InitialBalance, &a.Archived, &a.Decimals); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListAccountsWithBalance computes the balance: initial + income − expenses ± transfers ± debts.
func (s *Store) ListAccountsWithBalance(ctx context.Context, userID int64, withArchived bool) ([]Account, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.user_id, a.name, a.currency, a.initial_balance, a.archived, cur.decimals,
		       a.initial_balance
		       + COALESCE((SELECT SUM(CASE t.kind
		                                WHEN 'income'  THEN t.amount
		                                WHEN 'debt_in' THEN t.amount
		                                ELSE -t.amount END)
		                   FROM transactions t
		                   WHERE t.account_id = a.id AND t.deleted_at IS NULL), 0)
		       + COALESCE((SELECT SUM(COALESCE(t.to_amount, t.amount))
		                   FROM transactions t
		                   WHERE t.to_account_id = a.id AND t.deleted_at IS NULL), 0) AS balance
		FROM accounts a
		JOIN currencies cur ON cur.code = a.currency
		WHERE a.user_id = $1 AND ($2 OR NOT a.archived)
		ORDER BY a.archived, a.name`, userID, withArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.UserID, &a.Name, &a.Currency, &a.InitialBalance,
			&a.Archived, &a.Decimals, &a.Balance); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RenameAccount renames the place the given account belongs to — every currency
// kept there, since they are one place under one name.
func (s *Store) RenameAccount(ctx context.Context, userID, id int64, name string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE accounts SET name = $3
		 WHERE user_id = $2
		   AND name = (SELECT name FROM accounts WHERE id = $1 AND user_id = $2)`,
		id, userID, name)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PlaceCurrencies lists the currencies already kept at a place, archived
// included — a place cannot hold the same currency twice.
func (s *Store) PlaceCurrencies(ctx context.Context, userID int64, place string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT currency FROM accounts WHERE user_id = $1 AND lower(name) = lower($2)`, userID, place)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) SetAccountArchived(ctx context.Context, userID, id int64, archived bool) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE accounts SET archived = $3 WHERE id = $1 AND user_id = $2`, id, userID, archived)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetInitialBalance adjusts the starting balance — this is how the user "corrects" the balance to match reality.
func (s *Store) SetInitialBalance(ctx context.Context, userID, id, amount int64) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE accounts SET initial_balance = $3 WHERE id = $1 AND user_id = $2`, id, userID, amount)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AccountBalance is one account's current balance — shown on an operation's card,
// so the figure that matters after recording something is right there.
func (s *Store) AccountBalance(ctx context.Context, userID, accountID int64) (int64, error) {
	var balance int64
	err := s.pool.QueryRow(ctx, `
		SELECT a.initial_balance
		       + COALESCE((SELECT SUM(CASE t.kind
		                                WHEN 'income'  THEN t.amount
		                                WHEN 'debt_in' THEN t.amount
		                                ELSE -t.amount END)
		                   FROM transactions t
		                   WHERE t.account_id = a.id AND t.deleted_at IS NULL), 0)
		       + COALESCE((SELECT SUM(COALESCE(t.to_amount, t.amount))
		                   FROM transactions t
		                   WHERE t.to_account_id = a.id AND t.deleted_at IS NULL), 0)
		FROM accounts a
		WHERE a.id = $1 AND a.user_id = $2`, accountID, userID).Scan(&balance)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return balance, err
}
