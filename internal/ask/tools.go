package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/DeLucca990/argos-bot/internal/receipt"
	"github.com/DeLucca990/argos-bot/internal/store"
)

const (
	defaultRows = 20
	maxRows     = 50
)

var paymentLabels = map[string]string{
	"pix": "Pix", "credito": "Crédito", "debito": "Débito", "dinheiro": "Dinheiro",
	"boleto": "Boleto", "vale": "Vale", "outro": "Outro", "": "não informado",
}

type tools struct {
	store        *store.Store
	cats         map[string]store.Category
	params       []anthropic.BetaToolUnionParam
	categoryList string
}

type filterArgs struct {
	From          string `json:"from"`
	To            string `json:"to"`
	Category      string `json:"category"`
	Merchant      string `json:"merchant"`
	PaymentMethod string `json:"payment_method"`
	MinTotal      string `json:"min_total"`
	MaxTotal      string `json:"max_total"`
}

func newTools(st *store.Store, cats []store.Category) *tools {
	t := &tools{store: st, cats: map[string]store.Category{}}
	slugs := make([]any, 0, len(cats))
	var list []string
	for _, c := range cats {
		t.cats[c.Slug] = c
		slugs = append(slugs, c.Slug)
		list = append(list, c.Slug+" = "+c.Label)
	}
	t.categoryList = "The categories, as the tools take them: " + strings.Join(list, "; ") + "."

	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	filters := func(extra map[string]any) map[string]any {
		props := map[string]any{
			"from":           str("first day, YYYY-MM-DD, included; the first expense ever when omitted"),
			"to":             str("last day, YYYY-MM-DD, included; today when omitted"),
			"category":       map[string]any{"type": "string", "enum": slugs, "description": "only this category"},
			"merchant":       str("only expenses whose merchant or description contains this, ignoring case"),
			"payment_method": map[string]any{"type": "string", "enum": []any{"pix", "credito", "debito", "dinheiro", "boleto", "vale", "outro"}},
			"min_total":      str(`only expenses of at least this many reais, e.g. "100.00"`),
			"max_total":      str(`only expenses of at most this many reais`),
		}
		for k, v := range extra {
			props[k] = v
		}
		return props
	}
	groupBy := make([]any, 0)
	for _, g := range store.Groupings() {
		groupBy = append(groupBy, g)
	}

	t.params = []anthropic.BetaToolUnionParam{
		tool("list_expenses", "Lists individual expenses matching the filters: date, time, merchant, category, "+
			"payment method, total and description. For the biggest ones, order_by total.",
			filters(map[string]any{
				"order_by": map[string]any{"type": "string", "enum": []any{"date", "total"}, "description": "newest first, or largest first"},
				"limit":    map[string]any{"type": "integer", "description": fmt.Sprintf("how many, %d unless set, at most %d", defaultRows, maxRows)},
			}), nil),
		tool("search_items", "Searches the line items printed on receipts — \"cerveja\", \"gasolina\", \"Dipirona\" — "+
			"and returns each matching line with its expense's date and merchant, and their sum. Only receipts "+
			"that listed their items have any.",
			filters(map[string]any{
				"text":  str("what the item line contains, ignoring case"),
				"limit": map[string]any{"type": "integer", "description": fmt.Sprintf("how many lines, at most %d", maxRows)},
			}), []string{"text"}),
		tool("spending_totals", "Sums the expenses matching the filters, overall or grouped: by category, "+
			"merchant, payment method, day, week (keyed by its Monday) or month. Each group comes with its total, "+
			"its count and its share of the whole.",
			filters(map[string]any{
				"group_by": map[string]any{"type": "string", "enum": groupBy, "description": "none for a single total"},
			}), []string{"group_by"}),
	}
	return t
}

func tool(name, description string, properties map[string]any, required []string) anthropic.BetaToolUnionParam {
	p := anthropic.BetaToolParam{
		Name:        name,
		Description: anthropic.String(description),
		InputSchema: anthropic.BetaToolInputSchemaParam{Properties: properties, Required: required},
	}
	return anthropic.BetaToolUnionParam{OfTool: &p}
}

