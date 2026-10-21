package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func atVersion5(t *testing.T) (string, *Store) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "argos.db")
	old, err := openWith(file, migrations[:5])
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (telegram_id, created_at, last_seen_at) VALUES (4242, '2026-10-01T12:00:00Z', '2026-10-01T12:00:00Z')`,
		`INSERT INTO users (telegram_id, created_at, last_seen_at) VALUES (7777, '2026-10-01T12:00:00Z', '2026-10-01T12:00:00Z')`,
		`UPDATE categories SET emoji = '🍺' WHERE slug = 'lazer'`,
		`INSERT INTO attachments (sha256, path, mime, size_bytes, created_at) VALUES ('abc', '', 'image/jpeg', 1, '2026-10-01T12:00:00Z')`,
	} {
		if _, err := old.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []struct {
		user                  int64
		category, status, ref string
	}{
		{1, "farmacia", "confirmed", "1"}, {2, "mercado", "confirmed", "NULL"}, {1, "lazer", "discarded", "NULL"}, {1, "outros", "pending", "NULL"},
	} {
		if _, err := old.db.Exec(fmt.Sprintf(`INSERT INTO expenses (user_id, status, spent_on, merchant, category,
			payment_method, account, card_last4, total_cents, source, attachment_id, created_at, updated_at)
			VALUES (?, ?, '2026-10-18', 'Drogaria', ?, 'debito', 'Nubank', '1234', 4230, 'photo', %s,
			'2026-10-18T12:00:00Z', '2026-10-18T12:00:00Z')`, e.ref), e.user, e.status, e.category); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO expense_items (expense_id, position, description, total_cents) VALUES (1, 0, 'Dipirona', 4230)`,
		`INSERT INTO cards (chat_id, message_id, expense_id) VALUES (4242, 10, 1)`,
		`INSERT INTO extractions (user_id, attachment_id, expense_id, model, created_at) VALUES (1, 1, 1, 'claude-opus-5-5', '2026-10-18T12:00:00Z')`,
		`DELETE FROM expenses WHERE id = 4`,
	} {
		if _, err := old.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return file, old
}

func TestCustomCategoriesMigration(t *testing.T) {
	file, old := atVersion5(t)
	old.Close()

	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := strings.Join(s.Applied(), ","); got != "0006_custom_categories" {
		t.Fatalf("applied %q", got)
	}

	var rows, shared int
	if err := s.db.QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE user_id IS NULL) FROM categories`).Scan(&rows, &shared); err != nil ||
		rows != 16 || shared != 16 {
		t.Errorf("the defaults are kept once, for everyone: %d rows, %d shared, %v", rows, shared, err)
	}
	for _, userID := range []int64{1, 2} {
		cats, err := s.Categories(ctx, userID)
		if err != nil || len(cats) != 16 || cats[0].Slug != "mercado" || cats[15].Slug != FallbackCategory {
			t.Fatalf("user %d: %d categories, %v", userID, len(cats), err)
		}
		if cats[9].Slug != "lazer" || cats[9].Emoji != "🍺" || cats[9].Custom {
			t.Errorf("user %d: an emoji changed before the migration was lost: %+v", userID, cats[9])
		}
	}

	e, err := s.Expense(ctx, 1)
	if err != nil || e.Category != "farmacia" || e.UserID != 1 || e.Account != "Nubank" || e.CardLast4 != "1234" ||
		e.AttachmentID != 1 || len(e.Items) != 1 || e.Items[0].Description != "Dipirona" {
		t.Errorf("expense 1 after the migration: %+v, %v", e, err)
	}
	if e, err := s.Expense(ctx, 2); err != nil || e.Category != "mercado" || e.UserID != 2 {
		t.Errorf("expense 2 after the migration: %+v, %v", e, err)
	}
	if e, err := s.Expense(ctx, 3); err != nil || e.Category != "lazer" || e.Status != Discarded {
		t.Errorf("expense 3 after the migration: %+v, %v", e, err)
	}
	if id, err := s.CardExpense(ctx, 4242, 10); err != nil || id != 1 {
		t.Errorf("the card of expense 1: %d, %v", id, err)
	}
	if x, err := s.ExtractionFor(ctx, 1); err != nil || x.ExpenseID != 1 {
		t.Errorf("the extraction of expense 1: %+v, %v", x, err)
	}

	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Errorf("foreign keys after the migration: %d, %v", fk, err)
	}
	var indexes int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name IN ('expenses_by_user_and_day',
		'expenses_one_per_attachment', 'expenses_one_per_nfce_key', 'categories_shared', 'categories_own')`).Scan(&indexes); err != nil || indexes != 5 {
		t.Errorf("the indexes: %d, %v", indexes, err)
	}

	if _, err := s.AddExpense(ctx, Expense{UserID: 1, SpentOn: day("2026-10-19"), TotalCents: 100, Category: "mercado",
		Source: SourcePhoto, AttachmentID: 1}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("the same file again after the migration: %v", err)
	}
	if id := add(t, s, expense(1, "2026-10-19", 100, "mercado")); id != 5 {
		t.Errorf("the next expense got id %d, want 5: the id of the one deleted before the migration came back", id)
	}
}

