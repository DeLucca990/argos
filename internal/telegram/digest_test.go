package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"

	"github.com/DeLucca990/argos-bot/internal/store"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func seedMonths(t *testing.T, b *Bot, user store.User) {
	t.Helper()
	other, _ := b.store.SaveUser(ctx, store.User{TelegramID: 7777})
	for _, e := range []struct {
		user                         int64
		on, merchant, category, paid string
		cents                        int64
	}{
		{user.ID, "2026-08-05", "Mercado de agosto", "mercado", "pix", 10000},
		{user.ID, "2026-09-02", "Supermercado Extra", "mercado", "credito", 25000},
		{user.ID, "2026-09-15", "Padaria", "restaurante", "pix", 1800},
		{user.ID, "2026-09-29", "Extra Hiper", "mercado", "debito", 12000},
		{user.ID, "2026-10-05", "Posto Shell", "combustivel", "credito", 20000},
		{user.ID, "2026-10-10", "OXXO", "mercado", "", 5000},
		{other.ID, "2026-09-10", "De outra pessoa", "lazer", "pix", 99999},
	} {
		day, _ := time.ParseInLocation("2006-01-02", e.on, time.Local)
		if _, err := b.store.AddExpense(ctx, store.Expense{UserID: e.user, SpentOn: day, Merchant: e.merchant,
			Category: e.category, PaymentMethod: e.paid, TotalCents: e.cents, Source: store.SourcePhoto,
			Status: store.Confirmed}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDigestDue(t *testing.T) {
	nine := DailyAt{Hour: 9}
	for now, want := range map[string]string{
		"2026-11-01 08:59": "",
		"2026-11-01 09:00": "2026-10",
		"2026-11-03 02:00": "2026-10",
		"2026-11-07 23:59": "2026-10",
		"2026-11-08 09:00": "",
		"2027-01-01 10:00": "2026-12",
	} {
		p, due := digestDue(at(now), nine)
		if got := map[bool]string{true: p.slug, false: ""}[due]; got != want {
			t.Errorf("at %s: %q, want %q", now, got, want)
		}
	}
}

func TestMonthDigest(t *testing.T) {
	b, user := testBot(t, answering(pharmacy()))
	seedMonths(t, b, user)

	september, _ := parseMonth("setembro", fixed)
	text, count, err := b.monthDigest(ctx, user.ID, september, fixed)
	if err != nil || count != 3 {
		t.Fatalf("%d expenses, %v", count, err)
	}
	for _, want := range []string{
		"📅 <b>Resumo de setembro de 2026</b>",
		"💰 <b>R$ 388,00</b> em 3 gastos · média de R$ 12,93 por dia",
		"↗️ R$ 288,00 a mais que em agosto (+288%)",
		"🛒 Mercado — R$ 370,00 (95%) · ↗️ +R$ 270,00",
		"🍽️ Restaurante — R$ 18,00 (5%) · novo",
		"• 02/09 🛒 Supermercado Extra — R$ 250,00\n• 29/09 🛒 Extra Hiper — R$ 120,00\n• 15/09 🍽️ Padaria — R$ 18,00",
		"Crédito R$ 250,00 · Débito R$ 120,00 · Pix R$ 18,00",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the digest lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "999") {
		t.Errorf("someone else's expense is in the digest:\n%s", text)
	}

	october, _ := parseMonth("", fixed)
	partial, _, _ := b.monthDigest(ctx, user.ID, october, fixed)
	for _, want := range []string{"Outubro de 2026, até o dia 20", "R$ 250,00</b> em 2 gastos · média de R$ 12,50 por dia",
		"↘️ R$ 18,00 a menos que no mesmo período de setembro (−7%)", "não informado R$ 50,00"} {
		if !strings.Contains(partial, want) {
			t.Errorf("the month so far lacks %q:\n%s", want, partial)
		}
	}

	july, _ := parseMonth("julho", fixed)
	if text, count, _ := b.monthDigest(ctx, user.ID, july, fixed); count != 0 || !strings.Contains(text, "Nenhum gasto") {
		t.Errorf("an empty month: %q", text)
	}
	if text, _ := b.digestCommand(ctx, user.ID, "", fixed); !strings.Contains(text, "Resumo de setembro de 2026") {
		t.Errorf("/resumo with no month is the last closed one:\n%s", text)
	}
}

func TestSendDigests(t *testing.T) {
	b, user := testBot(t, answering(pharmacy()))
	seedMonths(t, b, user)
	quiet, _ := b.store.SaveUser(ctx, store.User{TelegramID: 5555})
	b.cfg.AllowedUsers = map[int64]bool{user.TelegramID: true, quiet.TelegramID: true}
	b.cfg.DigestAt = &DailyAt{Hour: 9}

	var sent []int64
	var fail error
	b.notify = func(_ context.Context, chatID int64, text string) error {
		if fail != nil {
			return fail
		}
		if !strings.Contains(text, "Resumo de setembro de 2026") {
			t.Errorf("sent %q", text)
		}
		sent = append(sent, chatID)
		return nil
	}

	fail = errors.New("telegram is down")
	b.sendDigests(ctx, at("2026-10-01 09:10"))
	if done, _ := b.store.DigestSent(ctx, user.ID, "2026-09"); done {
		t.Error("a digest that failed to send was recorded as sent")
	}

	fail = nil
	b.sendDigests(ctx, at("2026-10-01 09:20"))
	b.sendDigests(ctx, at("2026-10-02 12:00"))
	if len(sent) != 1 || sent[0] != user.TelegramID {
		t.Errorf("sent to %v, want only %d, once", sent, user.TelegramID)
	}
	if done, _ := b.store.DigestSent(ctx, quiet.ID, "2026-09"); !done {
		t.Error("a user with nothing to report should be marked done, not asked again")
	}
	if done, _ := b.store.DigestSent(ctx, 2, "2026-09"); done {
		t.Error("a user outside the allowlist got a digest recorded")
	}

	b.sendDigests(ctx, at("2026-11-01 08:00"))
	fail = bot.ErrorForbidden
	b.sendDigests(ctx, at("2026-11-01 09:00"))
	if done, _ := b.store.DigestSent(ctx, user.ID, "2026-10"); !done {
		t.Error("a user who blocked the bot is asked every ten minutes for a week")
	}
}

func TestMonthDigestAccounts(t *testing.T) {
	b, user := testBot(t, answering(pharmacy()))
	seedMonths(t, b, user)
	september, _ := parseMonth("setembro", fixed)
	if text, _, _ := b.monthDigest(ctx, user.ID, september, fixed); strings.Contains(text, "Contas") {
		t.Errorf("no account is known, and the digest has a section for them:\n%s", text)
	}

	list, _ := b.store.Find(ctx, user.ID, store.Filter{From: september.from, To: september.to}, true, 2)
	for i, account := range []string{"Nubank", "Inter"} {
		list[i].Account = account
		if _, err := b.store.UpdateExpense(ctx, user.ID, list[i]); err != nil {
			t.Fatal(err)
		}
	}
	text, _, _ := b.monthDigest(ctx, user.ID, september, fixed)
	if !strings.Contains(text, "<b>Contas</b>\nNubank R$ 250,00 · Inter R$ 120,00 · não informada R$ 18,00") {
		t.Errorf("the accounts:\n%s", text)
	}
}
