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
	if _, err := old.db.Exec(`INSERT INTO users (telegram_id, created_at, last_seen_at) VALUES (42, '2026-10-01T12:00:00Z', '2026-10-01T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := strings.Join(s.Applied(), ","); got != names(migrations[1:]) || s.Version() != len(migrations) {
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

	s.RecordExtraction(ctx, Extraction{UserID: me, ExpenseID: id, Model: "m", InputTokens: 1000, OutputTokens: 200, USD: 0.04})
	s.RecordExtraction(ctx, Extraction{UserID: me, ExpenseID: id, Model: "m", InputTokens: 300, CacheReadTokens: 50, USD: 0.01})
	s.RecordExtraction(ctx, Extraction{UserID: me, Model: "m", InputTokens: 9999, USD: 1})
	sp, err := s.ExpenseCost(ctx, id)
	if err != nil || sp.USD < 0.0499 || sp.USD > 0.0501 || sp.Input != 1300 || sp.Output != 200 || sp.CacheRead != 50 {
		t.Errorf("cost: %+v, %v", sp, err)
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

func TestScheduledBackupKeepsTheNewest(t *testing.T) {
	s := open(t)
	dir := filepath.Join(t.TempDir(), "backups")
	start := time.Date(2026, 10, 1, 3, 0, 0, 0, time.Local)
	for i := range 5 {
		at := start.Add(time.Duration(i) * 6 * time.Hour)
		written, _, next, err := s.ScheduledBackup(ctx, dir, 3, 6*time.Hour, at)
		if err != nil || written == "" || !next.Equal(at.Add(6*time.Hour)) {
			t.Fatalf("backup %d: %q, next %v, %v", i, written, next, err)
		}
	}
	last := start.Add(24 * time.Hour)
	written, removed, next, err := s.ScheduledBackup(ctx, dir, 3, 6*time.Hour, last.Add(5*time.Hour+59*time.Minute))
	if err != nil || written != "" || removed != 0 || !next.Equal(last.Add(6*time.Hour)) {
		t.Errorf("before it is due: %q, %d, next %v, %v", written, removed, next, err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(files) != 3 || filepath.Base(files[0]) != "argos-2026-10-01-150000.db" || filepath.Base(files[2]) != "argos-2026-10-02-030000.db" {
		t.Errorf("kept %v", files)
	}
}

func TestScheduledBackupAfterDailyOnes(t *testing.T) {
	s := open(t)
	dir := t.TempDir()
	for _, name := range []string{"argos-2026-10-19.db", "argos-2026-10-20.db", "argos-copy.db"} {
		os.WriteFile(filepath.Join(dir, name), nil, 0o600)
	}
	written, removed, _, err := s.ScheduledBackup(ctx, dir, 2, 24*time.Hour, time.Date(2026, 10, 20, 23, 0, 0, 0, time.Local))
	if err != nil || written != "" || removed != 0 {
		t.Errorf("a day's backup written that day, under its old name, is not due: %q, %d, %v", written, removed, err)
	}
	written, removed, _, err = s.ScheduledBackup(ctx, dir, 2, 24*time.Hour, time.Date(2026, 10, 21, 0, 30, 0, 0, time.Local))
	if err != nil || filepath.Base(written) != "argos-2026-10-21-003000.db" || removed != 1 {
		t.Errorf("the next day: %q, %d, %v", written, removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "argos-copy.db")); err != nil {
		t.Errorf("a file that is not a backup was removed: %v", err)
	}
}

func TestDigestsAndUsers(t *testing.T) {
	s := open(t)
	if sent, err := s.DigestSent(ctx, me, "2026-09"); err != nil || sent {
		t.Errorf("not sent yet: %v, %v", sent, err)
	}
	for range 2 {
		if err := s.MarkDigestSent(ctx, me, "2026-09"); err != nil {
			t.Fatal(err)
		}
	}
	if sent, _ := s.DigestSent(ctx, me, "2026-09"); !sent {
		t.Error("marked, but not sent")
	}
	if sent, _ := s.DigestSent(ctx, them, "2026-09"); sent {
		t.Error("someone else's digest counted as sent")
	}
	if err := s.MarkDigestSent(ctx, me, "setembro"); err == nil {
		t.Error("a month that is not YYYY-MM was accepted")
	}

	users, err := s.Users(ctx)
	if err != nil || len(users) != 2 || users[0].TelegramID != 4242 || users[1].TelegramID != 7777 {
		t.Errorf("users: %+v, %v", users, err)
	}
}

func TestFilesForgottenAndKeptAgain(t *testing.T) {
	s := open(t)
	kept, _ := s.AddAttachment(ctx, Attachment{SHA256: "a", Path: "receipts/a.jpg", MIME: "image/jpeg"})
	s.AddAttachment(ctx, Attachment{SHA256: "b", MIME: "image/jpeg"})
	if n, err := s.ForgetFiles(ctx); err != nil || n != 1 {
		t.Errorf("forgot %d, %v", n, err)
	}
	if a, _ := attachment(ctx, s.db, "a"); a.Path != "" || a.ID != kept.ID {
		t.Errorf("after forgetting: %+v", a)
	}
	if again, _ := s.AddAttachment(ctx, Attachment{SHA256: "a", Path: "receipts/a.jpg", MIME: "image/jpeg"}); again.Path != "receipts/a.jpg" || again.ID != kept.ID {
		t.Errorf("the same file kept again: %+v", again)
	}
}