func (t *tools) call(ctx context.Context, name string, input []byte, userID int64, now time.Time) (string, error) {
	var args struct {
		filterArgs
		GroupBy string `json:"group_by"`
		OrderBy string `json:"order_by"`
		Text    string `json:"text"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("the arguments are not a JSON object of the schema: %v", err)
	}
	f, err := t.filter(args.filterArgs, now)
	if err != nil {
		return "", err
	}
	limit := args.Limit
	if limit <= 0 {
		limit = defaultRows
	}
	limit = min(limit, maxRows)

	var out any
	switch name {
	case "spending_totals":
		out, err = t.totals(ctx, userID, f, args.GroupBy)
	case "list_expenses":
		out, err = t.list(ctx, userID, f, args.OrderBy == "total", limit)
	case "search_items":
		if strings.TrimSpace(args.Text) == "" {
			return "", fmt.Errorf("text is required: what the item line contains")
		}
		out, err = t.items(ctx, userID, strings.TrimSpace(args.Text), f, limit)
	default:
		return "", fmt.Errorf("there is no tool %q", name)
	}
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(out)
	return string(body), err
}

func (t *tools) filter(a filterArgs, now time.Time) (store.Filter, error) {
	loc := now.Location()
	f := store.Filter{
		From:     time.Date(1, 1, 1, 0, 0, 0, 0, loc),
		To:       time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc),
		Merchant: strings.TrimSpace(a.Merchant), PaymentMethod: a.PaymentMethod,
	}
	if a.From != "" {
		d, err := time.ParseInLocation("2006-01-02", a.From, loc)
		if err != nil {
			return f, fmt.Errorf("from %q is not a YYYY-MM-DD date", a.From)
		}
		f.From = d
	}
	if a.To != "" {
		d, err := time.ParseInLocation("2006-01-02", a.To, loc)
		if err != nil {
			return f, fmt.Errorf("to %q is not a YYYY-MM-DD date", a.To)
		}
		f.To = d.AddDate(0, 0, 1)
	}
	if a.Category != "" {
		if _, ok := t.cats[a.Category]; !ok {
			return f, fmt.Errorf("there is no category %q", a.Category)
		}
		f.Category = a.Category
	}
	for _, m := range []struct {
		raw  string
		into *int64
	}{{a.MinTotal, &f.MinCents}, {a.MaxTotal, &f.MaxCents}} {
		if m.raw == "" {
			continue
		}
		cents, err := receipt.ParseCents(m.raw)
		if err != nil {
			return f, fmt.Errorf("%q is not an amount in reais", m.raw)
		}
		*m.into = cents
	}
	return f, nil
}

type groupOut struct {
	Key   string `json:"key"`
	Total string `json:"total"`
	Count int    `json:"count"`
	Share string `json:"share"`
}

func (t *tools) totals(ctx context.Context, userID int64, f store.Filter, groupBy string) (any, error) {
	if groupBy == "" {
		groupBy = "none"
	}
	groups, err := t.store.Totals(ctx, userID, f, groupBy)
	if err != nil {
		return nil, err
	}
	var total int64
	count := 0
	for _, g := range groups {
		total, count = total+g.TotalCents, count+g.Count
	}
	out := struct {
		Period string     `json:"period"`
		Total  string     `json:"total"`
		Count  int        `json:"count"`
		Groups []groupOut `json:"groups,omitempty"`
	}{Period: period(f), Total: receipt.FormatBRL(total), Count: count}
	if groupBy != "none" {
		for _, g := range groups {
			key := g.Key
			if groupBy == "payment_method" {
				key = paymentLabels[key]
			}
			out.Groups = append(out.Groups, groupOut{Key: key, Total: receipt.FormatBRL(g.TotalCents), Count: g.Count,
				Share: fmt.Sprintf("%d%%", (g.TotalCents*100+total/2)/max(total, 1))})
		}
	}
	return out, nil
}

type expenseOut struct {
	ID          int64  `json:"id"`
	Date        string `json:"date"`
	Time        string `json:"time,omitempty"`
	Merchant    string `json:"merchant"`
	Category    string `json:"category"`
	Payment     string `json:"payment"`
	Total       string `json:"total"`
	Description string `json:"description,omitempty"`
}

func (t *tools) list(ctx context.Context, userID int64, f store.Filter, byTotal bool, limit int) (any, error) {
	list, err := t.store.Find(ctx, userID, f, byTotal, limit+1)
	if err != nil {
		return nil, err
	}
	out := struct {
		Period    string       `json:"period"`
		Expenses  []expenseOut `json:"expenses"`
		Truncated bool         `json:"truncated,omitempty"`
	}{Period: period(f), Expenses: []expenseOut{}, Truncated: len(list) > limit}
	for i, e := range list {
		if i == limit {
			break
		}
		out.Expenses = append(out.Expenses, expenseOut{
			ID: e.ID, Date: e.SpentOn.Format("2006-01-02"), Time: e.SpentTime, Merchant: e.Merchant,
			Category: t.cats[e.Category].Label, Payment: paymentLabels[e.PaymentMethod],
			Total: money(e.TotalCents, e.Currency), Description: e.Description,
		})
	}
	return out, nil
}

func (t *tools) items(ctx context.Context, userID int64, text string, f store.Filter, limit int) (any, error) {
	matches, err := t.store.SearchItems(ctx, userID, text, f, limit+1)
	if err != nil {
		return nil, err
	}
	type itemOut struct {
		Date     string  `json:"date"`
		Merchant string  `json:"merchant"`
		Item     string  `json:"item"`
		Quantity float64 `json:"quantity"`
		Total    string  `json:"total"`
	}
	out := struct {
		Period    string    `json:"period"`
		Sum       string    `json:"sum"`
		Lines     []itemOut `json:"lines"`
		Truncated bool      `json:"truncated,omitempty"`
	}{Period: period(f), Lines: []itemOut{}, Truncated: len(matches) > limit}
	var sum int64
	for i, m := range matches {
		if i == limit {
			break
		}
		sum += m.Item.TotalCents
		out.Lines = append(out.Lines, itemOut{Date: m.SpentOn.Format("2006-01-02"), Merchant: m.Merchant,
			Item: m.Item.Description, Quantity: m.Item.Quantity, Total: receipt.FormatBRL(m.Item.TotalCents)})
	}
	out.Sum = receipt.FormatBRL(sum)
	return out, nil
}

func period(f store.Filter) string {
	from := "the first expense"
	if f.From.Year() > 1 {
		from = f.From.Format("2006-01-02")
	}
	return from + " to " + f.To.AddDate(0, 0, -1).Format("2006-01-02")
}

func money(cents int64, currency string) string {
	brl := receipt.FormatBRL(cents)
	if currency == "" || currency == "BRL" {
		return brl
	}
	return strings.Replace(brl, "R$", currency, 1)
}
