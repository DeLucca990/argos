package telegram

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DeLucca990/argos/internal/cost"
	"github.com/DeLucca990/argos/internal/extract"
	"github.com/DeLucca990/argos/internal/receipt"
	"github.com/DeLucca990/argos/internal/store"
)

func answer(raw receipt.Raw, usd float64) extract.Result {
	body, _ := json.Marshal(raw)
	return extract.Result{Raw: raw, RawJSON: string(body), Model: "claude-opus-5-5", Spend: cost.Spend{USD: usd}}
}

func TestCorrectAppliesWhatTheModelChanged(t *testing.T) {
	fx := answering(pharmacy())
	corrected := pharmacy()
	corrected.Total, corrected.PaymentMethod, corrected.Category = "45.90", "credito", "saude"
	corrected.Items = nil
	fx.fixed = answer(corrected, 0.01)
	b, user := testBot(t, fx)
	b.read(ctx, user, incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto})

	r, updated := b.correct(ctx, user, 1, "foi 45,90 no crédito, e é saúde")
	if updated == nil {
		t.Fatalf("not corrected: %q", r.text)
	}
	for _, want := range []string{"total: R$ 42,30 → R$ 45,90", "pagamento: Débito → Crédito", "categoria: Farmácia → Saúde", "itens corrigidos"} {
		if !strings.Contains(r.text, want) {
			t.Errorf("the reply lacks %q:\n%s", want, r.text)
		}
	}
	if strings.Contains(r.text, "estabelecimento") || r.expenseID != 1 {
		t.Errorf("a field that did not change was reported, or the reply is not a card: %q", r.text)
	}

	c := fx.corrections[0]
	if c.Text != "foi 45,90 no crédito, e é saúde" || c.Current.Total != "42.30" || c.Current.Merchant != "Drogaria Boa Saúde" ||
		c.Current.Items[0].UnitPrice != "9.90" || !c.Today.Equal(fixed) {
		t.Errorf("the model was shown %+v", c)
	}

	e, _ := b.store.Expense(ctx, 1)
	if e.TotalCents != 4590 || e.PaymentMethod != "credito" || e.Category != "saude" || e.Status != store.Confirmed || len(e.Items) != 0 {
		t.Errorf("the expense: %+v", e)
	}
	card := b.card(ctx, *updated, false)
	if !strings.Contains(card.text, "R$ 45,90") || !strings.Contains(card.text, "US$ 0,0500") || strings.Contains(card.text, "soma dos itens") {
		t.Errorf("the card after the correction:\n%s", card.text)
	}
}

func TestCorrectionsThatChangeNothing(t *testing.T) {
	fx := answering(pharmacy())
	same := pharmacy()
	same.Notes = "não entendi o que mudar"
	fx.fixed = answer(same, 0.01)
	b, user := testBot(t, fx)
	b.read(ctx, user, incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto})

	if r, _ := b.correct(ctx, user, 1, "hmm"); !strings.Contains(r.text, "Nada mudou.") || !strings.Contains(r.text, "📝 não entendi") {
		t.Errorf("nothing to apply: %q", r.text)
	}

	b.store.Discard(ctx, user.ID, 1)
	if r, updated := b.correct(ctx, user, 1, "foi 10"); updated != nil || !strings.Contains(r.text, "descartado") || len(fx.corrections) != 1 {
		t.Errorf("a discarded expense: %q, %d calls", r.text, len(fx.corrections))
	}
	if r, _ := b.correct(ctx, user, 99, "foi 10"); !strings.Contains(r.text, "Não encontrei") {
		t.Errorf("an unknown expense: %q", r.text)
	}
}

func TestTheDailyBudget(t *testing.T) {
	fx := answering(pharmacy())
	fx.fixed = answer(pharmacy(), 0.01)
	b, user := testBot(t, fx)
	b.cfg.DailyBudget = 0.03
	b.now = time.Now

	b.read(ctx, user, incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto})
	r := b.read(ctx, user, incoming{text: "almoço 30", source: store.SourceText})
	if fx.calls != 1 || !strings.Contains(r.text, "teto diário de US$ 0,03") || !strings.Contains(r.text, "US$ 0,0400 hoje") {
		t.Errorf("over the budget: %d calls, %q", fx.calls, r.text)
	}
	if r, _ := b.correct(ctx, user, 1, "foi 10"); len(fx.corrections) != 0 || !strings.Contains(r.text, "teto diário") {
		t.Errorf("a correction over the budget: %q", r.text)
	}
	if text, _ := b.costText(ctx, time.Now()); !strings.Contains(text, "Teto diário: US$ 0,03 — 133% usado hoje") {
		t.Errorf("/cost:\n%s", text)
	}
}

func TestRawFromRoundTrips(t *testing.T) {
	b, user := testBot(t, answering(pharmacy()))
	b.read(ctx, user, incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto})
	e, _ := b.store.Expense(ctx, 1)

	r, err := receipt.Parse(rawFrom(e), fixed)
	if err != nil {
		t.Fatal(err)
	}
	cats, _ := b.categories(ctx, user.ID)
	back := expenseFrom(r, cats, user.ID, e.Source, e.AttachmentID)
	if back.TotalCents != e.TotalCents || !back.SpentOn.Equal(e.SpentOn) || back.SpentTime != e.SpentTime ||
		back.Merchant != e.Merchant || back.PaymentMethod != e.PaymentMethod || back.Category != e.Category ||
		len(back.Items) != 1 || back.Items[0] != e.Items[0] {
		t.Errorf("round trip:\n%+v\n%+v", e, back)
	}
}

func TestAccountAndCardOnTheCard(t *testing.T) {
	raw := pharmacy()
	raw.PaymentAccount, raw.CardLast4 = "Nubank", "final 1234"
	fx := answering(raw)
	moved := raw
	moved.PaymentAccount, moved.CardLast4 = "Inter", "9876"
	fx.fixed = answer(moved, 0.01)
	b, user := testBot(t, fx)

	card := b.read(ctx, user, incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto})
	if !strings.Contains(card.text, "💳 Débito · Nubank ···1234") {
		t.Errorf("the card:\n%s", card.text)
	}
	saved, _ := b.store.Expense(ctx, 1)
	if c := rawFrom(saved); c.PaymentAccount != "Nubank" || c.CardLast4 != "1234" {
		t.Errorf("a correction is shown %+v", c)
	}

	r, _ := b.correct(ctx, user, 1, "foi no cartão do Inter, final 9876")
	if !strings.Contains(r.text, "pagamento: Débito · Nubank ···1234 → Débito · Inter ···9876") {
		t.Errorf("the correction:\n%s", r.text)
	}

	_, data, _, _ := b.exportCSV(ctx, user.ID, "outubro", fixed)
	if !strings.Contains(string(data), ";Débito;Inter;9876;42,30;") {
		t.Errorf("the CSV:\n%s", data)
	}

	e, _ := b.store.Expense(ctx, 1)
	e.PaymentMethod, e.CardLast4 = "", ""
	if got := paidWith(e); got != "Inter" {
		t.Errorf("an account alone: %q", got)
	}
	e.Account = ""
	if got := paidWith(e); got != "" {
		t.Errorf("nothing known: %q", got)
	}
}
