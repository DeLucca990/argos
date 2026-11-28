package telegram

import (
	"bytes"
	"context"
	"html"
	"log"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos/internal/chart"
	"github.com/DeLucca990/argos/internal/store"
)

type picture struct {
	png     []byte
	caption string
}

func (b *Bot) deliverPhoto(ctx context.Context, chatID int64, p picture) error {
	_, err := b.tg.SendPhoto(ctx, &bot.SendPhotoParams{
		ChatID: chatID, Photo: &models.InputFileUpload{Filename: "grafico.png", Data: bytes.NewReader(p.png)},
		Caption: p.caption, ParseMode: models.ParseModeHTML,
	})
	return err
}

func (b *Bot) send(ctx context.Context, chatID int64, r reply) {
	b.show(ctx, chatID, 0, r)
	b.postPicture(ctx, chatID, r.photo)
}

func (b *Bot) postPicture(ctx context.Context, chatID int64, p *picture) {
	if p == nil {
		return
	}
	if err := b.sendPhoto(ctx, chatID, *p); err != nil {
		log.Printf("telegram: sending a chart to chat %d: %v", chatID, err)
	}
}

func chartTitle(shape string, p period) string {
	if shape != "pace" {
		return p.title
	}
	title := strings.ToLower(p.title[:1]) + p.title[1:]
	if strings.HasPrefix(title, "semana") {
		return "Ritmo da " + title
	}
	return "Ritmo de " + title
}

func (b *Bot) periodChart(ctx context.Context, userID int64, shape string, p period) (picture, error) {
	c, err := chart.Spending(ctx, b.store, userID, chart.Request{Filter: store.Filter{From: p.from, To: p.to},
		GroupBy: shape, Title: chartTitle(shape, p)}, b.now())
	if err != nil {
		return picture{}, err
	}
	return picture{png: c.PNG, caption: "📊 " + html.EscapeString(c.Title)}, nil
}

func (b *Bot) withChart(ctx context.Context, userID int64, r reply, shape string, p period) reply {
	pic, err := b.periodChart(ctx, userID, shape, p)
	if err != nil {
		log.Printf("user %d: the %s chart of %s: %v", userID, shape, p.title, err)
		return r
	}
	r.photo = &pic
	return r
}

func (b *Bot) digestChart(ctx context.Context, u store.User, p period) {
	pic, err := b.periodChart(ctx, u.ID, "pace", p)
	if err == nil {
		err = b.sendPhoto(ctx, u.TelegramID, pic)
	}
	if err != nil {
		log.Printf("digest: user %d, %s: the chart: %v", u.ID, p.slug, err)
	}
}
