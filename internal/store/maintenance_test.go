package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnExistingDatabaseGetsTheNewMigrations(t *testing.T) {
	file := filepath.Join(t.TempDir(), "argos.db")
	old, err := openWith(file, migrations[:1])
	if err != nil {
		t.Fatal(err)
	}
	old.SaveUser(ctx, User{TelegramID: 42})
	old.Close()

	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := strings.Join(s.Applied(), ","); got != "0002_cards" || s.Version() != 2 {
		t.Errorf("applied %q, now at %d", got, s.Version())
	}
	if u, err := s.User(ctx, 1); err != nil || u.TelegramID != 42 {
		t.Errorf("the data from before the migration: %+v, %v", u, err)
	}
}

func TestUpdateExpense(t *testing.T) {
	s := open(t)
	e := expense(me, "2026-10-18", 1000, "mercado")
	e.Items = []Item{{Description: "Arroz", Quantity: 1, TotalCents: 1000}}
	e.Status = Confirmed
	id := add(t, s, e)

	fixed := expense(me, "2026-10-17", 4590, "farmacia")
	fixed.ID, fixed.Merchant, fixed.PaymentMethod, fixed.SpentTime = id, "Drogaria", "credito", "09:30"
	fixed.Items = []Item{{Description: "Dipirona", Quantity: 2, UnitCents: 2295, TotalCents: 4590}}
	got, err := s.UpdateExpense(ctx, me, fixed)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalCents != 4590 || got.Category != "farmacia" || got.Merchant != "Drogaria" || got.PaymentMethod != "credito" ||
		got.SpentTime != "09:30" || got.SpentOn.Format(dateLayout) != "2026-10-17" || got.Status != Confirmed ||
		len(got.Items) != 1 || got.Items[0].Description != "Dipirona" {
		t.Errorf("updated: %+v", got)
	}

	if _, err := s.UpdateExpense(ctx, them, fixed); !errors.Is(err, ErrNotFound) {
		t.Errorf("another user's: %v", err)
	}
	fixed.Category = "cassino"
	if _, err := s.UpdateExpense(ctx, me, fixed); !errors.Is(err, ErrUnknownCategory) {
		t.Errorf("an unknown category: %v", err)
	}

	key := strings.Repeat("1", 43) + "2"
	other := expense(me, "2026-10-18", 100, "mercado")
	other.NFCeKey = key
	otherID := add(t, s, other)
	fixed.Category, fixed.NFCeKey = "farmacia", key
	var dup *DuplicateError
	if _, err := s.UpdateExpense(ctx, me, fixed); !errors.As(err, &dup) || dup.Existing.ID != otherID {
		t.Errorf("taking another expense's NFC-e key: %v", err)
	}

	s.Discard(ctx, me, id)
	fixed.NFCeKey = ""
	if _, err := s.UpdateExpense(ctx, me, fixed); !errors.Is(err, ErrNotPending) {
		t.Errorf("a discarded expense: %v", err)
	}
}

func TestCardsAndCost(t *testing.T) {
	s := open(t)
	id := add(t, s, expense(me, "2026-10-18", 100, "mercado"))
	if _, err := s.CardExpense(ctx, 42, 7); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown card: %v", err)
	}
	if err := s.RememberCard(ctx, 42, 7, id); err != nil {
		t.Fatal(err)
	}
	if err := s.RememberCard(ctx, 42, 7, id); err != nil {
		t.Errorf("remembering a card twice: %v", err)
	}
	if got, err := s.CardExpense(ctx, 42, 7); err != nil || got != id {
		t.Errorf("card: %d, %v", got, err)
	}

	s.RecordExtraction(ctx, Extraction{UserID: me, ExpenseID: id, Model: "m", USD: 0.04})
	s.RecordExtraction(ctx, Extraction{UserID: me, ExpenseID: id, Model: "m", USD: 0.01})
	s.RecordExtraction(ctx, Extraction{UserID: me, Model: "m", USD: 1})
	if usd, err := s.ExpenseCost(ctx, id); err != nil || usd < 0.0499 || usd > 0.0501 {
		t.Errorf("cost: %f, %v", usd, err)
	}
}

func TestBackup(t *testing.T) {
	s := open(t)
	add(t, s, expense(me, "2026-10-18", 100, "mercado"))
	file := filepath.Join(t.TempDir(), "argos-2026-10-20.db")
	if err := s.Backup(ctx, file); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Error("the partial file was left behind")
	}

	copy, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	if e, err := copy.Expense(ctx, 1); err != nil || e.TotalCents != 100 {
		t.Errorf("the backup: %+v, %v", e, err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Errorf("ping: %v", err)
	}
}

func TestDailyBackupKeepsTheNewest(t *testing.T) {
	s := open(t)
	dir := filepath.Join(t.TempDir(), "backups")
	day := time.Date(2026, 10, 1, 3, 0, 0, 0, time.Local)
	for i := range 5 {
		if written, _, err := s.DailyBackup(ctx, dir, 3, day.AddDate(0, 0, i)); err != nil || written == "" {
			t.Fatalf("day %d: %q, %v", i, written, err)
		}
	}
	written, removed, err := s.DailyBackup(ctx, dir, 3, day.AddDate(0, 0, 4))
	if err != nil || written != "" || removed != 0 {
		t.Errorf("a second run the same day: %q, %d, %v", written, removed, err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(files) != 3 || filepath.Base(files[0]) != "argos-2026-10-03.db" || filepath.Base(files[2]) != "argos-2026-10-05.db" {
		t.Errorf("kept %v", files)
	}
}
