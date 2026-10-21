package store

import (
	"context"
	"time"
)

type CategoryTotal struct {
	Category   Category
	TotalCents int64
	Count      int
}

type Summary struct {
	TotalCents int64
	Count      int
	ByCategory []CategoryTotal
}

func (s *Store) Summary(ctx context.Context, userID int64, from, to time.Time) (Summary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.slug, c.label, c.emoji, SUM(e.total_cents), COUNT(*)
		FROM expenses e JOIN categories c ON c.id = e.category_id
		WHERE e.user_id = ? AND e.status = 'confirmed' AND e.spent_on >= ? AND e.spent_on < ?
		GROUP BY c.id ORDER BY SUM(e.total_cents) DESC, c.position`,
		userID, from.Format(dateLayout), to.Format(dateLayout))
	if err != nil {
		return Summary{}, err
	}
	defer rows.Close()

	var sum Summary
	for rows.Next() {
		var t CategoryTotal
		if err := rows.Scan(&t.Category.ID, &t.Category.Slug, &t.Category.Label, &t.Category.Emoji, &t.TotalCents, &t.Count); err != nil {
			return Summary{}, err
		}
		sum.TotalCents += t.TotalCents
		sum.Count += t.Count
		sum.ByCategory = append(sum.ByCategory, t)
	}
	return sum, rows.Err()
}

func (s *Store) Recent(ctx context.Context, userID int64, limit int) ([]Expense, error) {
	return s.expenses(ctx, `SELECT `+expenseColumns+` FROM expenses
		WHERE user_id = ? AND status = 'confirmed'
		ORDER BY spent_on DESC, spent_time IS NULL, spent_time DESC, id DESC LIMIT ?`, userID, limit)
}

func (s *Store) Between(ctx context.Context, userID int64, from, to time.Time) ([]Expense, error) {
	return s.expenses(ctx, `SELECT `+expenseColumns+` FROM expenses
		WHERE user_id = ? AND status = 'confirmed' AND spent_on >= ? AND spent_on < ?
		ORDER BY spent_on, spent_time IS NOT NULL, spent_time, id`,
		userID, from.Format(dateLayout), to.Format(dateLayout))
}

func (s *Store) expenses(ctx context.Context, query string, args ...any) ([]Expense, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Expense
	for rows.Next() {
		e, err := scanExpense(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, e)
	}
	return list, rows.Err()
}
