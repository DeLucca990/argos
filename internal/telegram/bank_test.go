package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DeLucca990/argos-bot/internal/extract"
	"github.com/DeLucca990/argos-bot/internal/pluggy"
	"github.com/DeLucca990/argos-bot/internal/store"
)

const testItem = "6f1b8a3e-2c4d-4e5f-8a9b-0c1d2e3f4a5b"

type fakeBank struct {
	item     pluggy.Item
	itemErr  error
	accounts []pluggy.Account
	txs      map[string][]pluggy.Transaction
	listed   int
	froms    []time.Time
}

func (f *fakeBank) Item(_ context.Context, id string) (pluggy.Item, error) {
	if f.itemErr != nil {
		return pluggy.Item{}, f.itemErr
	}
	it := f.item
	it.ID = id
	return it, nil
}

func (f *fakeBank) Accounts(context.Context, string) ([]pluggy.Account, error) {
	f.listed++
	return f.accounts, nil
}

func (f *fakeBank) Transactions(_ context.Context, accountID string, from time.Time) ([]pluggy.Transaction, error) {
	f.froms = append(f.froms, from)
	return f.txs[accountID], nil
}

func bareDate(s string) time.Time {
	d, _ := time.Parse("2006-01-02", s)
	return d
}

func spent(id, on, amount, description string) pluggy.Transaction {
	return pluggy.Transaction{ID: id, Date: bareDate(on), Amount: json.Number(amount), CurrencyCode: "BRL",
		Type: "DEBIT", Status: "POSTED", Description: description}
}

func pix(id, on, amount, to, toDoc, fromDoc string) pluggy.Transaction {
	t := spent(id, on, amount, "Pix enviado "+to)
	t.OperationType = "PIX"
	t.PaymentData = &pluggy.PaymentData{PaymentMethod: "PIX",
		Payer:    &pluggy.Participant{Name: "Ana", DocumentNumber: &pluggy.Document{Type: "CPF", Value: fromDoc}},
		Receiver: &pluggy.Participant{Name: to, DocumentNumber: &pluggy.Document{Type: "CPF", Value: toDoc}}}
	return t
}

func bankBot(t *testing.T, fx *fakeExtractor) (*Bot, store.User, *fakeBank) {
	t.Helper()
	b, user := testBot(t, fx)
	updated := time.Date(2026, 10, 20, 9, 12, 30, 123e6, time.UTC)
	fb := &fakeBank{
		item: pluggy.Item{Connector: pluggy.Connector{Name: "MeuPluggy"}, Status: "UPDATED", LastUpdatedAt: &updated},
		accounts: []pluggy.Account{
			{ID: "card", Type: "CREDIT", Subtype: "CREDIT_CARD", Name: "Nubank Mastercard", Number: "9876", CurrencyCode: "BRL"},
			{ID: "checking", Type: "BANK", Subtype: "CHECKING_ACCOUNT", Name: "Nubank", TaxNumber: "123.456.789-09", CurrencyCode: "BRL"},
		},
		txs: map[string][]pluggy.Transaction{},
	}
	b.bank = fb
	return b, user, fb
}

func notFatura(l extract.BankLine) extract.Filed {
	if strings.Contains(l.Description, "fatura") {
		return extract.Filed{Spending: false, Category: "outros"}
	}
	return extract.Filed{Spending: true, Merchant: "iFood", Description: "Jantar", Category: "delivery"}
}

