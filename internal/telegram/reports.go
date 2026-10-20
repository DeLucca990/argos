package telegram

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos-bot/internal/receipt"
	"github.com/DeLucca990/argos-bot/internal/store"
)

const (
	defaultRecent = 10
	maxRecent     = 50
	maxDayLines   = 30
)

var sourceLabels = map[store.Source]string{store.SourcePhoto: "foto", store.SourcePDF: "pdf", store.SourceText: "texto"}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func (b *Bot) onCommand(ctx context.Context, msg *models.Message, name, rest string) {
	user, chatID, now := userFrom(ctx), msg.Chat.ID, b.now()
	var (
		text string
		err  error
	)
	switch name {
	case "hoje":
		text, err = b.dayText(ctx, user.ID, now)
	case "semana":
		text, err = b.summaryText(ctx, user.ID, weekPeriod(now))
	case "mes":
		var p period
		if p, err = parseMonth(rest, now); err != nil {
			err = usageError{err.Error()}
			break
		}
		text, err = b.summaryText(ctx, user.ID, p)
	case "ultimos":
		text, err = b.recentText(ctx, user.ID, rest)
	case "desfazer":
		text, err = b.undoText(ctx, user.ID)
		log.Printf("user %d: /desfazer", user.ID)
	case "exportar":
		b.export(ctx, msg, rest, now)
		return
	case "resumo":
		text, err = b.digestCommand(ctx, user.ID, rest, now)
	case "categorias":
		text = categoriesText(b.catList)
	case "cost":
		text, err = b.costText(ctx, now)
	default:
		text = helpText()
	}

	var usage usageError
	switch {
	case errors.As(err, &usage):
		text = "⚠️ " + html.EscapeString(usage.msg)
	case err != nil:
		log.Printf("user %d: /%s: %v", user.ID, name, err)
		text = dbErrorText
	}
	b.sendHTML(ctx, chatID, text)
}

func (b *Bot) export(ctx context.Context, msg *models.Message, arg string, now time.Time) {
	user := userFrom(ctx)
	name, data, rows, err := b.exportCSV(ctx, user.ID, arg, now)
	var usage usageError
	switch {
	case errors.As(err, &usage):
		b.sendHTML(ctx, msg.Chat.ID, "⚠️ "+html.EscapeString(usage.msg))
		return
	case err != nil:
		log.Printf("user %d: /exportar: %v", user.ID, err)
		b.sendHTML(ctx, msg.Chat.ID, dbErrorText)
		return
	case rows == 0:
		b.sendHTML(ctx, msg.Chat.ID, "Nenhum gasto registrado nesse período.")
		return
	}

	caption := fmt.Sprintf("%d gastos", rows)
	if rows == 1 {
		caption = "1 gasto"
	}
	if _, err := b.tg.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID:   msg.Chat.ID,
		Document: &models.InputFileUpload{Filename: name, Data: bytes.NewReader(data)},
		Caption:  caption,
	}); err != nil {
		log.Printf("telegram: sending %s: %v", name, err)
	}
}

func (b *Bot) dayText(ctx context.Context, userID int64, now time.Time) (string, error) {
	p := dayPeriod(now)
	list, err := b.store.Between(ctx, userID, p.from, p.to)
	if err != nil {
		return "", err
	}

	var s strings.Builder
	var total int64
	for _, e := range list {
		total += e.TotalCents
	}
	s.WriteString(header(p.title, total, len(list)))
	for i, e := range list {
		if i == maxDayLines {
			fmt.Fprintf(&s, "… e mais %d\n", len(list)-maxDayLines)
			break
		}
		clock := ""
		if e.SpentTime != "" {
			clock = e.SpentTime + " "
		}
		s.WriteString("• " + clock + b.line(e) + "\n")
	}
	return s.String(), nil
}

func (b *Bot) summaryText(ctx context.Context, userID int64, p period) (string, error) {
	sum, err := b.store.Summary(ctx, userID, p.from, p.to)
	if err != nil {
		return "", err
	}
	var s strings.Builder
	s.WriteString(header(p.title, sum.TotalCents, sum.Count))
	for _, c := range sum.ByCategory {
		fmt.Fprintf(&s, "%s %s — %s (%d%%) · %d\n", c.Category.Emoji, html.EscapeString(c.Category.Label),
			receipt.FormatBRL(c.TotalCents), (c.TotalCents*100+sum.TotalCents/2)/sum.TotalCents, c.Count)
	}
	return s.String(), nil
}

func (b *Bot) recentText(ctx context.Context, userID int64, arg string) (string, error) {
	n := defaultRecent
	if arg = strings.TrimSpace(arg); arg != "" {
		v, err := strconv.Atoi(arg)
		if err != nil || v < 1 {
			return "", usageError{"Diga quantos gastos mostrar, por exemplo /ultimos 20."}
		}
		n = min(v, maxRecent)
	}
	list, err := b.store.Recent(ctx, userID, n)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "Nenhum gasto registrado ainda.", nil
	}

	var s strings.Builder
	fmt.Fprintf(&s, "🧾 <b>Últimos %d gastos</b>\n\n", len(list))
	for _, e := range list {
		s.WriteString("• " + e.SpentOn.Format("02/01") + " " + b.line(e) + "\n")
	}
	return s.String(), nil
}

