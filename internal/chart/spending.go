package chart

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/DeLucca990/argos/internal/receipt"
	"github.com/DeLucca990/argos/internal/store"
)

var ErrEmpty = errors.New("no expenses to draw")

const (
	maxBars     = 10
	maxColumns  = 62
	maxPaceDays = 92
)

var (
	monthNames = []string{"janeiro", "fevereiro", "março", "abril", "maio", "junho", "julho", "agosto", "setembro",
		"outubro", "novembro", "dezembro"}
	paymentLabels = map[string]string{
		"pix": "Pix", "credito": "Crédito", "debito": "Débito", "dinheiro": "Dinheiro",
		"boleto": "Boleto", "vale": "Vale", "outro": "Outro", "": "Não informado",
	}
)

type Request struct {
	Filter  store.Filter
	GroupBy string
	Title   string
}

type Point struct {
	Key     string
	Cents   int64
	Count   int
	Partial bool
}

type Chart struct {
	PNG        []byte
	Title      string
	TotalCents int64
	Count      int
	Points     []Point
}

func Shapes() []string {
	return []string{"category", "merchant", "payment_method", "account", "day", "week", "month", "pace"}
}

func Spending(ctx context.Context, st *store.Store, userID int64, r Request, now time.Time) (Chart, error) {
	r.Title = strings.TrimSpace(r.Title)
	switch r.GroupBy {
	case "category", "merchant", "payment_method", "account":
		return ranking(ctx, st, userID, r)
	case "day", "week", "month":
		return timeline(ctx, st, userID, r, now)
	case "pace":
		return pace(ctx, st, userID, r, now)
	}
	return Chart{}, fmt.Errorf("cannot draw %q: group by one of %s", r.GroupBy, strings.Join(Shapes(), ", "))
}

func ranking(ctx context.Context, st *store.Store, userID int64, r Request) (Chart, error) {
	groups, err := st.Totals(ctx, userID, r.Filter, r.GroupBy)
	if err != nil {
		return Chart{}, err
	}
	out := Chart{Title: nonEmpty(r.Title, "Gastos "+span(r.Filter))}
	for _, g := range groups {
		out.TotalCents, out.Count = out.TotalCents+g.TotalCents, out.Count+g.Count
	}
	if out.Count == 0 {
		return out, ErrEmpty
	}

	var bars []bar
	for i, g := range groups {
		if i == maxBars-1 && len(groups) > maxBars {
			rest := Point{Key: fmt.Sprintf("Demais (%d)", len(groups)-i)}
			for _, g := range groups[i:] {
				rest.Cents, rest.Count = rest.Cents+g.TotalCents, rest.Count+g.Count
			}
			out.Points = append(out.Points, rest)
			bars = append(bars, bar{label: rest.Key, cents: rest.Cents, other: true})
			break
		}
		key := g.Key
		switch {
		case r.GroupBy == "payment_method":
			key = paymentLabels[key]
		case r.GroupBy == "account" && key == "":
			key = "Não informada"
		}
		out.Points = append(out.Points, Point{Key: key, Cents: g.TotalCents, Count: g.Count})
		bars = append(bars, bar{label: key, cents: g.TotalCents})
	}
	out.PNG, err = drawBars(out.Title, summary(out.TotalCents, out.Count), bars, out.TotalCents)
	return out, err
}

type bucket struct {
	from, to time.Time
	key      string
}

func buckets(groupBy string, from, to time.Time) ([]bucket, error) {
	start, next := from, func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }
	key := func(t time.Time) string { return t.Format("2006-01-02") }
	switch groupBy {
	case "week":
		start = from.AddDate(0, 0, -((int(from.Weekday()) + 6) % 7))
		next = func(t time.Time) time.Time { return t.AddDate(0, 0, 7) }
	case "month":
		start = time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, from.Location())
		next = func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }
		key = func(t time.Time) string { return t.Format("2006-01") }
	}
	var list []bucket
	for b := start; b.Before(to); b = next(b) {
		if len(list) == maxColumns {
			return nil, fmt.Errorf("too many columns: more than %d %ss in the period; group by week or month, or pass a shorter period",
				maxColumns, groupBy)
		}
		list = append(list, bucket{from: b, to: next(b), key: key(b)})
	}
	return list, nil
}

func timeline(ctx context.Context, st *store.Store, userID int64, r Request, now time.Time) (Chart, error) {
	out := Chart{Title: nonEmpty(r.Title, "Gastos "+span(r.Filter))}
	first, ok, err := st.FirstSpent(ctx, userID)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, ErrEmpty
	}
	from, to := r.Filter.From, r.Filter.To
	if first.After(from) {
		from = first
	}
	if end := startOfDay(now).AddDate(0, 0, 1); end.Before(to) {
		to = end
	}
	if !from.Before(to) {
		return out, ErrEmpty
	}
	list, err := buckets(r.GroupBy, from, to)
	if err != nil {
		return out, err
	}

	groups, err := st.Totals(ctx, userID, r.Filter, r.GroupBy)
	if err != nil {
		return out, err
	}
	byKey := map[string]store.Group{}
	for _, g := range groups {
		byKey[g.Key] = g
	}
	cols := make([]column, len(list))
	for i, b := range list {
		g := byKey[b.key]
		out.TotalCents, out.Count = out.TotalCents+g.TotalCents, out.Count+g.Count
		col := column{cents: g.TotalCents}
		switch r.GroupBy {
		case "month":
			col.label, col.sub = monthNames[b.from.Month()-1][:3], strconv.Itoa(b.from.Year())
		default:
			col.label = b.from.Format("02/01")
		}
		if r.GroupBy != "day" && (b.from.Before(from) || b.to.After(to)) {
			col.partial = true
			col.note = short(maxTime(b.from, from), minTime(b.to, to))
		}
		cols[i] = col
		out.Points = append(out.Points, Point{Key: b.key, Cents: g.TotalCents, Count: g.Count, Partial: col.partial})
	}
	if out.Count == 0 {
		return out, ErrEmpty
	}
	out.PNG, err = drawColumns(out.Title, summary(out.TotalCents, out.Count), cols)
	return out, err
}

