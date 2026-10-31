package telegram

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos/internal/extract"
	"github.com/DeLucca990/argos/internal/store"
)

func TestParseCategory(t *testing.T) {
	for arg, want := range map[string]store.Category{
		"🏋️ Academia — mensalidade, personal": {Label: "Academia", Emoji: "🏋️", Hint: "mensalidade, personal"},
		"academia 🏋️":                         {Label: "Academia", Emoji: "🏋️"},
		"🏋️Academia":                          {Label: "Academia", Emoji: "🏋️"},
		"Pet shop":                            {Label: "Pet shop"},
		"  👨‍👩‍👧  Família –  escola,   mesada ": {Label: "Família", Emoji: "👨‍👩‍👧", Hint: "escola, mesada"},
		"Carro (manutenção) - oficina, peças":   {Label: "Carro (manutenção)", Hint: "oficina, peças"},
		"🇧🇷 Impostos":                           {Label: "Impostos", Emoji: "🇧🇷"},
		"🍕 🍔 Lanches":                           {Label: "Lanches", Emoji: "🍕🍔"},
	} {
		got, err := parseCategory(arg)
		if err != nil || got != want {
			t.Errorf("parseCategory(%q) = %+v, %v; want %+v", arg, got, err, want)
		}
	}

	for _, arg := range []string{"", "🏋️", "— só a dica", strings.Repeat("a", maxLabelRunes+1),
		"Academia — " + strings.Repeat("a", maxHintRunes+1), strings.Repeat("🏋️", 6) + " Academia"} {
		var usage usageError
		if _, err := parseCategory(arg); !errors.As(err, &usage) {
			t.Errorf("parseCategory(%q) was accepted: %v", arg, err)
		}
	}
}

func TestCreatingACategory(t *testing.T) {
	raw := pharmacy()
	raw.Merchant, raw.Category = "Smart Fit", "academia"
	fx := answering(raw)
	b, user := testBot(t, fx)

	text, err := b.saveCategory(ctx, user.ID, "🏋️ academia — mensalidade, personal")
	if err != nil || !strings.Contains(text, "Criei a categoria 🏋️ <b>Academia</b>") || !strings.Contains(text, "mensalidade, personal") {
		t.Fatalf("creating it: %q, %v", text, err)
	}
	if text, err := b.saveCategory(ctx, user.ID, "Academia"); err != nil || !strings.Contains(text, "já existe") {
		t.Errorf("the same name again: %q, %v", text, err)
	}
	if text, err := b.saveCategory(ctx, user.ID, "💪 Academia"); err != nil || !strings.Contains(text, "Atualizei 💪 <b>Academia</b>") ||
		!strings.Contains(text, "mensalidade, personal") {
		t.Errorf("a new emoji: %q, %v", text, err)
	}
	if _, err := b.saveCategory(ctx, user.ID, ""); err == nil {
		t.Error("an empty /categoria was accepted")
	}
	if text, err := b.saveCategory(ctx, user.ID, "🏥 Saúde — consultas"); err != nil || !strings.Contains(text, "🩺 <b>Saúde</b> é uma categoria padrão") {
		t.Errorf("changing a default: %q, %v", text, err)
	}

	list := menuText(t, b, user.ID)
	if !strings.Contains(list, "🩺 Saúde\n") || !strings.Contains(list, "📦 Outros\n\n<b>Suas</b>\n💪 Academia — <i>mensalidade, personal</i>\n") {
		t.Errorf("/categorias:\n%s", list)
	}

	r := b.read(ctx, user, incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto})
	if e, err := b.store.Expense(ctx, 1); err != nil || e.Category != "academia" || !strings.Contains(r.text, "💪 Academia") {
		t.Errorf("a receipt in the new category: %+v, %v\n%s", e, err, r.text)
	}
	last := fx.last.Categories[len(fx.last.Categories)-2]
	if len(fx.last.Categories) != 17 || last.Slug != "academia" || last.Hint != "mensalidade, personal" {
		t.Errorf("the model was given %+v", fx.last.Categories)
	}

	other, err := b.store.SaveUser(ctx, store.User{TelegramID: 7777})
	if err != nil {
		t.Fatal(err)
	}
	if list := menuText(t, b, other.ID); strings.Contains(list, "💪") || strings.Contains(list, "Suas") {
		t.Errorf("someone else's /categorias shows it:\n%s", list)
	}
	fx.res.Raw.MerchantTaxID = ""
	b.read(ctx, other, incoming{text: "academia 90", source: store.SourceText})
	if e, err := b.store.Expense(ctx, 2); err != nil || e.Category != store.FallbackCategory {
		t.Errorf("a category someone else made: %+v, %v", e, err)
	}
}

