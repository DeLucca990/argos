package ask

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/DeLucca990/argos/internal/store"
)

var (
	ctx = context.Background()
	now = time.Date(2026, 10, 20, 15, 0, 0, 0, time.Local)
)

type fakeAPI struct {
	mu        sync.Mutex
	responses []string
	requests  []map[string]any
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(raw, &body)
	f.requests = append(f.requests, body)
	w.Header().Set("Content-Type", "application/json")
	if len(f.responses) == 0 {
		w.WriteHeader(500)
		io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"no more scripted responses"}}`)
		return
	}
	next := f.responses[0]
	f.responses = f.responses[1:]
	io.WriteString(w, next)
}

func toolUse(name, input string) string {
	return `{"id":"msg_t","type":"message","role":"assistant","model":"claude-opus-5-5",
"content":[{"type":"tool_use","id":"toolu_` + name + `","name":"` + name + `","input":` + input + `}],
"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":2000,"output_tokens":100}}`
}

func text(answer string) string {
	body, _ := json.Marshal(answer)
	return `{"id":"msg_a","type":"message","role":"assistant","model":"claude-opus-5-5",
"content":[{"type":"text","text":` + string(body) + `}],
"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":2500,"output_tokens":80}}`
}

func refusal() string {
	return `{"id":"msg_r","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],
"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"cyber","explanation":""},
"usage":{"input_tokens":100,"output_tokens":1}}`
}

func setup(t *testing.T, responses ...string) (*Asker, *fakeAPI, int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "argos.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	user, _ := st.SaveUser(ctx, store.User{TelegramID: 42})
	other, _ := st.SaveUser(ctx, store.User{TelegramID: 7})
	for _, c := range []struct {
		user int64
		cat  store.Category
	}{{user.ID, store.Category{Label: "Academia", Emoji: "🏋️", Hint: "mensalidade, personal"}}, {other.ID, store.Category{Label: "Cassino"}}} {
		if _, _, err := st.SaveCategory(ctx, c.user, c.cat); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []struct {
		user                   int64
		on, merchant, category string
		cents                  int64
		items                  []store.Item
		account, card          string
	}{
		{user.ID, "2026-09-02", "Supermercado Extra", "mercado", 25000, []store.Item{{Description: "Cerveja Heineken", Quantity: 6, TotalCents: 3300}}, "Nubank", "1234"},
		{user.ID, "2026-09-29", "Extra Hiper", "mercado", 12000, nil, "Inter", ""},
		{user.ID, "2026-09-15", "Padaria", "restaurante", 1800, nil, "", ""},
		{user.ID, "2026-10-05", "Posto Shell", "combustivel", 20000, nil, "", ""},
		{user.ID, "2026-10-10", "Smart Fit", "academia", 9000, nil, "", ""},
		{other.ID, "2026-09-10", "De outra pessoa", "mercado", 99999, nil, "Nubank", ""},
	} {
		day, _ := time.ParseInLocation("2006-01-02", e.on, time.Local)
		if _, err := st.AddExpense(ctx, store.Expense{UserID: e.user, SpentOn: day, Merchant: e.merchant, Category: e.category,
			TotalCents: e.cents, Source: store.SourcePhoto, Items: e.items, Account: e.account, CardLast4: e.card,
			Status: store.Confirmed}); err != nil {
			t.Fatal(err)
		}
	}
	api := &fakeAPI{responses: responses}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return New("claude-opus-5-5", "low", st, option.WithBaseURL(srv.URL), option.WithAPIKey("test"), option.WithMaxRetries(0)), api, user.ID
}

func lastToolResult(t *testing.T, req map[string]any) (string, bool) {
	t.Helper()
	msgs := req["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if last["type"] != "tool_result" {
		t.Fatalf("the last message is %v, not a tool result", last["type"])
	}
	content := last["content"].([]any)[0].(map[string]any)["text"].(string)
	isError, _ := last["is_error"].(bool)
	return content, isError
}

func TestAskRunsTheToolsAndAnswers(t *testing.T) {
	a, api, user := setup(t,
		toolUse("spending_totals", `{"group_by":"category","from":"2026-09-01","to":"2026-09-30"}`),
		text("Em setembro você gastou R$ 388,00, a maior parte no mercado (R$ 370,00)."),
	)

	ans, err := a.Ask(ctx, user, " quanto gastei em setembro, por categoria? ", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ans.Text, "R$ 388,00") || ans.Steps != 2 || ans.Spend.Input != 4500 || ans.Spend.USD <= 0 {
		t.Errorf("answer: %+v", ans)
	}

	result, isError := lastToolResult(t, api.requests[1])
	for _, want := range []string{`"total":"R$ 388,00"`, `"count":3`, `"key":"Mercado","total":"R$ 370,00","count":2,"share":"95%"`,
		`"period":"2026-09-01 to 2026-09-30"`} {
		if isError || !strings.Contains(result, want) {
			t.Errorf("the tool result lacks %s:\n%s", want, result)
		}
	}
	if strings.Contains(result, "999") {
		t.Errorf("someone else's expense reached the tool result: %s", result)
	}

	first := api.requests[0]
	question := first["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if question != "Hoje é terça-feira, 2026-10-20.\n\nquanto gastei em setembro, por categoria?" {
		t.Errorf("the question went as %q", question)
	}
	system := first["system"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(system, "mercado = Mercado") || len(first["tools"].([]any)) != 3 || first["fallbacks"] != "default" {
		t.Errorf("system, tools or fallbacks: %v tools, fallbacks %v", len(first["tools"].([]any)), first["fallbacks"])
	}
	if first["cache_control"] == nil {
		t.Error("the request is not cached")
	}
}

func TestToolsFilterAndRefuse(t *testing.T) {
	a, api, user := setup(t,
		toolUse("list_expenses", `{"order_by":"total","limit":2}`),
		toolUse("search_items", `{"text":"cerveja"}`),
		toolUse("spending_totals", `{"group_by":"none","from":"setembro"}`),
		toolUse("delete_everything", `{}`),
		text("ok"),
	)
	if _, err := a.Ask(ctx, user, "quais foram os maiores?", now); err != nil {
		t.Fatal(err)
	}

	listed, _ := lastToolResult(t, api.requests[1])
	if !strings.Contains(listed, `"merchant":"Supermercado Extra"`) || !strings.Contains(listed, `"merchant":"Posto Shell"`) ||
		strings.Contains(listed, "Padaria") || !strings.Contains(listed, `"truncated":true`) {
		t.Errorf("list_expenses by total, 2 of 5:\n%s", listed)
	}
	items, _ := lastToolResult(t, api.requests[2])
	if !strings.Contains(items, `"item":"Cerveja Heineken"`) || !strings.Contains(items, `"sum":"R$ 33,00"`) {
		t.Errorf("search_items:\n%s", items)
	}
	if msg, isError := lastToolResult(t, api.requests[3]); !isError || !strings.Contains(msg, "YYYY-MM-DD") {
		t.Errorf("a bad date: %q, error %v", msg, isError)
	}
	if msg, isError := lastToolResult(t, api.requests[4]); !isError || !strings.Contains(msg, "no tool") {
		t.Errorf("an unknown tool: %q, error %v", msg, isError)
	}
}

func TestAFailedTurnIsTakenBack(t *testing.T) {
	a, api, user := setup(t, refusal(), text("R$ 200,00"), text("R$ 388,00"))

	if _, err := a.Ask(ctx, user, "primeira?", now); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("a refusal: %v", err)
	}
	if _, err := a.Ask(ctx, user, "quanto em outubro?", now); err != nil {
		t.Fatal(err)
	}
	if n := len(api.requests[1]["messages"].([]any)); n != 1 {
		t.Errorf("the refused turn stayed in the history: %d messages", n)
	}

	if _, err := a.Ask(ctx, user, "e em setembro?", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := len(api.requests[2]["messages"].([]any)); n != 3 {
		t.Errorf("a follow-up should carry the conversation: %d messages", n)
	}
}

func TestTheConversationEndsWhenIdle(t *testing.T) {
	a, api, user := setup(t, text("a"), text("b"))
	a.Ask(ctx, user, "primeira?", now)
	a.Ask(ctx, user, "depois de uma hora?", now.Add(time.Hour))
	if n := len(api.requests[1]["messages"].([]any)); n != 1 {
		t.Errorf("an idle conversation was kept: %d messages", n)
	}
}

func TestToolsByAccount(t *testing.T) {
	a, api, user := setup(t,
		toolUse("spending_totals", `{"group_by":"account","from":"2026-09-01","to":"2026-09-30"}`),
		toolUse("list_expenses", `{"account":"nubank"}`),
		text("ok"),
	)
	if _, err := a.Ask(ctx, user, "quanto foi em cada conta?", now); err != nil {
		t.Fatal(err)
	}
	totals, _ := lastToolResult(t, api.requests[1])
	for _, want := range []string{`"key":"Nubank","total":"R$ 250,00"`, `"key":"Inter","total":"R$ 120,00"`, `"key":"não informada","total":"R$ 18,00"`} {
		if !strings.Contains(totals, want) {
			t.Errorf("by account lacks %s:\n%s", want, totals)
		}
	}
	listed, _ := lastToolResult(t, api.requests[2])
	if !strings.Contains(listed, `"account":"Nubank","card_last4":"1234"`) || strings.Contains(listed, "Extra Hiper") || strings.Contains(listed, "999") {
		t.Errorf("filtered by account:\n%s", listed)
	}
}

func TestToolsTakeTheUsersOwnCategories(t *testing.T) {
	a, api, user := setup(t,
		toolUse("spending_totals", `{"group_by":"none","category":"academia"}`),
		toolUse("spending_totals", `{"group_by":"none","category":"cassino"}`),
		text("ok"),
	)
	if _, err := a.Ask(ctx, user, "quanto gastei com academia?", now); err != nil {
		t.Fatal(err)
	}

	first := api.requests[0]
	system := first["system"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(system, "academia = Academia (mensalidade, personal)") || strings.Contains(system, "cassino") {
		t.Errorf("the categories in the system prompt:\n%s", system)
	}
	var enum []any
	for _, tl := range first["tools"].([]any) {
		props := tl.(map[string]any)["input_schema"].(map[string]any)["properties"].(map[string]any)
		enum = props["category"].(map[string]any)["enum"].([]any)
	}
	if !slices.Contains(enum, any("academia")) || slices.Contains(enum, any("cassino")) || len(enum) != 17 {
		t.Errorf("the category enum: %v", enum)
	}

	if totals, _ := lastToolResult(t, api.requests[1]); !strings.Contains(totals, `"total":"R$ 90,00","count":1`) {
		t.Errorf("their own category:\n%s", totals)
	}
	if msg, isError := lastToolResult(t, api.requests[2]); !isError || !strings.Contains(msg, `no category "cassino"`) {
		t.Errorf("someone else's category: %q, error %v", msg, isError)
	}
}
