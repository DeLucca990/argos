package telegram

import (
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos-bot/internal/extract"
	"github.com/DeLucca990/argos-bot/internal/receipt"
	"github.com/DeLucca990/argos-bot/internal/store"
)

const nubankLayout = "comprovante de Pix em tela de app, títulos em roxo"

func nubankPix() receipt.Raw {
	return receipt.Raw{
		IsReceipt: true, Merchant: "MARIA S SOUZA", Date: "2026-10-19", Total: "150.00", Currency: "BRL",
		PaymentMethod: "pix", Category: "outros", Description: "Pix", Items: []receipt.RawItem{},
		Layout: nubankLayout, Remembered: []string{},
	}
}

func TestLearningFromACorrection(t *testing.T) {
	fx := answering(nubankPix())
	b, user := testBot(t, fx)
	calls := fakeTelegram(t, b)
	b.read(ctx, user, incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto})

	fixed := nubankPix()
	fixed.PaymentAccount, fixed.Merchant, fixed.Category, fixed.Total = "Nubank", "Personal trainer", "lazer", "160.00"
	fx.fixed = answer(fixed, 0.01)
	r, _ := b.correct(ctx, user, 1, "foi no Nubank, é o personal, lazer, e foi 160")
	for _, want := range []string{
		"🧠 Vou lembrar disso nos próximos comprovantes:",
		"\n• pagamentos a <i>MARIA S SOUZA</i>: nome Personal trainer, categoria 🎉 Lazer",
		"\n• comprovantes como este (<i>" + nubankLayout + "</i>): conta Nubank",
	} {
		if !strings.Contains(r.text, want) {
			t.Errorf("the reply lacks %q:\n%s", want, r.text)
		}
	}
	if r.keyboard == nil || r.keyboard.InlineKeyboard[0][0].CallbackData != "ls:1:3" {
		t.Errorf("the button to forget them: %+v", r.keyboard)
	}
	lessons, _ := b.store.Lessons(ctx, user.ID)
	if len(lessons) != 3 {
		t.Fatalf("the total, which changes with every receipt, was learned too: %+v", lessons)
	}

	next := nubankPix()
	next.PaymentAccount, next.Remembered = "Nubank", []string{"payment_account"}
	fx.res = answer(next, 0.02)
	b.read(ctx, user, incoming{data: append([]byte("another "), photo...), mime: "image/jpeg", source: store.SourcePhoto})
	given := fx.last.Lessons
	if len(given) != 3 || given[1] != (extract.Lesson{When: `receipts that look like "` + nubankLayout + `"`, Field: "payment_account", Value: "Nubank"}) ||
		given[0] != (extract.Lesson{When: `payments to "MARIA S SOUZA"`, Field: "category", Value: "lazer"}) {
		t.Errorf("the model was given %+v", given)
	}
	e, _ := b.store.Expense(ctx, 2)
	if card := b.card(ctx, e, false).text; !strings.Contains(card, "🧠 <i>Lembrado das suas correções: conta.</i>") || !strings.Contains(card, "Pix · Nubank") {
		t.Errorf("the card does not say what was remembered:\n%s", card)
	}

	shown := "✏️ Corrigido:\n• total: R$ 150,00 → R$ 160,00\n\n🧠 Vou lembrar disso nos próximos comprovantes:\n• …"
	forget := func(data string) {
		b.onLessonCallback(withUser(ctx, user), &models.CallbackQuery{ID: "q", Data: data, Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{ID: 77, Chat: models.Chat{ID: user.TelegramID}, Text: shown}}})
	}
	forget("ls:3:1")
	if got := calls.last("answerCallbackQuery"); got["text"] != "Botão desconhecido." {
		t.Errorf("a backwards range: %v", got)
	}
	forget("ls:1:3")
	if lessons, _ := b.store.Lessons(ctx, user.ID); len(lessons) != 0 {
		t.Errorf("not forgotten: %+v", lessons)
	}
	if got := calls.last("editMessageText"); got["text"] != "✏️ Corrigido:\n• total: R$ 150,00 → R$ 160,00\n\n🙅 Não vou lembrar disso." ||
		got["reply_markup"] != "" {
		t.Errorf("the reply after forgetting: %v", got)
	}
}

func TestWhatIsNotLearned(t *testing.T) {
	raw := nubankPix()
	raw.Layout = ""
	fx := answering(raw)
	b, user := testBot(t, fx)
	b.read(ctx, user, incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto})

	corrected := raw
	corrected.PaymentAccount, corrected.Date = "Nubank", "2026-10-18"
	fx.fixed = answer(corrected, 0.01)
	if r, _ := b.correct(ctx, user, 1, "foi no Nubank, ontem"); strings.Contains(r.text, "🧠") || r.keyboard != nil {
		t.Errorf("a receipt with no layout taught the account, or the date was learned:\n%s", r.text)
	}

	fx.res.Raw.Total = "35.90"
	b.read(ctx, user, incoming{text: "almoço 35,90 no pix", source: store.SourceText})
	typed := fx.res.Raw
	typed.Merchant = "Restaurante da Esquina"
	fx.fixed = answer(typed, 0.01)
	if r, _ := b.correct(ctx, user, 2, "foi no Restaurante da Esquina"); strings.Contains(r.text, "🧠") {
		t.Errorf("a typed purchase taught something:\n%s", r.text)
	}

	c, _, _ := b.store.SaveCategory(ctx, user.ID, store.Category{Label: "Academia"})
	b.store.AddLessons(ctx, user.ID, []store.Lesson{
		{Field: "category", Value: "academia", Payee: "SMART FIT"},
		{Field: "category", Value: "cassino", Payee: "BET"},
	})
	if got := b.lessonsFor(ctx, user.ID, mustCategories(t, b, user.ID)); len(got) != 1 || got[0].Value != "academia" {
		t.Errorf("lessons for categories that exist: %+v", got)
	}
	b.store.AddExpense(ctx, store.Expense{UserID: user.ID, SpentOn: fixed, TotalCents: 100, Category: "academia", Source: store.SourceText})
	b.store.RemoveCategory(ctx, user.ID, c.ID)
	if got := b.lessonsFor(ctx, user.ID, mustCategories(t, b, user.ID)); len(got) != 0 {
		t.Errorf("a lesson for an archived category: %+v", got)
	}
}

func mustCategories(t *testing.T, b *Bot, userID int64) catalog {
	t.Helper()
	cats, err := b.categories(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	return cats
}
