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

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos/internal/store"
)

const (
	categoryPrefix = "cat:"
	maxPending     = 200
)

type pending struct {
	kind      string
	category  int64
	chatID    int64
	messageID int
}

type pendingKey struct {
	chatID    int64
	messageID int
}

func categoryCallback(id int64, action string) string {
	return categoryPrefix + strconv.FormatInt(id, 10) + ":" + action
}

func (b *Bot) categoryMenu(ctx context.Context, userID int64) (reply, error) {
	cats, err := b.categories(ctx, userID)
	if err != nil {
		return reply{}, err
	}
	var shared, own strings.Builder
	var buttons []models.InlineKeyboardButton
	for _, c := range cats {
		if c.Archived {
			continue
		}
		line := c.Emoji + " " + html.EscapeString(c.Label)
		if c.Hint != "" {
			line += " — <i>" + html.EscapeString(c.Hint) + "</i>"
		}
		if !c.Custom {
			shared.WriteString(line + "\n")
			continue
		}
		own.WriteString(line + "\n")
		buttons = append(buttons, models.InlineKeyboardButton{Text: c.Emoji + " " + c.Label, CallbackData: categoryCallback(c.ID, "open")})
	}

	text := "<b>Categorias</b>\n\n" + shared.String()
	if own.Len() > 0 {
		text += "\n<b>Suas</b>\n" + own.String() + "\nToque numa das suas para mudar o nome, o emoji ou a dica, ou para removê-la. "
	} else {
		text += "\n"
	}
	text += "Para criar uma sua, toque em ➕ Nova categoria."

	var rows [][]models.InlineKeyboardButton
	for i, button := range buttons {
		if i%2 == 0 {
			rows = append(rows, nil)
		}
		rows[len(rows)-1] = append(rows[len(rows)-1], button)
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: "➕ Nova categoria", CallbackData: categoryCallback(0, "new")}})
	return reply{text: text, keyboard: &models.InlineKeyboardMarkup{InlineKeyboard: rows}}, nil
}

func (b *Bot) categoryDetail(ctx context.Context, userID int64, c store.Category) (reply, error) {
	far := time.Date(9999, 1, 1, 0, 0, 0, 0, time.Local)
	groups, err := b.store.Totals(ctx, userID, store.Filter{To: far, Category: c.Slug}, "none")
	if err != nil {
		return reply{}, err
	}
	count := 0
	for _, g := range groups {
		count += g.Count
	}

	text := c.Emoji + " <b>" + html.EscapeString(c.Label) + "</b>\n"
	if c.Hint != "" {
		text += "<i>" + html.EscapeString(c.Hint) + "</i>\n"
	} else {
		text += "Sem dica: o modelo só tem o nome para saber o que entra nela.\n"
	}
	text += fmt.Sprintf("\n%d %s nela. Remover a tira das opções; os gastos que já estão nela continuam nos relatórios.",
		count, plural(count, "gasto", "gastos"))

	button := func(label, action string) models.InlineKeyboardButton {
		return models.InlineKeyboardButton{Text: label, CallbackData: categoryCallback(c.ID, action)}
	}
	return reply{text: text, keyboard: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{button("✏️ Nome", "name"), button("😀 Emoji", "emoji"), button("💡 Dica", "hint")},
		{button("🗑️ Remover", "del"), {Text: "↩️ Voltar", CallbackData: categoryCallback(0, "list")}},
	}}}, nil
}

func (b *Bot) onCategoryCallback(ctx context.Context, q *models.CallbackQuery) {
	user := userFrom(ctx)
	idText, action, found := strings.Cut(strings.TrimPrefix(q.Data, categoryPrefix), ":")
	id, err := strconv.ParseInt(idText, 10, 64)
	if !found || err != nil {
		b.answer(ctx, q, "Botão desconhecido.")
		return
	}
	chatID, messageID := callbackTarget(q)
	back := pending{chatID: chatID, messageID: messageID}

	if action == "new" {
		back.kind = "new"
		b.prompt(ctx, chatID, newCategoryQuestion, categoryExample, back)
		b.answer(ctx, q, "")
		return
	}
	cats, err := b.categories(ctx, user.ID)
	if err != nil {
		log.Printf("user %d: %v", user.ID, err)
		b.answer(ctx, q, dbErrorText)
		return
	}
	c, ok := cats.byID(id)
	if action != "list" && (!ok || !c.Custom || c.Archived) {
		b.refreshMenu(ctx, user.ID, back, 0)
		b.answer(ctx, q, "Essa categoria não existe mais.")
		return
	}

	toast := ""
	back.category = c.ID
	switch action {
	case "list":
		b.refreshMenu(ctx, user.ID, back, 0)
	case "open":
		b.refreshMenu(ctx, user.ID, back, c.ID)
	case "name":
		back.kind = action
		b.prompt(ctx, chatID, "Qual o novo nome de "+c.Emoji+" <b>"+html.EscapeString(c.Label)+"</b>?", c.Label, back)
	case "emoji":
		back.kind = action
		b.prompt(ctx, chatID, "Mande o novo emoji de <b>"+html.EscapeString(c.Label)+"</b>.", "💪", back)
	case "hint":
		back.kind = action
		current := ""
		if c.Hint != "" {
			current = "\nHoje: <i>" + html.EscapeString(c.Hint) + "</i>"
		}
		b.prompt(ctx, chatID, "O que entra em "+c.Emoji+" <b>"+html.EscapeString(c.Label)+"</b>? "+
			"Mande a nova dica, ou um traço (-) para apagá-la."+current, "mensalidade, personal", back)
	case "del":
		archived, err := b.store.RemoveCategory(ctx, user.ID, c.ID)
		if err != nil {
			log.Printf("user %d: removing category %d: %v", user.ID, c.ID, err)
			b.answer(ctx, q, dbErrorText)
			return
		}
		log.Printf("user %d: category %d removed, archived %v", user.ID, c.ID, archived)
		toast = "Removida."
		if archived {
			toast = "Removida. Os gastos dela continuam nos relatórios."
		}
		b.refreshMenu(ctx, user.ID, back, 0)
	default:
		toast = "Botão desconhecido."
	}
	b.answer(ctx, q, toast)
}

