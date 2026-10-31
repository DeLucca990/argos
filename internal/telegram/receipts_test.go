package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DeLucca990/argos-bot/internal/cost"
	"github.com/DeLucca990/argos-bot/internal/extract"
	"github.com/DeLucca990/argos-bot/internal/receipt"
	"github.com/DeLucca990/argos-bot/internal/store"
)

var (
	ctx   = context.Background()
	fixed = time.Date(2026, 10, 20, 15, 0, 0, 0, time.Local)
	photo = []byte("\xff\xd8\xff\xe0 not really a jpeg, the fake extractor never decodes it")
)

type fakeExtractor struct {
	res         extract.Result
	err         error
	calls       int
	last        extract.Input
	fixed       extract.Result
	corrections []extract.Correction
	statements  []extract.Statement
	file        func(extract.BankLine) extract.Filed
	fileErr     error
}

func (f *fakeExtractor) Classify(_ context.Context, s extract.Statement) ([]extract.Filed, extract.Result, error) {
	f.statements = append(f.statements, s)
	res := extract.Result{Model: "claude-opus-5-5", RawJSON: "{}", Spend: cost.Spend{USD: 0.01}}
	if f.fileErr != nil {
		return nil, res, f.fileErr
	}
	filed := make([]extract.Filed, len(s.Lines))
	for i, l := range s.Lines {
		filed[i] = extract.Filed{Spending: true, Merchant: nonEmpty(l.PaidTo, l.Description), Description: "Compra", Category: "outros"}
		if f.file != nil {
			filed[i] = f.file(l)
		}
	}
	return filed, res, nil
}

func (f *fakeExtractor) Correct(_ context.Context, c extract.Correction) (extract.Result, error) {
	f.corrections = append(f.corrections, c)
	return f.fixed, nil
}

func (f *fakeExtractor) Extract(_ context.Context, in extract.Input) (extract.Result, error) {
	f.calls++
	f.last = in
	return f.res, f.err
}

func answering(raw receipt.Raw) *fakeExtractor {
	body, _ := json.Marshal(raw)
	return &fakeExtractor{res: extract.Result{
		Raw: raw, RawJSON: string(body), Model: "claude-opus-5-5",
		Spend: cost.Spend{Input: 5000, Output: 1000, USD: 0.04, Models: []string{"claude-opus-5-5"}},
	}}
}

func pharmacy() receipt.Raw {
	return receipt.Raw{
		IsReceipt: true, Merchant: "Drogaria Boa Saúde", MerchantTaxID: "11222333000181", Date: "2026-10-19",
		Time: "10:15", Total: "42.30", Currency: "BRL", PaymentMethod: "debito", Category: "farmacia",
		Description: "Remédios", Items: []receipt.RawItem{{Description: "Dipirona 500mg", Quantity: "2", UnitPrice: "9.90", Total: "19.80"}},
		Notes: "o último dígito do total está borrado",
	}
}

func testBot(t *testing.T, fx *fakeExtractor) (*Bot, store.User) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "argos.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	b := &Bot{cfg: Config{DataDir: dir, Model: "claude-opus-5-5"}, store: st, extractor: fx, now: func() time.Time { return fixed },
		slots: make(chan struct{}, maxConcurrency)}
	os.MkdirAll(filepath.Join(dir, receiptsDir), 0o750)
	user, err := st.SaveUser(ctx, store.User{TelegramID: 8683784233, FirstName: "Ana"})
	if err != nil {
		t.Fatal(err)
	}
	return b, user
}

