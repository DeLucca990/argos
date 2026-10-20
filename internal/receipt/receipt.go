package receipt

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

var PaymentMethods = []string{"pix", "credito", "debito", "dinheiro", "boleto", "vale", "outro"}

var ErrNoTotal = errors.New("no total")

type NotReceiptError struct{ Reason string }

func (e *NotReceiptError) Error() string { return "not a receipt: " + e.Reason }

type Raw struct {
	IsReceipt        bool      `json:"is_receipt"`
	NotReceiptReason string    `json:"not_receipt_reason"`
	Merchant         string    `json:"merchant"`
	MerchantTaxID    string    `json:"merchant_tax_id"`
	Date             string    `json:"date"`
	Time             string    `json:"time"`
	Total            string    `json:"total"`
	Currency         string    `json:"currency"`
	PaymentMethod    string    `json:"payment_method"`
	PaymentAccount   string    `json:"payment_account"`
	CardLast4        string    `json:"card_last4"`
	Category         string    `json:"category"`
	Description      string    `json:"description"`
	NFCeAccessKey    string    `json:"nfce_access_key"`
	Items            []RawItem `json:"items"`
	Notes            string    `json:"notes"`
}

type RawItem struct {
	Description string `json:"description"`
	Quantity    string `json:"quantity"`
	UnitPrice   string `json:"unit_price"`
	Total       string `json:"total"`
}

type Receipt struct {
	Merchant      string
	MerchantTaxID string
	SpentOn       time.Time
	SpentTime     string
	TotalCents    int64
	Currency      string
	PaymentMethod string
	Account       string
	CardLast4     string
	Category      string
	Description   string
	NFCeKey       string
	Items         []Item
	Notes         string
	Warnings      []string
}

type Item struct {
	Description string
	Quantity    float64
	UnitCents   int64
	TotalCents  int64
}

func Parse(raw Raw, today time.Time) (Receipt, error) {
	if !raw.IsReceipt {
		return Receipt{}, &NotReceiptError{Reason: strings.TrimSpace(raw.NotReceiptReason)}
	}
	total, err := ParseCents(raw.Total)
	if err != nil || total <= 0 {
		return Receipt{}, ErrNoTotal
	}

	r := Receipt{
		Merchant:    strings.TrimSpace(raw.Merchant),
		TotalCents:  total,
		Currency:    strings.ToUpper(strings.TrimSpace(raw.Currency)),
		Category:    strings.TrimSpace(raw.Category),
		Description: strings.TrimSpace(raw.Description),
		Notes:       strings.TrimSpace(raw.Notes),
	}
	r.parseDate(raw.Date, raw.Time, today)
	r.parseTaxID(raw.MerchantTaxID)
	r.parseNFCeKey(raw.NFCeAccessKey)
	r.parseItems(raw.Items)

	if r.Currency == "" {
		r.Currency = "BRL"
	} else if r.Currency != "BRL" {
		r.warn("O valor está em %s: guardei como está, sem converter para reais.", r.Currency)
	}
	if m := strings.ToLower(strings.TrimSpace(raw.PaymentMethod)); slices.Contains(PaymentMethods, m) {
		r.PaymentMethod = m
	}
	r.Account = strings.TrimSpace(raw.PaymentAccount)
	if card := strings.Map(func(c rune) rune {
		if c >= '0' && c <= '9' {
			return c
		}
		return -1
	}, raw.CardLast4); len(card) == 4 {
		r.CardLast4 = card
	}
	return r, nil
}

func (r *Receipt) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

func (r *Receipt) parseDate(date, clock string, today time.Time) {
	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location())
	on, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(date), today.Location())
	switch {
	case strings.TrimSpace(date) == "":
		on = day
		r.warn("O comprovante não mostra a data: usei hoje, %s.", day.Format("02/01/2006"))
	case err != nil:
		on = day
		r.warn("Não entendi a data %q: usei hoje, %s.", date, day.Format("02/01/2006"))
	case on.After(day):
		r.warn("A data %s é no futuro: confira.", on.Format("02/01/2006"))
	case on.Before(day.AddDate(-1, 0, 0)):
		r.warn("A data %s tem mais de um ano: confira.", on.Format("02/01/2006"))
	}
	r.SpentOn = on

	if t, err := time.Parse("15:04", strings.TrimSpace(clock)); err == nil {
		r.SpentTime = t.Format("15:04")
	}
}

func (r *Receipt) parseTaxID(raw string) {
	id := normalizeID(raw)
	switch {
	case id == "":
	case len(id) == 14 && ValidCNPJ(id), len(id) == 11 && ValidCPF(id):
		r.MerchantTaxID = id
	case len(id) == 14:
		r.MerchantTaxID = id
		r.warn("Os dígitos verificadores do CNPJ %s não conferem: talvez tenha sido lido errado.", FormatTaxID(id))
	case len(id) == 11:
		r.MerchantTaxID = id
		r.warn("Os dígitos verificadores do CPF %s não conferem: talvez tenha sido lido errado.", FormatTaxID(id))
	}
}

func (r *Receipt) parseNFCeKey(raw string) {
	key := normalizeID(raw)
	switch {
	case key == "":
	case ValidNFCeKey(key):
		r.NFCeKey = key
	default:
		r.warn("A chave de acesso da nota não confere, então ela não vai servir para detectar este comprovante se ele for enviado de novo.")
	}
}

func (r *Receipt) parseItems(raw []RawItem) {
	var sum int64
	for _, it := range raw {
		desc := strings.TrimSpace(it.Description)
		total, err := ParseCents(it.Total)
		if desc == "" || err != nil {
			continue
		}
		item := Item{Description: desc, Quantity: ParseQuantity(it.Quantity), TotalCents: total}
		if unit, err := ParseCents(it.UnitPrice); err == nil {
			item.UnitCents = unit
		}
		r.Items = append(r.Items, item)
		sum += total
	}

	if diff := sum - r.TotalCents; len(r.Items) > 0 && (diff > 1 || diff < -1) {
		r.warn("A soma dos itens (%s) é diferente do total (%s): pode ser desconto, taxa ou um valor lido errado.",
			FormatBRL(sum), FormatBRL(r.TotalCents))
	}
}
