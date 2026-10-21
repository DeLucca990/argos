package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Filter struct {
	From, To      time.Time
	Category      string
	Merchant      string
	PaymentMethod string
	Account       string
	MinCents      int64
	MaxCents      int64
}

type Group struct {
	Key        string
	TotalCents int64
	Count      int
}

type ItemMatch struct {
	ExpenseID int64
	SpentOn   time.Time
	Merchant  string
	Category  string
	Item      Item
}

var groupings = map[string]string{
	"none":           "''",
	"category":       "c.label",
	"merchant":       "COALESCE(NULLIF(e.merchant, ''), NULLIF(e.description, ''), '(sem nome)')",
	"payment_method": "COALESCE(e.payment_method, '')",
	"account":        "e.account",
	"day":            "e.spent_on",
	"week":           "date(e.spent_on, '-6 days', 'weekday 1')",
	"month":          "substr(e.spent_on, 1, 7)",
}

func Groupings() []string {
	return []string{"none", "category", "merchant", "payment_method", "account", "day", "week", "month"}
}

func (f Filter) where(userID int64) (string, []any) {
	clauses := []string{"e.user_id = ?", "e.status = 'confirmed'", "e.spent_on >= ?", "e.spent_on < ?"}
	args := []any{userID, f.From.Format(dateLayout), f.To.Format(dateLayout)}
	if f.Category != "" {
		clauses = append(clauses, "e.category_id = (SELECT id FROM categories WHERE slug = ? AND "+visibleTo+")")
		args = append(args, f.Category, userID)
	}
	if f.Merchant != "" {
		pattern := like(f.Merchant)
		clauses = append(clauses, `(e.merchant LIKE ? ESCAPE '\' OR e.description LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern)
	}
	if f.PaymentMethod != "" {
		clauses, args = append(clauses, "e.payment_method = ?"), append(args, f.PaymentMethod)
	}
	if f.Account != "" {
		clauses = append(clauses, `e.account LIKE ? ESCAPE '\'`)
		args = append(args, like(f.Account))
	}
	if f.MinCents != 0 {
		clauses, args = append(clauses, "e.total_cents >= ?"), append(args, f.MinCents)
	}
	if f.MaxCents != 0 {
		clauses, args = append(clauses, "e.total_cents <= ?"), append(args, f.MaxCents)
	}
	return strings.Join(clauses, " AND "), args
}

func (s *Store) Totals(ctx context.Context, userID int64, f Filter, groupBy string) ([]Group, error) {
	key, ok := groupings[groupBy]
	if !ok {
		return nil, fmt.Errorf("cannot group by %q", groupBy)
	}
	order := "SUM(e.total_cents) DESC, key"
	if groupBy == "day" || groupBy == "week" || groupBy == "month" {
		order = "key"
	}
	where, args := f.where(userID)
	rows, err := s.db.QueryContext(ctx, `SELECT `+key+` AS key, SUM(e.total_cents), COUNT(*)
		FROM expenses e JOIN categories c ON c.id = e.category_id WHERE `+where+` GROUP BY key ORDER BY `+order, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.Key, &g.TotalCents, &g.Count); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

func (s *Store) Find(ctx context.Context, userID int64, f Filter, byTotal bool, limit int) ([]Expense, error) {
	order := "e.spent_on DESC, e.spent_time IS NULL, e.spent_time DESC, e.id DESC"
	if byTotal {
		order = "e.total_cents DESC, e.spent_on DESC"
	}
	where, args := f.where(userID)
	return s.expenses(ctx, `SELECT `+expenseColumns+` FROM expenses e WHERE `+where+` ORDER BY `+order+` LIMIT ?`,
		append(args, limit)...)
}

func (s *Store) SearchItems(ctx context.Context, userID int64, text string, f Filter, limit int) ([]ItemMatch, error) {
	where, args := f.where(userID)
	pattern := like(text)
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.spent_on, e.merchant, `+categorySlug+`, i.description, i.quantity,
		COALESCE(i.unit_cents, 0), i.total_cents FROM expense_items i JOIN expenses e ON e.id = i.expense_id
		WHERE `+where+` AND i.description LIKE ? ESCAPE '\' ORDER BY e.spent_on DESC, e.id DESC, i.position LIMIT ?`,
		append(args, pattern, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []ItemMatch
	for rows.Next() {
		var (
			m  ItemMatch
			on string
		)
		if err := rows.Scan(&m.ExpenseID, &on, &m.Merchant, &m.Category, &m.Item.Description, &m.Item.Quantity,
			&m.Item.UnitCents, &m.Item.TotalCents); err != nil {
			return nil, err
		}
		if m.SpentOn, err = time.ParseInLocation(dateLayout, on, time.Local); err != nil {
			return nil, err
		}
		matches = append(matches, m)
	}
	return matches, rows.Err()
}

func like(s string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s) + "%"
}