func TestReadRecordsAPendingExpense(t *testing.T) {
	fx := answering(pharmacy())
	b, user := testBot(t, fx)

	r := b.read(ctx, user, incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto, text: "remédio da vó"})

	for _, want := range []string{"Drogaria Boa Saúde", "CNPJ 11.222.333/0001-81", "19/10/2026 às 10:15", "Débito",
		"💊 Farmácia", "R$ 42,30", "Dipirona 500mg — 2 × R$ 9,90 = R$ 19,80", "⚠️ A soma dos itens (R$ 19,80)",
		"📝 o último dígito", "✅ <b>Salvo.</b>", "<i>leitura (claude-opus-5-5): ≈ US$ 0,0400 · 6.000 tokens: 5.000 entrada, 1.000 saída</i>"} {
		if !strings.Contains(r.text, want) {
			t.Errorf("the card lacks %q:\n%s", want, r.text)
		}
	}
	if r.keyboard == nil || len(r.keyboard.InlineKeyboard[0]) != 2 ||
		r.keyboard.InlineKeyboard[0][0].CallbackData != "ex:1:cat" || r.keyboard.InlineKeyboard[0][1].CallbackData != "ex:1:no" {
		t.Errorf("a saved expense offers its category and discarding it: %+v", r.keyboard)
	}
	if fx.last.Text != "remédio da vó" || !fx.last.Today.Equal(fixed) {
		t.Errorf("the extractor got %+v", fx.last)
	}

	e, err := b.store.Expense(ctx, 1)
	if err != nil || e.Status != store.Confirmed || e.ConfirmedAt.IsZero() || e.UserID != user.ID || e.TotalCents != 4230 || e.Category != "farmacia" ||
		e.Source != store.SourcePhoto || e.AttachmentID == 0 || len(e.Items) != 1 {
		t.Fatalf("the expense: %+v, %v", e, err)
	}
	sum := sha256.Sum256(photo)
	if saved, err := os.ReadFile(filepath.Join(b.cfg.DataDir, "receipts", hex.EncodeToString(sum[:])+".jpg")); err != nil || string(saved) != string(photo) {
		t.Errorf("the file on disk: %v", err)
	}
	if x, err := b.store.ExtractionFor(ctx, e.ID); err != nil || x.USD != 0.04 || x.InputTokens != 5000 || x.Failed {
		t.Errorf("the extraction: %+v, %v", x, err)
	}
}

func TestTheSameFileTwiceSkipsTheModel(t *testing.T) {
	fx := answering(pharmacy())
	b, user := testBot(t, fx)
	in := incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto}

	b.read(ctx, user, in)
	r := b.read(ctx, user, in)
	if fx.calls != 1 || !strings.Contains(r.text, "já foi registrado") || !strings.Contains(r.text, "salvo") {
		t.Errorf("%d calls, %q", fx.calls, r.text)
	}

	b.store.Discard(ctx, user.ID, 1)
	if r := b.read(ctx, user, in); fx.calls != 2 || r.keyboard == nil {
		t.Errorf("a file whose expense was discarded should be read again: %d calls, %q", fx.calls, r.text)
	}
}

func TestReadsThatRecordNoExpenseStillRecordTheirCost(t *testing.T) {
	notReceipt := answering(receipt.Raw{IsReceipt: false, NotReceiptReason: "É a foto de um gato."})
	noTotal := answering(receipt.Raw{IsReceipt: true, Merchant: "Bar", Total: ""})
	failed := &fakeExtractor{err: errors.New("claude: 529 overloaded"), res: extract.Result{Spend: cost.Spend{USD: 0.01}}}

	for name, c := range map[string]struct {
		fx   *fakeExtractor
		want string
	}{
		"not a receipt": {notReceipt, "não parece um comprovante: É a foto de um gato."},
		"no total":      {noTotal, "Não encontrei o valor total"},
		"failed":        {failed, "Não consegui ler: o serviço de leitura falhou"},
	} {
		b, user := testBot(t, c.fx)
		r := b.read(ctx, user, incoming{text: "bar 12", source: store.SourceText})
		if !strings.Contains(r.text, c.want) || r.keyboard != nil {
			t.Errorf("%s: %q", name, r.text)
		}
		if usd, n, _ := b.store.Spent(ctx, time.Unix(0, 0)); n != 1 || usd != c.fx.res.Spend.USD {
			t.Errorf("%s: %d extractions, $%f", name, n, usd)
		}
		if _, err := b.store.Expense(ctx, 1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: an expense was recorded", name)
		}
	}
}

