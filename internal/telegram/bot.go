package telegram

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos-bot/internal/store"
)

type Bot struct {
	cfg   Config
	store *store.Store
	tg    *bot.Bot
}

func New(cfg Config, st *store.Store) (*Bot, error) {
	b := &Bot{cfg: cfg, store: st}

	tg, err := bot.New(cfg.Token,
		bot.WithMiddlewares(b.authorize, b.remember),
		bot.WithDefaultHandler(b.handle),
		bot.WithAllowedUpdates(bot.AllowedUpdates{"message"}),
		bot.WithErrorsHandler(onError),
	)
	if err != nil {
		return nil, fmt.Errorf("telegram: %w (is %s the token @BotFather gave?)", err, TokenEnv)
	}
	b.tg = tg
	return b, nil
}

func (b *Bot) Run(ctx context.Context) {
	var cmds []models.BotCommand
	for _, c := range commands {
		cmds = append(cmds, models.BotCommand{Command: c.name, Description: c.description})
	}
	if _, err := b.tg.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: cmds}); err != nil {
		log.Printf("telegram: could not publish the command list: %v", err)
	}

	log.Printf("telegram bot running for %d user(s), data in %s", len(b.cfg.AllowedUsers), b.cfg.DataDir)
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
		msg := u.Message
		if msg == nil || msg.From == nil {
			return
		}
		if chat := msg.Chat; chat.Type != models.ChatTypePrivate {
			log.Printf("telegram: ignored a message in %s chat %d (%q): only private chats are answered",
				chat.Type, chat.ID, chat.Title)
			return
		}
		if !b.cfg.AllowedUsers[msg.From.ID] {
			log.Printf("telegram: ignored a message from user %d, who is not in %s", msg.From.ID, AllowedUsersEnv)
			return
		}
		next(ctx, tg, u)
	}
}

func (b *Bot) remember(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, tg *bot.Bot, u *models.Update) {
		from := u.Message.From
		user, err := b.store.SaveUser(ctx, store.User{
			TelegramID:   from.ID,
			FirstName:    from.FirstName,
			LastName:     from.LastName,
			Username:     from.Username,
			LanguageCode: from.LanguageCode,
		})
		if err != nil {
			log.Printf("telegram user %d: saving their profile: %v", from.ID, err)
			b.sendHTML(ctx, u.Message.Chat.ID, dbErrorText)
			return
		}
		next(withUser(ctx, user), tg, u)
	}
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
	chatID := u.Message.Chat.ID
	switch name, _, _ := parseCommand(u.Message.Text); name {
	case "categorias":
		cats, err := b.store.Categories(ctx)
		if err != nil {
			log.Printf("chat %d: categories: %v", chatID, err)
			b.sendHTML(ctx, chatID, dbErrorText)
			return
		}
		b.sendHTML(ctx, chatID, categoriesText(cats))
	default:
		b.sendHTML(ctx, chatID, helpText())
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
