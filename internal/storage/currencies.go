package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Store) Currencies(ctx context.Context) ([]Currency, error) {
	rows, err := s.pool.Query(ctx, `SELECT code, name, decimals FROM currencies ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Currency
	for rows.Next() {
		var c Currency
		if err := rows.Scan(&c.Code, &c.Name, &c.Decimals); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Currency(ctx context.Context, code string) (Currency, error) {
	var c Currency
	err := s.pool.QueryRow(ctx,
		`SELECT code, name, decimals FROM currencies WHERE code = $1`, code).Scan(&c.Code, &c.Name, &c.Decimals)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// DecimalsMap — currency code → number of decimal places.
func (s *Store) DecimalsMap(ctx context.Context) (map[string]int, error) {
	cs, err := s.Currencies(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]int, len(cs))
	for _, c := range cs {
		m[c.Code] = c.Decimals
	}
	return m, nil
}

// SetRate stores a rate for a date: how much base per 1 quote (KZT/USD = 540).
func (s *Store) SetRate(ctx context.Context, base, quote string, day time.Time, rate float64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO exchange_rates (base_code, quote_code, rate_date, rate)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (base_code, quote_code, rate_date) DO UPDATE SET rate = EXCLUDED.rate`,
		base, quote, day, rate)
	return err
}

// PivotCurrency — the currency used to compute a cross rate when there's no direct pair.
const PivotCurrency = "KZT"

// Rate looks up the quote→base rate on day, or the closest earlier date.
// Returns 1 for identical currencies and ErrNotFound if no rate exists at all.
func (s *Store) Rate(ctx context.Context, base, quote string, day time.Time) (float64, error) {
	return s.rate(ctx, base, quote, day, true)
}

func (s *Store) rate(ctx context.Context, base, quote string, day time.Time, allowPivot bool) (float64, error) {
	if base == quote {
		return 1, nil
	}
	var rate float64
	err := s.pool.QueryRow(ctx,
		`SELECT rate FROM exchange_rates
		 WHERE base_code = $1 AND quote_code = $2 AND rate_date <= $3
		 ORDER BY rate_date DESC LIMIT 1`, base, quote, day).Scan(&rate)
	if err == nil {
		return rate, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	// try the reverse direction
	err = s.pool.QueryRow(ctx,
		`SELECT rate FROM exchange_rates
		 WHERE base_code = $2 AND quote_code = $1 AND rate_date <= $3 AND rate <> 0
		 ORDER BY rate_date DESC LIMIT 1`, base, quote, day).Scan(&rate)
	if err == nil {
		return 1 / rate, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	// cross rate via the pivot currency: base→KZT→quote
	if allowPivot && base != PivotCurrency && quote != PivotCurrency {
		toPivot, err1 := s.rate(ctx, base, PivotCurrency, day, false)
		fromPivot, err2 := s.rate(ctx, PivotCurrency, quote, day, false)
		if err1 == nil && err2 == nil {
			return toPivot * fromPivot, nil
		}
	}
	return 0, ErrNotFound
}

// LatestRates — the most recent known rates against the base currency.
func (s *Store) LatestRates(ctx context.Context, base string) (map[string]float64, time.Time, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT ON (quote_code) quote_code, rate, rate_date
		 FROM exchange_rates WHERE base_code = $1
		 ORDER BY quote_code, rate_date DESC`, base)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()
	out := map[string]float64{}
	var newest time.Time
	for rows.Next() {
		var code string
		var rate float64
		var day time.Time
		if err := rows.Scan(&code, &rate, &day); err != nil {
			return nil, time.Time{}, err
		}
		out[code] = rate
		if day.After(newest) {
			newest = day
		}
	}
	return out, newest, rows.Err()
}

// MissingRateDays lists the days in a period that have operations needing a rate
// but no rate on or before them — what a backfill has to fetch. One row per day,
// oldest first; currencies already covered by an earlier rate are left out.
func (s *Store) MissingRateDays(ctx context.Context, userID int64, base string, from, to time.Time) ([]time.Time, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT date(t.occurred_at AT TIME ZONE 'UTC') AS day
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id
		WHERE t.user_id = $1 AND t.deleted_at IS NULL
		  AND t.occurred_at >= $3 AND t.occurred_at < $4
		  AND a.currency <> $2
		  AND COALESCE(t.original_currency, '') <> $2
		  AND NOT EXISTS (
		      SELECT 1 FROM exchange_rates r
		      WHERE r.base_code = $2 AND r.quote_code = a.currency
		        AND r.rate_date <= date(t.occurred_at AT TIME ZONE 'UTC'))
		ORDER BY day`, userID, base, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var day time.Time
		if err := rows.Scan(&day); err != nil {
			return nil, err
		}
		out = append(out, day)
	}
	return out, rows.Err()
}
