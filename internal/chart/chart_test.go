package chart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DeLucca990/argos/internal/store"
)

var (
	ctx = context.Background()
	now = time.Date(2026, 11, 20, 15, 0, 0, 0, time.Local)
)

func day(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

type seed struct {
	on, merchant, category, paid string
	cents                        int64
}

func setup(t *testing.T, expenses ...seed) (*store.Store, int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "argos.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	user, _ := st.SaveUser(ctx, store.User{TelegramID: 42})
	other, _ := st.SaveUser(ctx, store.User{TelegramID: 7})
	if _, err := st.AddExpense(ctx, store.Expense{UserID: other.ID, SpentOn: day("2026-09-01"), Merchant: "De outra pessoa",
		Category: "lazer", TotalCents: 999999, Source: store.SourcePhoto, Status: store.Confirmed}); err != nil {
		t.Fatal(err)
	}
	for _, e := range expenses {
		if _, err := st.AddExpense(ctx, store.Expense{UserID: user.ID, SpentOn: day(e.on), Merchant: e.merchant,
			Category: e.category, PaymentMethod: e.paid, TotalCents: e.cents, Source: store.SourcePhoto,
			Status: store.Confirmed}); err != nil {
			t.Fatal(err)
		}
	}
	return st, user.ID
}

func checkPNG(t *testing.T, name string, data []byte) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("%s: not a PNG: %v", name, err)
	}
	if b := img.Bounds(); b.Dx() != width*scale || b.Dy() < 300 {
		t.Errorf("%s: %dx%d", name, b.Dx(), b.Dy())
	}
}

func TestBarsFoldTheTail(t *testing.T) {
	var expenses []seed
	for i := range 13 {
		expenses = append(expenses, seed{"2026-11-02", fmt.Sprintf("Loja %02d", i), "mercado", "pix", int64(10000 - i*100)})
	}
	expenses = append(expenses, seed{"2026-11-03", "Loja 00", "mercado", "credito", 500}, seed{"2026-10-31", "Outubro", "mercado", "pix", 7777})
	st, user := setup(t, expenses...)

	c, err := Spending(ctx, st, user, Request{Filter: store.Filter{From: day("2026-11-01"), To: day("2026-12-01")},
		GroupBy: "merchant", Title: "Novembro"}, now)
	if err != nil {
		t.Fatal(err)
	}
	checkPNG(t, "bars", c.PNG)
	if c.Title != "Novembro" || c.Count != 14 || c.TotalCents != 122700 || len(c.Points) != maxBars {
		t.Fatalf("%q: %d expenses, %d cents, %d points", c.Title, c.Count, c.TotalCents, len(c.Points))
	}
	if first := c.Points[0]; first.Key != "Loja 00" || first.Cents != 10500 || first.Count != 2 {
		t.Errorf("the largest: %+v", first)
	}
	if rest := c.Points[maxBars-1]; rest.Key != "Demais (4)" || rest.Cents != 9100+9000+8900+8800 || rest.Count != 4 {
		t.Errorf("the tail: %+v", rest)
	}

	c, err = Spending(ctx, st, user, Request{Filter: store.Filter{From: day("2026-11-01"), To: day("2026-12-01")},
		GroupBy: "payment_method"}, now)
	if err != nil || len(c.Points) != 2 || c.Points[0].Key != "Pix" || c.Points[1].Key != "Crédito" {
		t.Errorf("by payment: %+v, %v", c.Points, err)
	}
	if !strings.HasPrefix(c.Title, "Gastos de 01/11 a 30/11/2026") {
		t.Errorf("the default title: %q", c.Title)
	}
}