func (b *Bot) refreshMenu(ctx context.Context, userID int64, at pending, categoryID int64) {
	if at.messageID == 0 {
		return
	}
	var (
		r   reply
		err error
	)
	if categoryID == 0 {
		r, err = b.categoryMenu(ctx, userID)
	} else {
		var cats catalog
		if cats, err = b.categories(ctx, userID); err == nil {
			c, _ := cats.byID(categoryID)
			r, err = b.categoryDetail(ctx, userID, c)
		}
	}
	if err != nil {
		log.Printf("user %d: the categories menu: %v", userID, err)
		return
	}
	b.edit(ctx, at.chatID, at.messageID, r)
}

const (
	newCategoryQuestion = "Mande o emoji e o nome da categoria nova e, se quiser, depois de um travessão, " +
		"o que entra nela. Por exemplo: <i>" + categoryExample + "</i>"
)

func (b *Bot) prompt(ctx context.Context, chatID int64, text, placeholder string, p pending) {
	sent, err := b.tg.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: &models.ForceReply{ForceReply: true, InputFieldPlaceholder: placeholder},
	})
	if err != nil {
		log.Printf("telegram: sending to chat %d: %v", chatID, err)
		return
	}
	b.pendingMu.Lock()
	defer b.pendingMu.Unlock()
	if b.pending == nil || len(b.pending) >= maxPending {
		b.pending = map[pendingKey]pending{}
	}
	b.pending[pendingKey{chatID, sent.ID}] = p
}

func (b *Bot) pendingFor(msg *models.Message) (pendingKey, pending, bool) {
	r := msg.ReplyToMessage
	if r == nil || msg.Text == "" {
		return pendingKey{}, pending{}, false
	}
	key := pendingKey{msg.Chat.ID, r.ID}
	b.pendingMu.Lock()
	defer b.pendingMu.Unlock()
	p, ok := b.pending[key]
	return key, p, ok
}

func (b *Bot) onPending(ctx context.Context, msg *models.Message, key pendingKey, p pending) {
	user := userFrom(ctx)
	text, err := b.answerPending(ctx, user.ID, strings.TrimSpace(msg.Text), p)
	var usage usageError
	switch {
	case errors.As(err, &usage):
		text = "⚠️ " + html.EscapeString(usage.msg) + "\n\nResponda à pergunta de novo, ou siga com outra coisa."
	case err != nil:
		log.Printf("user %d: answering about a category: %v", user.ID, err)
		text = dbErrorText
		fallthrough
	default:
		b.pendingMu.Lock()
		delete(b.pending, key)
		b.pendingMu.Unlock()
	}
	b.sendHTML(ctx, msg.Chat.ID, text)
}

func (b *Bot) answerPending(ctx context.Context, userID int64, text string, p pending) (string, error) {
	if p.kind == "new" {
		out, err := b.saveCategory(ctx, userID, text)
		if err == nil {
			b.refreshMenu(ctx, userID, p, 0)
		}
		return out, err
	}

	cats, err := b.categories(ctx, userID)
	if err != nil {
		return "", err
	}
	c, ok := cats.byID(p.category)
	if !ok || !c.Custom || c.Archived {
		return "Essa categoria não existe mais.", nil
	}
	switch p.kind {
	case "name":
		parsed, err := parseCategory(text)
		if err != nil {
			return "", usageError{"Mande o novo nome, com letras."}
		}
		c.Label = parsed.Label
		if parsed.Emoji != "" {
			c.Emoji = parsed.Emoji
		}
	case "emoji":
		emoji := strings.Join(strings.Fields(text), "")
		if !isEmoji(emoji) || len([]rune(emoji)) > maxEmojiRunes {
			return "", usageError{"Mande só o emoji, como 💪."}
		}
		c.Emoji = emoji
	case "hint":
		hint := strings.Join(strings.Fields(text), " ")
		if hint == "-" || hint == "—" || hint == "–" {
			hint = ""
		}
		if len([]rune(hint)) > maxHintRunes {
			return "", usageError{fmt.Sprintf("A dica pode ter até %d caracteres.", maxHintRunes)}
		}
		c.Hint = hint
	}

	saved, err := b.store.UpdateCategory(ctx, userID, c)
	switch {
	case errors.Is(err, store.ErrSharedCategory):
		return "", usageError{c.Label + " já é uma categoria padrão. Escolha outro nome."}
	case errors.Is(err, store.ErrCategoryExists):
		return "", usageError{"Você já tem uma categoria chamada " + c.Label + "."}
	case err != nil:
		return "", err
	}
	b.refreshMenu(ctx, userID, p, saved.ID)

	name := saved.Emoji + " <b>" + html.EscapeString(saved.Label) + "</b>"
	switch {
	case p.kind != "hint":
		return "✏️ Agora é " + name + ".", nil
	case saved.Hint == "":
		return "✏️ Apaguei a dica de " + name + ".", nil
	}
	return "✏️ Dica de " + name + ": <i>" + html.EscapeString(saved.Hint) + "</i>", nil
}
