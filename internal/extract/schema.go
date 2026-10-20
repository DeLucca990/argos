package extract

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/DeLucca990/argos-bot/internal/receipt"
)

type Category struct {
	Slug  string
	Label string
}

func schema(categories []Category) (json.RawMessage, error) {
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	object := func(properties map[string]any) map[string]any {
		required := make([]string, 0, len(properties))
		for name := range properties {
			required = append(required, name)
		}
		slices.Sort(required)
		return map[string]any{
			"type":                 "object",
			"properties":           properties,
			"required":             required,
			"additionalProperties": false,
		}
	}

	slugs := make([]string, 0, len(categories))
	labels := make([]string, 0, len(categories))
	for _, c := range categories {
		slugs = append(slugs, c.Slug)
		labels = append(labels, c.Slug+" = "+c.Label)
	}

	item := object(map[string]any{
		"description": str("the line as printed, expanded where an abbreviation is obvious"),
		"quantity":    str(`decimal with a dot, e.g. "2" or "0.385"; "1" when not printed`),
		"unit_price":  str(`decimal with a dot, e.g. "5.899"; "" when not printed`),
		"total":       str(`the line's amount, decimal with a dot and two decimals, e.g. "7.66"; negative for a discount line`),
	})

	return json.Marshal(object(map[string]any{
		"is_receipt":         map[string]any{"type": "boolean", "description": "false when nothing here records a purchase or payment with an amount"},
		"not_receipt_reason": str("when is_receipt is false, why, in one short Brazilian Portuguese sentence; otherwise empty"),
		"merchant":           str("who was paid: the trade name as printed, else the legal name; for a transfer, the recipient"),
		"merchant_tax_id":    str("the merchant's or recipient's CNPJ or CPF, letters and digits only, no punctuation; empty when absent"),
		"date":               str("the date of the purchase or payment, YYYY-MM-DD; empty when unknown"),
		"time":               str("the time of the purchase or payment, HH:MM in 24 hours; empty when not shown"),
		"total":              str(`the amount actually paid, after discounts, decimal with a dot and two decimals and no thousands separator, e.g. "1234.50"`),
		"currency":           str(`ISO 4217 code; "BRL" unless the document shows another currency`),
		"payment_method": map[string]any{
			"type":        "string",
			"enum":        append([]string{""}, receipt.PaymentMethods...),
			"description": "credito for credit card, debito for debit card, vale for meal or food vouchers; empty when not shown",
		},
		"category": map[string]any{
			"type":        "string",
			"enum":        slugs,
			"description": "the best fit among: " + strings.Join(labels, "; "),
		},
		"description":     str("two to five words in Brazilian Portuguese saying what was bought, e.g. \"Almoço\" or \"Compras do mês\""),
		"nfce_access_key": str("the 44-character chave de acesso of an NF-e or NFC-e, without spaces; empty when not printed"),
		"items":           map[string]any{"type": "array", "items": item, "description": "the line items when the document lists them; empty otherwise"},
		"notes":           str("in Brazilian Portuguese, anything you are unsure of — an illegible digit, two candidate totals; empty when nothing"),
	}))
}
