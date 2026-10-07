package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("not found")

const userCols = `u.id, u.telegram_id, COALESCE(u.username,''), u.timezone,
	u.main_currency, u.monthly_report, u.last_report_month, u.created_at, u.onboarded_at,
	u.daily_reminder, u.last_reminder`

// no alias — for RETURNING, where aliases aren't available
const userColsPlain = `id, telegram_id, COALESCE(username,''), timezone,
	main_currency, monthly_report, last_report_month, created_at, onboarded_at,
	daily_reminder, last_reminder`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.TelegramID, &u.Username, &u.Timezone,
		&u.MainCurrency, &u.MonthlyReport, &u.LastReportMonth, &u.CreatedAt, &u.OnboardedAt,
		&u.DailyReminder, &u.LastReminder)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// EnsureUser looks up a user by telegram_id or creates a new one.
func (s *Store) EnsureUser(ctx context.Context, telegramID int64, username, tz, currency string) (User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx,
		`SELECT `+userCols+` FROM users u WHERE telegram_id = $1`, telegramID))
	if err == nil {
		if username != "" && u.Username != username {
			_, _ = s.pool.Exec(ctx, `UPDATE users SET username = $2 WHERE id = $1`, u.ID, username)
			u.Username = username
		}
		return u, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return u, err
	}
	return scanUser(s.pool.QueryRow(ctx,
		`INSERT INTO users (telegram_id, username, timezone, main_currency)
		 VALUES ($1, NULLIF($2,''), $3, $4)
		 ON CONFLICT (telegram_id) DO UPDATE SET username = EXCLUDED.username
		 RETURNING `+userColsPlain, telegramID, username, tz, currency))
}

func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users u WHERE id = $1`, id))
}

// MarkOnboarded records that the first-run setup is done (or was skipped).
// SetDailyReminder turns the evening nudge on or off.
func (s *Store) SetDailyReminder(ctx context.Context, userID int64, on bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET daily_reminder = $2 WHERE id = $1`, userID, on)
	return err
}

// MarkReminderSent records that today's nudge has gone out.
func (s *Store) MarkReminderSent(ctx context.Context, userID int64, day time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET last_reminder = $2 WHERE id = $1`, userID, day)
	return err
}

func (s *Store) MarkOnboarded(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET onboarded_at = COALESCE(onboarded_at, now()) WHERE id = $1`, userID)
	return err
}

func (s *Store) SetTimezone(ctx context.Context, userID int64, tz string) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET timezone = $2 WHERE id = $1`, userID, tz)
	return err
}

func (s *Store) SetMainCurrency(ctx context.Context, userID int64, code string) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET main_currency = $2 WHERE id = $1`, userID, code)
	return err
}

func (s *Store) SetMonthlyReport(ctx context.Context, userID int64, on bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET monthly_report = $2 WHERE id = $1`, userID, on)
	return err
}

func (s *Store) MarkReportSent(ctx context.Context, userID int64, month time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET last_report_month = $2 WHERE id = $1`, userID, month)
	return err
}

// AllUsers — every bot user (the scheduler walks them on each tick).
func (s *Store) AllUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userCols+` FROM users u ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// --- dialog state ---

func (s *Store) SaveState(ctx context.Context, userID int64, state string, data []byte) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_states (user_id, state, data, updated_at)
		 VALUES ($1, $2, $3, now())
		 ON CONFLICT (user_id) DO UPDATE SET state = EXCLUDED.state, data = EXCLUDED.data, updated_at = now()`,
		userID, state, data)
	return err
}

func (s *Store) LoadState(ctx context.Context, userID int64) (string, []byte, error) {
	var state string
	var data []byte
	err := s.pool.QueryRow(ctx,
		`SELECT state, data FROM user_states WHERE user_id = $1`, userID).Scan(&state, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil
	}
	return state, data, err
}

func (s *Store) ClearState(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM user_states WHERE user_id = $1`, userID)
	return err
}

// UserData counts what a user has in the books — shown before erasing it, so
// the scale of what is about to go is on screen.
type UserData struct {
	Accounts   int
	Operations int
	Categories int
	Budgets    int
	Recurring  int
	Loans      int
}

// Any reports whether there is anything to erase at all.
func (d UserData) Any() bool {
	return d.Accounts+d.Operations+d.Categories+d.Budgets+d.Recurring+d.Loans > 0
}

// UserDataSummary counts everything belonging to a user, deleted operations
// included: a soft-deleted row is still their data.
func (s *Store) UserDataSummary(ctx context.Context, userID int64) (UserData, error) {
	var d UserData
	err := s.pool.QueryRow(ctx, `
		SELECT (SELECT COUNT(*) FROM accounts     WHERE user_id = $1),
		       (SELECT COUNT(*) FROM transactions WHERE user_id = $1),
		       (SELECT COUNT(*) FROM categories   WHERE user_id = $1),
		       (SELECT COUNT(*) FROM budgets      WHERE user_id = $1),
		       (SELECT COUNT(*) FROM recurring    WHERE user_id = $1),
		       (SELECT COUNT(*) FROM loans        WHERE user_id = $1)`, userID).
		Scan(&d.Accounts, &d.Operations, &d.Categories, &d.Budgets, &d.Recurring, &d.Loans)
	return d, err
}

// DeleteUserData erases everything a user has, including the user row, so the
// next message starts them over from the first-run setup.
//
// Market exchange rates are left alone: that table has no owner — it is the
// National Bank's published rates, shared by everyone using this bot.
//
// All of it happens in one transaction: either the person is gone completely or
// nothing changed. The order follows the foreign keys.
func (s *Store) DeleteUserData(ctx context.Context, userID int64) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		for _, statement := range []string{
			`DELETE FROM transactions WHERE user_id = $1`,
			`DELETE FROM budgets      WHERE user_id = $1`,
			`DELETE FROM recurring    WHERE user_id = $1`,
			`DELETE FROM user_states  WHERE user_id = $1`,
			`DELETE FROM loans        WHERE user_id = $1`,
			`DELETE FROM accounts     WHERE user_id = $1`,
			`DELETE FROM categories   WHERE user_id = $1`,
			`DELETE FROM users        WHERE id = $1`,
		} {
			if _, err := tx.Exec(ctx, statement, userID); err != nil {
				return fmt.Errorf("erasing user %d: %w", userID, err)
			}
		}
		return nil
	})
}
