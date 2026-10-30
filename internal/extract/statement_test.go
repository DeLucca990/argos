package extract

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	filed := `{"transactions":[
		{"index":1,"spending":false,"merchant":"Nubank","description":"Pagamento da fatura","category":"outros"},
		{"index":0,"spending":true,"merchant":"Drogaria Boa Saúde","description":"Remédios","category":"farmacia"}]}`
	srv, seen := fake(t, message("claude-opus-5-5", "end_turn", filed))
	lines := []BankLine{
		{Date: "2026-10-19", Amount: "42.30", Currency: "BRL", Description: "DROGARIA BOA SAUDE", Account: "Nubank, cartão de crédito", Method: "crédito"},
		{Date: "2026-10-10", Amount: "1500.00", Currency: "BRL", Description: "Pagamento de fatura", Account: "Nubank, conta corrente"},
	}
	got, res, err := extractor(t, srv, "claude-opus-5-5").Classify(ctx, Statement{Lines: lines, Today: today, Categories: categories})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Spending || got[0].Category != "farmacia" || got[0].Merchant != "Drogaria Boa Saúde" ||
		got[1].Spending || res.Spend.USD == 0 || res.Model != "claude-opus-5-5" {
		t.Errorf("filed %+v, %+v", got, res)
	}

	req := (*seen)[0]
	system := req.body["system"].([]any)[0].(map[string]any)["text"].(string)
	text := req.body["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(system, "paying a credit card bill") || !strings.Contains(text, "2026-10-20") ||
		!strings.Contains(text, `"index": 1`) || !strings.Contains(text, `"description": "Pagamento de fatura"`) ||
		strings.Contains(text, `"installment"`) {
		t.Errorf("system %q\ntext %s", system, text)
	}
	schema := req.body["output_config"].(map[string]any)["format"].(map[string]any)["schema"].(map[string]any)
	item := schema["properties"].(map[string]any)["transactions"].(map[string]any)["items"].(map[string]any)
	category := item["properties"].(map[string]any)["category"].(map[string]any)
	if len(category["enum"].([]any)) != 4 || len(item["required"].([]any)) != 5 {
		t.Errorf("schema: %v", schema)
	}
}

func TestClassifyRefusesAnAnswerThatLeavesOneOut(t *testing.T) {
	srv, _ := fake(t, message("claude-opus-5-5", "end_turn", `{"transactions":[{"index":0,"spending":true,"merchant":"a","description":"b","category":"outros"}]}`))
	lines := []BankLine{{Description: "a"}, {Description: "b"}}
	if _, res, err := extractor(t, srv, "claude-opus-5-5").Classify(ctx, Statement{Lines: lines, Today: today, Categories: categories}); err == nil ||
		!strings.Contains(err.Error(), "left out transaction 2 of 2") || res.Spend.USD == 0 {
		t.Errorf("got %v, spend %+v", err, res.Spend)
	}
}
