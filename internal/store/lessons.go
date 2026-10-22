package store

import (
	"context"
	"database/sql"
	"time"
)

const MaxLessons = 20

type Lesson struct {
	ID        int64
	ExpenseID int64
	Field     string
	Value     string
	Layout    string
	Payee     string
	CreatedAt time.Time
}

func (s *Store) AddLessons(ctx context.Context, userID int64, lessons []Lesson) ([]Lesson, error) {
	var saved []Lesson
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		now := s.timestamp()
		for _, l := range lessons {
			if _, err := tx.ExecContext(ctx, `DELETE FROM lessons WHERE user_id = ? AND field = ? AND layout = ? AND payee = ?`,
				userID, l.Field, l.Layout, l.Payee); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO lessons (user_id, expense_id, field, value, layout, payee, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, userID, nullInt(l.ExpenseID), l.Field, l.Value, l.Layout, l.Payee, now)
			if err != nil {
				return err
			}
			if l.ID, err = res.LastInsertId(); err != nil {
				return err
			}
			saved = append(saved, l)
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM lessons WHERE user_id = ? AND id NOT IN
			(SELECT id FROM lessons WHERE user_id = ? ORDER BY id DESC LIMIT ?)`, userID, userID, MaxLessons)
		return err
	})
	return saved, err
}

func (s *Store) Lessons(ctx context.Context, userID int64) ([]Lesson, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, expense_id, field, value, layout, payee, created_at FROM lessons
		WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lessons []Lesson
	for rows.Next() {
		var (
			l       Lesson
			expense sql.NullInt64
			created sql.NullString
		)
		if err := rows.Scan(&l.ID, &expense, &l.Field, &l.Value, &l.Layout, &l.Payee, &created); err != nil {
			return nil, err
		}
		l.ExpenseID = expense.Int64
		if l.CreatedAt, err = parseTimestamp(created); err != nil {
			return nil, err
		}
		lessons = append(lessons, l)
	}
	return lessons, rows.Err()
}

func (s *Store) ForgetLessons(ctx context.Context, userID int64, ids ...int64) (int64, error) {
	var forgotten int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			res, err := tx.ExecContext(ctx, `DELETE FROM lessons WHERE id = ? AND user_id = ?`, id, userID)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			forgotten += n
		}
		return nil
	})
	return forgotten, err
}
