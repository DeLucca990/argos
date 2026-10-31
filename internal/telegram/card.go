package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos-bot/internal/cost"
	"github.com/DeLucca990/argos-bot/internal/receipt"
	"github.com/DeLucca990/argos-bot/internal/store"
)

const (
	callbackPrefix = "ex:"
	maxCardItems   = 10
	maxItemRunes   = 60
)

var paymentLabels = map[string]string{
	"pix": "Pix", "credito": "Crédito", "debito": "Débito", "dinheiro": "Dinheiro",
	"boleto": "Boleto", "vale": "Vale", "outro": "Outro",
}

type reply struct {
	text      string
	keyboard  *models.InlineKeyboardMarkup
	expenseID int64
	silent    bool
}

type details struct {
	warnings   []string
	notes      string
	remembered []string
	statement  string
	spend      cost.Spend
	model      string
}

func (b *Bot) card(ctx context.Context, e store.Expense, picking bool) reply {
	cats, err := b.categories(ctx, e.UserID)
	if err != nil {
		log.Printf("expense %d: %v", e.ID, err)
	}
	var choices catalog
	if picking {
		choices = b.byUse(ctx, e.UserID, cats)
	}
	return reply{text: cardText(e, b.details(ctx, e), cats), keyboard: keyboard(e, picking, choices)}
}

func (b *Bot) details(ctx context.Context, e store.Expense) details {
	var d details
	if t, err := b.store.BankTransactionFor(ctx, e.ID); err == nil {
		d.statement = nonEmpty(t.Description, "—")
	} else if !errors.Is(err, store.ErrNotFound) {
		log.Printf("expense %d: reading its bank transaction: %v", e.ID, err)
	}
	x, err := b.store.ExtractionFor(ctx, e.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("expense %d: reading its extraction: %v", e.ID, err)
		}
		return d
	}
	d.model = x.Model
	d.spend = cost.Spend{Input: x.InputTokens, Output: x.OutputTokens, CacheRead: x.CacheReadTokens,
		CacheWrite: x.CacheWriteTokens, USD: x.USD}
	if total, err := b.store.ExpenseCost(ctx, e.ID); err == nil {
		d.spend = total
	}
	var raw receipt.Raw
	if json.Unmarshal([]byte(x.RawJSON), &raw) == nil {
		if r, err := receipt.Parse(raw, x.CreatedAt.In(time.Local)); err == nil {
			d.warnings, d.notes, d.remembered = r.Warnings, r.Notes, r.Remembered
		}
	}
	return d
}

func cardText(e store.Expense, d details, cats catalog) string {
	var s strings.Builder
	title := e.Merchant
	if title == "" {
		title = e.Description
	}
	if title == "" {
		title = "Gasto"
	}
	icon := "🧾"
	if e.Source == store.SourceBank {
		icon = "🏦"
	}
	s.WriteString(icon + " <b>" + html.EscapeString(title) + "</b>\n")
	switch len(e.MerchantTaxID) {
	case 14:
		s.WriteString("CNPJ " + receipt.FormatTaxID(e.MerchantTaxID) + "\n")
	case 11:
		s.WriteString("CPF " + receipt.FormatTaxID(e.MerchantTaxID) + "\n")
	}

	when := "📅 " + e.SpentOn.Format("02/01/2006")
	if e.SpentTime != "" {
		when += " às " + e.SpentTime
	}
	if paid := paidWith(e); paid != "" {
		when += " · 💳 " + html.EscapeString(paid)
	}
	s.WriteString(when + "\n")

	cat := cats.of(e.Category)
	s.WriteString(cat.Emoji + " " + html.EscapeString(cat.Label) + "\n")
	s.WriteString("💰 <b>" + money(e.TotalCents, e.Currency) + "</b>\n")
	if e.Description != "" && e.Description != title {
		s.WriteString("<i>" + html.EscapeString(e.Description) + "</i>\n")
	}

	if len(e.Items) > 0 {
		s.WriteString("\n")
		for i, it := range e.Items {
			if i == maxCardItems {
				fmt.Fprintf(&s, "… e mais %d itens\n", len(e.Items)-maxCardItems)
				break
			}
			s.WriteString("• " + itemLine(it, e.Currency) + "\n")
		}
	}

	if len(d.warnings) > 0 || d.notes != "" || len(d.remembered) > 0 || d.statement != "" {
		s.WriteString("\n")
		if d.statement != "" {
			s.WriteString("🏦 No extrato: <i>" + html.EscapeString(d.statement) + "</i>\n")
		}
		for _, w := range d.warnings {
			s.WriteString("⚠️ " + html.EscapeString(w) + "\n")
		}
		if d.notes != "" {
			s.WriteString("📝 " + html.EscapeString(d.notes) + "\n")
		}
		if len(d.remembered) > 0 {
			s.WriteString(rememberedText(d.remembered) + "\n")
		}
	}

	s.WriteString("\n")
	switch e.Status {
	case store.Pending:
		s.WriteString("⏳ <b>Pendente</b>: confira e toque em Salvar.")
	case store.Confirmed:
		s.WriteString("✅ <b>Salvo.</b>")
	case store.Discarded:
		s.WriteString("❌ <b>Descartado.</b>")
	}
	if d.model != "" {
		s.WriteString("\n" + usageLine(d.spend, "leitura ("+d.model+")"))
	}
	return s.String()
}

