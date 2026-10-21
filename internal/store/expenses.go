package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Status string

const (
	Pending   Status = "pending"
	Confirmed Status = "confirmed"
	Discarded Status = "discarded"
)

type Source string

const (
	SourcePhoto Source = "photo"
	SourcePDF   Source = "pdf"
	SourceText  Source = "text"
)

type Expense struct {
	ID            int64
	UserID        int64
	Status        Status
	SpentOn       time.Time
	SpentTime     string
	Merchant      string
	MerchantTaxID string
	Category      string
	PaymentMethod string
	Account       string
	CardLast4     string
	TotalCents    int64
	Currency      string
	Description   string
	NFCeKey       string
	Source        Source
	AttachmentID  int64
	Items         []Item
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ConfirmedAt   time.Time
}

type Item struct {
	Description string
	Quantity    float64
	UnitCents   int64
	TotalCents  int64
}

type DuplicateError struct{ Existing Expense }

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("already recorded as expense %d (%s)", e.Existing.ID, e.Existing.Status)
}

func (e *DuplicateError) Is(target error) bool { return target == ErrDuplicate }

const categorySlug = `(SELECT c.slug FROM categories c WHERE c.id = category_id)`

const expenseColumns = `id, user_id, status, spent_on, spent_time, merchant, merchant_tax_id, ` + categorySlug + `,
	payment_method, account, card_last4, total_cents, currency, description, nfce_key, source, attachment_id,
	created_at, updated_at, confirmed_at`

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (s *Store) AddExpense(ctx context.Context, e Expense) (int64, error) {
	if e.TotalCents <= 0 {
		return 0, fmt.Errorf("total must be positive, got %d cents", e.TotalCents)
	}
	if e.Currency == "" {
		e.Currency = "BRL"
	}
	switch e.Status {
	case "":
		e.Status = Pending
	case Pending, Confirmed:
	default:
		return 0, fmt.Errorf("a new expense is pending or confirmed, not %s", e.Status)
	}

	var id int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := user(ctx, tx, e.UserID); err != nil {
			return err
		}
		category, err := categoryBySlug(ctx, tx, e.UserID, e.Category)
		if err != nil {
			return err
		}
		if existing, found, err := duplicateOf(ctx, tx, e); err != nil {
			return err
		} else if found {
			return &DuplicateError{Existing: existing}
		}

		now := s.timestamp()
		var confirmedAt any
		if e.Status == Confirmed {
			confirmedAt = now
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO expenses (user_id, status, spent_on, spent_time, merchant,
			merchant_tax_id, category_id, payment_method, account, card_last4, total_cents, currency, description,
			nfce_key, source, attachment_id, created_at, updated_at, confirmed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.UserID, e.Status, e.SpentOn.Format(dateLayout), nullString(e.SpentTime), e.Merchant, e.MerchantTaxID,
			category.ID, nullString(e.PaymentMethod), e.Account, nullString(e.CardLast4), e.TotalCents, e.Currency,
			e.Description,
			nullString(e.NFCeKey), e.Source, nullInt(e.AttachmentID), now, now, confirmedAt)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}

		return insertItems(ctx, tx, id, e.Items)
	})
	return id, err
}

func (s *Store) Duplicate(ctx context.Context, e Expense) (Expense, bool, error) {
	return duplicateOf(ctx, s.db, e)
}

func duplicateOf(ctx context.Context, q querier, e Expense) (Expense, bool, error) {
	if e.AttachmentID == 0 && e.NFCeKey == "" {
		return Expense{}, false, nil
	}
	existing, err := scanExpense(q.QueryRowContext(ctx, `SELECT `+expenseColumns+` FROM expenses
		WHERE status != 'discarded' AND (attachment_id = ? OR nfce_key = ?) ORDER BY id LIMIT 1`,
		nullInt(e.AttachmentID), nullString(e.NFCeKey)))
	if errors.Is(err, ErrNotFound) {
		return Expense{}, false, nil
	}
	return existing, err == nil, err
}

func (s *Store) Expense(ctx context.Context, id int64) (Expense, error) {
	return expenseWithItems(ctx, s.db, id)
}

func expenseWithItems(ctx context.Context, q querier, id int64) (Expense, error) {
	e, err := scanExpense(q.QueryRowContext(ctx, `SELECT `+expenseColumns+` FROM expenses WHERE id = ?`, id))
	if err != nil {
		return Expense{}, err
	}

	rows, err := q.QueryContext(ctx, `SELECT description, quantity, unit_cents, total_cents
		FROM expense_items WHERE expense_id = ? ORDER BY position`, id)
	if err != nil {
		return Expense{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			item Item
			unit sql.NullInt64
		)
		if err := rows.Scan(&item.Description, &item.Quantity, &unit, &item.TotalCents); err != nil {
			return Expense{}, err
		}
		item.UnitCents = unit.Int64
		e.Items = append(e.Items, item)
	}
	return e, rows.Err()
}

func (s *Store) Confirm(ctx context.Context, userID, id int64) (Expense, error) {
	return s.decide(ctx, userID, id, Confirmed, Pending)
}

func (s *Store) Discard(ctx context.Context, userID, id int64) (Expense, error) {
	return s.decide(ctx, userID, id, Discarded, Pending, Confirmed)
}

func (s *Store) decide(ctx context.Context, userID, id int64, to Status, from ...Status) (Expense, error) {
	var e Expense
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		now := s.timestamp()
		var confirmedAt any
		if to == Confirmed {
			confirmedAt = now
		}
		args := []any{to, now, confirmedAt, id, userID}
		for _, f := range from {
			args = append(args, f)
		}
		res, err := tx.ExecContext(ctx, `UPDATE expenses SET status = ?, updated_at = ?,
			confirmed_at = COALESCE(?, confirmed_at) WHERE id = ? AND user_id = ? AND status IN (`+
			strings.TrimSuffix(strings.Repeat("?, ", len(from)), ", ")+`)`, args...)
		if err != nil {
			return err
		}
		if err := changedOne(ctx, tx, res, userID, id); err != nil {
			return err
		}
		e, err = expenseWithItems(ctx, tx, id)
		return err
	})
	return e, err
}

