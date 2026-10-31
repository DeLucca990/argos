package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DeLucca990/argos/internal/cost"
)

const backupLayout = "2006-01-02-150405"

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

func (s *Store) ExpenseCost(ctx context.Context, expenseID int64) (cost.Spend, error) {
	var sp cost.Spend
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(cache_read_tokens), 0), COALESCE(SUM(cache_write_tokens), 0), COALESCE(SUM(usd), 0)
		FROM extractions WHERE expense_id = ?`, expenseID).Scan(&sp.Input, &sp.Output, &sp.CacheRead, &sp.CacheWrite, &sp.USD)
	return sp, err
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

// ScheduledBackup writes a backup once the newest in dir is every old, keeps the newest keep, and
// says when the next is due; a backup's time is read from its name, so restarts do not add any.
func (s *Store) ScheduledBackup(ctx context.Context, dir string, keep int, every time.Duration, now time.Time) (written string, removed int, next time.Time, err error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", 0, time.Time{}, err
	}
	files, err := filepath.Glob(filepath.Join(dir, "argos-*.db"))
	if err != nil {
		return "", 0, time.Time{}, err
	}
	type backup struct {
		file string
		at   time.Time
	}
	var backups []backup
	for _, f := range files {
		if at, ok := backupTime(f, now.Location()); ok {
			backups = append(backups, backup{f, at})
		}
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].at.Before(backups[j].at) })

	if len(backups) == 0 || !now.Before(backups[len(backups)-1].at.Add(every)) {
		file := filepath.Join(dir, "argos-"+now.Format(backupLayout)+".db")
		if err := s.Backup(ctx, file); err != nil {
			return "", 0, time.Time{}, err
		}
		written = file
		backups = append(backups, backup{file, now})
	}
	next = backups[len(backups)-1].at.Add(every)

	for len(backups) > keep {
		if err := os.Remove(backups[0].file); err != nil {
			return written, removed, next, err
		}
		backups, removed = backups[1:], removed+1
	}
	return written, removed, next, nil
}

func backupTime(file string, loc *time.Location) (time.Time, bool) {
	name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(file), "argos-"), ".db")
	for _, layout := range []string{backupLayout, "2006-01-02"} {
		if at, err := time.ParseInLocation(layout, name, loc); err == nil {
			return at, true
		}
	}
	return time.Time{}, false
}

func (s *Store) DigestSent(ctx context.Context, userID int64, month string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM digests WHERE user_id = ? AND month = ?`, userID, month).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) MarkDigestSent(ctx context.Context, userID int64, month string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO digests (user_id, month, sent_at) VALUES (?, ?, ?)
		ON CONFLICT (user_id, month) DO NOTHING`, userID, month, s.timestamp())
	return err
}