func TestPickingACategoryByItsID(t *testing.T) {
	b, user := testBot(t, answering(pharmacy()))
	calls := fakeTelegram(t, b)
	b.read(ctx, user, incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto})
	c, _, err := b.store.SaveCategory(ctx, user.ID, store.Category{Label: "Remédios da vó", Emoji: "👵"})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := b.store.SaveUser(ctx, store.User{TelegramID: 7777})
	theirs, _, _ := b.store.SaveCategory(ctx, other.ID, store.Category{Label: "Cassino", Emoji: "🎰"})

	e, _ := b.store.Expense(ctx, 1)
	picker := b.card(ctx, e, true).keyboard.InlineKeyboard
	button := picker[7][1]
	if picker[0][0].Text != "✓ 💊 Farmácia" || picker[0][1].Text != "🛒 Mercado" {
		t.Errorf("the category in use goes first: %+v", picker[0])
	}
	if button.Text != "👵 Remédios da vó" || button.CallbackData != "ex:1:c:"+strconv.FormatInt(c.ID, 10) ||
		picker[8][0].Text != "📦 Outros" || len(picker[9]) != 1 || picker[9][0].CallbackData != "ex:1:back" {
		t.Fatalf("the new category in the picker: %+v", picker[7:])
	}

	press := func(data string) { pressOn(b, user, 10, data) }
	press(button.CallbackData)
	if e, _ := b.store.Expense(ctx, 1); e.Category != c.Slug {
		t.Errorf("after the press: %q", e.Category)
	}
	if got := calls.last("answerCallbackQuery"); got["text"] != "Categoria: Remédios da vó" {
		t.Errorf("the toast: %v", got)
	}
	if got := calls.last("editMessageText"); !strings.Contains(got["text"], "👵 Remédios da vó") {
		t.Errorf("the edited card: %v", got)
	}

	press("ex:1:c:" + strconv.FormatInt(theirs.ID, 10))
	if e, _ := b.store.Expense(ctx, 1); e.Category != c.Slug {
		t.Errorf("someone else's category was applied: %q", e.Category)
	}
	if got := calls.last("answerCallbackQuery"); got["text"] != "Essa categoria não existe mais." {
		t.Errorf("the toast for someone else's category: %v", got)
	}
}

