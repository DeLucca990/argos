package store

import (
	"context"
	"time"
)

type Extraction struct {
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
