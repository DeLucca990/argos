package store

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func seedQueries(t *testing.T, s *Store) {
	t.Helper()
	for _, e := range []struct {
		user                         int64
		on, merchant, category, paid string
		cents                        int64
		items                        []Item
		status                       Status
	}{
		{me, "2026-09-02", "Supermercado Extra", "mercado", "credito", 25000, []Item{
			{Description: "Cerveja Heineken 350ml", Quantity: 6, UnitCents: 550, TotalCents: 3300},
			{Description: "Arroz 5kg", Quantity: 1, TotalCents: 2890},
		}, Confirmed},
		{me, "2026-09-15", "Padaria 100% Pão", "restaurante", "pix", 1800, nil, Confirmed},
		{me, "2026-09-29", "Extra Hiper", "mercado", "debito", 12000, []Item{
			{Description: "Cerveja Brahma lata", Quantity: 12, UnitCents: 350, TotalCents: 4200},
		}, Confirmed},
		{me, "2026-10-05", "Posto Shell", "combustivel", "credito", 20000, nil, Confirmed},
		{me, "2026-09-10", "Descartado", "mercado", "pix", 99999, nil, Discarded},
		{them, "2026-09-10", "Extra de outra pessoa", "mercado", "pix", 99999, nil, Confirmed},
	} {
		day, _ := time.ParseInLocation(dateLayout, e.on, time.Local)
		x := Expense{UserID: e.user, SpentOn: day, Merchant: e.merchant, Category: e.category, PaymentMethod: e.paid,
			TotalCents: e.cents, Source: SourcePhoto, Items: e.items, Status: Confirmed}
		id := add(t, s, x)
		if e.status == Discarded {
			s.Discard(ctx, e.user, id)
		}
	}
}

func september() Filter {
	return Filter{From: day("2026-09-01"), To: day("2026-10-01")}
}

func groups(g []Group) string {
	var parts []string
	for _, x := range g {
		parts = append(parts, x.Key+"="+strconv.FormatInt(x.TotalCents, 10)+"/"+strconv.Itoa(x.Count))
	}
	return strings.Join(parts, " ")
}

func TestTotals(t *testing.T) {
	s := open(t)
	seedQueries(t, s)

	for groupBy, want := range map[string]string{
		"none":           "=38800/3",
		"category":       "Mercado=37000/2 Restaurante=1800/1",
		"payment_method": "credito=25000/1 debito=12000/1 pix=1800/1",
		"month":          "2026-09=38800/3",
		"week":           "2026-08-31=25000/1 2026-09-14=1800/1 2026-09-28=12000/1",
		"day":            "2026-09-02=25000/1 2026-09-15=1800/1 2026-09-29=12000/1",
		"merchant":       "Supermercado Extra=25000/1 Extra Hiper=12000/1 Padaria 100% Pão=1800/1",
	} {
		g, err := s.Totals(ctx, me, september(), groupBy)
		if err != nil || groups(g) != want {
			t.Errorf("%s: %s, %v; want %s", groupBy, groups(g), err, want)
		}
	}
	if _, err := s.Totals(ctx, me, september(), "e.user_id; DROP TABLE expenses"); err == nil {
		t.Error("an unknown grouping was accepted")
	}

	f := september()
	f.Merchant = "extra"
	if g, _ := s.Totals(ctx, me, f, "none"); groups(g) != "=37000/2" {
		t.Errorf("merchant filter: %s", groups(g))
	}
	f = september()
	f.Merchant = "100%"
	if g, _ := s.Totals(ctx, me, f, "none"); groups(g) != "=1800/1" {
		t.Errorf("a %% in the filter is a percent sign, not a wildcard: %s", groups(g))
	}
	f = september()
	f.Category, f.MinCents = "mercado", 20000
	if g, _ := s.Totals(ctx, me, f, "none"); groups(g) != "=25000/1" {
		t.Errorf("category and minimum: %s", groups(g))
	}
	f = Filter{From: day("2026-01-01"), To: day("2027-01-01"), PaymentMethod: "credito", MaxCents: 20000}
	if g, _ := s.Totals(ctx, me, f, "none"); groups(g) != "=20000/1" {
		t.Errorf("payment and maximum: %s", groups(g))
	}
}

func TestFindAndSearchItems(t *testing.T) {
	s := open(t)
	seedQueries(t, s)

	byDate, err := s.Find(ctx, me, september(), false, 10)
	if err != nil || len(byDate) != 3 || byDate[0].Merchant != "Extra Hiper" {
		t.Fatalf("by date: %d, %v", len(byDate), err)
	}
	byTotal, _ := s.Find(ctx, me, september(), true, 2)
	if len(byTotal) != 2 || byTotal[0].TotalCents != 25000 || byTotal[1].TotalCents != 12000 {
		t.Errorf("by total: %+v", byTotal)
	}

	beer, err := s.SearchItems(ctx, me, "cerveja", september(), 10)
	if err != nil || len(beer) != 2 || beer[0].Item.Description != "Cerveja Brahma lata" || beer[1].Item.UnitCents != 550 ||
		beer[1].Merchant != "Supermercado Extra" {
		t.Errorf("items: %+v, %v", beer, err)
	}
	if none, _ := s.SearchItems(ctx, me, "cerveja", Filter{From: day("2026-10-01"), To: day("2026-11-01")}, 10); len(none) != 0 {
		t.Errorf("items outside the dates: %+v", none)
	}
}
