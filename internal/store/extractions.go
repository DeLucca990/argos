package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Extraction struct {
	ID               int64
	Kind             string
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

const (
	KindReceipt    = "receipt"
	KindCorrection = "correction"
	KindQuestion   = "question"
	KindBank       = "bank"
)

func (s *Store) ExtractionFor(ctx context.Context, expenseID int64) (Extraction, error) {
	return s.extraction(ctx, `expense_id = ? ORDER BY id DESC`, expenseID)
}

func (s *Store) FirstReading(ctx context.Context, expenseID int64) (Extraction, error) {
	return s.extraction(ctx, `expense_id = ? AND kind = '`+KindReceipt+`' AND NOT failed ORDER BY id`, expenseID)
}

func (s *Store) extraction(ctx context.Context, where string, args ...any) (Extraction, error) {
	var (
		x                   Extraction
		attachment, expense sql.NullInt64
		created             sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, kind, user_id, attachment_id, expense_id, model, raw_json, failed,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, usd, created_at
		FROM extractions WHERE `+where+` LIMIT 1`, args...).
		Scan(&x.ID, &x.Kind, &x.UserID, &attachment, &expense, &x.Model, &x.RawJSON, &x.Failed, &x.InputTokens,
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
	if x.Kind == "" {
		x.Kind = KindReceipt
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO extractions (kind, user_id, attachment_id, expense_id, model,
		raw_json, failed, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, usd, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		x.Kind, x.UserID, nullInt(x.AttachmentID), nullInt(x.ExpenseID), x.Model, x.RawJSON, x.Failed,
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

func (s *Store) Calls(ctx context.Context, since time.Time) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, COUNT(*) FROM extractions WHERE created_at >= ? GROUP BY kind`,
		since.UTC().Format(timestampLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	calls := map[string]int{}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		calls[kind] = n
	}
	return calls, rows.Err()
}
