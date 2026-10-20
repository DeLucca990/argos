package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrUnknownUser = errors.New("unknown user")

type User struct {
	ID           int64
	TelegramID   int64
	FirstName    string
	LastName     string
	Username     string
	LanguageCode string
	CreatedAt    time.Time
	LastSeenAt   time.Time
}

const userColumns = `id, telegram_id, first_name, last_name, username, language_code, created_at, last_seen_at`

func (s *Store) SaveUser(ctx context.Context, u User) (User, error) {
	var stored User
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		now := s.timestamp()
		res, err := tx.ExecContext(ctx, `UPDATE users SET first_name = ?, last_name = ?, username = ?,
			language_code = ?, last_seen_at = ? WHERE telegram_id = ?`,
			u.FirstName, u.LastName, u.Username, u.LanguageCode, now, u.TelegramID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO users (telegram_id, first_name, last_name, username,
				language_code, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				u.TelegramID, u.FirstName, u.LastName, u.Username, u.LanguageCode, now, now); err != nil {
				return err
			}
		}
		stored, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE telegram_id = ?`,
			u.TelegramID))
		return err
	})
	return stored, err
}

func (s *Store) User(ctx context.Context, id int64) (User, error) { return user(ctx, s.db, id) }

func user(ctx context.Context, q querier, id int64) (User, error) {
	u, err := scanUser(q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, fmt.Errorf("%w: %d", ErrUnknownUser, id)
	}
	return u, err
}

func scanUser(row *sql.Row) (User, error) {
	var (
		u                 User
		created, lastSeen sql.NullString
	)
	err := row.Scan(&u.ID, &u.TelegramID, &u.FirstName, &u.LastName, &u.Username, &u.LanguageCode,
		&created, &lastSeen)
	if err != nil {
		return User{}, err
	}
	if u.CreatedAt, err = parseTimestamp(created); err != nil {
		return User{}, err
	}
	u.LastSeenAt, err = parseTimestamp(lastSeen)
	return u, err
}