func TestTypedPurchases(t *testing.T) {
	raw := receipt.Raw{IsReceipt: true, Merchant: "", Date: "2026-10-19", Total: "35.90", PaymentMethod: "pix",
		Category: "cassino", Description: "Almoço"}
	fx := answering(raw)
	b, user := testBot(t, fx)

	r := b.read(ctx, user, incoming{text: "almoço 35,90 no pix ontem", source: store.SourceText})
	e, err := b.store.Expense(ctx, 1)
	if err != nil || e.Source != store.SourceText || e.AttachmentID != 0 || e.Category != "outros" {
		t.Fatalf("the expense: %+v, %v", e, err)
	}
	if fx.last.MIME != "" || fx.last.Data != nil || !strings.Contains(r.text, "🧾 <b>Almoço</b>") || !strings.Contains(r.text, "Outros") {
		t.Errorf("input %+v, card:\n%s", fx.last, r.text)
	}
}

func TestTheCardSurvivesAButtonPress(t *testing.T) {
	b, user := testBot(t, answering(pharmacy()))
	b.read(ctx, user, incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto})

	e, err := b.store.SetCategory(ctx, user.ID, 1, "saude")
	if err != nil {
		t.Fatal(err)
	}
	r := b.card(ctx, e, false)
	if !strings.Contains(r.text, "🩺 Saúde") || !strings.Contains(r.text, "⚠️ A soma dos itens") ||
		!strings.Contains(r.text, "📝 o último dígito") || !strings.Contains(r.text, "✅ <b>Salvo.</b>") {
		t.Errorf("after changing the category:\n%s", r.text)
	}

	picker := b.card(ctx, e, true).keyboard.InlineKeyboard
	if len(picker) != 9 || picker[0][0].Text != "✓ 🩺 Saúde" || picker[0][0].CallbackData != "ex:1:c:6" ||
		len(picker[8]) != 1 || picker[8][0].CallbackData != "ex:1:back" {
		t.Errorf("the category picker: %+v", picker)
	}

	discarded, err := b.store.Discard(ctx, user.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r := b.card(ctx, discarded, true); r.keyboard != nil || !strings.Contains(r.text, "Descartado") {
		t.Errorf("a discarded expense has no buttons: %+v", r.keyboard)
	}
}

func TestParseCallback(t *testing.T) {
	for data, want := range map[string]struct {
		id          int64
		action, arg string
		ok          bool
	}{
		"ex:12:ok":  {12, "ok", "", true},
		"ex:12:c:7": {12, "c", "7", true},
		"ex:0:ok":   {0, "", "", false},
		"ex:x:ok":   {0, "", "", false},
		"ap:12:ok":  {0, "", "", false},
		"ex:12":     {0, "", "", false},
	} {
		id, action, arg, ok := parseCallback(data)
		if id != want.id || action != want.action || arg != want.arg || ok != want.ok {
			t.Errorf("parseCallback(%q) = %d %q %q %v", data, id, action, arg, ok)
		}
	}
	if got := callbackData(123456789012, "c:123456789012"); len(got) > 64 {
		t.Errorf("%q is over Telegram's 64 bytes", got)
	}
}

func TestReceiptsNotKept(t *testing.T) {
	fx := answering(pharmacy())
	b, user := testBot(t, fx)
	dir := filepath.Join(b.cfg.DataDir, receiptsDir)
	os.WriteFile(filepath.Join(dir, "old.jpg"), []byte("kept before"), 0o600)
	b.store.AddAttachment(ctx, store.Attachment{SHA256: "old", Path: "receipts/old.jpg", MIME: "image/jpeg"})

	b.cfg.DeleteFiles = true
	if err := b.purgeFiles(ctx); err != nil {
		t.Fatal(err)
	}
	in := incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto}
	if r := b.read(ctx, user, in); r.expenseID != 1 {
		t.Fatalf("not read: %q", r.text)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("files left in %s: %v", dir, left)
	}
	e, _ := b.store.Expense(ctx, 1)
	if e.AttachmentID == 0 {
		t.Error("the expense lost its attachment row, which is what tells a resent file apart")
	}
	if r := b.read(ctx, user, in); fx.calls != 1 || !strings.Contains(r.text, "já foi registrado") {
		t.Errorf("the same file sent again: %d calls, %q", fx.calls, r.text)
	}
}