func TestTimeline(t *testing.T) {
	st, user := setup(t,
		seed{"2026-10-07", "Mercado", "mercado", "pix", 10000},
		seed{"2026-10-20", "Posto", "combustivel", "credito", 20000},
		seed{"2026-11-05", "Farmácia", "farmacia", "debito", 3000},
	)

	c, err := Spending(ctx, st, user, Request{Filter: store.Filter{From: day("2026-06-01"), To: day("2026-12-01")}, GroupBy: "month"}, now)
	if err != nil {
		t.Fatal(err)
	}
	checkPNG(t, "months", c.PNG)
	want := []Point{{Key: "2026-10", Cents: 30000, Count: 2, Partial: true}, {Key: "2026-11", Cents: 3000, Count: 1, Partial: true}}
	if fmt.Sprint(c.Points) != fmt.Sprint(want) {
		t.Errorf("months before the first expense are left out, and the ends are partial:\n%v\nwant %v", c.Points, want)
	}

	c, err = Spending(ctx, st, user, Request{Filter: store.Filter{From: day("2026-10-01"), To: day("2026-11-01")},
		GroupBy: "month"}, now)
	if err != nil || len(c.Points) != 1 || c.Points[0].Partial != true || c.Count != 2 {
		t.Errorf("one month, from the first expense: %+v, %v", c.Points, err)
	}

	c, err = Spending(ctx, st, user, Request{Filter: store.Filter{From: day("2026-11-01"), To: day("2026-11-21")}, GroupBy: "day"}, now)
	if err != nil || len(c.Points) != 20 || c.Points[4] != (Point{Key: "2026-11-05", Cents: 3000, Count: 1}) || c.Points[0].Cents != 0 {
		t.Errorf("days, with the empty ones: %d points, %v", len(c.Points), err)
	}

	c, err = Spending(ctx, st, user, Request{Filter: store.Filter{From: day("2026-10-12"), To: day("2026-11-21"), Categories: []string{"combustivel"}},
		GroupBy: "week"}, now)
	if err != nil || len(c.Points) != 6 || c.Points[1] != (Point{Key: "2026-10-19", Cents: 20000, Count: 1}) || !c.Points[5].Partial {
		t.Errorf("weeks of a category: %+v, %v", c.Points, err)
	}

	if _, err := Spending(ctx, st, user, Request{Filter: store.Filter{From: day("2026-11-10"), To: day("2026-11-21"), Categories: []string{"mercado"}},
		GroupBy: "day"}, now); !errors.Is(err, ErrEmpty) {
		t.Errorf("nothing in the period: %v", err)
	}
}

func TestTooManyColumns(t *testing.T) {
	st, user := setup(t, seed{"2026-01-05", "Mercado", "mercado", "pix", 10000}, seed{"2026-11-05", "Posto", "combustivel", "pix", 5000})
	f := store.Filter{From: day("2026-01-01"), To: day("2026-11-21")}
	if _, err := Spending(ctx, st, user, Request{Filter: f, GroupBy: "day"}, now); err == nil || !strings.Contains(err.Error(), "too many columns") {
		t.Errorf("a year of days: %v", err)
	}
	if c, err := Spending(ctx, st, user, Request{Filter: f, GroupBy: "week"}, now); err != nil || len(c.Points) != 46 {
		t.Errorf("the same in weeks: %d, %v", len(c.Points), err)
	}
}

