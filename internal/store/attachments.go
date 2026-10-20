package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Attachment struct {
	ID        int64
	SHA256    string
	Path      string
	MIME      string
	Size      int64
	CreatedAt time.Time
}

// AddAttachment returns the row already stored for a.SHA256 when there is one,
// with its original path, so the same file sent twice is kept once.
func (s *Store) AddAttachment(ctx context.Context, a Attachment) (Attachment, error) {
	var stored Attachment
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		stored, err = attachment(ctx, tx, a.SHA256)
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO attachments (sha256, path, mime, size_bytes, created_at)
			VALUES (?, ?, ?, ?, ?)`, a.SHA256, a.Path, a.MIME, a.Size, s.timestamp()); err != nil {
			return err
		}
		stored, err = attachment(ctx, tx, a.SHA256)
		return err
	})
	return stored, err
}

func attachment(ctx context.Context, q querier, sha256 string) (Attachment, error) {
	var (
		a       Attachment
		created sql.NullString
	)
	err := q.QueryRowContext(ctx, `SELECT id, sha256, path, mime, size_bytes, created_at
		FROM attachments WHERE sha256 = ?`, sha256).
		Scan(&a.ID, &a.SHA256, &a.Path, &a.MIME, &a.Size, &created)
	if err != nil {
		return Attachment{}, err
	}
	a.CreatedAt, err = parseTimestamp(created)
	return a, err
}
