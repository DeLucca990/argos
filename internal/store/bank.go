package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var ErrItemTaken = errors.New("linked by another user")

const matchWindow = "2 days"

type BankItem struct {
	ID           int64
	PluggyID     string
	UserID       int64
	Name         string
	Status       string
	Error        string
	ImportFrom   time.Time
	ImportedUpTo time.Time
	SyncedAt     time.Time
	ConsentUntil time.Time
	CreatedAt    time.Time
}

type BankTransaction struct {
	ID          int64
	PluggyID    string
	UserID      int64
	AccountID   string
	ProviderID  string
	SpentOn     time.Time
	TotalCents  int64
	Description string
	ExpenseID   int64
}

const bankItemColumns = `id, pluggy_id, user_id, name, status, error, import_from, imported_upto, synced_at,
	consent_until, created_at`

const bankTransactionColumns = `id, pluggy_id, user_id, account_id, provider_id, spent_on, total_cents, description,
	expense_id`

func (s *Store) LinkBankItem(ctx context.Context, userID int64, pluggyID string, importFrom time.Time) (BankItem, bool, error) {
	var (
		it      BankItem
		created bool
	)
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		existing, err := scanBankItem(tx.QueryRowContext(ctx, `SELECT `+bankItemColumns+` FROM bank_items
			WHERE pluggy_id = ?`, pluggyID))
		switch {
		case err == nil && existing.UserID != userID:
			return ErrItemTaken
		case err == nil:
			it = existing
			return nil
		case !errors.Is(err, ErrNotFound):
			return err
		}
		if _, err := user(ctx, tx, userID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO bank_items (pluggy_id, user_id, import_from, created_at)
			VALUES (?, ?, ?, ?)`, pluggyID, userID, importFrom.Format(dateLayout), s.timestamp())
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		it, err = scanBankItem(tx.QueryRowContext(ctx, `SELECT `+bankItemColumns+` FROM bank_items WHERE id = ?`, id))
		created = true
		return err
	})
	return it, created, err
}

func (s *Store) BankItems(ctx context.Context) ([]BankItem, error) {
	return s.bankItems(ctx, `1`)
}

func (s *Store) UserBankItems(ctx context.Context, userID int64) ([]BankItem, error) {
	return s.bankItems(ctx, `user_id = ?`, userID)
}

func (s *Store) bankItems(ctx context.Context, where string, args ...any) ([]BankItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+bankItemColumns+` FROM bank_items WHERE `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []BankItem
	for rows.Next() {
		it, err := scanBankItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func (s *Store) BankItem(ctx context.Context, userID, id int64) (BankItem, error) {
	return scanBankItem(s.db.QueryRowContext(ctx, `SELECT `+bankItemColumns+` FROM bank_items
		WHERE id = ? AND user_id = ?`, id, userID))
}

func (s *Store) UnlinkBankItem(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM bank_items WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SaveBankItem(ctx context.Context, it BankItem) error {
	_, err := s.db.ExecContext(ctx, `UPDATE bank_items SET name = ?, status = ?, error = ?, imported_upto = ?,
		synced_at = ?, consent_until = ? WHERE id = ?`, it.Name, it.Status, it.Error, nullTime(it.ImportedUpTo),
		nullTime(it.SyncedAt), nullTime(it.ConsentUntil), it.ID)
	return err
}

func (s *Store) KnownBankTransactions(ctx context.Context, pluggyIDs []string) (map[string]bool, error) {
	known := map[string]bool{}
	for len(pluggyIDs) > 0 {
		chunk := pluggyIDs[:min(len(pluggyIDs), 500)]
		pluggyIDs = pluggyIDs[len(chunk):]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := s.db.QueryContext(ctx, `SELECT pluggy_id FROM bank_transactions WHERE pluggy_id IN (`+
			strings.TrimSuffix(strings.Repeat("?, ", len(chunk)), ", ")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			known[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return known, nil
}

func (s *Store) BankTransactions(ctx context.Context, accountID string, since time.Time) ([]BankTransaction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+bankTransactionColumns+` FROM bank_transactions
		WHERE account_id = ? AND spent_on >= ? ORDER BY id`, accountID, since.Format(dateLayout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []BankTransaction
	for rows.Next() {
		t, err := scanBankTransaction(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, t)
	}
	return list, rows.Err()
}

func (s *Store) BankTransactionFor(ctx context.Context, expenseID int64) (BankTransaction, error) {
	return scanBankTransaction(s.db.QueryRowContext(ctx, `SELECT `+bankTransactionColumns+` FROM bank_transactions
		WHERE expense_id = ? ORDER BY id DESC LIMIT 1`, expenseID))
}

func (s *Store) BankTransactionByProvider(ctx context.Context, userID int64, providerID string) (BankTransaction, error) {
	if providerID == "" {
		return BankTransaction{}, ErrNotFound
	}
	return scanBankTransaction(s.db.QueryRowContext(ctx, `SELECT `+bankTransactionColumns+` FROM bank_transactions
		WHERE user_id = ? AND provider_id = ? ORDER BY id DESC LIMIT 1`, userID, providerID))
}

func (s *Store) RecordBankTransaction(ctx context.Context, t BankTransaction) error {
	return s.insertBankTransaction(ctx, s.db, t)
}

func (s *Store) AddBankExpense(ctx context.Context, t BankTransaction, e Expense) (int64, error) {
	var id int64
	err := s.inTx(ctx, func(tx *sql.Tx) (err error) {
		if id, err = s.insertExpense(ctx, tx, e); err != nil {
			return err
		}
		t.ExpenseID = id
		return s.insertBankTransaction(ctx, tx, t)
	})
	return id, err
}

func (s *Store) insertBankTransaction(ctx context.Context, q interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}, t BankTransaction) error {
	_, err := q.ExecContext(ctx, `INSERT INTO bank_transactions (pluggy_id, user_id, account_id, provider_id,
		spent_on, total_cents, description, expense_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.PluggyID, t.UserID, t.AccountID, t.ProviderID, t.SpentOn.Format(dateLayout), t.TotalCents, t.Description,
		nullInt(t.ExpenseID), s.timestamp())
	return err
}

func (s *Store) MatchExpense(ctx context.Context, userID, cents int64, on time.Time) (Expense, bool, error) {
	day := on.Format(dateLayout)
	e, err := scanExpense(s.db.QueryRowContext(ctx, `SELECT `+expenseColumns+` FROM expenses e
		WHERE user_id = ? AND status = 'confirmed' AND source != 'bank' AND total_cents = ?
			AND COALESCE(payment_method, '') != 'dinheiro'
			AND spent_on BETWEEN date(?, '-`+matchWindow+`') AND date(?, '+`+matchWindow+`')
			AND NOT EXISTS (SELECT 1 FROM bank_transactions b WHERE b.expense_id = e.id)
		ORDER BY abs(julianday(spent_on) - julianday(?)), id LIMIT 1`, userID, cents, day, day, day))
	if errors.Is(err, ErrNotFound) {
		return Expense{}, false, nil
	}
	return e, err == nil, err
}

func (s *Store) AbsorbBankExpense(ctx context.Context, userID, id int64) (Expense, bool, error) {
	var (
		absorbed Expense
		found    bool
	)
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		e, err := scanExpense(tx.QueryRowContext(ctx, `SELECT `+expenseColumns+` FROM expenses
			WHERE id = ? AND user_id = ?`, id, userID))
		if err != nil {
			return err
		}
		if e.Status != Confirmed || e.Source == SourceBank || e.PaymentMethod == "dinheiro" {
			return nil
		}
		var linked bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM bank_transactions WHERE expense_id = ?)`,
			id).Scan(&linked); err != nil || linked {
			return err
		}

		day := e.SpentOn.Format(dateLayout)
		absorbed, err = scanExpense(tx.QueryRowContext(ctx, `SELECT `+expenseColumns+` FROM expenses
			WHERE user_id = ? AND status = 'confirmed' AND source = 'bank' AND total_cents = ?
				AND spent_on BETWEEN date(?, '-`+matchWindow+`') AND date(?, '+`+matchWindow+`')
			ORDER BY abs(julianday(spent_on) - julianday(?)), id LIMIT 1`, userID, e.TotalCents, day, day, day))
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE expenses SET status = 'discarded', updated_at = ? WHERE id = ?`,
			s.timestamp(), absorbed.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bank_transactions SET expense_id = ? WHERE expense_id = ?`,
			id, absorbed.ID); err != nil {
			return err
		}
		absorbed.Status, found = Discarded, true
		return nil
	})
	return absorbed, found, err
}

func (s *Store) LastFiled(ctx context.Context, userID int64, description string) (BankTransaction, Expense, bool, error) {
	if description == "" {
		return BankTransaction{}, Expense{}, false, nil
	}
	t, err := scanBankTransaction(s.db.QueryRowContext(ctx, `SELECT `+bankTransactionColumns+` FROM bank_transactions b
		WHERE user_id = ? AND description = ? AND (expense_id IS NULL
			OR EXISTS (SELECT 1 FROM expenses e WHERE e.id = b.expense_id AND e.status = 'confirmed'))
		ORDER BY id DESC LIMIT 1`, userID, description))
	switch {
	case errors.Is(err, ErrNotFound):
		return BankTransaction{}, Expense{}, false, nil
	case err != nil:
		return BankTransaction{}, Expense{}, false, err
	case t.ExpenseID == 0:
		return t, Expense{}, true, nil
	}
	e, err := s.Expense(ctx, t.ExpenseID)
	return t, e, err == nil, err
}

func scanBankItem(row interface{ Scan(...any) error }) (BankItem, error) {
	var (
		it                                     BankItem
		importFrom                             string
		importedUpTo, synced, consent, created sql.NullString
	)
	err := row.Scan(&it.ID, &it.PluggyID, &it.UserID, &it.Name, &it.Status, &it.Error, &importFrom, &importedUpTo,
		&synced, &consent, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return BankItem{}, ErrNotFound
	}
	if err != nil {
		return BankItem{}, err
	}
	if it.ImportFrom, err = time.ParseInLocation(dateLayout, importFrom, time.Local); err != nil {
		return BankItem{}, err
	}
	for _, f := range []struct {
		raw sql.NullString
		to  *time.Time
	}{{importedUpTo, &it.ImportedUpTo}, {synced, &it.SyncedAt}, {consent, &it.ConsentUntil}, {created, &it.CreatedAt}} {
		if *f.to, err = parseTimestamp(f.raw); err != nil {
			return BankItem{}, err
		}
	}
	return it, nil
}

func scanBankTransaction(row interface{ Scan(...any) error }) (BankTransaction, error) {
	var (
		t       BankTransaction
		spentOn string
		expense sql.NullInt64
	)
	err := row.Scan(&t.ID, &t.PluggyID, &t.UserID, &t.AccountID, &t.ProviderID, &spentOn, &t.TotalCents,
		&t.Description, &expense)
	if errors.Is(err, sql.ErrNoRows) {
		return BankTransaction{}, ErrNotFound
	}
	if err != nil {
		return BankTransaction{}, err
	}
	t.ExpenseID = expense.Int64
	t.SpentOn, err = time.ParseInLocation(dateLayout, spentOn, time.Local)
	return t, err
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(timestampLayout)
}
