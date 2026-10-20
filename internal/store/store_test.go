package store

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "argos.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}
	for _, u := range []struct{ want, telegramID int64 }{{me, 4242}, {them, 7777}} {
		if got, err := s.SaveUser(ctx, User{TelegramID: u.telegramID}); err != nil || got.ID != u.want {
			t.Fatalf("user %d: got id %d, %v", u.telegramID, got.ID, err)
		}
	}
	return s
}

const (
	me   int64 = 1
	them int64 = 2
)

func day(s string) time.Time {
	d, err := time.ParseInLocation(dateLayout, s, time.Local)
	if err != nil {
		panic(err)
	}
	return d
}

func expense(user int64, on string, cents int64, category string) Expense {
	return Expense{UserID: user, SpentOn: day(on), TotalCents: cents, Category: category, Source: SourceText}
}

func add(t *testing.T, s *Store, e Expense) int64 {
	t.Helper()
	id, err := s.AddExpense(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func confirmed(t *testing.T, s *Store, e Expense) int64 {
	t.Helper()
	id := add(t, s, e)
	if _, err := s.Confirm(ctx, e.UserID, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestOpenMigratesOnceAndReopens(t *testing.T) {
	file := filepath.Join(t.TempDir(), "argos.db")
	for range 2 {
		s, err := Open(file)
		if err != nil {
			t.Fatal(err)
		}
		var version int
		if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
			t.Errorf("user_version = %d, %v", version, err)
		}
		cats, err := s.Categories(ctx)
		if err != nil || len(cats) != 16 || cats[0].Slug != "mercado" {
			t.Errorf("categories: %d, %v", len(cats), err)
		}
		var journal string
		if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
			t.Errorf("journal_mode = %q, %v", journal, err)
		}
		s.Close()
	}
}

func TestOpenRefusesANewerSchema(t *testing.T) {
	file := filepath.Join(t.TempDir(), "argos.db")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	if _, err := Open(file); err == nil || !strings.Contains(err.Error(), "newer argos") {
		t.Fatalf("a database from a newer build was opened: %v", err)
	}
}

func TestOpenReportsWhatItApplied(t *testing.T) {
	file := filepath.Join(t.TempDir(), "argos.db")
	for _, want := range []string{"0001_init", ""} {
		s, err := Open(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(s.Applied(), ","); got != want || s.Version() != 1 {
			t.Errorf("applied %q at version %d, want %q at 1", got, s.Version(), want)
		}
		s.Close()
	}

	next := append(migrations[:len(migrations):len(migrations)], migration{name: "0002_notes", sql: `CREATE TABLE notes (body TEXT)`})
	s, err := openWith(file, next)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Applied(), ","); got != "0002_notes" || s.Version() != 2 {
		t.Errorf("a later migration: applied %q at version %d", got, s.Version())
	}
	s.Close()
}

func TestOpenRefusesABrokenMigrationList(t *testing.T) {
	file := filepath.Join(t.TempDir(), "argos.db")
	if _, err := openWith(file, nil); err == nil {
		t.Error("a build with no migrations opened the database")
	}
	skipped := []migration{migrations[0], {name: "0003_skipped", sql: `SELECT 1`}}
	if _, err := openWith(file, skipped); err == nil || !strings.Contains(err.Error(), "out of sequence") {
		t.Errorf("a list missing 0002 was accepted: %v", err)
	}
}

func TestExpenseRoundTrip(t *testing.T) {
	s := open(t)
	att, err := s.AddAttachment(ctx, Attachment{SHA256: "abc", Path: "receipts/abc.jpg", MIME: "image/jpeg", Size: 1234})
	if err != nil {
		t.Fatal(err)
	}

	in := Expense{
		UserID: me, SpentOn: day("2026-10-18"), SpentTime: "19:42",
		Merchant: "Padaria Pão Quente", MerchantTaxID: "12345678000190", Category: "mercado",
		PaymentMethod: "pix", TotalCents: 3590, Description: "pão e leite",
		NFCeKey: strings.Repeat("1", 44), Source: SourcePhoto, AttachmentID: att.ID,
		Items: []Item{
			{Description: "Pão francês", Quantity: 0.385, UnitCents: 1990, TotalCents: 766},
			{Description: "Leite", Quantity: 2, TotalCents: 2824},
		},
	}
	id := add(t, s, in)

	got, err := s.Expense(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Pending || got.Currency != "BRL" || !got.ConfirmedAt.IsZero() {
		t.Errorf("a new expense: status %q, currency %q, confirmed %v", got.Status, got.Currency, got.ConfirmedAt)
	}
	if !got.SpentOn.Equal(in.SpentOn) || got.SpentTime != "19:42" || got.PaymentMethod != "pix" ||
		got.NFCeKey != in.NFCeKey || got.AttachmentID != att.ID || got.Merchant != in.Merchant {
		t.Errorf("fields did not round-trip: %+v", got)
	}
	if len(got.Items) != 2 || got.Items[0].Quantity != 0.385 || got.Items[1].UnitCents != 0 ||
		got.Items[1].Description != "Leite" {
		t.Errorf("items: %+v", got.Items)
	}

	bare := add(t, s, expense(me, "2026-10-18", 100, "outros"))
	if got, err := s.Expense(ctx, bare); err != nil || got.SpentTime != "" || got.PaymentMethod != "" ||
		got.NFCeKey != "" || got.AttachmentID != 0 || got.Items != nil {
		t.Errorf("empty fields did not come back empty: %+v, %v", got, err)
	}
}

func TestExpenseIsRefusedWhenInvalid(t *testing.T) {
	s := open(t)
	if _, err := s.AddExpense(ctx, expense(me, "2026-10-18", 0, "mercado")); err == nil {
		t.Error("a zero total was accepted")
	}
	if _, err := s.AddExpense(ctx, expense(me, "2026-10-18", 100, "cassino")); !errors.Is(err, ErrUnknownCategory) {
		t.Errorf("an unknown category: %v", err)
	}
	e := expense(me, "2026-10-18", 100, "mercado")
	e.PaymentMethod = "cheque"
	if _, err := s.AddExpense(ctx, e); err == nil {
		t.Error("an unknown payment method was accepted")
	}
}

func TestConfirmAndDiscard(t *testing.T) {
	s := open(t)
	id := add(t, s, expense(me, "2026-10-18", 100, "mercado"))

	if _, err := s.Confirm(ctx, them, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("another user confirmed it: %v", err)
	}
	e, err := s.Confirm(ctx, me, id)
	if err != nil || e.Status != Confirmed || e.ConfirmedAt.IsZero() {
		t.Fatalf("confirm: %+v, %v", e, err)
	}
	if _, err := s.Confirm(ctx, me, id); !errors.Is(err, ErrNotPending) {
		t.Errorf("confirmed twice: %v", err)
	}
	if _, err := s.Discard(ctx, them, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("another user discarded it: %v", err)
	}
	discarded, err := s.Discard(ctx, me, id)
	if err != nil || discarded.Status != Discarded || !discarded.ConfirmedAt.Equal(e.ConfirmedAt) {
		t.Errorf("discarding a saved expense: %+v, %v", discarded, err)
	}
	if _, err := s.Discard(ctx, me, id); !errors.Is(err, ErrNotPending) {
		t.Errorf("discarded twice: %v", err)
	}
	if _, err := s.Confirm(ctx, me, id); !errors.Is(err, ErrNotPending) {
		t.Errorf("a discarded expense was confirmed: %v", err)
	}
	if _, err := s.Confirm(ctx, me, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing expense: %v", err)
	}

	other := add(t, s, expense(me, "2026-10-18", 100, "mercado"))
	if e, err := s.Discard(ctx, me, other); err != nil || e.Status != Discarded {
		t.Errorf("discarding a pending expense: %+v, %v", e, err)
	}
}

func TestAddExpenseAlreadyConfirmed(t *testing.T) {
	s := open(t)
	e := expense(me, "2026-10-18", 100, "mercado")
	e.Status = Confirmed
	got, err := s.Expense(ctx, add(t, s, e))
	if err != nil || got.Status != Confirmed || got.ConfirmedAt.IsZero() {
		t.Errorf("an expense saved without asking: %+v, %v", got, err)
	}
	e.Status = Discarded
	if _, err := s.AddExpense(ctx, e); err == nil {
		t.Error("a new expense was recorded as discarded")
	}
}

func TestSetCategory(t *testing.T) {
	s := open(t)
	id := add(t, s, expense(me, "2026-10-18", 100, "outros"))

	if e, err := s.SetCategory(ctx, me, id, "farmacia"); err != nil || e.Category != "farmacia" {
		t.Errorf("set: %+v, %v", e, err)
	}
	if _, err := s.SetCategory(ctx, me, id, "cassino"); !errors.Is(err, ErrUnknownCategory) {
		t.Errorf("unknown: %v", err)
	}
	if _, err := s.SetCategory(ctx, them, id, "lazer"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another user's: %v", err)
	}
	s.Discard(ctx, me, id)
	if _, err := s.SetCategory(ctx, me, id, "lazer"); !errors.Is(err, ErrNotPending) {
		t.Errorf("a discarded expense: %v", err)
	}
}

func TestDuplicates(t *testing.T) {
	s := open(t)
	att, _ := s.AddAttachment(ctx, Attachment{SHA256: "abc", Path: "receipts/abc.jpg", MIME: "image/jpeg", Size: 1})
	again, _ := s.AddAttachment(ctx, Attachment{SHA256: "abc", Path: "receipts/other.jpg", MIME: "image/jpeg", Size: 1})
	if again.ID != att.ID || again.Path != "receipts/abc.jpg" {
		t.Errorf("the same file was stored twice: %+v", again)
	}

	key := strings.Repeat("3", 44)
	first := expense(me, "2026-10-18", 100, "mercado")
	first.AttachmentID, first.NFCeKey = att.ID, key
	id := confirmed(t, s, first)

	sameFile := expense(me, "2026-10-18", 100, "mercado")
	sameFile.AttachmentID = att.ID
	sameReceipt := expense(me, "2026-10-18", 100, "mercado")
	sameReceipt.NFCeKey = key

	for name, e := range map[string]Expense{"same file": sameFile, "same nfc-e key": sameReceipt} {
		_, err := s.AddExpense(ctx, e)
		var dup *DuplicateError
		if !errors.Is(err, ErrDuplicate) || !errors.As(err, &dup) || dup.Existing.ID != id {
			t.Errorf("%s: %v", name, err)
		}
	}

	s.Undo(ctx, me)
	if _, err := s.AddExpense(ctx, sameReceipt); err != nil {
		t.Errorf("a receipt whose first record was discarded was refused: %v", err)
	}
}

func TestUndoDiscardsTheLastConfirmed(t *testing.T) {
	s := open(t)
	older := confirmed(t, s, expense(me, "2026-10-19", 100, "mercado"))
	newer := confirmed(t, s, expense(me, "2026-10-01", 200, "mercado"))
	confirmed(t, s, expense(them, "2026-10-20", 300, "mercado"))
	add(t, s, expense(me, "2026-10-20", 400, "mercado"))

	for _, want := range []int64{newer, older} {
		e, err := s.Undo(ctx, me)
		if err != nil || e.ID != want || e.Status != Discarded {
			t.Fatalf("undo: got %d (%s), %v, want %d", e.ID, e.Status, err, want)
		}
	}
	if _, err := s.Undo(ctx, me); !errors.Is(err, ErrNotFound) {
		t.Errorf("nothing left to undo: %v", err)
	}
}

func TestReports(t *testing.T) {
	s := open(t)
	morning := expense(me, "2026-10-05", 1500, "restaurante")
	morning.SpentTime = "08:10"
	evening := expense(me, "2026-10-05", 2000, "mercado")
	evening.SpentTime = "21:30"
	untimed := expense(me, "2026-10-05", 500, "mercado")

	confirmed(t, s, morning)
	confirmed(t, s, evening)
	confirmed(t, s, untimed)
	confirmed(t, s, expense(me, "2026-10-31", 1000, "lazer"))
	confirmed(t, s, expense(me, "2026-09-30", 9999, "mercado"))
	confirmed(t, s, expense(me, "2026-11-01", 9999, "mercado"))
	confirmed(t, s, expense(them, "2026-10-10", 9999, "mercado"))
	add(t, s, expense(me, "2026-10-10", 9999, "mercado"))

	sum, err := s.Summary(ctx, me, day("2026-10-01"), day("2026-11-01"))
	if err != nil {
		t.Fatal(err)
	}
	if sum.TotalCents != 5000 || sum.Count != 4 {
		t.Errorf("october: %d cents in %d", sum.TotalCents, sum.Count)
	}
	var got []string
	for _, c := range sum.ByCategory {
		got = append(got, c.Category.Slug)
	}
	if strings.Join(got, ",") != "mercado,restaurante,lazer" || sum.ByCategory[0].TotalCents != 2500 ||
		sum.ByCategory[0].Count != 2 || sum.ByCategory[0].Category.Label != "Mercado" {
		t.Errorf("by category: %+v", sum.ByCategory)
	}

	recent, err := s.Recent(ctx, me, 4)
	if err != nil {
		t.Fatal(err)
	}
	if order := totals(recent); order != "9999,1000,2000,1500" {
		t.Errorf("recent: %s", order)
	}

	between, err := s.Between(ctx, me, day("2026-10-01"), day("2026-11-01"))
	if err != nil {
		t.Fatal(err)
	}
	if order := totals(between); order != "500,1500,2000,1000" {
		t.Errorf("between: %s", order)
	}
}

func totals(list []Expense) string {
	var parts []string
	for _, e := range list {
		parts = append(parts, strconv.FormatInt(e.TotalCents, 10))
	}
	return strings.Join(parts, ",")
}

func TestSpent(t *testing.T) {
	s := open(t)
	s.RecordExtraction(ctx, Extraction{UserID: me, Model: "claude-opus-5-5", USD: 0.04})
	since := s.now()
	s.RecordExtraction(ctx, Extraction{UserID: me, Model: "claude-opus-5-5", USD: 0.05})
	s.RecordExtraction(ctx, Extraction{UserID: me, Model: "claude-opus-5-5", USD: 0.01, Failed: true})

	usd, n, err := s.Spent(ctx, since)
	if err != nil || n != 2 || usd < 0.0599 || usd > 0.0601 {
		t.Errorf("spent: $%f in %d, %v", usd, n, err)
	}
}

func TestExtractionFor(t *testing.T) {
	s := open(t)
	id := add(t, s, expense(me, "2026-10-18", 100, "mercado"))
	if _, err := s.ExtractionFor(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("no extraction yet: %v", err)
	}
	s.RecordExtraction(ctx, Extraction{UserID: me, ExpenseID: id, Model: "claude-opus-5-5", RawJSON: `{"a":1}`, USD: 0.01})
	s.RecordExtraction(ctx, Extraction{UserID: me, ExpenseID: id, Model: "claude-sonnet-5-5", RawJSON: `{"a":2}`, USD: 0.02})

	x, err := s.ExtractionFor(ctx, id)
	if err != nil || x.Model != "claude-sonnet-5-5" || x.RawJSON != `{"a":2}` || x.ExpenseID != id || x.CreatedAt.IsZero() {
		t.Errorf("the latest extraction: %+v, %v", x, err)
	}

	att, _ := s.AddAttachment(ctx, Attachment{SHA256: "f", Path: "f", MIME: "image/jpeg"})
	e := expense(me, "2026-10-18", 100, "mercado")
	e.AttachmentID = att.ID
	if _, found, _ := s.Duplicate(ctx, e); found {
		t.Error("a file with no expense yet is a duplicate")
	}
	first := add(t, s, e)
	if dup, found, err := s.Duplicate(ctx, Expense{AttachmentID: att.ID}); err != nil || !found || dup.ID != first {
		t.Errorf("the file's expense: %+v, %v, %v", dup, found, err)
	}
}