func TestCustomCategoriesMigrationStopsOnAnOrphan(t *testing.T) {
	file, old := atVersion5(t)
	for _, q := range []string{`PRAGMA foreign_keys = OFF`, `UPDATE expenses SET category = 'cassino' WHERE id = 2`} {
		if _, err := old.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	if _, err := Open(file); err == nil || !strings.Contains(err.Error(), "0006_custom_categories") {
		t.Fatalf("an expense in a category that does not exist was migrated: %v", err)
	}
	again, err := openWith(file, migrations[:5])
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	var n int
	if err := again.db.QueryRow(`SELECT COUNT(*) FROM expenses`).Scan(&n); err != nil || n != 3 || again.Version() != 5 {
		t.Errorf("after the failed migration: %d expenses at version %d, %v", n, again.Version(), err)
	}
}

func TestSaveCategory(t *testing.T) {
	s := open(t)
	c, created, err := s.SaveCategory(ctx, me, Category{Label: " Pet Shop ", Emoji: "🐶", Hint: "ração, banho"})
	if err != nil || !created || c.Slug != "pet-shop" || c.Label != "Pet Shop" || c.ID == 0 || !c.Custom {
		t.Fatalf("a new category: %+v, %v, %v", c, created, err)
	}

	cats, _ := s.Categories(ctx, me)
	if len(cats) != 17 || cats[15].Slug != "pet-shop" || cats[16].Slug != FallbackCategory || cats[15].Hint != "ração, banho" {
		t.Errorf("the new category goes before %s: %+v", FallbackCategory, cats[14:])
	}
	if theirs, _ := s.Categories(ctx, them); len(theirs) != 16 {
		t.Errorf("someone else sees it: %d", len(theirs))
	}

	if again, created, err := s.SaveCategory(ctx, me, Category{Label: "pet shop", Emoji: "🐕"}); err != nil || created ||
		again.ID != c.ID || again.Label != "Pet Shop" || again.Emoji != "🐕" || again.Hint != "ração, banho" {
		t.Errorf("the same name again: %+v, %v, %v", again, created, err)
	}
	for _, label := range []string{"SAUDE", "Mercado"} {
		if shared, _, err := s.SaveCategory(ctx, me, Category{Label: label, Emoji: "🏥", Hint: "consultas"}); !errors.Is(err, ErrSharedCategory) || shared.Custom {
			t.Errorf("%s, a default: %+v, %v", label, shared, err)
		}
	}
	if saude, _ := categoryBySlug(ctx, s.db, them, "saude"); saude.Emoji != "🩺" || saude.Hint != "" {
		t.Errorf("a default was changed: %+v", saude)
	}

	mine, _, _ := s.SaveCategory(ctx, me, Category{Label: "Academia", Emoji: "🏋️"})
	theirs, _, err := s.SaveCategory(ctx, them, Category{Label: "Academia"})
	if err != nil || theirs.ID == mine.ID || theirs.Emoji != defaultEmoji {
		t.Errorf("two people with the same name: %+v and %+v, %v", mine, theirs, err)
	}
	var rows int
	if s.db.QueryRow(`SELECT COUNT(*) FROM categories`).Scan(&rows); rows != 16+3 {
		t.Errorf("the table holds the defaults once and each one's own: %d rows", rows)
	}

	id := add(t, s, expense(me, "2026-10-18", 100, "pet-shop"))
	if _, err := s.AddExpense(ctx, expense(them, "2026-10-18", 100, "pet-shop")); !errors.Is(err, ErrUnknownCategory) {
		t.Errorf("someone else's category: %v", err)
	}
	if e, err := s.SetCategory(ctx, me, id, "saude"); err != nil || e.Category != "saude" {
		t.Errorf("moving to a default category: %+v, %v", e, err)
	}
	if e, err := s.SetCategory(ctx, me, id, "academia"); err != nil || e.Category != "academia" {
		t.Errorf("moving to their own academia: %+v, %v", e, err)
	}
	f := Filter{From: day("2026-10-01"), To: day("2026-11-01"), Category: "academia"}
	if _, err := s.Confirm(ctx, me, id); err != nil && !errors.Is(err, ErrNotPending) {
		t.Fatal(err)
	}
	if g, err := s.Totals(ctx, me, f, "none"); err != nil || len(g) != 1 || g[0].TotalCents != 100 {
		t.Errorf("filtering by their own category: %+v, %v", g, err)
	}

	if _, _, err := s.SaveCategory(ctx, me, Category{Label: "🎉 !!"}); err == nil {
		t.Error("a name with no letter was saved")
	}
	if _, _, err := s.SaveCategory(ctx, 999, Category{Label: "Natação"}); !errors.Is(err, ErrUnknownUser) {
		t.Errorf("a category for a user never seen: %v", err)
	}
	for i := 18; i < MaxCategories; i++ {
		if _, _, err := s.SaveCategory(ctx, me, Category{Label: fmt.Sprintf("Extra %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.SaveCategory(ctx, me, Category{Label: "Uma a mais"}); !errors.Is(err, ErrCategoryLimit) {
		t.Errorf("past the limit: %v", err)
	}
	if _, created, err := s.SaveCategory(ctx, me, Category{Label: "Pet Shop", Emoji: "🐈"}); err != nil || created {
		t.Errorf("changing one at the limit: %v, %v", created, err)
	}
}

func TestSlug(t *testing.T) {
	for label, want := range map[string]string{
		"Combustível": "combustivel", "Educação": "educacao", "Pet Shop": "pet-shop", "  Casa & Jardim ": "casa-jardim",
		"Sau\u0301de": "saude", "Bebê 2": "bebe-2", "🎉": "", "Ônibus/Metrô": "onibus-metro",
	} {
		if got := Slug(label); got != want {
			t.Errorf("Slug(%q) = %q, want %q", label, got, want)
		}
	}
	cats, err := open(t).Categories(ctx, me)
	if err != nil || len(cats) == 0 {
		t.Fatal(err)
	}
	for _, c := range cats {
		if Slug(c.Label) != c.Slug {
			t.Errorf("the default %q has slug %q: a category of one's own named like it would not be recognised", c.Label, c.Slug)
		}
	}
}

func TestUpdateAndRemoveCategory(t *testing.T) {
	s := open(t)
	gym, _, _ := s.SaveCategory(ctx, me, Category{Label: "Academia", Emoji: "🏋️", Hint: "mensalidade"})
	tennis, _, _ := s.SaveCategory(ctx, me, Category{Label: "Tênis"})
	theirs, _, _ := s.SaveCategory(ctx, them, Category{Label: "Cassino"})

	gym.Label, gym.Emoji, gym.Hint = "Musculação", "💪", ""
	if got, err := s.UpdateCategory(ctx, me, gym); err != nil || got.Slug != "musculacao" || got.Emoji != "💪" || got.Hint != "" {
		t.Errorf("renaming: %+v, %v", got, err)
	}
	for label, want := range map[string]error{"Mercado": ErrSharedCategory, "tenis": ErrCategoryExists} {
		renamed := gym
		renamed.Label = label
		if _, err := s.UpdateCategory(ctx, me, renamed); !errors.Is(err, want) {
			t.Errorf("renaming to %s: %v, want %v", label, err, want)
		}
	}
	saude, _ := categoryBySlug(ctx, s.db, me, "saude")
	for _, c := range []Category{saude, theirs} {
		c.Emoji = "🎰"
		if _, err := s.UpdateCategory(ctx, me, c); !errors.Is(err, ErrUnknownCategory) {
			t.Errorf("changing %s, not their own: %v", c.Label, err)
		}
		if _, err := s.RemoveCategory(ctx, me, c.ID); !errors.Is(err, ErrUnknownCategory) {
			t.Errorf("removing %s, not their own: %v", c.Label, err)
		}
	}

	id := confirmed(t, s, expense(me, "2026-10-18", 100, "musculacao"))
	if archived, err := s.RemoveCategory(ctx, me, gym.ID); err != nil || !archived {
		t.Errorf("removing one in use: archived %v, %v", archived, err)
	}
	if archived, err := s.RemoveCategory(ctx, me, tennis.ID); err != nil || archived {
		t.Errorf("removing one never used: archived %v, %v", archived, err)
	}
	cats, _ := s.Categories(ctx, me)
	if len(cats) != 17 || !cats[15].Archived || cats[15].Slug != "musculacao" {
		t.Errorf("after removing both: %+v", cats[15:])
	}
	if e, err := s.Expense(ctx, id); err != nil || e.Category != "musculacao" {
		t.Errorf("an expense in an archived category: %+v, %v", e, err)
	}
	if g, _ := s.Totals(ctx, me, Filter{From: day("2026-10-01"), To: day("2026-11-01")}, "category"); len(g) != 1 || g[0].Key != "Musculação" {
		t.Errorf("the reports still see it: %+v", g)
	}

	for i := 16; i < MaxCategories; i++ {
		if _, _, err := s.SaveCategory(ctx, me, Category{Label: fmt.Sprintf("Extra %d", i)}); err != nil {
			t.Fatalf("an archived category counted against the limit: %v", err)
		}
	}
	if _, _, err := s.SaveCategory(ctx, me, Category{Label: "Musculação"}); !errors.Is(err, ErrCategoryLimit) {
		t.Errorf("bringing one back past the limit: %v", err)
	}
	extra, _ := categoryBySlug(ctx, s.db, me, "extra-16")
	if _, err := s.RemoveCategory(ctx, me, extra.ID); err != nil {
		t.Fatal(err)
	}
	back, created, err := s.SaveCategory(ctx, me, Category{Label: "musculação", Hint: "academia, personal"})
	if err != nil || !created || back.ID != gym.ID || back.Archived || back.Emoji != "💪" || back.Hint != "academia, personal" {
		t.Errorf("bringing it back: %+v, %v, %v", back, created, err)
	}

	use, err := s.CategoryUse(ctx, me, day("2026-10-01"))
	if err != nil || len(use) != 1 || use[gym.ID] != 1 {
		t.Errorf("use since October: %v, %v", use, err)
	}
	if use, _ := s.CategoryUse(ctx, me, day("2026-10-19")); len(use) != 0 {
		t.Errorf("use since the 19th: %v", use)
	}
}
