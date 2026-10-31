package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DeLucca990/argos/internal/ask"
	"github.com/DeLucca990/argos/internal/cost"
	"github.com/DeLucca990/argos/internal/store"
)

type fakeAsker struct {
	ans   ask.Answer
	err   error
	asked []string
}

func (f *fakeAsker) Ask(_ context.Context, _ int64, q string, _ time.Time) (ask.Answer, error) {
	f.asked = append(f.asked, q)
	return f.ans, f.err
}

func TestIsQuestion(t *testing.T) {
	for text, want := range map[string]bool{
		"quanto gastei em 2026?":                                     true,
		"Quanto gastei com mercado em setembro":                      true,
		"qual foi meu maior gasto":                                   true,
		"o que eu mais comprei":                                      true,
		"me mostra os gastos de ontem":                               true,
		"compara setembro e outubro":                                 true,
		"Faça um resumo financeiro de todos os gastos até o momento": true,
		"faça um resumo dos gastos de 2026":                          true,
		"me dá o total de outubro de 2026":                           true,
		"gastos por categoria":                                       true,
		"obrigado":                                                   false,
		"almoço 35,90 no pix":                                        false,
		"uber 23,50 ontem":                                           false,
		"comi pizza 50":                                              false,
		"quantia 30":                                                 false,
		"gastei 30 no mercado":                                       false,
		"oi":                                                         false,
	} {
		if got := isQuestion(text); got != want {
			t.Errorf("isQuestion(%q) = %v", text, got)
		}
	}
}

func TestQuestion(t *testing.T) {
	b, user := testBot(t, answering(pharmacy()))
	b.now = time.Now
	fa := &fakeAsker{ans: ask.Answer{Text: "Em setembro: R$ 388,00 <no mercado>", Steps: 2,
		Spend: cost.Spend{Input: 4500, Output: 180, USD: 0.02, Models: []string{"claude-opus-5-5"}}}}
	b.asker = fa

	r := b.question(ctx, user, "quanto gastei em setembro?")
	if r.text != "Em setembro: R$ 388,00 &lt;no mercado&gt;\n\n<i>≈ US$ 0,0200 · 4.680 tokens: 4.500 entrada, 180 saída</i>" || r.keyboard != nil || r.expenseID != 0 {
		t.Errorf("reply: %q", r.text)
	}
	calls, _ := b.store.Calls(ctx, time.Unix(0, 0))
	if calls[store.KindQuestion] != 1 || len(calls) != 1 {
		t.Errorf("calls: %v", calls)
	}
	if text, _ := b.costText(ctx, time.Now()); !strings.Contains(text, "Hoje: US$ 0,0200 · 1 pergunta") {
		t.Errorf("/cost:\n%s", text)
	}

	fa.err, fa.ans = errors.New("claude-opus-5-5 declined to answer (cyber)"), ask.Answer{Steps: 1, Spend: cost.Spend{USD: 0.001}}
	if r := b.question(ctx, user, "?"); !strings.Contains(r.text, "se recusou a responder") {
		t.Errorf("a refusal: %q", r.text)
	}
	if usd, n, _ := b.store.Spent(ctx, time.Unix(0, 0)); n != 2 || usd < 0.0209 || usd > 0.0211 {
		t.Errorf("a failed question was not recorded: %d calls, $%f", n, usd)
	}

	fa.err, fa.ans = ask.ErrBusy, ask.Answer{}
	if r := b.question(ctx, user, "e agora?"); !strings.Contains(r.text, "pergunta anterior") {
		t.Errorf("busy: %q", r.text)
	}
	if usd, n, _ := b.store.Spent(ctx, time.Unix(0, 0)); n != 2 || usd > 0.0211 {
		t.Errorf("a question that never reached the model was recorded: %d calls", n)
	}

	b.cfg.DailyBudget = 0.01
	asked := len(fa.asked)
	if r := b.question(ctx, user, "quanto?"); !strings.Contains(r.text, "teto diário") || len(fa.asked) != asked {
		t.Errorf("over the budget: %q, asked %d times", r.text, len(fa.asked)-asked)
	}
}
