package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"slices"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos-bot/internal/extract"
	"github.com/DeLucca990/argos-bot/internal/receipt"
	"github.com/DeLucca990/argos-bot/internal/store"
)

const lessonPrefix = "ls:"

var fieldLabels = map[string]string{
	"merchant": "nome", "payment_method": "pagamento", "payment_account": "conta", "card_last4": "cartão", "category": "categoria",
}

func payerField(field string) bool {
	return field == "payment_method" || field == "payment_account" || field == "card_last4"
}

func (b *Bot) lessonsFor(ctx context.Context, userID int64, cats catalog) []extract.Lesson {
	lessons, err := b.store.Lessons(ctx, userID)
	if err != nil {
		log.Printf("user %d: reading their lessons: %v", userID, err)
		return nil
	}
	var out []extract.Lesson
	for _, l := range lessons {
		if c := cats.of(l.Value); l.Field == "category" && (c.ID == 0 || c.Archived) {
			continue
		}
		when := fmt.Sprintf("receipts that look like %q", l.Layout)
		if l.Payee != "" {
			when = fmt.Sprintf("payments to %q", l.Payee)
		}
		out = append(out, extract.Lesson{When: when, Field: l.Field, Value: l.Value})
	}
	return out
}

func (b *Bot) learn(ctx context.Context, userID int64, before, after store.Expense) ([]store.Lesson, error) {
	if before.AttachmentID == 0 {
		return nil, nil
	}
	x, err := b.store.FirstReading(ctx, before.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var raw receipt.Raw
	if err := json.Unmarshal([]byte(x.RawJSON), &raw); err != nil {
		return nil, nil
	}
	layout, payee := strings.TrimSpace(raw.Layout), payeeOf(raw)

	var lessons []store.Lesson
	for _, field := range receipt.LessonFields {
		was, is := fieldValue(before, field), fieldValue(after, field)
		if was == is || is == "" {
			continue
		}
		l := store.Lesson{ExpenseID: before.ID, Field: field, Value: is}
		switch {
		case !payerField(field) && payee != "":
			l.Payee = payee
		case layout != "":
			l.Layout = layout
		default:
			continue
		}
		lessons = append(lessons, l)
	}
	if len(lessons) == 0 {
		return nil, nil
	}
	return b.store.AddLessons(ctx, userID, lessons)
}

func payeeOf(raw receipt.Raw) string {
	name := strings.TrimSpace(raw.Merchant)
	if slices.Contains(raw.Remembered, "merchant") {
		name = ""
	}
	taxID := strings.TrimSpace(raw.MerchantTaxID)
	switch {
	case name != "" && taxID != "":
		return name + " (" + taxID + ")"
	case name != "":
		return name
	}
	return taxID
}

func fieldValue(e store.Expense, field string) string {
	return map[string]string{
		"merchant": e.Merchant, "payment_method": e.PaymentMethod, "payment_account": e.Account,
		"card_last4": e.CardLast4, "category": e.Category,
	}[field]
}

func lessonsText(lessons []store.Lesson, cats catalog) string {
	var keys []string
	what := map[string][]string{}
	for _, l := range lessons {
		key := "comprovantes como este (<i>" + html.EscapeString(l.Layout) + "</i>)"
		if l.Payee != "" {
			key = "pagamentos a <i>" + html.EscapeString(l.Payee) + "</i>"
		}
		if _, seen := what[key]; !seen {
			keys = append(keys, key)
		}
		value := html.EscapeString(l.Value)
		switch l.Field {
		case "payment_method":
			value = paymentLabels[l.Value]
		case "card_last4":
			value = "final " + l.Value
		case "category":
			c := cats.of(l.Value)
			value = c.Emoji + " " + html.EscapeString(c.Label)
		}
		what[key] = append(what[key], fieldLabels[l.Field]+" "+value)
	}
	var s strings.Builder
	s.WriteString("🧠 Vou lembrar disso nos próximos comprovantes:")
	for _, key := range keys {
		s.WriteString("\n• " + key + ": " + strings.Join(what[key], ", "))
	}
	return s.String()
}

func forgetKeyboard(lessons []store.Lesson) *models.InlineKeyboardMarkup {
	first, last := lessons[0].ID, lessons[len(lessons)-1].ID
	data := lessonPrefix + strconv.FormatInt(first, 10) + ":" + strconv.FormatInt(last, 10)
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "🙅 Não lembrar", CallbackData: data}}}}
}

func (b *Bot) onLessonCallback(ctx context.Context, q *models.CallbackQuery) {
	user := userFrom(ctx)
	firstText, lastText, _ := strings.Cut(strings.TrimPrefix(q.Data, lessonPrefix), ":")
	first, err1 := strconv.ParseInt(firstText, 10, 64)
	last, err2 := strconv.ParseInt(lastText, 10, 64)
	if err1 != nil || err2 != nil || last < first || last-first > int64(len(receipt.LessonFields)) {
		b.answer(ctx, q, "Botão desconhecido.")
		return
	}
	var ids []int64
	for id := first; id <= last; id++ {
		ids = append(ids, id)
	}
	if _, err := b.store.ForgetLessons(ctx, user.ID, ids...); err != nil {
		log.Printf("user %d: forgetting lessons %d to %d: %v", user.ID, first, last, err)
		b.answer(ctx, q, dbErrorText)
		return
	}
	log.Printf("user %d: lessons %d to %d forgotten", user.ID, first, last)

	if m := q.Message.Message; m != nil {
		before, _, _ := strings.Cut(m.Text, "🧠")
		b.edit(ctx, m.Chat.ID, m.ID, reply{text: html.EscapeString(before) + "🙅 Não vou lembrar disso."})
	}
	b.answer(ctx, q, "Esquecido.")
}

func rememberedText(fields []string) string {
	var labels []string
	for _, f := range fields {
		labels = append(labels, fieldLabels[f])
	}
	return "🧠 <i>Lembrado das suas correções: " + strings.Join(labels, ", ") + ".</i>"
}
