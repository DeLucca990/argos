package store

import (
	"errors"
	"testing"
)

func TestSaveUserUpdatesEverythingButIDAndCreatedAt(t *testing.T) {
	s := open(t)
	first, err := s.SaveUser(ctx, User{TelegramID: 8683784233, FirstName: "Pedro", Username: "pedro", LanguageCode: "pt-br"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != 3 || first.TelegramID != 8683784233 {
		t.Errorf("a new user: id %d, telegram id %d", first.ID, first.TelegramID)
	}
	if first.CreatedAt.IsZero() || !first.LastSeenAt.Equal(first.CreatedAt) {
		t.Errorf("a new user: created %v, last seen %v", first.CreatedAt, first.LastSeenAt)
	}

	again, err := s.SaveUser(ctx, User{ID: 99, TelegramID: 8683784233, FirstName: "Pedro", LastName: "L", Username: "pedrol"})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || !again.CreatedAt.Equal(first.CreatedAt) || !again.LastSeenAt.After(first.LastSeenAt) {
		t.Errorf("id %d → %d, created %v → %v, last seen %v → %v", first.ID, again.ID,
			first.CreatedAt, again.CreatedAt, first.LastSeenAt, again.LastSeenAt)
	}
	if again.LastName != "L" || again.Username != "pedrol" || again.LanguageCode != "" {
		t.Errorf("the profile was not replaced: %+v", again)
	}

	if got, err := s.User(ctx, first.ID); err != nil || got != again {
		t.Errorf("read back: %+v, %v", got, err)
	}
	if _, err := s.User(ctx, 999); !errors.Is(err, ErrUnknownUser) {
		t.Errorf("a user never seen: %v", err)
	}

	next, err := s.SaveUser(ctx, User{TelegramID: 1})
	if err != nil || next.ID != 4 {
		t.Errorf("an update consumed an id: the next user got %d, %v", next.ID, err)
	}
}

func TestExpensesBelongToAKnownUser(t *testing.T) {
	s := open(t)
	if _, err := s.AddExpense(ctx, expense(999, "2026-10-18", 100, "mercado")); !errors.Is(err, ErrUnknownUser) {
		t.Errorf("an expense for a user never seen: %v", err)
	}
	if _, err := s.RecordExtraction(ctx, Extraction{UserID: 999, Model: "claude-opus-5-5"}); err == nil {
		t.Error("an extraction for a user never seen was recorded")
	}
}

func TestIDsAreNeverReused(t *testing.T) {
	s := open(t)
	first := add(t, s, expense(me, "2026-10-18", 100, "mercado"))
	if _, err := s.db.Exec(`DELETE FROM expenses WHERE id = ?`, first); err != nil {
		t.Fatal(err)
	}
	if next := add(t, s, expense(me, "2026-10-18", 100, "mercado")); next == first {
		t.Errorf("the id %d of a deleted expense was handed out again", first)
	}

	for _, sha := range []string{"a", "a", "a", "b"} {
		if _, err := s.AddAttachment(ctx, Attachment{SHA256: sha, Path: sha, MIME: "image/jpeg"}); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := attachment(ctx, s.db, "b"); b.ID != 2 {
		t.Errorf("the same file sent again spent ids: the next file got %d", b.ID)
	}
}