func insertItems(ctx context.Context, tx *sql.Tx, expenseID int64, items []Item) error {
	for i, item := range items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO expense_items (expense_id, position, description,
			quantity, unit_cents, total_cents) VALUES (?, ?, ?, ?, ?, ?)`,
			expenseID, i, item.Description, item.Quantity, nullInt(item.UnitCents), item.TotalCents); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpdateExpense(ctx context.Context, userID int64, e Expense) (Expense, error) {
	if e.TotalCents <= 0 {
		return Expense{}, fmt.Errorf("total must be positive, got %d cents", e.TotalCents)
	}
	if e.Currency == "" {
		e.Currency = "BRL"
	}
	var updated Expense
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		category, err := categoryBySlug(ctx, tx, userID, e.Category)
		if err != nil {
			return err
		}
		if e.NFCeKey != "" {
			existing, err := scanExpense(tx.QueryRowContext(ctx, `SELECT `+expenseColumns+` FROM expenses
				WHERE nfce_key = ? AND status != 'discarded' AND id != ?`, e.NFCeKey, e.ID))
			switch {
			case err == nil:
				return &DuplicateError{Existing: existing}
			case !errors.Is(err, ErrNotFound):
				return err
			}
		}

		res, err := tx.ExecContext(ctx, `UPDATE expenses SET spent_on = ?, spent_time = ?, merchant = ?,
			merchant_tax_id = ?, category_id = ?, payment_method = ?, account = ?, card_last4 = ?, total_cents = ?,
			currency = ?, description = ?, nfce_key = ?, updated_at = ?
			WHERE id = ? AND user_id = ? AND status != 'discarded'`,
			e.SpentOn.Format(dateLayout), nullString(e.SpentTime), e.Merchant, e.MerchantTaxID, category.ID,
			nullString(e.PaymentMethod), e.Account, nullString(e.CardLast4), e.TotalCents, e.Currency,
			e.Description, nullString(e.NFCeKey),
			s.timestamp(), e.ID, userID)
		if err != nil {
			return err
		}
		if err := changedOne(ctx, tx, res, userID, e.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM expense_items WHERE expense_id = ?`, e.ID); err != nil {
			return err
		}
		if err := insertItems(ctx, tx, e.ID, e.Items); err != nil {
			return err
		}
		updated, err = expenseWithItems(ctx, tx, e.ID)
		return err
	})
	return updated, err
}

func (s *Store) SetCategory(ctx context.Context, userID, id int64, category string) (Expense, error) {
	var e Expense
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		c, err := categoryBySlug(ctx, tx, userID, category)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE expenses SET category_id = ?, updated_at = ?
			WHERE id = ? AND user_id = ? AND status != 'discarded'`, c.ID, s.timestamp(), id, userID)
		if err != nil {
			return err
		}
		if err := changedOne(ctx, tx, res, userID, id); err != nil {
			return err
		}
		e, err = expenseWithItems(ctx, tx, id)
		return err
	})
	return e, err
}

func (s *Store) Undo(ctx context.Context, userID int64) (Expense, error) {
	var e Expense
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM expenses WHERE user_id = ? AND status = 'confirmed'
			ORDER BY confirmed_at DESC, id DESC LIMIT 1`, userID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE expenses SET status = 'discarded', updated_at = ? WHERE id = ?`,
			s.timestamp(), id); err != nil {
			return err
		}
		e, err = expenseWithItems(ctx, tx, id)
		return err
	})
	return e, err
}

func changedOne(ctx context.Context, q querier, res sql.Result, userID, id int64) error {
	n, err := res.RowsAffected()
	if err != nil || n == 1 {
		return err
	}
	var owner int64
	err = q.QueryRowContext(ctx, `SELECT user_id FROM expenses WHERE id = ?`, id).Scan(&owner)
	switch {
	case errors.Is(err, sql.ErrNoRows), err == nil && owner != userID:
		return ErrNotFound
	case err != nil:
		return err
	}
	return ErrNotPending
}

func scanExpense(row interface{ Scan(...any) error }) (Expense, error) {
	var (
		e                           Expense
		spentOn                     string
		spentTime, payment, nfce    sql.NullString
		card                        sql.NullString
		created, updated, confirmed sql.NullString
		attachment                  sql.NullInt64
	)
	err := row.Scan(&e.ID, &e.UserID, &e.Status, &spentOn, &spentTime, &e.Merchant, &e.MerchantTaxID,
		&e.Category, &payment, &e.Account, &card, &e.TotalCents, &e.Currency, &e.Description, &nfce, &e.Source, &attachment,
		&created, &updated, &confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return Expense{}, ErrNotFound
	}
	if err != nil {
		return Expense{}, err
	}

	if e.SpentOn, err = time.ParseInLocation(dateLayout, spentOn, time.Local); err != nil {
		return Expense{}, err
	}
	e.SpentTime, e.PaymentMethod, e.NFCeKey, e.CardLast4 = spentTime.String, payment.String, nfce.String, card.String
	e.AttachmentID = attachment.Int64
	if e.CreatedAt, err = parseTimestamp(created); err != nil {
		return Expense{}, err
	}
	if e.UpdatedAt, err = parseTimestamp(updated); err != nil {
		return Expense{}, err
	}
	if e.ConfirmedAt, err = parseTimestamp(confirmed); err != nil {
		return Expense{}, err
	}
	return e, nil
}