func TestConnectImportsTheMonth(t *testing.T) {
	fx := answering(pharmacy())
	b, user, fb := bankBot(t, fx)
	b.read(ctx, user, incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto})
	fx.file = notFatura

	purchase := spent("t-ifood", "2026-10-19", "35.9", "IFD*IFOOD")
	purchase.CreditCardMetadata = &pluggy.CardMetadata{CardNumber: "•••• 5678"}
	future := spent("t-future", "2026-11-19", "100", "LOJA X 2/3")
	future.CreditCardMetadata = &pluggy.CardMetadata{InstallmentNumber: 2, TotalInstallments: 3}
	fatura := spent("t-paid", "2026-10-10", "-1500", "Pagamento recebido")
	fatura.Type, fatura.OperationType = "CREDIT", "PAGAMENTO_FATURA"
	salary := spent("t-salary", "2026-10-05", "5000", "Salário")
	salary.Type = "CREDIT"
	fb.txs["card"] = []pluggy.Transaction{purchase, spent("t-drug", "2026-10-19", "42.3", "DROGARIA BOA SAUDE"), future, fatura}
	fb.txs["checking"] = []pluggy.Transaction{
		pix("t-maria", "2026-10-18", "-120", "Maria Souza", "529.982.247-25", "123.456.789-09"),
		pix("t-mine", "2026-10-17", "-500", "Ana", "123.456.789-09", "123.456.789-09"),
		spent("t-bill", "2026-10-10", "-1500", "Pagamento de fatura"),
		salary,
	}

	r, s := b.connect(ctx, user, testItem)
	for _, want := range []string{"Nubank Mastercard</b> conectado", "Contas: Nubank Mastercard ···9876, Nubank",
		"2 gastos novos</b>, R$ 155,90", "1 já estava registrado pelos", "1 ficou de fora"} {
		if !strings.Contains(r.text, want) {
			t.Errorf("reply lacks %q:\n%s", want, r.text)
		}
	}
	if len(s.added) != 2 || s.matched != 1 || s.ignored != 1 || s.waiting != 0 {
		t.Fatalf("sync: %d added, %d matched, %d ignored, %d waiting", len(s.added), s.matched, s.ignored, s.waiting)
	}
	if len(fx.statements) != 1 || len(fx.statements[0].Lines) != 3 {
		t.Fatalf("the model was asked %d time(s): %+v", len(fx.statements), fx.statements)
	}
	if want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local); !fb.froms[0].Equal(want) {
		t.Errorf("imported from %v, want the 1st of the month", fb.froms[0])
	}

	maria, ifood := s.added[0], s.added[1]
	if maria.Source != store.SourceBank || maria.PaymentMethod != "pix" || maria.MerchantTaxID != "52998224725" ||
		maria.TotalCents != 12000 || maria.Account != "Nubank" || maria.CardLast4 != "" || maria.SpentTime != "" {
		t.Errorf("the Pix: %+v", maria)
	}
	if ifood.PaymentMethod != "credito" || ifood.CardLast4 != "5678" || ifood.Category != "delivery" ||
		ifood.Merchant != "iFood" || ifood.Account != "Nubank Mastercard" || ifood.TotalCents != 3590 {
		t.Errorf("the card purchase: %+v", ifood)
	}
	line := fx.statements[0].Lines[0]
	if line.Account != "Nubank Mastercard, cartão de crédito" || line.Amount != "35.90" || line.Date != "2026-10-19" {
		t.Errorf("what the model was shown: %+v", line)
	}

	known, _ := b.store.KnownBankTransactions(ctx, []string{"t-ifood", "t-drug", "t-maria", "t-bill", "t-mine", "t-future", "t-salary"})
	for id, want := range map[string]bool{"t-ifood": true, "t-drug": true, "t-maria": true, "t-bill": true,
		"t-mine": false, "t-future": false, "t-salary": false} {
		if known[id] != want {
			t.Errorf("%s recorded: %v", id, known[id])
		}
	}
	if calls, _ := b.store.Calls(ctx, time.Time{}); calls[store.KindBank] != 1 {
		t.Errorf("model calls: %v", calls)
	}
	card := b.card(ctx, ifood, false).text
	if !strings.HasPrefix(card, "🏦 <b>iFood</b>") || !strings.Contains(card, "No extrato: <i>IFD*IFOOD</i>") {
		t.Errorf("card:\n%s", card)
	}

	again, _ := b.connect(ctx, user, testItem)
	if !strings.Contains(again.text, "já está conectado") {
		t.Errorf("connecting twice: %s", again.text)
	}
}

