package telegram

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos-bot/internal/extract"
	"github.com/DeLucca990/argos-bot/internal/store"
)

const fallbackCategory = "outros"

type Bot struct {
	cfg       Config
	store     *store.Store
	extractor extractor
	tg        *bot.Bot

	cats    map[string]store.Category
	catList []store.Category
	slots   chan struct{}
	now     func() time.Time
}

func New(cfg Config, st *store.Store) (*Bot, error) {
	b := &Bot{cfg: cfg, store: st, slots: make(chan struct{}, maxConcurrency), now: time.Now}
	if err := b.loadCategories(context.Background()); err != nil {
		return nil, err
	}

	var cats []extract.Category
	for _, c := range b.catList {
		cats = append(cats, extract.Category{Slug: c.Slug, Label: c.Label})
	}
	x, err := extract.New(cfg.Model, cfg.Effort, cats)
	if err != nil {
		return nil, err
	}
	b.extractor = x

	if err := os.MkdirAll(filepath.Join(cfg.DataDir, receiptsDir), 0o750); err != nil {
		return nil, err
	}

	tg, err := bot.New(cfg.Token,
		bot.WithMiddlewares(b.authorize, b.remember),
		bot.WithDefaultHandler(b.handle),
		bot.WithAllowedUpdates(bot.AllowedUpdates{"message", "callback_query"}),
		bot.WithErrorsHandler(onError),
	)
	if err != nil {
		return nil, fmt.Errorf("telegram: %w (is %s the token @BotFather gave?)", err, TokenEnv)
	}
	b.tg = tg
	return b, nil
}

func (b *Bot) loadCategories(ctx context.Context) error {
	cats, err := b.store.Categories(ctx)
	if err != nil {
		return fmt.Errorf("reading the categories: %w", err)
	}
	b.catList, b.cats = cats, map[string]store.Category{}
	for _, c := range cats {
		b.cats[c.Slug] = c
	}
	if _, ok := b.cats[fallbackCategory]; !ok {
		return fmt.Errorf("the categories have no %q, which a receipt that fits none of them goes to", fallbackCategory)
	}
	return nil
}

func (b *Bot) Run(ctx context.Context) {
	var cmds []models.BotCommand
	for _, c := range commands {
		cmds = append(cmds, models.BotCommand{Command: c.name, Description: c.menuDescription()})
	}
	if _, err := b.tg.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: cmds}); err != nil {
		log.Printf("telegram: could not publish the command list: %v", err)
	}

	budget := "no daily budget"
	if b.cfg.DailyBudget > 0 {
		budget = fmt.Sprintf("a daily budget of $%.2f", b.cfg.DailyBudget)
	}
	log.Printf("telegram bot running for %d user(s), data in %s, reading receipts with %s at effort %s, %s",
		len(b.cfg.AllowedUsers), b.cfg.DataDir, b.cfg.Model, b.cfg.Effort, budget)
	go b.heartbeat(ctx)
	b.tg.Start(ctx)
}

func onError(err error) {
	if errors.Is(err, bot.ErrorConflict) {
		log.Printf("telegram: %v — another process is polling this token; "+
			"%s must belong to this bot alone", err, TokenEnv)
		return
	}
	log.Printf("telegram: %v", err)
}

func (b *Bot) authorize(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, tg *bot.Bot, u *models.Update) {
		var from int64
		var chat *models.Chat
		switch {
		case u.Message != nil && u.Message.From != nil:
			from, chat = u.Message.From.ID, &u.Message.Chat
		case u.CallbackQuery != nil:
			from = u.CallbackQuery.From.ID
			if m := u.CallbackQuery.Message.Message; m != nil {
				chat = &m.Chat
			}
		default:
			return
		}
		if chat != nil && chat.Type != models.ChatTypePrivate {
			log.Printf("telegram: ignored an update in %s chat %d (%q): only private chats are answered",
				chat.Type, chat.ID, chat.Title)
			return
		}
		if !b.cfg.AllowedUsers[from] {
			log.Printf("telegram: ignored an update from user %d, who is not in %s", from, AllowedUsersEnv)
			return
		}
		next(ctx, tg, u)
	}
}

