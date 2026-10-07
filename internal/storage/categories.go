package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

const catCols = `c.id, c.user_id, c.name, c.kind, COALESCE(c.emoji,''), c.archived`

// no alias — for RETURNING
const catColsPlain = `id, user_id, name, kind, COALESCE(emoji,''), archived`

func scanCategory(row pgx.Row) (Category, error) {
	var c Category
	err := row.Scan(&c.ID, &c.UserID, &c.Name, &c.Kind, &c.Emoji, &c.Archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// ListCategories returns the user's categories of a kind, most-used first, so the
// ones they reach for are the first buttons on screen.
func (s *Store) ListCategories(ctx context.Context, userID int64, kind string) ([]Category, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+catCols+`
		 FROM categories c
		 WHERE c.user_id = $1
		   AND ($2 = '' OR c.kind = $2)
		   AND NOT c.archived
		 ORDER BY c.kind,
		          (SELECT COUNT(*) FROM transactions t
		           WHERE t.category_id = c.id AND t.deleted_at IS NULL) DESC,
		          c.name`, userID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Category
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Category(ctx context.Context, userID, id int64) (Category, error) {
	return scanCategory(s.pool.QueryRow(ctx,
		`SELECT `+catCols+` FROM categories c
		 WHERE c.id = $1 AND c.user_id = $2`, id, userID))
}

// FindCategory looks up a category by name: exact match, then prefix, then substring.
func (s *Store) FindCategory(ctx context.Context, userID int64, kind, name string) (Category, error) {
	return scanCategory(s.pool.QueryRow(ctx,
		`SELECT `+catCols+`
		 FROM categories c
		 WHERE c.user_id = $1 AND c.kind = $2 AND NOT c.archived
		   AND (lower(c.name) = lower($3)
		        OR lower(c.name) LIKE lower($3) || '%'
		        OR lower($3) LIKE lower(c.name) || '%')
		 ORDER BY (lower(c.name) = lower($3)) DESC,
		          (lower(c.name) LIKE lower($3) || '%') DESC,
		          length(c.name)
		 LIMIT 1`, userID, kind, name))
}

func (s *Store) CreateCategory(ctx context.Context, userID int64, name, kind, emoji string) (Category, error) {
	return scanCategory(s.pool.QueryRow(ctx,
		`INSERT INTO categories (user_id, name, kind, emoji)
		 VALUES ($1, $2, $3, NULLIF($4,''))
		 RETURNING `+catColsPlain, userID, name, kind, emoji))
}

// ArchiveCategory hides a category from the pickers. The operations already
// filed under it keep pointing at it, so past statements don't change.
func (s *Store) ArchiveCategory(ctx context.Context, userID, id int64) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE categories SET archived = true WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RenameCategory renames a category and sets its icon.
func (s *Store) RenameCategory(ctx context.Context, userID, id int64, name, emoji string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE categories SET name = $3, emoji = NULLIF($4,'') WHERE id = $1 AND user_id = $2`,
		id, userID, name, emoji)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CategoryUsage counts the operations filed under a category — asked before
// archiving one, so the answer says what it would hide.
func (s *Store) CategoryUsage(ctx context.Context, userID, id int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM transactions
		 WHERE user_id = $1 AND category_id = $2 AND deleted_at IS NULL`, userID, id).Scan(&n)
	return n, err
}