func TestPace(t *testing.T) {
	st, user := setup(t,
		seed{"2026-10-03", "Mercado", "mercado", "pix", 10000},
		seed{"2026-10-25", "Posto", "combustivel", "credito", 20000},
		seed{"2026-11-02", "Farmácia", "farmacia", "debito", 3000},
		seed{"2026-11-15", "Restaurante", "restaurante", "pix", 4500},
	)

	c, err := Spending(ctx, st, user, Request{Filter: store.Filter{From: day("2026-11-01"), To: day("2026-12-01")},
		GroupBy: "pace", Title: "Ritmo de novembro de 2026"}, now)
	if err != nil {
		t.Fatal(err)
	}
	checkPNG(t, "pace", c.PNG)
	if c.TotalCents != 7500 || c.Count != 2 || len(c.Points) != 3 {
		t.Fatalf("%d cents in %d expenses, %+v", c.TotalCents, c.Count, c.Points)
	}
	for i, want := range []Point{
		{Key: "this period, 01/11 a 20/11/2026", Cents: 7500, Count: 2, Partial: true},
		{Key: "the period before, the same days: 01/10 a 20/10/2026", Cents: 10000},
		{Key: "the period before, whole: 01/10 a 31/10/2026", Cents: 30000, Count: 2},
	} {
		if c.Points[i] != want {
			t.Errorf("point %d: %+v, want %+v", i, c.Points[i], want)
		}
	}

	c, err = Spending(ctx, st, user, Request{Filter: store.Filter{From: day("2026-11-01"), To: day("2026-11-16")}, GroupBy: "pace"}, now)
	if err != nil || len(c.Points) != 3 || c.Points[1].Key != "the period before, the same days: 01/10 a 15/10/2026" || c.Points[1].Cents != 10000 ||
		c.Points[2].Cents != 30000 {
		t.Errorf("part of a month against the month before: %+v, %v", c.Points, err)
	}

	c, err = Spending(ctx, st, user, Request{Filter: store.Filter{From: day("2026-11-06"), To: day("2026-11-21")}, GroupBy: "pace"}, now)
	if err != nil || len(c.Points) != 2 || c.Points[1].Key != "the period before, the same days: 22/10 a 05/11/2026" || c.Points[1].Cents != 23000 {
		t.Errorf("15 days against the 15 before: %+v, %v", c.Points, err)
	}

	for _, f := range []store.Filter{
		{From: time.Date(1, 1, 1, 0, 0, 0, 0, time.Local), To: day("2026-11-21")},
		{From: day("2026-01-01"), To: day("2026-11-21")},
	} {
		if _, err := Spending(ctx, st, user, Request{Filter: f, GroupBy: "pace"}, now); err == nil || errors.Is(err, ErrEmpty) {
			t.Errorf("pace from %s: %v", f.From.Format("2006-01-02"), err)
		}
	}
	if _, err := Spending(ctx, st, user, Request{Filter: store.Filter{From: day("2026-12-01"), To: day("2027-01-01")}, GroupBy: "pace"}, now); !errors.Is(err, ErrEmpty) {
		t.Errorf("a month still to come: %v", err)
	}
}

func TestNothingToDraw(t *testing.T) {
	st, user := setup(t)
	f := store.Filter{From: day("2026-09-01"), To: day("2026-10-01")}
	for _, g := range Shapes() {
		if _, err := Spending(ctx, st, user, Request{Filter: f, GroupBy: g}, now); !errors.Is(err, ErrEmpty) {
			t.Errorf("%s: %v, and the only expense in September is someone else's", g, err)
		}
	}
	if _, err := Spending(ctx, st, user, Request{Filter: f, GroupBy: "pie"}, now); err == nil || errors.Is(err, ErrEmpty) {
		t.Errorf("an unknown shape: %v", err)
	}
}

func TestTextTheFontLacks(t *testing.T) {
	c, err := newCanvas(100)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.clean("Viagem 🏖️ a\tLisboa"); got != "Viagem  a Lisboa" {
		t.Errorf("clean: %q", got)
	}
	if got := c.fit("Supermercado Pão de Açúcar Unidade Centro", labelSize, false, 120); !strings.HasSuffix(got, "…") ||
		c.measure(got, labelSize, false) > 120 {
		t.Errorf("fit: %q", got)
	}
}

func TestTicks(t *testing.T) {
	for top, want := range map[int64][2]int64{
		0: {25, 100}, 3000: {1000, 3000}, 340293: {100000, 400000}, 548200: {200000, 600000}, 147500: {50000, 150000},
	} {
		if step, max := ticks(top); step != want[0] || max != want[1] {
			t.Errorf("ticks(%d) = %d, %d; want %v", top, step, max, want)
		}
	}
	if amount(340293) != "R$ 3.403" || amount(7376) != "R$ 73,76" || tickLabel(250) != "R$ 2,50" {
		t.Errorf("amounts: %s, %s, %s", amount(340293), amount(7376), tickLabel(250))
	}
}