func TestLaterSyncs(t *testing.T) {
	fx := answering(pharmacy())
	fx.file = notFatura
	b, user, fb := bankBot(t, fx)
	fb.txs["card"] = []pluggy.Transaction{spent("t1", "2026-10-15", "35.9", "IFD*IFOOD")}
	fb.txs["checking"] = []pluggy.Transaction{spent("b1", "2026-10-10", "-1500", "Pagamento de fatura")}
	_, s := b.connect(ctx, user, testItem)
	if len(s.added) != 1 {
		t.Fatalf("first import: %d", len(s.added))
	}
	if _, err := b.store.SetCategory(ctx, user.ID, s.added[0].ID, "restaurante"); err != nil {
		t.Fatal(err)
	}
	item := s.item

	s, err := b.syncItem(ctx, fb, item, false)
	if err != nil || fb.listed != 1 || len(s.added) != 0 {
		t.Errorf("nothing new at Pluggy: listed accounts %d times, %d added, %v", fb.listed, len(s.added), err)
	}

	later := fb.item.LastUpdatedAt.Add(24 * time.Hour)
	fb.item.LastUpdatedAt = &later
	fb.txs["card"] = []pluggy.Transaction{
		spent("t1-recreated", "2026-10-16", "35.9", "IFOOD"),
		spent("t2", "2026-10-20", "28", "IFD*IFOOD"),
	}
	fb.txs["checking"] = []pluggy.Transaction{spent("b1", "2026-10-10", "-1500", "Pagamento de fatura"),
		spent("b2", "2026-10-20", "-900", "Pagamento de fatura")}
	asked := len(fx.statements)
	s, err = b.syncItem(ctx, fb, item, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.statements) != asked {
		t.Errorf("the model was asked about transactions it had filed before: %+v", fx.statements[asked:])
	}
	if len(s.added) != 1 || s.ignored != 1 || s.added[0].Category != "restaurante" || s.added[0].TotalCents != 2800 {
		t.Fatalf("second import: %+v, %d ignored", s.added, s.ignored)
	}
	if got, _ := b.store.Recent(ctx, user.ID, 10); len(got) != 2 {
		t.Errorf("the recreated transaction made an expense of its own: %d expenses", len(got))
	}

	b.now = func() time.Time { return fixed.AddDate(0, 2, 0) }
	s.item.SyncedAt = fixed.AddDate(0, 1, 15)
	if _, err := b.syncItem(ctx, fb, s.item, true); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 5, 0, 0, 0, 0, time.Local); !fb.froms[len(fb.froms)-1].Equal(want) {
		t.Errorf("a sync long after the first reads from %v, want 30 days before the last sync, %v", fb.froms[len(fb.froms)-1], want)
	}
}

func TestReceiptAfterImportReplacesIt(t *testing.T) {
	fx := answering(pharmacy())
	b, user, fb := bankBot(t, fx)
	fb.txs["checking"] = []pluggy.Transaction{spent("t-drug", "2026-10-19", "-42.3", "DROGARIA BOA SAUDE")}
	_, s := b.connect(ctx, user, testItem)
	if len(s.added) != 1 {
		t.Fatalf("imported %d", len(s.added))
	}

	r := b.read(ctx, user, incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto})
	if old, _ := b.store.Expense(ctx, s.added[0].ID); old.Status != store.Discarded {
		t.Errorf("the imported expense is %s", old.Status)
	}
	if !strings.Contains(r.text, "No extrato: <i>DROGARIA BOA SAUDE</i>") || !strings.HasPrefix(r.text, "🧾") {
		t.Errorf("card:\n%s", r.text)
	}
	if got, _ := b.store.Recent(ctx, user.ID, 10); len(got) != 1 || got[0].Source != store.SourcePhoto {
		t.Errorf("expenses: %+v", got)
	}
}

func TestFilingWaitsForTheModel(t *testing.T) {
	fx := answering(pharmacy())
	fx.fileErr = context.DeadlineExceeded
	b, user, fb := bankBot(t, fx)
	fb.txs["card"] = []pluggy.Transaction{spent("t1", "2026-10-15", "35.9", "IFD*IFOOD")}
	r, s := b.connect(ctx, user, testItem)
	if s.waiting != 1 || len(s.added) != 0 || !s.item.ImportedUpTo.IsZero() || !strings.Contains(r.text, "1 espera a leitura") {
		t.Fatalf("waiting %d, added %d, imported up to %v:\n%s", s.waiting, len(s.added), s.item.ImportedUpTo, r.text)
	}

	fx.fileErr = nil
	s, err := b.syncItem(ctx, fb, s.item, false)
	if err != nil || len(s.added) != 1 || s.item.ImportedUpTo.IsZero() {
		t.Errorf("once the model answers: %d added, imported up to %v, %v", len(s.added), s.item.ImportedUpTo, err)
	}
}

func TestItemTrouble(t *testing.T) {
	b, user, fb := bankBot(t, answering(pharmacy()))
	_, s := b.connect(ctx, user, testItem)
	before := s.item

	fb.item.Status, fb.item.Error = "LOGIN_ERROR", &pluggy.ItemError{Code: "USER_AUTHORIZATION_EXPIRED", Message: "consent expired"}
	s, err := b.syncItem(ctx, fb, before, false)
	if err != nil {
		t.Fatal(err)
	}
	text, ok := troubleText(before, s.item)
	if !ok || !strings.Contains(text, "a autorização venceu") || !strings.Contains(text, "consent expired") {
		t.Errorf("trouble: %v %q", ok, text)
	}
	if _, again := troubleText(s.item, s.item); again {
		t.Error("the same trouble was told twice")
	}

	menu, err := b.bankMenu(ctx, user)
	if err != nil || !strings.Contains(menu.text, "⚠️ a autorização venceu") || len(menu.keyboard.InlineKeyboard) != 1 {
		t.Errorf("menu: %+v, %v", menu, err)
	}

	fb.itemErr = pluggy.ErrNotFound
	if s, err = b.syncItem(ctx, fb, s.item, true); err != nil || s.item.Status != itemGone {
		t.Errorf("a deleted item: %q, %v", s.item.Status, err)
	}
}

