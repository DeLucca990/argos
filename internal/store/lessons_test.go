package store

import (
	"fmt"
	"testing"
)

func TestLessons(t *testing.T) {
	s := open(t)
	id := add(t, s, expense(me, "2026-10-18", 100, "mercado"))
	saved, err := s.AddLessons(ctx, me, []Lesson{
		{ExpenseID: id, Field: "payment_account", Value: "Nubank", Layout: "tela de Pix, títulos em roxo"},
		{ExpenseID: id, Field: "merchant", Value: "Personal", Payee: "MARIA S SOUZA"},
	})
	if err != nil || len(saved) != 2 || saved[0].ID == 0 || saved[1].ID == saved[0].ID {
		t.Fatalf("saved %+v, %v", saved, err)
	}
	s.AddLessons(ctx, them, []Lesson{{Field: "payment_account", Value: "Inter", Layout: "tela laranja"}})

	again, _ := s.AddLessons(ctx, me, []Lesson{{Field: "payment_account", Value: "Inter", Layout: "tela de Pix, títulos em roxo"}})
	lessons, err := s.Lessons(ctx, me)
	if err != nil || len(lessons) != 2 || lessons[0].ID != again[0].ID || lessons[0].Value != "Inter" || lessons[1].Value != "Personal" ||
		lessons[1].ExpenseID != id || lessons[1].CreatedAt.IsZero() {
		t.Errorf("the same pattern again replaces it, newest first: %+v, %v", lessons, err)
	}

	if n, err := s.ForgetLessons(ctx, me, lessons[0].ID, lessons[1].ID); err != nil || n != 2 {
		t.Errorf("forgetting: %d, %v", n, err)
	}
	theirs, _ := s.Lessons(ctx, them)
	if n, _ := s.ForgetLessons(ctx, me, theirs[0].ID); n != 0 {
		t.Error("someone else's lesson was forgotten")
	}

	for i := range MaxLessons + 5 {
		s.AddLessons(ctx, me, []Lesson{{Field: "merchant", Value: "X", Payee: fmt.Sprint("payee ", i)}})
	}
	if lessons, _ := s.Lessons(ctx, me); len(lessons) != MaxLessons || lessons[0].Payee != fmt.Sprint("payee ", MaxLessons+4) {
		t.Errorf("only the newest %d are kept: %d, newest %q", MaxLessons, len(lessons), lessons[0].Payee)
	}
	if theirs, _ := s.Lessons(ctx, them); len(theirs) != 1 {
		t.Errorf("someone else's lessons were pruned: %d", len(theirs))
	}
}

func TestFirstReading(t *testing.T) {
	s := open(t)
	id := add(t, s, expense(me, "2026-10-18", 100, "mercado"))
	for _, x := range []Extraction{
		{UserID: me, ExpenseID: id, Model: "m", Failed: true, RawJSON: "failed"},
		{UserID: me, ExpenseID: id, Model: "m", RawJSON: "first"},
		{UserID: me, ExpenseID: id, Model: "m", Kind: KindCorrection, RawJSON: "corrected"},
	} {
		if _, err := s.RecordExtraction(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	if x, err := s.FirstReading(ctx, id); err != nil || x.RawJSON != "first" {
		t.Errorf("the first reading: %+v, %v", x, err)
	}
	if x, err := s.ExtractionFor(ctx, id); err != nil || x.RawJSON != "corrected" {
		t.Errorf("the latest: %+v, %v", x, err)
	}
}