func TestCategoryMenu(t *testing.T) {
	raw := pharmacy()
	raw.Category = "academia"
	fx := answering(raw)
	b, user := testBot(t, fx)
	calls := fakeTelegram(t, b)
	c, _, _ := b.store.SaveCategory(ctx, user.ID, store.Category{Label: "Academia", Emoji: "🏋️", Hint: "mensalidade"})
	id := strconv.FormatInt(c.ID, 10)

	menu, err := b.categoryMenu(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	rows := menu.keyboard.InlineKeyboard
	if len(rows) != 2 || rows[0][0].Text != "🏋️ Academia" || rows[0][0].CallbackData != "cat:"+id+":open" || rows[1][0].CallbackData != "cat:0:new" {
		t.Fatalf("the menu buttons: %+v", rows)
	}

	pressOn(b, user, 50, "cat:"+id+":open")
	if got := calls.last("editMessageText"); !strings.Contains(got["text"], "🏋️ <b>Academia</b>\n<i>mensalidade</i>") ||
		!strings.Contains(got["text"], "0 gastos nela") || !strings.Contains(got["reply_markup"], "cat:"+id+":del") {
		t.Errorf("the detail: %v", got)
	}

	ask := func(action, answer string) map[string]string {
		t.Helper()
		pressOn(b, user, 50, "cat:"+id+":"+action)
		question := calls.last("sendMessage")
		if !strings.Contains(question["reply_markup"], `"force_reply":true`) {
			t.Fatalf("%s: no question was asked: %v", action, question)
		}
		replyTo(b, user, calls.lastID(), answer)
		return calls.last("sendMessage")
	}
	if got := ask("emoji", "💪"); !strings.Contains(got["text"], "Agora é 💪 <b>Academia</b>") {
		t.Errorf("a new emoji: %v", got)
	}
	if got := ask("name", "Mercado"); !strings.Contains(got["text"], "Mercado já é uma categoria padrão") {
		t.Errorf("renaming to a default: %v", got)
	}
	replyTo(b, user, calls.lastID()-1, "musculação")
	if got := calls.last("sendMessage"); !strings.Contains(got["text"], "Agora é 💪 <b>Musculação</b>") {
		t.Errorf("answering the same question again: %v", got)
	}
	if got := calls.last("editMessageText"); !strings.Contains(got["text"], "💪 <b>Musculação</b>") {
		t.Errorf("the menu follows the change: %v", got)
	}
	if got := ask("hint", "-"); !strings.Contains(got["text"], "Apaguei a dica") {
		t.Errorf("clearing the hint: %v", got)
	}
	cats, _ := b.categories(ctx, user.ID)
	if c, _ := cats.byID(c.ID); c.Slug != "musculacao" || c.Emoji != "💪" || c.Hint != "" {
		t.Errorf("after the edits: %+v", c)
	}

	b.read(ctx, user, incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto})
	if e, _ := b.store.Expense(ctx, 1); e.Category != store.FallbackCategory {
		t.Errorf("the model answered the old name, academia: %q", e.Category)
	}
	b.store.SetCategory(ctx, user.ID, 1, "musculacao")

	pressOn(b, user, 50, "cat:"+id+":del")
	if got := calls.last("answerCallbackQuery"); got["text"] != "Removida. Os gastos dela continuam nos relatórios." {
		t.Errorf("removing one with expenses: %v", got)
	}
	if got := calls.last("editMessageText"); strings.Contains(got["text"], "Musculação") {
		t.Errorf("the menu still lists it: %v", got)
	}
	e, _ := b.store.Expense(ctx, 1)
	if e.Category != "musculacao" || !strings.Contains(b.card(ctx, e, false).text, "💪 Musculação") {
		t.Errorf("the expense left its archived category: %+v", e)
	}
	for _, row := range b.card(ctx, e, true).keyboard.InlineKeyboard {
		for _, button := range row {
			if strings.Contains(button.Text, "Musculação") {
				t.Errorf("the picker offers an archived category")
			}
		}
	}
	fx.res.Raw.MerchantTaxID = ""
	b.read(ctx, user, incoming{text: "academia 90", source: store.SourceText})
	for _, mc := range fx.last.Categories {
		if mc.Slug == "musculacao" {
			t.Error("the model is offered an archived category")
		}
	}
	fx.fixed = answer(rawFrom(e), 0)
	b.correct(ctx, user, 1, "foi no débito")
	if keep := fx.corrections[len(fx.corrections)-1].Categories; !slices.ContainsFunc(keep, func(c extract.Category) bool { return c.Slug == "musculacao" }) {
		t.Error("a correction cannot keep the archived category the expense is in")
	}

	if text, _ := b.saveCategory(ctx, user.ID, "Musculação"); !strings.Contains(text, "Criei") {
		t.Errorf("bringing it back: %q", text)
	}

	other, _ := b.store.SaveUser(ctx, store.User{TelegramID: 7777})
	theirs, _, _ := b.store.SaveCategory(ctx, other.ID, store.Category{Label: "Cassino"})
	for _, data := range []string{"cat:" + strconv.FormatInt(theirs.ID, 10) + ":emoji", "cat:6:del"} {
		pressOn(b, user, 50, data)
		if got := calls.last("answerCallbackQuery"); got["text"] != "Essa categoria não existe mais." {
			t.Errorf("%s: %v", data, got)
		}
	}
	if cats, _ := b.categories(ctx, user.ID); len(cats) != 17 {
		t.Errorf("a default was removed: %d categories", len(cats))
	}
}