func TestConnectRefusals(t *testing.T) {
	b, user, fb := bankBot(t, answering(pharmacy()))
	for arg, want := range map[string]string{
		"":           "meu.pluggy.ai",
		"not-an-id":  "36 caracteres",
		testItem[1:]: "36 caracteres",
	} {
		if r, _ := b.connect(ctx, user, arg); !strings.Contains(r.text, want) {
			t.Errorf("%q: %s", arg, r.text)
		}
	}
	fb.itemErr = pluggy.ErrNotFound
	if r, _ := b.connect(ctx, user, testItem); !strings.Contains(r.text, "Não encontrei esse item") {
		t.Errorf("unknown item: %s", r.text)
	}

	b.bank = nil
	if r, _ := b.connect(ctx, user, testItem); !strings.Contains(r.text, "PLUGGY_CLIENT_ID_8683784233") {
		t.Errorf("off: %s", r.text)
	}
	if help := b.helpText(user.TelegramID); strings.Contains(help, "/conectar") || strings.Contains(help, "/bancos") {
		t.Errorf("bank commands offered with bank imports off:\n%s", help)
	}
}

func TestEachPersonTheirOwnApplication(t *testing.T) {
	b, ana, shared := bankBot(t, answering(pharmacy()))
	bia, err := b.store.SaveUser(ctx, store.User{TelegramID: 5551234, FirstName: "Bia"})
	if err != nil {
		t.Fatal(err)
	}
	carla, err := b.store.SaveUser(ctx, store.User{TelegramID: 7770001, FirstName: "Carla"})
	if err != nil {
		t.Fatal(err)
	}
	b.cfg.AllowedUsers = map[int64]bool{ana.TelegramID: true, bia.TelegramID: true, carla.TelegramID: true}
	own := &fakeBank{item: shared.item, accounts: shared.accounts, txs: map[string][]pluggy.Transaction{
		"card": {spent("bia-1", "2026-10-19", "50", "PADARIA")},
	}}
	b.ownBanks = map[int64]bankAPI{bia.TelegramID: own}

	_, s := b.connect(ctx, bia, testItem)
	if len(s.added) != 1 || own.listed != 1 || shared.listed != 0 || s.added[0].UserID != bia.ID {
		t.Fatalf("Bia's item went through the wrong application: own %d, shared %d, %+v", own.listed, shared.listed, s.added)
	}
	if r, _ := b.connect(ctx, ana, testItem); !strings.Contains(r.text, "já está conectado por outra pessoa") {
		t.Errorf("Ana connecting Bia's item: %s", r.text)
	}

	shared.itemErr = pluggy.ErrNotFound
	r, _ := b.connect(ctx, carla, "0f1b8a3e-2c4d-4e5f-8a9b-0c1d2e3f4a5b")
	if !strings.Contains(r.text, "Não encontrei") || !strings.Contains(r.text, "PLUGGY_CLIENT_ID_7770001") {
		t.Errorf("Carla, on the shared application: %s", r.text)
	}
	if help := b.connectHelp(bia.TelegramID); strings.Contains(help, "PLUGGY_CLIENT_ID_") {
		t.Errorf("Bia has her own application and is told to get one:\n%s", help)
	}

	b.bank = nil
	if menu, _ := b.bankMenu(ctx, carla); !strings.Contains(menu.text, "não está ligada para você") {
		t.Errorf("Carla, with no application: %s", menu.text)
	}
	if menu, _ := b.bankMenu(ctx, bia); !strings.Contains(menu.text, "Contas conectadas") {
		t.Errorf("Bia's menu: %s", menu.text)
	}
	if !strings.Contains(b.helpText(bia.TelegramID), "/conectar") || strings.Contains(b.helpText(carla.TelegramID), "/conectar") {
		t.Error("the help offers the bank to the wrong person")
	}
}