func itemLine(it store.Item, currency string) string {
	desc := []rune(it.Description)
	if len(desc) > maxItemRunes {
		desc = append(desc[:maxItemRunes-1], '…')
	}
	line := html.EscapeString(string(desc)) + " — "
	switch {
	case it.Quantity != 1 && it.UnitCents != 0:
		line += receipt.FormatQuantity(it.Quantity) + " × " + money(it.UnitCents, currency) + " = "
	case it.Quantity != 1:
		line += receipt.FormatQuantity(it.Quantity) + " · "
	}
	return line + money(it.TotalCents, currency)
}

func money(cents int64, currency string) string {
	brl := receipt.FormatBRL(cents)
	if currency == "" || currency == "BRL" {
		return brl
	}
	return strings.Replace(brl, "R$", html.EscapeString(currency), 1)
}

func keyboard(e store.Expense, picking bool, choices catalog) *models.InlineKeyboardMarkup {
	button := func(text, action string) models.InlineKeyboardButton {
		return models.InlineKeyboardButton{Text: text, CallbackData: callbackData(e.ID, action)}
	}

	switch {
	case e.Status == store.Discarded:
		return nil
	case picking:
		var rows [][]models.InlineKeyboardButton
		for i, c := range choices {
			text := c.Emoji + " " + c.Label
			if c.Slug == e.Category {
				text = "✓ " + text
			}
			if i%2 == 0 {
				rows = append(rows, nil)
			}
			rows[len(rows)-1] = append(rows[len(rows)-1], button(text, "c:"+strconv.FormatInt(c.ID, 10)))
		}
		rows = append(rows, []models.InlineKeyboardButton{button("↩️ Voltar", "back")})
		return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
	case e.Status == store.Pending:
		return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			button("✅ Salvar", "ok"), button("🏷️ Categoria", "cat"), button("❌ Descartar", "no"),
		}}}
	default:
		return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			button("🏷️ Categoria", "cat"), button("❌ Descartar", "no"),
		}}}
	}
}

func callbackData(id int64, action string) string {
	return callbackPrefix + strconv.FormatInt(id, 10) + ":" + action
}

func parseCallback(data string) (id int64, action, arg string, ok bool) {
	rest, found := strings.CutPrefix(data, callbackPrefix)
	if !found {
		return 0, "", "", false
	}
	idText, action, found := strings.Cut(rest, ":")
	if !found {
		return 0, "", "", false
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		return 0, "", "", false
	}
	action, arg, _ = strings.Cut(action, ":")
	return id, action, arg, true
}

func duplicateText(e store.Expense) string {
	state := map[store.Status]string{store.Pending: "pendente", store.Confirmed: "salvo", store.Discarded: "descartado"}[e.Status]
	return fmt.Sprintf("🔁 Esse comprovante já foi registrado: %s, %s em %s (%s).",
		html.EscapeString(nonEmpty(e.Merchant, e.Description, "gasto")), money(e.TotalCents, e.Currency),
		e.SpentOn.Format("02/01/2006"), state)
}

func nonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func paidWith(e store.Expense) string {
	var parts []string
	if label, ok := paymentLabels[e.PaymentMethod]; ok {
		parts = append(parts, label)
	}
	account := e.Account
	if e.CardLast4 != "" {
		account = strings.TrimSpace(account + " ···" + e.CardLast4)
	}
	if account != "" {
		parts = append(parts, account)
	}
	return strings.Join(parts, " · ")
}
