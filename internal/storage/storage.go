package storage

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"aksha-bitpesin/migrations"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 8
	cfg.MaxConnLifetime = time.Hour

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connecting to the database: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging the database: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Ping checks that the database still answers — used by the health endpoint.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// migrationLock guards the migration run across processes. Any constant works,
// as long as every instance uses the same one.
const migrationLock = 8_452_119_004

// Migrate brings the schema up to date by running every .sql file in filename
// order, on every start. There is no record of what was applied: each file is
// written to be idempotent (CREATE ... IF NOT EXISTS, ADD COLUMN IF NOT EXISTS),
// so running it against a schema that already has it changes nothing. A database
// that is behind gets the missing tables and columns; one updated by hand is left
// as it is. Changes that cannot be written that way — moving data around — are
// done by hand, not here.
//
// The run happens in one transaction holding a Postgres advisory lock, so two
// processes starting at once don't race, and since Postgres DDL is transactional
// a failure halfway leaves the schema untouched.
func (s *Store) Migrate(ctx context.Context) error {
	return s.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrationLock)); err != nil {
			return fmt.Errorf("taking the migration lock: %w", err)
		}

		entries, err := fs.Glob(migrations.FS, "*.sql")
		if err != nil {
			return err
		}
		sort.Strings(entries)

		for _, name := range entries {
			body, err := migrations.FS.ReadFile(name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(body)); err != nil {
				return fmt.Errorf("migration %s: %w", name, err)
			}
			slog.Debug("schema file applied", "file", name)
		}
		return nil
	})
}

// InTx runs fn inside a transaction: commits on nil, rolls back on error.
func (s *Store) InTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}
