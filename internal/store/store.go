package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound        = errors.New("not found")
	ErrNotPending      = errors.New("no longer pending")
	ErrDuplicate       = errors.New("already recorded")
	ErrUnknownCategory = errors.New("unknown category")
)

const (
	dateLayout      = "2006-01-02"
	timeLayout      = "15:04"
	timestampLayout = time.RFC3339
)

type Store struct {
	db  *sql.DB
	now func() time.Time

	version int
	applied []string
}

func Open(file string) (*Store, error) { return openWith(file, migrations) }

func openWith(file string, list []migration) (*Store, error) {
	query := url.Values{
		"_pragma": {"busy_timeout(5000)", "foreign_keys(1)", "journal_mode(WAL)", "synchronous(NORMAL)"},
		"_txlock": {"immediate"},
	}
	db, err := sql.Open("sqlite", file+"?"+query.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db, now: time.Now}
	if err := s.migrate(context.Background(), list); err != nil {
		db.Close()
		return nil, fmt.Errorf("database %s: %w", file, err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Version() int { return s.version }

func (s *Store) Applied() []string { return s.applied }

func (s *Store) migrate(ctx context.Context, list []migration) error {
	if len(list) == 0 {
		return errors.New("this build has no migrations")
	}
	for i, m := range list {
		if n, _, _ := strings.Cut(m.name, "_"); n != fmt.Sprintf("%04d", i+1) {
			return fmt.Errorf("migration %s is out of sequence: expected %04d", m.name, i+1)
		}
	}

	var current int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return err
	}
	if current > len(list) {
		return fmt.Errorf("it is at schema version %d and this build knows only %d: it was written by a newer argos",
			current, len(list))
	}
	s.version = current

	for i, m := range list[current:] {
		version := current + i + 1
		if err := s.inTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return fmt.Errorf("migration %s: %w", m.name, err)
			}
			_, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(version))
			return err
		}); err != nil {
			return err
		}
		s.version = version
		s.applied = append(s.applied, m.name)
	}
	return nil
}

func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) timestamp() string { return s.now().UTC().Format(timestampLayout) }

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func parseTimestamp(v sql.NullString) (time.Time, error) {
	if !v.Valid {
		return time.Time{}, nil
	}
	return time.Parse(timestampLayout, v.String)
}