func (b *Bot) undoText(ctx context.Context, userID int64) (string, error) {
	e, err := b.store.Undo(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return "Não há gasto salvo para desfazer.", nil
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("↩️ Descartei o último gasto salvo: %s, %s em %s.\n\n"+
		"Ele sai dos totais. Para registrá-lo de novo, mande o comprovante outra vez.",
		html.EscapeString(nonEmpty(e.Merchant, e.Description, "gasto")), money(e.TotalCents, e.Currency),
		e.SpentOn.Format("02/01/2006")), nil
}

func (b *Bot) costText(ctx context.Context, now time.Time) (string, error) {
	var s strings.Builder
	s.WriteString("💲 <b>Leituras com o Claude</b> (estimativa a preço de tabela)\n\n")
	today := startOfDay(now)
	var spentToday float64
	for _, w := range []struct {
		label string
		since time.Time
	}{{"Hoje", today}, {"7 dias", today.AddDate(0, 0, -6)}, {"30 dias", today.AddDate(0, 0, -29)}} {
		usd, n, err := b.store.Spent(ctx, w.since)
		if err != nil {
			return "", err
		}
		if w.since.Equal(today) {
			spentToday = usd
		}
		calls, err := b.store.Calls(ctx, w.since)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&s, "%s: US$ %s · %s\n", w.label, dollars(usd, 4), callsText(n, calls))
	}
	s.WriteString("\nModelo: " + html.EscapeString(b.cfg.Model) + ", effort " + html.EscapeString(b.cfg.Effort))
	if b.cfg.DailyBudget > 0 {
		fmt.Fprintf(&s, "\nTeto diário: US$ %s — %d%% usado hoje", dollars(b.cfg.DailyBudget, 2),
			int(spentToday*100/b.cfg.DailyBudget))
	}
	return s.String(), nil
}

func (b *Bot) exportCSV(ctx context.Context, userID int64, arg string, now time.Time) (string, []byte, int, error) {
	p := allTime()
	if strings.ToLower(strings.TrimSpace(arg)) != "tudo" {
		var err error
		if p, err = parseMonth(arg, now); err != nil {
			return "", nil, 0, usageError{err.Error()}
		}
	}
	list, err := b.store.Between(ctx, userID, p.from, p.to)
	if err != nil {
		return "", nil, 0, err
	}

	var buf bytes.Buffer
	buf.WriteString("\ufeff")
	w := csv.NewWriter(&buf)
	w.Comma = ';'
	w.Write([]string{"data", "hora", "estabelecimento", "cnpj_cpf", "categoria", "pagamento", "valor", "moeda",
		"descricao", "origem", "id"})
	for _, e := range list {
		w.Write([]string{
			e.SpentOn.Format("02/01/2006"), e.SpentTime, cell(e.Merchant), e.MerchantTaxID,
			b.cats[e.Category].Label, paymentLabels[e.PaymentMethod], decimal(e.TotalCents), e.Currency,
			cell(e.Description), sourceLabels[e.Source], strconv.FormatInt(e.ID, 10),
		})
	}
	w.Flush()
	return "argos-" + p.slug + ".csv", buf.Bytes(), len(list), w.Error()
}

func (b *Bot) line(e store.Expense) string {
	return b.cats[e.Category].Emoji + " " + html.EscapeString(nonEmpty(e.Merchant, e.Description, "gasto")) +
		" — " + money(e.TotalCents, e.Currency)
}

func header(title string, total int64, count int) string {
	if count == 0 {
		return "📊 <b>" + html.EscapeString(title) + "</b>\nNenhum gasto registrado.\n"
	}
	noun := "gastos"
	if count == 1 {
		noun = "gasto"
	}
	return fmt.Sprintf("📊 <b>%s</b>\n💰 <b>%s</b> em %d %s\n\n", html.EscapeString(title), receipt.FormatBRL(total), count, noun)
}

func decimal(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d,%02d", sign, cents/100, cents%100)
}

// Merchant names and descriptions come off the receipt; one that starts like a formula
// would run as one when the spreadsheet opens.
func cell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func callsText(total int, calls map[string]int) string {
	var parts []string
	for _, k := range []struct{ kind, one, many string }{
		{store.KindReceipt, "leitura", "leituras"},
		{store.KindCorrection, "correção", "correções"},
		{store.KindQuestion, "pergunta", "perguntas"},
	} {
		switch n := calls[k.kind]; {
		case n == 1:
			parts = append(parts, "1 "+k.one)
		case n > 1:
			parts = append(parts, strconv.Itoa(n)+" "+k.many)
		}
	}
	if len(parts) == 0 {
		return strconv.Itoa(total) + " leituras"
	}
	return strings.Join(parts, " · ")
}
