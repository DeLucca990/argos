package receipt

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var today = time.Date(2026, 10, 20, 15, 30, 0, 0, time.Local)

func TestParseCents(t *testing.T) {
	for in, want := range map[string]int64{
		"35.90":      3590,
		"35,90":      3590,
		"R$ 35,9":    3590,
		"1234.50":    123450,
		"1.234,56":   123456,
		"1,234.56":   123456,
		"1.234.567":  123456700,
		"7":          700,
		"-2.00":      -200,
		"5.899":      590,
		"0.385":      39,
		"12.500":     1250,
		"19.995 BRL": 2000,
		"1.2345":     123,
	} {
		if got, err := ParseCents(in); err != nil || got != want {
			t.Errorf("ParseCents(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "R$"} {
		if _, err := ParseCents(in); err == nil {
			t.Errorf("ParseCents(%q) accepted", in)
		}
	}
}

func TestFormat(t *testing.T) {
	for cents, want := range map[int64]string{3590: "R$ 35,90", 123456789: "R$ 1.234.567,89", 5: "R$ 0,05", -200: "-R$ 2,00"} {
		if got := FormatBRL(cents); got != want {
			t.Errorf("FormatBRL(%d) = %q, want %q", cents, got, want)
		}
	}
	if got := FormatQuantity(0.385); got != "0,385" {
		t.Errorf("FormatQuantity: %q", got)
	}
	if got := FormatTaxID("11222333000181"); got != "11.222.333/0001-81" {
		t.Errorf("FormatTaxID: %q", got)
	}
}

func TestTaxIDs(t *testing.T) {
	for id, want := range map[string]bool{
		"11222333000181": true,
		"11222333000182": false,
		"12ABC34501DE35": true,
		"12ABC34501DE36": false,
		"11111111111111": false,
	} {
		if got := ValidCNPJ(id); got != want {
			t.Errorf("ValidCNPJ(%s) = %v", id, got)
		}
	}
	for id, want := range map[string]bool{"52998224725": true, "52998224724": false, "11111111111": false} {
		if got := ValidCPF(id); got != want {
			t.Errorf("ValidCPF(%s) = %v", id, got)
		}
	}
	ones := strings.Repeat("1", 43)
	if !ValidNFCeKey(ones+"2") || ValidNFCeKey(ones+"3") || ValidNFCeKey(ones) {
		t.Error("NFC-e check digit")
	}
}

func raw() Raw {
	return Raw{
		IsReceipt: true, Merchant: " Padaria Pão Quente ", MerchantTaxID: "11.222.333/0001-81",
		Date: "2026-10-18", Time: "19:42", Total: "35.90", Currency: "brl", PaymentMethod: "Pix",
		Category: "mercado", NFCeAccessKey: strings.Repeat("1", 43) + "2",
		Items: []RawItem{
			{Description: "Pão francês", Quantity: "0.385", UnitPrice: "19.90", Total: "7.66"},
			{Description: "Leite", Quantity: "2", UnitPrice: "14.12", Total: "28.24"},
			{Description: "", Total: "1.00"},
		},
	}
}

func TestParse(t *testing.T) {
	r, err := Parse(raw(), today)
	if err != nil {
		t.Fatal(err)
	}
	if r.Merchant != "Padaria Pão Quente" || r.MerchantTaxID != "11222333000181" || r.TotalCents != 3590 ||
		r.Currency != "BRL" || r.PaymentMethod != "pix" || r.SpentTime != "19:42" || len(r.NFCeKey) != 44 {
		t.Errorf("parsed: %+v", r)
	}
	if !r.SpentOn.Equal(time.Date(2026, 10, 18, 0, 0, 0, 0, time.Local)) {
		t.Errorf("spent on %v", r.SpentOn)
	}
	if len(r.Items) != 2 || r.Items[0].Quantity != 0.385 || r.Items[0].UnitCents != 1990 {
		t.Errorf("items: %+v", r.Items)
	}
	if len(r.Warnings) != 0 {
		t.Errorf("a clean receipt warned: %v", r.Warnings)
	}
}

func TestParseWarns(t *testing.T) {
	in := raw()
	in.Total = "40.00"
	in.Date = ""
	in.Time = "25:99"
	in.MerchantTaxID = "11.222.333/0001-82"
	in.NFCeAccessKey = strings.Repeat("1", 44)
	in.Currency = "USD"
	in.PaymentMethod = "cheque"
	in.CardLast4 = "12345"

	r, err := Parse(in, today)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(r.Warnings, "\n")
	for _, want := range []string{"não mostra a data", "CNPJ 11.222.333/0001-82", "chave de acesso", "USD", "R$ 35,90", "R$ 40,00"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no warning mentioning %q:\n%s", want, joined)
		}
	}
	if r.NFCeKey != "" || r.SpentTime != "" || r.PaymentMethod != "" || r.CardLast4 != "" {
		t.Errorf("invalid values were kept: key %q, time %q, payment %q", r.NFCeKey, r.SpentTime, r.PaymentMethod)
	}
	if !r.SpentOn.Equal(time.Date(2026, 10, 20, 0, 0, 0, 0, time.Local)) {
		t.Errorf("a missing date should be today: %v", r.SpentOn)
	}

	future := raw()
	future.Date = "2026-11-02"
	if r, _ := Parse(future, today); len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "futuro") {
		t.Errorf("future: %v", r.Warnings)
	}
}

func TestParseRefuses(t *testing.T) {
	var notReceipt *NotReceiptError
	if _, err := Parse(Raw{IsReceipt: false, NotReceiptReason: "é a foto de um gato"}, today); !errors.As(err, &notReceipt) ||
		notReceipt.Reason != "é a foto de um gato" {
		t.Errorf("not a receipt: %v", err)
	}
	for _, total := range []string{"", "0.00", "-5.00"} {
		in := raw()
		in.Total = total
		if _, err := Parse(in, today); !errors.Is(err, ErrNoTotal) {
			t.Errorf("total %q: %v", total, err)
		}
	}
}

func TestParseAccountAndCard(t *testing.T) {
	for card, want := range map[string]string{"1234": "1234", "••••1234": "1234", "final 1234": "1234", " 1234 ": "1234",
		"123": "", "12345": "", "": "", "abcd": ""} {
		in := raw()
		in.PaymentAccount, in.CardLast4 = "  Nubank ", card
		r, err := Parse(in, today)
		if err != nil || r.Account != "Nubank" || r.CardLast4 != want {
			t.Errorf("card %q: account %q, card %q, %v; want %q", card, r.Account, r.CardLast4, err, want)
		}
	}
}

func TestParseKeepsWhatWasRemembered(t *testing.T) {
	raw := Raw{IsReceipt: true, Total: "10.00", Layout: "  tela roxa ", Remembered: []string{"payment_account", "total", "payment_account", "merchant"}}
	r, err := Parse(raw, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	if err != nil || r.Layout != "tela roxa" || strings.Join(r.Remembered, ",") != "payment_account,merchant" {
		t.Errorf("%+v, %v", r, err)
	}
}