func TestNewCategoryFromTheList(t *testing.T) {
	b, user := testBot(t, answering(pharmacy()))
	calls := fakeTelegram(t, b)

	pressOn(b, user, 50, "cat:0:new")
	question := calls.last("sendMessage")
	if !strings.Contains(question["text"], "categoria nova") || !strings.Contains(question["reply_markup"], `"force_reply":true`) {
		t.Fatalf("the question: %v", question)
	}
	replyTo(b, user, calls.lastID(), "tênis")
	if got := calls.last("sendMessage"); !strings.Contains(got["text"], "Criei a categoria 🏷️ <b>Tênis</b>") {
		t.Errorf("the answer: %v", got)
	}
	if got := calls.last("editMessageText"); got["message_id"] != "50" || !strings.Contains(got["reply_markup"], `"text":"🏷️ Tênis"`) {
		t.Errorf("the list was not refreshed with it: %v", got)
	}

	pressOn(b, user, 50, "cat:0:new")
	replyTo(b, user, calls.lastID(), "🏋️")
	if got := calls.last("sendMessage"); !strings.Contains(got["text"], "Responda à pergunta de novo") {
		t.Errorf("an answer with no name: %v", got)
	}
	replyTo(b, user, calls.lastID()-1, "🏋️ Academia — mensalidade")
	if cats, _ := b.categories(ctx, user.ID); len(cats) != 18 || cats[16].Slug != "academia" || cats[16].Hint != "mensalidade" {
		t.Errorf("answering the same question again: %+v", cats[15:])
	}

	replyTo(b, user, 1, "/categoria 🎰 Cassino")
	if cats, _ := b.categories(ctx, user.ID); len(cats) != 18 {
		t.Errorf("/categoria still creates one: %d categories", len(cats))
	}

	b.read(ctx, user, incoming{data: photo, mime: "image/jpeg", source: store.SourcePhoto})
	e, _ := b.store.Expense(ctx, 1)
	for _, row := range b.card(ctx, e, true).keyboard.InlineKeyboard {
		for _, button := range row {
			if strings.Contains(button.Text, "Nova") || strings.HasSuffix(button.CallbackData, ":new") {
				t.Errorf("the card's picker offers to create a category: %+v", button)
			}
		}
	}
}

func pressOn(b *Bot, user store.User, messageID int, data string) {
	b.handle(withUser(ctx, user), nil, &models.Update{CallbackQuery: &models.CallbackQuery{ID: "q", From: models.User{ID: user.TelegramID},
		Data: data, Message: models.MaybeInaccessibleMessage{Message: &models.Message{ID: messageID, Chat: models.Chat{ID: user.TelegramID}}}}})
}

func replyTo(b *Bot, user store.User, messageID int, text string) {
	b.handle(withUser(ctx, user), nil, &models.Update{Message: &models.Message{ID: 9000, Text: text,
		Chat: models.Chat{ID: user.TelegramID, Type: models.ChatTypePrivate}, From: &models.User{ID: user.TelegramID},
		ReplyToMessage: &models.Message{ID: messageID, Chat: models.Chat{ID: user.TelegramID}}}})
}

func menuText(t *testing.T, b *Bot, userID int64) string {
	t.Helper()
	r, err := b.categoryMenu(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	return r.text
}

type telegramCalls struct {
	mu    sync.Mutex
	calls map[string][]map[string]string
	next  int
}

func (c *telegramCalls) last(method string) map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if list := c.calls[method]; len(list) > 0 {
		return list[len(list)-1]
	}
	return nil
}

func (c *telegramCalls) lastID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.next
}

func fakeTelegram(t *testing.T, b *Bot) *telegramCalls {
	t.Helper()
	calls := &telegramCalls{calls: map[string][]map[string]string{}, next: 100}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := path.Base(r.URL.Path)
		fields := map[string]string{}
		if err := r.ParseMultipartForm(1 << 20); err == nil {
			for k, v := range r.MultipartForm.Value {
				fields[k] = v[0]
			}
		}
		calls.mu.Lock()
		calls.calls[method] = append(calls.calls[method], fields)
		id := fields["message_id"]
		if method == "sendMessage" {
			calls.next++
			id = strconv.Itoa(calls.next)
		}
		calls.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if method == "answerCallbackQuery" {
			io.WriteString(w, `{"ok":true,"result":true}`)
			return
		}
		io.WriteString(w, `{"ok":true,"result":{"message_id":`+id+`,"date":0,"chat":{"id":1,"type":"private"}}}`)
	}))
	t.Cleanup(srv.Close)
	tg, err := bot.New("123:abc", bot.WithServerURL(srv.URL), bot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	b.tg = tg
	return calls
}
