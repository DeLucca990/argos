package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

func (s *Store) RememberCard(ctx context.Context, chatID int64, messageID int, expenseID int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO cards (chat_id, message_id, expense_id) VALUES (?, ?, ?)
		ON CONFLICT (chat_id, message_id) DO UPDATE SET expense_id = excluded.expense_id`, chatID, messageID, expenseID)
	return err
}

func (s *Store) CardExpense(ctx context.Context, chatID int64, messageID int) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT expense_id FROM cards WHERE chat_id = ? AND message_id = ?`,
		chatID, messageID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

func (s *Store) ExpenseCost(ctx context.Context, expenseID int64) (float64, error) {
	var usd float64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(usd), 0) FROM extractions WHERE expense_id = ?`, expenseID).Scan(&usd)
	return usd, err
}

func (s *Store) Backup(ctx context.Context, file string) error {
	tmp := file + ".part"
	os.Remove(tmp)
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, file)
}

func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one)
}

// DailyBackup writes today's backup into dir unless it exists, and keeps the newest keep.
func (s *Store) DailyBackup(ctx context.Context, dir string, keep int, now time.Time) (written string, removed int, err error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", 0, err
	}
	file := filepath.Join(dir, "argos-"+now.Format("2006-01-02")+".db")
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		if err := s.Backup(ctx, file); err != nil {
			return "", 0, err
		}
		written = file
	}

	old, err := filepath.Glob(filepath.Join(dir, "argos-*.db"))
	if err != nil {
		return written, 0, err
	}
	sort.Strings(old)
	for len(old) > keep {
		if err := os.Remove(old[0]); err != nil {
			return written, removed, err
		}
		old, removed = old[1:], removed+1
	}
	return written, removed, nil
}
