package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

const pluggyItem = "6f1b8a3e-2c4d-4e5f-8a9b-0c1d2e3f4a5b"

func TestBankMigrationKeepsEverything(t *testing.T) {
	file := filepath.Join(t.TempDir(), "argos.db")
	old, err := openWith(file, migrations[:7])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.SaveUser(ctx, User{TelegramID: 4242}); err != nil {
		t.Fatal(err)
	}
	old.now = func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }
	first := add(t, old, expense(me, "2026-10-18", 4230, "farmacia"))
	second := add(t, old, expense(me, "2026-10-19", 1000, "mercado"))
	if _, err := old.Discard(ctx, me, second); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO extractions (user_id, expense_id, model, usd, created_at, kind) VALUES (1, 1, 'claude-opus-5-5', 0.04, '2026-10-18T12:00:00Z', 'receipt')`,
		`INSERT INTO extractions (user_id, expense_id, model, usd, created_at, kind) VALUES (1, 1, 'claude-opus-5-5', 0.01, '2026-10-18T12:01:00Z', 'correction')`,
		`INSERT INTO cards (chat_id, message_id, expense_id) VALUES (4242, 10, 1)`,
		`INSERT INTO lessons (user_id, expense_id, field, value, layout, created_at) VALUES (1, 1, 'payment_account', 'Nubank', 'roxo', '2026-10-18T12:01:00Z')`,
		`DELETE FROM expenses WHERE id = 2`,
	} {
		if _, err := old.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.Applied(); len(got) != 1 || got[0] != "0008_bank" {
		t.Fatalf("applied %v", got)
	}

	e, err := s.Expense(ctx, first)
	if err != nil || e.TotalCents != 4230 || e.Category != "farmacia" || e.Source != SourceText {
		t.Errorf("expense %d after the migration: %+v, %v", first, e, err)
	}
	if id, err := s.CardExpense(ctx, 4242, 10); err != nil || id != first {
		t.Errorf("the card: %d, %v", id, err)
	}
	if calls, err := s.Calls(ctx, time.Time{}); err != nil || calls[KindReceipt] != 1 || calls[KindCorrection] != 1 {
		t.Errorf("extractions: %v, %v", calls, err)
	}

	bank := expense(me, "2026-10-20", 990, "delivery")
	bank.Source, bank.Status = SourceBank, Confirmed
	id, err := s.AddExpense(ctx, bank)
	if err != nil || id != 3 {
		t.Errorf("a bank expense after one deleted at id 2: id %d, %v", id, err)
	}
	if _, err := s.RecordExtraction(ctx, Extraction{Kind: KindBank, UserID: me, Model: "claude-opus-5-5"}); err != nil {
		t.Errorf("a bank extraction: %v", err)
	}
}

func TestLinkBankItem(t *testing.T) {
	s := open(t)
	from := day("2026-10-01")

	it, created, err := s.LinkBankItem(ctx, me, pluggyItem, from)
	if err != nil || !created || it.ID == 0 || it.PluggyID != pluggyItem || !it.ImportFrom.Equal(from) || !it.SyncedAt.IsZero() {
		t.Fatalf("linked: %+v, %v, %v", it, created, err)
	}
	again, created, err := s.LinkBankItem(ctx, me, pluggyItem, day("2026-11-01"))
	if err != nil || created || again.ID != it.ID || !again.ImportFrom.Equal(from) {
		t.Errorf("linked again: %+v, %v, %v", again, created, err)
	}
	if _, _, err := s.LinkBankItem(ctx, them, pluggyItem, from); !errors.Is(err, ErrItemTaken) {
		t.Errorf("someone else's item: %v", err)
	}

	synced := time.Date(2026, 10, 20, 15, 4, 5, 0, time.UTC)
	it.Name, it.Status, it.Error, it.SyncedAt, it.ImportedUpTo = "Nubank", "LOGIN_ERROR", "consent expired", synced, synced.Add(-time.Hour)
	if err := s.SaveBankItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	got, err := s.BankItem(ctx, me, it.ID)
	if err != nil || got.Name != "Nubank" || got.Status != "LOGIN_ERROR" || got.Error != "consent expired" ||
		!got.SyncedAt.Equal(synced) || !got.ImportedUpTo.Equal(synced.Add(-time.Hour)) || !got.ConsentUntil.IsZero() {
		t.Errorf("saved: %+v, %v", got, err)
	}
	if _, err := s.BankItem(ctx, them, it.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("someone else reads it: %v", err)
	}
	if list, err := s.BankItems(ctx); err != nil || len(list) != 1 {
		t.Errorf("all items: %v, %v", list, err)
	}

	if err := s.UnlinkBankItem(ctx, them, it.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("someone else unlinks it: %v", err)
	}
	if err := s.UnlinkBankItem(ctx, me, it.ID); err != nil {
		t.Fatal(err)
	}
	if list, err := s.UserBankItems(ctx, me); err != nil || len(list) != 0 {
		t.Errorf("after unlinking: %v, %v", list, err)
	}
	if _, _, err := s.LinkBankItem(ctx, them, pluggyItem, from); err != nil {
		t.Errorf("once unlinked, someone else may link it: %v", err)
	}
}

func bankExpense(user int64, on string, cents int64, category string) Expense {
	e := expense(user, on, cents, category)
	e.Source, e.Status = SourceBank, Confirmed
	return e
}

func TestBankTransactions(t *testing.T) {
	s := open(t)
	tx := BankTransaction{PluggyID: "t1", UserID: me, AccountID: "acc", SpentOn: day("2026-10-19"), TotalCents: 3590, Description: "IFD*IFOOD"}
	id, err := s.AddBankExpense(ctx, tx, bankExpense(me, "2026-10-19", 3590, "delivery"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordBankTransaction(ctx, BankTransaction{PluggyID: "t2", UserID: me, AccountID: "acc",
		SpentOn: day("2026-10-10"), TotalCents: 150000, Description: "Pagamento de fatura"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordBankTransaction(ctx, BankTransaction{PluggyID: "t1", UserID: me, AccountID: "acc",
		SpentOn: day("2026-10-19"), TotalCents: 3590}); err == nil {
		t.Error("the same transaction was recorded twice")
	}

	known, err := s.KnownBankTransactions(ctx, []string{"t1", "t2", "t3"})
	if err != nil || !known["t1"] || !known["t2"] || known["t3"] {
		t.Errorf("known: %v, %v", known, err)
	}
	list, err := s.BankTransactions(ctx, "acc", day("2026-10-15"))
	if err != nil || len(list) != 1 || list[0].PluggyID != "t1" || list[0].ExpenseID != id {
		t.Errorf("since the 15th: %+v, %v", list, err)
	}
	if got, err := s.BankTransactionFor(ctx, id); err != nil || got.Description != "IFD*IFOOD" {
		t.Errorf("the expense's transaction: %+v, %v", got, err)
	}

	if err := s.RecordBankTransaction(ctx, BankTransaction{PluggyID: "t4", UserID: me, AccountID: "acc",
		ProviderID: "of-1", SpentOn: day("2026-10-19"), TotalCents: 990, ExpenseID: id}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.BankTransactionByProvider(ctx, me, "of-1"); err != nil || got.PluggyID != "t4" || got.ExpenseID != id {
		t.Errorf("by Open Finance id: %+v, %v", got, err)
	}
	for _, c := range []struct {
		user     int64
		provider string
	}{{them, "of-1"}, {me, ""}, {me, "of-2"}} {
		if _, err := s.BankTransactionByProvider(ctx, c.user, c.provider); !errors.Is(err, ErrNotFound) {
			t.Errorf("user %d, Open Finance id %q: %v", c.user, c.provider, err)
		}
	}

	if _, e, found, err := s.LastFiled(ctx, me, "IFD*IFOOD"); err != nil || !found || e.ID != id || e.Category != "delivery" {
		t.Errorf("filed before as: %+v, %v, %v", e, found, err)
	}
	if t2, _, found, err := s.LastFiled(ctx, me, "Pagamento de fatura"); err != nil || !found || t2.ExpenseID != 0 {
		t.Errorf("not spending before: %+v, %v, %v", t2, found, err)
	}
	if _, _, found, _ := s.LastFiled(ctx, them, "IFD*IFOOD"); found {
		t.Error("someone else's filing was used")
	}
	if _, err := s.Discard(ctx, me, id); err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := s.LastFiled(ctx, me, "IFD*IFOOD"); err != nil || found {
		t.Errorf("a discarded expense still teaches: %v, %v", found, err)
	}
}

func TestMatchExpense(t *testing.T) {
	s := open(t)
	far := add(t, s, Expense{UserID: me, SpentOn: day("2026-10-16"), TotalCents: 5000, Category: "lazer", Source: SourcePhoto, Status: Confirmed})
	near := add(t, s, Expense{UserID: me, SpentOn: day("2026-10-19"), TotalCents: 5000, Category: "lazer", Source: SourcePhoto, Status: Confirmed})
	add(t, s, Expense{UserID: me, SpentOn: day("2026-10-20"), TotalCents: 5000, Category: "lazer", Source: SourceText, Status: Confirmed, PaymentMethod: "dinheiro"})
	add(t, s, Expense{UserID: them, SpentOn: day("2026-10-20"), TotalCents: 5000, Category: "lazer", Source: SourceText, Status: Confirmed})
	add(t, s, bankExpense(me, "2026-10-20", 5000, "lazer"))

	e, found, err := s.MatchExpense(ctx, me, 5000, day("2026-10-20"))
	if err != nil || !found || e.ID != near {
		t.Fatalf("matched %d, %v, %v; want %d", e.ID, found, err, near)
	}
	if err := s.RecordBankTransaction(ctx, BankTransaction{PluggyID: "t1", UserID: me, AccountID: "acc",
		SpentOn: day("2026-10-20"), TotalCents: 5000, ExpenseID: near}); err != nil {
		t.Fatal(err)
	}
	if e, found, _ := s.MatchExpense(ctx, me, 5000, day("2026-10-20")); found {
		t.Errorf("matched %d, which is three days off or already linked", e.ID)
	}
	if e, found, _ := s.MatchExpense(ctx, me, 5000, day("2026-10-18")); !found || e.ID != far {
		t.Errorf("two days off: %d, %v", e.ID, found)
	}
	if _, found, _ := s.MatchExpense(ctx, me, 5001, day("2026-10-16")); found {
		t.Error("a different amount matched")
	}
}

func TestAbsorbBankExpense(t *testing.T) {
	s := open(t)
	imported, err := s.AddBankExpense(ctx, BankTransaction{PluggyID: "t1", UserID: me, AccountID: "acc",
		SpentOn: day("2026-10-19"), TotalCents: 4230, Description: "DROGARIA"}, bankExpense(me, "2026-10-19", 4230, "outros"))
	if err != nil {
		t.Fatal(err)
	}

	cash := add(t, s, Expense{UserID: me, SpentOn: day("2026-10-19"), TotalCents: 4230, Category: "farmacia",
		Source: SourcePhoto, Status: Confirmed, PaymentMethod: "dinheiro"})
	if _, found, err := s.AbsorbBankExpense(ctx, me, cash); err != nil || found {
		t.Errorf("a cash receipt absorbed the bank's: %v, %v", found, err)
	}

	receipt := add(t, s, Expense{UserID: me, SpentOn: day("2026-10-20"), TotalCents: 4230, Category: "farmacia",
		Source: SourcePhoto, Status: Confirmed})
	absorbed, found, err := s.AbsorbBankExpense(ctx, me, receipt)
	if err != nil || !found || absorbed.ID != imported {
		t.Fatalf("absorbed %d, %v, %v", absorbed.ID, found, err)
	}
	if e, _ := s.Expense(ctx, imported); e.Status != Discarded {
		t.Errorf("the imported expense is %s", e.Status)
	}
	if tx, err := s.BankTransactionFor(ctx, receipt); err != nil || tx.PluggyID != "t1" {
		t.Errorf("the transaction now points at: %+v, %v", tx, err)
	}
	if _, found, _ := s.AbsorbBankExpense(ctx, me, receipt); found {
		t.Error("absorbed twice")
	}
}