func pace(ctx context.Context, st *store.Store, userID int64, r Request, now time.Time) (Chart, error) {
	f := r.Filter
	out := Chart{Title: nonEmpty(r.Title, "Ritmo dos gastos "+span(f))}
	if f.From.Year() <= 1 {
		return out, errors.New("pace needs from: the first day of the period")
	}
	length := days(f.From, f.To)
	if length < 2 || length > maxPaceDays {
		return out, fmt.Errorf("pace draws a period of 2 to %d days, not %d: for longer ones group by week or month", maxPaceDays, length)
	}
	month := f.From.Day() == 1 && !f.To.After(f.From.AddDate(0, 1, 0))
	prev := f
	if month {
		length = days(f.From, f.From.AddDate(0, 1, 0))
		prev.From, prev.To = f.From.AddDate(0, -1, 0), f.From
	} else {
		prev.From, prev.To = f.From.AddDate(0, 0, -length), f.From
	}
	cur := f
	if end := startOfDay(now).AddDate(0, 0, 1); end.Before(cur.To) {
		cur.To = end
	}
	if !cur.From.Before(cur.To) {
		return out, ErrEmpty
	}

	curValues, curCount, err := cumulative(ctx, st, userID, cur)
	if err != nil {
		return out, err
	}
	prevValues, prevCount, err := cumulative(ctx, st, userID, prev)
	if err != nil {
		return out, err
	}
	if curCount == 0 && prevCount == 0 {
		return out, ErrEmpty
	}
	out.TotalCents, out.Count = curValues[len(curValues)-1], curCount
	elapsed := len(curValues)
	sameDays := prevValues[min(elapsed, len(prevValues))-1]
	out.Points = []Point{
		{Key: "this period, " + dates(cur.From, cur.To), Cents: out.TotalCents, Count: curCount, Partial: cur.To.Before(f.To)},
		{Key: "the period before, the same days: " + dates(prev.From, prev.From.AddDate(0, 0, min(elapsed, len(prevValues)))), Cents: sameDays},
	}
	if elapsed < len(prevValues) {
		out.Points = append(out.Points, Point{Key: "the period before, whole: " + dates(prev.From, prev.To),
			Cents: prevValues[len(prevValues)-1], Count: prevCount})
	}

	n := max(length, len(prevValues))
	curSeries := series{label: short(f.From, f.To), values: curValues}
	prevSeries := series{label: short(prev.From, prev.To), values: prevValues}
	var marks []mark
	if month {
		curSeries.label, prevSeries.label = monthNames[f.From.Month()-1], monthNames[prev.From.Month()-1]
		for _, d := range []int{1, 5, 10, 15, 20, 25, 30} {
			if d <= n {
				marks = append(marks, mark{d - 1, strconv.Itoa(d)})
			}
		}
	} else {
		step := max(1, int(math.Ceil(float64(n-1)/5)))
		for i := 0; i < n; i += step {
			marks = append(marks, mark{i, f.From.AddDate(0, 0, i).Format("02/01")})
		}
	}
	if prevCount == 0 {
		prevSeries.values = nil
	}
	out.PNG, err = drawPace(out.Title, summary(out.TotalCents, out.Count), curSeries, prevSeries, n, marks)
	return out, err
}

func cumulative(ctx context.Context, st *store.Store, userID int64, f store.Filter) ([]int64, int, error) {
	groups, err := st.Totals(ctx, userID, f, "day")
	if err != nil {
		return nil, 0, err
	}
	byDay := map[string]int64{}
	count := 0
	for _, g := range groups {
		byDay[g.Key] += g.TotalCents
		count += g.Count
	}
	values := make([]int64, days(f.From, f.To))
	var sum int64
	for i := range values {
		sum += byDay[f.From.AddDate(0, 0, i).Format("2006-01-02")]
		values[i] = sum
	}
	return values, count, nil
}

func days(from, to time.Time) int {
	return max(0, int(math.Round(to.Sub(from).Hours()/24)))
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func dates(from, to time.Time) string {
	last := to.AddDate(0, 0, -1)
	if last.Equal(from) {
		return from.Format("02/01/2006")
	}
	return from.Format("02/01") + " a " + last.Format("02/01/2006")
}

func short(from, to time.Time) string {
	last := to.AddDate(0, 0, -1)
	if last.Equal(from) {
		return from.Format("02/01")
	}
	return from.Format("02/01") + " a " + last.Format("02/01")
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func span(f store.Filter) string {
	if f.From.Year() <= 1 {
		return "até " + f.To.AddDate(0, 0, -1).Format("02/01/2006")
	}
	return "de " + dates(f.From, f.To)
}

func summary(total int64, count int) string {
	unit := "gastos"
	if count == 1 {
		unit = "gasto"
	}
	return fmt.Sprintf("%s em %d %s", receipt.FormatBRL(total), count, unit)
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
