package telegram

import (
	"context"
	"errors"
	"log"
	"strconv"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos/internal/store"
)

func (b *Bot) onCallback(ctx context.Context, q *models.CallbackQuery) {
	user := userFrom(ctx)
	id, action, arg, ok := parseCallback(q.Data)
	if !ok {
		b.answer(ctx, q, "Botão desconhecido.")
		return
	}

	var (
		e       store.Expense
		err     error
		toast   string
		picking bool
	)
	switch action {
	case "ok":
		e, err = b.store.Confirm(ctx, user.ID, id)
		toast = "Salvo."
	case "no":
		e, err = b.store.Discard(ctx, user.ID, id)
		toast = "Descartado."
	case "cat", "back":
		e, err = b.owned(ctx, user.ID, id)
		picking = action == "cat"
	case "c":
		var cats catalog
		if cats, err = b.categories(ctx, user.ID); err != nil {
			break
		}
		n, _ := strconv.ParseInt(arg, 10, 64)
		c, ok := cats.byID(n)
		if !ok {
			err = store.ErrUnknownCategory
			break
		}
		e, err = b.store.SetCategory(ctx, user.ID, id, c.Slug)
		toast = "Categoria: " + c.Label
	default:
		b.answer(ctx, q, "Botão desconhecido.")
		return
	}

	switch {
	case errors.Is(err, store.ErrNotPending):
		toast = "Esse gasto já foi decidido."
		e, err = b.owned(ctx, user.ID, id)
	case errors.Is(err, store.ErrUnknownCategory):
		toast = "Essa categoria não existe mais."
		e, err = b.owned(ctx, user.ID, id)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		b.answer(ctx, q, "Não encontrei esse gasto.")
		return
	case err != nil:
		log.Printf("user %d: expense %d, %s: %v", user.ID, id, action, err)
		b.answer(ctx, q, dbErrorText)
		return
	}

	if action == "ok" || action == "no" {
		log.Printf("user %d: expense %d %s", user.ID, id, e.Status)
	}
	chatID, messageID := callbackTarget(q)
	b.edit(ctx, chatID, messageID, b.card(ctx, e, picking && e.Status != store.Discarded))
	b.answer(ctx, q, toast)
}

func (b *Bot) owned(ctx context.Context, userID, id int64) (store.Expense, error) {
	e, err := b.store.Expense(ctx, id)
	if err == nil && e.UserID != userID {
		return store.Expense{}, store.ErrNotFound
	}
	return e, err
}

func callbackTarget(q *models.CallbackQuery) (chatID int64, messageID int) {
	switch m := q.Message; {
	case m.Message != nil:
		return m.Message.Chat.ID, m.Message.ID
	case m.InaccessibleMessage != nil:
		return m.InaccessibleMessage.Chat.ID, m.InaccessibleMessage.MessageID
	}
	return q.From.ID, 0
}

func (b *Bot) answer(ctx context.Context, q *models.CallbackQuery, text string) {
	if _, err := b.tg.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: text}); err != nil {
		log.Printf("telegram: answering a button press: %v", err)
	}
}
