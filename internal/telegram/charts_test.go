package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DeLucca990/argos/internal/ask"
	"github.com/DeLucca990/argos/internal/chart"
	"github.com/DeLucca990/argos/internal/store"
)

func TestReportsComeWithCharts(t *testing.T) {
	b, user := testBot(t, answering(pharmacy()))
	seedMonths(t, b, user)

	if week, err := b.summary(ctx, user.ID, weekPeriod(fixed)); err != nil || week.photo != nil || week.keyboard != nil {
		t.Errorf("a week with no expenses: %+v, %v", week.photo, err)
	}
	october, _ := parseMonth("outubro", fixed)
	month, err := b.summary(ctx, user.ID, october)
	if err != nil || month.photo == nil || len(month.photo.png) == 0 || month.photo.caption != "📊 Outubro de 2026" ||
		!strings.Contains(month.text, "R$ 250,00") {
		t.Errorf("/mes: %q, %v", month.text, err)
	}
	digest, err := b.digestCommand(ctx, user.ID, "setembro", fixed)
	if err != nil || digest.photo == nil || digest.photo.caption != "📊 Ritmo de setembro de 2026" || digest.keyboard != nil {
		t.Errorf("/resumo: %+v, %v", digest.photo, err)
	}
	if empty, _ := b.digestCommand(ctx, user.ID, "julho", fixed); empty.photo != nil {
		t.Error("a month with no expenses came with a chart")
	}

	july, _ := parseMonth("julho", fixed)
	if _, err := b.periodChart(ctx, user.ID, "category", july); !errors.Is(err, chart.ErrEmpty) {
		t.Errorf("a month with no expenses: %v", err)
	}
	if r := b.withChart(ctx, user.ID, reply{text: "julho"}, "category", july); r.photo != nil || r.text != "julho" {
		t.Errorf("a chart that cannot be drawn leaves the text alone: %+v", r)
	}
}

func TestQuestionSendsItsChart(t *testing.T) {
	b, user := testBot(t, answering(pharmacy()))
	seedMonths(t, b, user)
	f := store.Filter{From: time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local), To: time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)}
	c, err := chart.Spending(ctx, b.store, user.ID, chart.Request{Filter: f, GroupBy: "category", Title: "Setembro <todo>"}, fixed)
	if err != nil {
		t.Fatal(err)
	}
	b.asker = &fakeAsker{ans: ask.Answer{Text: "Em setembro: R$ 388,00.", Chart: &c, Steps: 2}}

	r := b.question(ctx, user, "faça um resumo de setembro")
	if !strings.HasPrefix(r.text, "Em setembro: R$ 388,00.") || r.photo == nil || r.photo.caption != "📊 Setembro &lt;todo&gt;" {
		t.Errorf("reply: %q, chart %+v", r.text, r.photo)
	}
	b.asker = &fakeAsker{ans: ask.Answer{Text: "R$ 388,00.", Steps: 1}}
	if r := b.question(ctx, user, "quanto gastei em setembro?"); r.photo != nil {
		t.Error("an answer with no chart sent one")
	}
}

func TestTheDigestComesWithItsChart(t *testing.T) {
	b, user := testBot(t, answering(pharmacy()))
	seedMonths(t, b, user)
	b.cfg.AllowedUsers = map[int64]bool{user.TelegramID: true}
	b.cfg.MonthlyDigestAt = &DailyAt{Hour: 9}
	var texts, pictures []string
	b.notify = func(_ context.Context, _ int64, text string) error {
		texts = append(texts, text)
		return nil
	}
	b.sendPhoto = func(_ context.Context, chatID int64, p picture) error {
		pictures = append(pictures, p.caption)
		return nil
	}

	b.sendDigests(ctx, at("2026-10-01 09:10"))
	b.sendDigests(ctx, at("2026-10-01 09:20"))
	if len(texts) != 1 || len(pictures) != 1 || pictures[0] != "📊 Ritmo de setembro de 2026" {
		t.Errorf("sent %d digests and the charts %q", len(texts), pictures)
	}
}
