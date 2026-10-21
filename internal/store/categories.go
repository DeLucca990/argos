package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const (
	FallbackCategory = "outros"
	MaxCategories    = 30
	defaultEmoji     = "🏷️"
)

var (
	ErrCategoryLimit  = errors.New("too many categories")
	ErrSharedCategory = errors.New("a default category, the same for everyone")
	ErrCategoryExists = errors.New("a category by that name already exists")
)

type Category struct {
	ID       int64
	Slug     string
	Label    string
	Emoji    string
	Hint     string
	Custom   bool
	Archived bool
}

var unaccent = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i", "ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u", "ç", "c", "ñ", "n",
)

func Slug(label string) string {
	var b strings.Builder
	gap := false
	for _, r := range unaccent.Replace(strings.ToLower(label)) {
		switch {
		case unicode.Is(unicode.Mn, r):
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if gap && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			gap = false
		default:
			gap = true
		}
	}
	return b.String()
}

const categoryColumns = `id, slug, label, emoji, hint, user_id IS NOT NULL, archived_at IS NOT NULL`

const visibleTo = `(user_id IS NULL OR user_id = ?)`

func (s *Store) Categories(ctx context.Context, userID int64) ([]Category, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+categoryColumns+` FROM categories WHERE `+visibleTo+`
		ORDER BY slug = ? AND user_id IS NULL, user_id IS NOT NULL, position, id`, userID, FallbackCategory)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cats []Category
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		cats = append(cats, c)
	}
	return cats, rows.Err()
}

func (s *Store) SaveCategory(ctx context.Context, userID int64, c Category) (saved Category, created bool, err error) {
	c.Label = strings.TrimSpace(c.Label)
	c.Slug = Slug(c.Label)
	if c.Slug == "" {
		return Category{}, false, fmt.Errorf("the name %q has no letter or digit", c.Label)
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		existing, err := categoryBySlug(ctx, tx, userID, c.Slug)
		switch {
		case err == nil && !existing.Custom:
			saved = existing
			return ErrSharedCategory
		case err == nil:
			if existing.Archived {
				if err := roomFor(ctx, tx, userID); err != nil {
					return err
				}
				created = true
			}
			saved = existing
			saved.Archived = false
			if c.Emoji != "" {
				saved.Emoji = c.Emoji
			}
			if c.Hint != "" {
				saved.Hint = c.Hint
			}
			_, err := tx.ExecContext(ctx, `UPDATE categories SET emoji = ?, hint = ?, archived_at = NULL WHERE id = ?`,
				saved.Emoji, saved.Hint, saved.ID)
			return err
		case !errors.Is(err, ErrUnknownCategory):
			return err
		}

		if _, err := user(ctx, tx, userID); err != nil {
			return err
		}
		if err := roomFor(ctx, tx, userID); err != nil {
			return err
		}
		var last int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) FROM categories WHERE user_id = ?`,
			userID).Scan(&last); err != nil {
			return err
		}
		if c.Emoji == "" {
			c.Emoji = defaultEmoji
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO categories (user_id, slug, label, emoji, hint, position)
			VALUES (?, ?, ?, ?, ?, ?)`, userID, c.Slug, c.Label, c.Emoji, c.Hint, last+1)
		if err != nil {
			return err
		}
		if c.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		c.Custom = true
		saved, created = c, true
		return nil
	})
	return saved, created, err
}

func (s *Store) UpdateCategory(ctx context.Context, userID int64, c Category) (Category, error) {
	c.Label = strings.TrimSpace(c.Label)
	c.Slug = Slug(c.Label)
	if c.Slug == "" {
		return Category{}, fmt.Errorf("the name %q has no letter or digit", c.Label)
	}
	if c.Emoji == "" {
		c.Emoji = defaultEmoji
	}
	var saved Category
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		current, err := ownCategory(ctx, tx, userID, c.ID)
		if err != nil {
			return err
		}
		if c.Slug != current.Slug {
			switch other, err := categoryBySlug(ctx, tx, userID, c.Slug); {
			case err == nil && !other.Custom:
				return ErrSharedCategory
			case err == nil:
				return ErrCategoryExists
			case !errors.Is(err, ErrUnknownCategory):
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE categories SET slug = ?, label = ?, emoji = ?, hint = ? WHERE id = ?`,
			c.Slug, c.Label, c.Emoji, c.Hint, c.ID); err != nil {
			return err
		}
		saved = c
		saved.Custom, saved.Archived = true, current.Archived
		return nil
	})
	return saved, err
}

func (s *Store) RemoveCategory(ctx context.Context, userID, id int64) (archived bool, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := ownCategory(ctx, tx, userID, id); err != nil {
			return err
		}
		var used bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM expenses WHERE category_id = ?)`, id).Scan(&used); err != nil {
			return err
		}
		if !used {
			_, err := tx.ExecContext(ctx, `DELETE FROM categories WHERE id = ?`, id)
			return err
		}
		archived = true
		_, err := tx.ExecContext(ctx, `UPDATE categories SET archived_at = COALESCE(archived_at, ?) WHERE id = ?`,
			s.timestamp(), id)
		return err
	})
	return archived, err
}

func (s *Store) CategoryUse(ctx context.Context, userID int64, since time.Time) (map[int64]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT category_id, COUNT(*) FROM expenses
		WHERE user_id = ? AND status = 'confirmed' AND spent_on >= ? GROUP BY category_id`, userID, since.Format(dateLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	use := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		use[id] = n
	}
	return use, rows.Err()
}

func roomFor(ctx context.Context, tx *sql.Tx, userID int64) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM categories WHERE archived_at IS NULL AND `+visibleTo,
		userID).Scan(&count); err != nil {
		return err
	}
	if count >= MaxCategories {
		return ErrCategoryLimit
	}
	return nil
}

func ownCategory(ctx context.Context, q querier, userID, id int64) (Category, error) {
	c, err := scanCategory(q.QueryRowContext(ctx, `SELECT `+categoryColumns+` FROM categories WHERE id = ? AND user_id = ?`,
		id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return Category{}, fmt.Errorf("%w: %d", ErrUnknownCategory, id)
	}
	return c, err
}

func categoryBySlug(ctx context.Context, q querier, userID int64, slug string) (Category, error) {
	c, err := scanCategory(q.QueryRowContext(ctx, `SELECT `+categoryColumns+` FROM categories
		WHERE slug = ? AND `+visibleTo+` ORDER BY user_id IS NOT NULL LIMIT 1`, slug, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return Category{}, fmt.Errorf("%w: %q", ErrUnknownCategory, slug)
	}
	return c, err
}

func scanCategory(row interface{ Scan(...any) error }) (Category, error) {
	var c Category
	err := row.Scan(&c.ID, &c.Slug, &c.Label, &c.Emoji, &c.Hint, &c.Custom, &c.Archived)
	return c, err
}