func TestReconnectingImportsNothingTwice(t *testing.T) {
	fx := answering(pharmacy())
	fx.file = notFatura
	b, user, fb := bankBot(t, fx)
	withProvider := func(t pluggy.Transaction, provider string) pluggy.Transaction {
		t.ProviderID = provider
		return t
	}
	fb.txs["card"] = []pluggy.Transaction{
		withProvider(spent("a-1", "2026-10-15", "35.9", "IFD*IFOOD"), "of-ifood"),
		withProvider(spent("a-2", "2026-10-16", "60", "ACADEMIA"), "of-gym"),
	}
	fb.txs["checking"] = []pluggy.Transaction{withProvider(spent("a-3", "2026-10-10", "-1500", "Pagamento de fatura"), "of-bill")}
	_, first := b.connect(ctx, user, testItem)
	if len(first.added) != 2 || first.ignored != 1 {
		t.Fatalf("first connection: %d added, %d ignored", len(first.added), first.ignored)
	}
	gym := first.added[1]
	if _, err := b.store.Discard(ctx, user.ID, gym.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.store.UnlinkBankItem(ctx, user.ID, first.item.ID); err != nil {
		t.Fatal(err)
	}

	fb.accounts = []pluggy.Account{
		{ID: "card-2", Type: "CREDIT", Name: "Nubank Mastercard", Number: "9876", CurrencyCode: "BRL"},
		{ID: "checking-2", Type: "BANK", Name: "Nubank", CurrencyCode: "BRL"},
	}
	fb.txs = map[string][]pluggy.Transaction{
		"card-2": {
			withProvider(spent("b-1", "2026-10-15", "35.9", "IFD*IFOOD"), "of-ifood"),
			withProvider(spent("b-2", "2026-10-16", "60", "ACADEMIA"), "of-gym"),
			withProvider(spent("b-4", "2026-10-19", "35.9", "IFD*IFOOD"), "of-ifood-again"),
		},
		"checking-2": {withProvider(spent("b-3", "2026-10-10", "-1500", "Pagamento de fatura"), "of-bill")},
	}
	asked := len(fx.statements)
	r, again := b.connect(ctx, user, "0f1b8a3e-2c4d-4e5f-8a9b-0c1d2e3f4a5b")
	if len(again.added) != 1 || again.carried != 2 || again.matched != 0 || again.ignored != 0 || len(fx.statements) != asked {
		t.Fatalf("second connection: %d added, %d carried, %d matched, %d ignored, model asked %d time(s)",
			len(again.added), again.carried, again.matched, again.ignored, len(fx.statements)-asked)
	}
	if !strings.Contains(r.text, "2 já tinham vindo por outra conexão") {
		t.Errorf("reply:\n%s", r.text)
	}
	if e, _ := b.store.Expense(ctx, gym.ID); e.Status != store.Discarded {
		t.Errorf("the discarded expense came back: %s", e.Status)
	}
	if tx, err := b.store.BankTransactionFor(ctx, first.added[0].ID); err != nil || tx.PluggyID != "b-1" {
		t.Errorf("the iFood expense's newest transaction: %+v, %v", tx, err)
	}
	if got, _ := b.store.Recent(ctx, user.ID, 10); len(got) != 2 {
		t.Errorf("expenses after connecting again: %d, want the first iFood and the new one", len(got))
	}
}

func TestItemName(t *testing.T) {
	accounts := []pluggy.Account{{Type: "BANK", Name: "Conta Corrente"}, {Type: "CREDIT", Name: "Nubank Mastercard"}}
	for connector, want := range map[string]string{
		"Pluggy Bank": "Pluggy Bank", "Nubank": "Nubank", "MeuPluggy": "Conta Corrente", "Meu Pluggy": "Conta Corrente",
	} {
		if got := itemName(connector, accounts); got != want {
			t.Errorf("%q: %q, want %q", connector, got, want)
		}
	}
}

func TestEveryText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Minute: "a cada minuto", 30 * time.Minute: "a cada 30 minutos", time.Hour: "a cada hora",
		6 * time.Hour: "a cada 6 horas", 90 * time.Minute: "a cada 1h30", 24 * time.Hour: "uma vez por dia",
		48 * time.Hour: "a cada 2 dias", 36 * time.Hour: "a cada 36 horas",
	} {
		if got := everyText(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

func TestWhenOf(t *testing.T) {
	on, clock := whenOf(bareDate("2026-10-19"))
	if !on.Equal(time.Date(2026, 10, 19, 0, 0, 0, 0, time.Local)) || clock != "" {
		t.Errorf("a bare date: %v %q", on, clock)
	}
	moment := time.Date(2026, 10, 19, 15, 4, 0, 0, time.Local)
	on, clock = whenOf(moment.UTC())
	if !on.Equal(time.Date(2026, 10, 19, 0, 0, 0, 0, time.Local)) || clock != "15:04" {
		t.Errorf("a moment: %v %q", on, clock)
	}
}