func (b *Bot) remember(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, tg *bot.Bot, u *models.Update) {
		from := sender(u)
		user, err := b.store.SaveUser(ctx, store.User{
			TelegramID:   from.ID,
			FirstName:    from.FirstName,
			LastName:     from.LastName,
			Username:     from.Username,
			LanguageCode: from.LanguageCode,
		})
		if err != nil {
			log.Printf("telegram user %d: saving their profile: %v", from.ID, err)
			if u.CallbackQuery != nil {
				b.answer(ctx, u.CallbackQuery, dbErrorText)
			} else {
				b.sendHTML(ctx, u.Message.Chat.ID, dbErrorText)
			}
			return
		}
		next(withUser(ctx, user), tg, u)
	}
}

func sender(u *models.Update) *models.User {
	if u.CallbackQuery != nil {
		return &u.CallbackQuery.From
	}
	return u.Message.From
}

type userKey struct{}

func withUser(ctx context.Context, u store.User) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

func userFrom(ctx context.Context) store.User {
	u, _ := ctx.Value(userKey{}).(store.User)
	return u
}

const dbErrorText = "⚠️ Não consegui acessar o banco de dados."

func (b *Bot) handle(ctx context.Context, _ *bot.Bot, u *models.Update) {
	if u.CallbackQuery != nil {
		b.onCallback(ctx, u.CallbackQuery)
		return
	}

	msg := u.Message
	name, _, rest := parseCommand(msg.Text)
	if name != "" {
		b.onCommand(ctx, msg, name, rest)
		return
	}
	if id, ok := b.cardFor(ctx, msg); ok {
		b.onCorrection(ctx, msg, id)
		return
	}
	switch {
	case len(msg.Photo) > 0 || msg.Document != nil:
		b.onFile(ctx, msg)
	case hasDigit(msg.Text):
		b.onText(ctx, msg)
	default:
		b.sendHTML(ctx, msg.Chat.ID, helpText())
	}
}

func (b *Bot) sendHTML(ctx context.Context, chatID int64, text string) {
	disabled := true
	if _, err := b.tg.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:             chatID,
		Text:               text,
		ParseMode:          models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &disabled},
	}); err != nil {
		log.Printf("telegram: sending to chat %d: %v", chatID, err)
	}
}

func (b *Bot) replyTo(ctx context.Context, msg *models.Message, text string) int {
	sent, err := b.tg.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          msg.Chat.ID,
		Text:            text,
		ReplyParameters: &models.ReplyParameters{MessageID: msg.ID, AllowSendingWithoutReply: true},
	})
	if err != nil {
		log.Printf("telegram: sending to chat %d: %v", msg.Chat.ID, err)
		return 0
	}
	return sent.ID
}

func (b *Bot) show(ctx context.Context, chatID int64, messageID int, r reply) int {
	if messageID != 0 && b.edit(ctx, chatID, messageID, r) {
		return messageID
	}
	disabled := true
	params := &bot.SendMessageParams{
		ChatID:             chatID,
		Text:               r.text,
		ParseMode:          models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &disabled},
	}
	if r.keyboard != nil {
		params.ReplyMarkup = r.keyboard
	}
	sent, err := b.tg.SendMessage(ctx, params)
	if err != nil {
		log.Printf("telegram: sending to chat %d: %v", chatID, err)
		return 0
	}
	return sent.ID
}

func (b *Bot) edit(ctx context.Context, chatID int64, messageID int, r reply) bool {
	disabled := true
	params := &bot.EditMessageTextParams{
		ChatID:             chatID,
		MessageID:          messageID,
		Text:               r.text,
		ParseMode:          models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &disabled},
	}
	if r.keyboard != nil {
		params.ReplyMarkup = r.keyboard
	}
	_, err := b.tg.EditMessageText(ctx, params)
	if err != nil && !strings.Contains(err.Error(), "message is not modified") {
		log.Printf("telegram: editing message %d in chat %d: %v", messageID, chatID, err)
		return false
	}
	return true
}

func (b *Bot) typing(ctx context.Context, chatID int64) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		for {
			b.tg.SendChatAction(ctx, &bot.SendChatActionParams{ChatID: chatID, Action: models.ChatActionTyping})
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return cancel
}
