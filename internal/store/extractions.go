package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Extraction struct {
	ID               int64
	UserID           int64
	AttachmentID     int64
	ExpenseID        int64
	Model            string
	RawJSON          string
	Failed           bool
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	USD              float64
	CreatedAt        time.Time
}

func (s *Store) ExtractionFor(ctx context.Context, expenseID int64) (Extraction, error) {
	var (
		x                   Extraction
		attachment, expense sql.NullInt64
		created             sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, attachment_id, expense_id, model, raw_json, failed,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, usd, created_at
		FROM extractions WHERE expense_id = ? ORDER BY id DESC LIMIT 1`, expenseID).
		Scan(&x.ID, &x.UserID, &attachment, &expense, &x.Model, &x.RawJSON, &x.Failed, &x.InputTokens,
			&x.OutputTokens, &x.CacheReadTokens, &x.CacheWriteTokens, &x.USD, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Extraction{}, ErrNotFound
	}
	if err != nil {
		return Extraction{}, err
	}
	x.AttachmentID, x.ExpenseID = attachment.Int64, expense.Int64
	x.CreatedAt, err = parseTimestamp(created)
	return x, err
}

func (s *Store) RecordExtraction(ctx context.Context, x Extraction) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO extractions (user_id, attachment_id, expense_id, model,
		raw_json, failed, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, usd, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		x.UserID, nullInt(x.AttachmentID), nullInt(x.ExpenseID), x.Model, x.RawJSON, x.Failed,
		x.InputTokens, x.OutputTokens, x.CacheReadTokens, x.CacheWriteTokens, x.USD, s.timestamp())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) Spent(ctx context.Context, since time.Time) (usd float64, extractions int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(usd), 0), COUNT(*) FROM extractions WHERE created_at >= ?`,
		since.UTC().Format(timestampLayout)).Scan(&usd, &extractions)
	return usd, extractions, err
}
