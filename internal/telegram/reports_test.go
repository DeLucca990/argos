package telegram

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DeLucca990/argos/internal/store"
)

func seed(t *testing.T, b *Bot, user store.User) {
	t.Helper()
	other, err := b.store.SaveUser(ctx, store.User{TelegramID: 7777})
	if err != nil {
		t.Fatal(err)
	}
	add := func(userID int64, on, clock, merchant, category string, cents int64, payment string, status store.Status) {
		t.Helper()
		day, _ := time.ParseInLocation("2006-01-02", on, time.Local)
		if _, err := b.store.AddExpense(ctx, store.Expense{UserID: userID, SpentOn: day, SpentTime: clock, Merchant: merchant,
			Category: category, TotalCents: cents, PaymentMethod: payment, Source: store.SourcePhoto, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	add(user.ID, "2026-10-20", "08:10", "Padaria Pão Quente", "restaurante", 1500, "pix", store.Confirmed)
	add(user.ID, "2026-10-20", "12:30", `=HYPERLINK("x")`, "mercado", 8740, "credito", store.Confirmed)
	add(user.ID, "2026-10-19", "", "OXXO", "mercado", 998, "", store.Confirmed)
	add(user.ID, "2026-10-13", "", "Posto Shell", "combustivel", 20000, "debito", store.Confirmed)
	add(user.ID, "2026-09-30", "", "Drogaria", "farmacia", 4230, "", store.Confirmed)
	add(user.ID, "2026-10-20", "", "Pendente", "outros", 99999, "", store.Pending)
	add(other.ID, "2026-10-20", "", "Outra pessoa", "lazer", 99999, "", store.Confirmed)
}

func TestDayAndPeriodReports(t *testing.T) {
	b, user := testBot(t, answering(pharmacy()))
	seed(t, b, user)

	day, err := b.dayText(ctx, user.ID, fixed)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Hoje, terça-feira, 20/10", "R$ 102,40</b> em 2 gastos",
		"• 08:10 🍽️ Padaria Pão Quente — R$ 15,00", "• 12:30 🛒 =HYPERLINK(&#34;x&#34;) — R$ 87,40"} {
		if !strings.Contains(day, want) {
			t.Errorf("/hoje lacks %q:\n%s", want, day)
		}
	}
	if strings.Contains(day, "99") {
		t.Errorf("/hoje counted a pending expense or someone else's:\n%s", day)
	}

	week, _ := b.summaryText(ctx, user.ID, weekPeriod(fixed))
	for _, want := range []string{"Semana de 19/10 a 25/10", "R$ 112,38</b> em 3 gastos", "🛒 Mercado — R$ 97,38 (87%) · 2",
		"🍽️ Restaurante — R$ 15,00 (13%) · 1"} {
		if !strings.Contains(week, want) {
			t.Errorf("/semana lacks %q:\n%s", want, week)
		}
	}

	october, _ := parseMonth("", fixed)
	month, _ := b.summaryText(ctx, user.ID, october)
	if !strings.Contains(month, "Outubro de 2026") || !strings.Contains(month, "R$ 312,38</b> em 4 gastos") ||
		strings.Index(month, "Combustível") > strings.Index(month, "Mercado") {
		t.Errorf("/mes:\n%s", month)
	}

	empty, _ := parseMonth("2026-08", fixed)
	if text, _ := b.summaryText(ctx, user.ID, empty); !strings.Contains(text, "Nenhum gasto registrado") {
		t.Errorf("an empty month:\n%s", text)
	}
}

func TestParseMonth(t *testing.T) {
	for arg, want := range map[string]string{
		"":              "2026-10",
		"2026-09":       "2026-09",
		"09/2026":       "2026-09",
		"9/2026":        "2026-09",
		"9":             "2026-09",
		"setembro":      "2026-09",
		"Set":           "2026-09",
		"dezembro":      "2025-12",
		"12":            "2025-12",
		"março 2025":    "2025-03",
		"marco de 2024": "2024-03",
	} {
		p, err := parseMonth(arg, fixed)
		if err != nil || p.slug != want || !p.to.Equal(p.from.AddDate(0, 1, 0)) {
			t.Errorf("parseMonth(%q) = %s, %v; want %s", arg, p.slug, err, want)
		}
	}
	for _, arg := range []string{"13", "abc", "ma", "outubro 99"} {
		if _, err := parseMonth(arg, fixed); err == nil {
			t.Errorf("parseMonth(%q) accepted", arg)
		}
	}
	if p, _ := parseMonth("2026-02", fixed); p.title != "Fevereiro de 2026" {
		t.Errorf("title %q", p.title)
	}
}

func TestRecentAndUndo(t *testing.T) {
	b, user := testBot(t, answering(pharmacy()))
	if text, _ := b.undoText(ctx, user.ID); !strings.Contains(text, "Não há gasto") {
		t.Errorf("undo with nothing saved: %q", text)
	}
	seed(t, b, user)

	all, _ := b.recentText(ctx, user.ID, "")
	if !strings.Contains(all, "Últimos 5 gastos") || strings.Index(all, "HYPERLINK") > strings.Index(all, "Padaria") {
		t.Errorf("/ultimos:\n%s", all)
	}
	if two, _ := b.recentText(ctx, user.ID, "2"); strings.Count(two, "\n•") != 2 {
		t.Errorf("/ultimos 2:\n%s", two)
	}
	var usage usageError
	if _, err := b.recentText(ctx, user.ID, "muitos"); !errors.As(err, &usage) {
		t.Errorf("/ultimos muitos: %v", err)
	}

	undo, err := b.undoText(ctx, user.ID)
	if err != nil || !strings.Contains(undo, "Drogaria, R$ 42,30 em 30/09/2026") {
		t.Errorf("/desfazer: %q, %v", undo, err)
	}
	if after, _ := b.recentText(ctx, user.ID, ""); strings.Contains(after, "Drogaria") || !strings.Contains(after, "Últimos 4") {
		t.Errorf("after /desfazer:\n%s", after)
	}
}

func TestExportCSV(t *testing.T) {
	b, user := testBot(t, answering(pharmacy()))
	seed(t, b, user)

	name, data, rows, err := b.exportCSV(ctx, user.ID, "", fixed)
	if err != nil || name != "argos-2026-10.csv" || rows != 4 {
		t.Fatalf("%s, %d rows, %v", name, rows, err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "\ufeffdata;hora;estabelecimento;cnpj_cpf;categoria;pagamento;conta;cartao_final;valor;moeda;descricao;origem;id\n") {
		t.Errorf("header:\n%s", text)
	}
	for _, want := range []string{
		`20/10/2026;12:30;"'=HYPERLINK(""x"")";;Mercado;Crédito;;;87,40;BRL;;foto;`,
		"20/10/2026;08:10;Padaria Pão Quente;;Restaurante;Pix;;;15,00;BRL;;foto;",
		"13/10/2026;;Posto Shell;;Combustível;Débito;;;200,00;BRL;;foto;",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the CSV lacks %q:\n%s", want, text)
		}
	}

	if name, _, rows, _ := b.exportCSV(ctx, user.ID, "tudo", fixed); name != "argos-tudo.csv" || rows != 5 {
		t.Errorf("tudo: %s, %d rows", name, rows)
	}
	var usage usageError
	if _, _, _, err := b.exportCSV(ctx, user.ID, "ontem", fixed); !errors.As(err, &usage) {
		t.Errorf("a bad month: %v", err)
	}
}

func TestCost(t *testing.T) {
	b, user := testBot(t, answering(pharmacy()))
	b.cfg.Effort = "low"
	b.store.RecordExtraction(ctx, store.Extraction{UserID: user.ID, Model: "claude-opus-5-5", USD: 0.04})
	b.store.RecordExtraction(ctx, store.Extraction{UserID: user.ID, Model: "claude-opus-5-5", USD: 0.0154, Failed: true})

	text, err := b.costText(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Hoje: US$ 0,0554 · 2 leituras", "30 dias: US$ 0,0554", "claude-opus-5-5, effort low"} {
		if !strings.Contains(text, want) {
			t.Errorf("/cost lacks %q:\n%s", want, text)
		}
	}
}
