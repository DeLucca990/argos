package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos/internal/extract"
	"github.com/DeLucca990/argos/internal/receipt"
	"github.com/DeLucca990/argos/internal/store"
)

func (b *Bot) cardFor(ctx context.Context, msg *models.Message) (int64, bool) {
	r := msg.ReplyToMessage
	if r == nil || msg.Text == "" || r.From == nil || r.From.ID != b.tg.ID() {
		return 0, false
	}
	id, err := b.store.CardExpense(ctx, msg.Chat.ID, r.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("chat %d: looking up the card of message %d: %v", msg.Chat.ID, r.ID, err)
		}
		return 0, false
	}
	return id, true
}

func (b *Bot) onCorrection(ctx context.Context, msg *models.Message, expenseID int64) {
	cardID := msg.ReplyToMessage.ID
	b.process(ctx, msg, "✏️ Corrigindo…", func(ctx context.Context) reply {
		r, updated := b.correct(ctx, userFrom(ctx), expenseID, msg.Text)
		if updated != nil {
			b.edit(ctx, msg.Chat.ID, cardID, b.card(ctx, *updated, false))
		}
		return r
	})
}

func (b *Bot) correct(ctx context.Context, user store.User, id int64, text string) (reply, *store.Expense) {
	now := b.now()
	e, err := b.owned(ctx, user.ID, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return reply{text: "Não encontrei esse gasto."}, nil
	case err != nil:
		log.Printf("user %d: expense %d: %v", user.ID, id, err)
		return reply{text: dbErrorText}, nil
	case e.Status == store.Discarded:
		return reply{text: "Esse gasto foi descartado. Para registrá-lo de novo, mande o comprovante outra vez."}, nil
	}
	if r, over := b.overBudget(ctx, now); over {
		return r, nil
	}
	cats, err := b.categories(ctx, user.ID)
	if err != nil {
		log.Printf("user %d: %v", user.ID, err)
		return reply{text: dbErrorText}, nil
	}

	res, err := b.extractor.Correct(ctx, extract.Correction{Current: rawFrom(e), Text: text, Today: now,
		Categories: cats.forModel(e.Category)})
	record := b.recordOf(user.ID, e.AttachmentID, res, err)
	record.Kind = store.KindCorrection
	record.ExpenseID = id
	logRead(user, res, err)
	b.record(ctx, record)
	if err != nil {
		return reply{text: "⚠️ Não consegui aplicar a correção: " + escape(friendly(err))}, nil
	}

	r, err := receipt.Parse(res.Raw, now)
	if err != nil {
		return reply{text: "Não entendi a correção. Diga, por exemplo, <i>foi 45,90</i>, <i>no crédito</i> ou <i>categoria farmácia</i>."}, nil
	}
	fixed := expenseFrom(r, cats, user.ID, e.Source, e.AttachmentID)
	fixed.ID = id
	saved, err := b.store.UpdateExpense(ctx, user.ID, fixed)
	var dup *store.DuplicateError
	switch {
	case errors.As(err, &dup):
		return reply{text: "A chave de acesso corrigida já é de outro gasto. " + duplicateText(dup.Existing)}, nil
	case errors.Is(err, store.ErrNotPending):
		return reply{text: "Esse gasto foi descartado. Para registrá-lo de novo, mande o comprovante outra vez."}, nil
	case err != nil:
		log.Printf("user %d: correcting expense %d: %v", user.ID, id, err)
		return reply{text: dbErrorText}, nil
	}

	log.Printf("user %d: expense %d corrected", user.ID, id)
	changes := changes(e, saved, cats)
	out := "✏️ Nada mudou."
	if len(changes) > 0 {
		out = "✏️ Corrigido:\n• " + strings.Join(changes, "\n• ")
	}
	if r.Notes != "" {
		out += "\n\n📝 " + html.EscapeString(r.Notes)
	}
	answer := reply{text: out, expenseID: id}
	learned, err := b.learn(ctx, user.ID, e, saved)
	if err != nil {
		log.Printf("user %d: learning from the correction of expense %d: %v", user.ID, id, err)
	}
	if len(learned) > 0 {
		log.Printf("user %d: %d lesson(s) learned from expense %d", user.ID, len(learned), id)
		answer.text += "\n\n" + lessonsText(learned, cats)
		answer.keyboard = forgetKeyboard(learned)
	}
	return answer, &saved
}

func changes(before, after store.Expense, cats catalog) []string {
	var out []string
	change := func(label, from, to string) {
		if from != to {
			out = append(out, label+": "+html.EscapeString(nonEmpty(from, "—"))+" → "+html.EscapeString(nonEmpty(to, "—")))
		}
	}
	change("total", money(before.TotalCents, before.Currency), money(after.TotalCents, after.Currency))
	change("data", before.SpentOn.Format("02/01/2006"), after.SpentOn.Format("02/01/2006"))
	change("hora", before.SpentTime, after.SpentTime)
	change("estabelecimento", before.Merchant, after.Merchant)
	change("CNPJ/CPF", receipt.FormatTaxID(before.MerchantTaxID), receipt.FormatTaxID(after.MerchantTaxID))
	change("categoria", cats.of(before.Category).Label, cats.of(after.Category).Label)
	change("pagamento", paidWith(before), paidWith(after))
	change("descrição", before.Description, after.Description)
	change("chave de acesso", before.NFCeKey, after.NFCeKey)
	if fmt.Sprint(before.Items) != fmt.Sprint(after.Items) {
		out = append(out, "itens corrigidos")
	}
	return out
}

func rawFrom(e store.Expense) receipt.Raw {
	raw := receipt.Raw{
		IsReceipt: true, Merchant: e.Merchant, MerchantTaxID: e.MerchantTaxID, Date: e.SpentOn.Format("2006-01-02"),
		Time: e.SpentTime, Total: dotDecimal(e.TotalCents), Currency: e.Currency, PaymentMethod: e.PaymentMethod,
		PaymentAccount: e.Account, CardLast4: e.CardLast4, Category: e.Category, Description: e.Description, NFCeAccessKey: e.NFCeKey, Items: []receipt.RawItem{},
		Remembered: []string{},
	}
	for _, it := range e.Items {
		unit := ""
		if it.UnitCents != 0 {
			unit = dotDecimal(it.UnitCents)
		}
		raw.Items = append(raw.Items, receipt.RawItem{
			Description: it.Description, Quantity: strconv.FormatFloat(it.Quantity, 'f', -1, 64),
			UnitPrice: unit, Total: dotDecimal(it.TotalCents),
		})
	}
	return raw
}

func dotDecimal(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

func (b *Bot) overBudget(ctx context.Context, now time.Time) (reply, bool) {
	if b.cfg.DailyBudget <= 0 {
		return reply{}, false
	}
	spent, _, err := b.store.Spent(ctx, startOfDay(now))
	if err != nil {
		log.Printf("reading today's spending: %v", err)
		return reply{}, false
	}
	if spent < b.cfg.DailyBudget {
		return reply{}, false
	}
	log.Printf("refused a read: today's cost $%.4f, the daily budget is $%.2f", spent, b.cfg.DailyBudget)
	return reply{text: fmt.Sprintf("O teto diário de US$ %s para leituras já foi gasto (US$ %s hoje). "+
		"Volto a ler comprovantes amanhã; os comandos continuam funcionando.",
		dollars(b.cfg.DailyBudget, 2), dollars(spent, 4))}, true
}

func dollars(v float64, places int) string {
	return strings.Replace(strconv.FormatFloat(v, 'f', places, 64), ".", ",", 1)
}
