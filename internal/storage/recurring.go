package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const recCols = `r.id, r.user_id, r.account_id, r.category_id, r.kind, r.amount,
	r.day_of_month, COALESCE(r.note,''), r.active, r.last_created_month,
	a.name, a.currency, cur.decimals, c.name`

const recFrom = `FROM recurring r
	JOIN accounts a ON a.id = r.account_id
	JOIN currencies cur ON cur.code = a.currency
	JOIN categories c ON c.id = r.category_id`

func scanRecurring(rows interface{ Scan(...any) error }) (Recurring, error) {
	var r Recurring
	err := rows.Scan(&r.ID, &r.UserID, &r.AccountID, &r.CategoryID, &r.Kind, &r.Amount,
		&r.DayOfMonth, &r.Note, &r.Active, &r.LastCreatedMonth,
		&r.AccountName, &r.AccountCurr, &r.Decimals, &r.CategoryName)
	return r, err
}

func (s *Store) CreateRecurring(ctx context.Context, r Recurring) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO recurring (user_id, account_id, category_id, kind, amount, day_of_month, note)
		 VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,'')) RETURNING id`,
		r.UserID, r.AccountID, r.CategoryID, r.Kind, r.Amount, r.DayOfMonth, r.Note).Scan(&id)
	return id, err
}

func (s *Store) ListRecurring(ctx context.Context, userID int64) ([]Recurring, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+recCols+` `+recFrom+`
		 WHERE r.user_id = $1 ORDER BY r.active DESC, r.day_of_month`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Recurring
	for rows.Next() {
		r, err := scanRecurring(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteRecurring(ctx context.Context, userID, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM recurring WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DueRecurring — the user's active recurring operations that are due to be created this month.
// day — today's day-of-month in the user's timezone, lastDay — the number of days in the month
// (so the "31st" isn't lost in February).
func (s *Store) DueRecurring(ctx context.Context, userID int64, month time.Time, day, lastDay int) ([]Recurring, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+recCols+` `+recFrom+`
		 WHERE r.user_id = $1 AND r.active
		   AND (r.last_created_month IS NULL OR r.last_created_month < $2)
		   AND LEAST(r.day_of_month, $4) <= $3
		 ORDER BY r.day_of_month`, userID, month, day, lastDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Recurring
	for rows.Next() {
		r, err := scanRecurring(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordRecurring creates this month's operation of a recurring one, at most
// once. Marking the month and inserting the operation happen in one transaction,
// and the mark is claimed first: a second pass running at the same time finds
// the month already taken and creates nothing. created is false then.
func (s *Store) RecordRecurring(ctx context.Context, r Recurring, month, at time.Time) (t Transaction, created bool, err error) {
	if r.Amount <= 0 {
		return t, false, fmt.Errorf("recurring %d: amount must be greater than zero", r.ID)
	}
	var id int64
	err = s.InTx(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`UPDATE recurring SET last_created_month = $3
			 WHERE id = $1 AND user_id = $2 AND active
			   AND (last_created_month IS NULL OR last_created_month < $3)`,
			r.ID, r.UserID, month)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return nil // another pass got here first
		}
		id, err = insertTransaction(ctx, tx, Transaction{
			UserID:     r.UserID,
			AccountID:  r.AccountID,
			CategoryID: &r.CategoryID,
			Kind:       r.Kind,
			Amount:     r.Amount,
			Note:       r.Note,
			Source:     r.CategoryName,
			OccurredAt: at,
		})
		return err
	})
	if err != nil || id == 0 {
		return t, false, err
	}
	// Recorded either way; the read back is only for the notification.
	t, err = s.Transaction(ctx, r.UserID, id)
	return t, true, err
}
