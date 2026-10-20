package telegram

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos-bot/internal/ask"
	"github.com/DeLucca990/argos-bot/internal/store"
)

const maxAnswerRunes = 3500

type asker interface {
	Ask(ctx context.Context, userID int64, question string, now time.Time) (ask.Answer, error)
}

var questionStarts = []string{
	"quanto", "quanta", "quantos", "quantas", "qual", "quais", "quando", "onde", "como", "quem",
	"o que", "por que", "porque", "mostra", "mostre", "me mostra", "lista", "liste", "compara", "compare",
	"resume", "resuma",
}

func isQuestion(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if strings.Contains(t, "?") {
		return true
	}
	for _, w := range questionStarts {
		if t == w || strings.HasPrefix(t, w+" ") || strings.HasPrefix(t, w+",") {
			return true
		}
	}
	return false
}

func (b *Bot) onQuestion(ctx context.Context, msg *models.Message) {
	b.process(ctx, msg, "🤔 Pensando…", func(ctx context.Context) reply {
		return b.question(ctx, userFrom(ctx), msg.Text)
	})
}

func (b *Bot) question(ctx context.Context, user store.User, text string) reply {
	now := b.now()
	if r, over := b.overBudget(ctx, now); over {
		return r
	}

	ans, err := b.asker.Ask(ctx, user.ID, text, now)
	if ans.Steps > 0 {
		model := b.cfg.Model
		if len(ans.Spend.Models) > 0 {
			model = ans.Spend.Models[len(ans.Spend.Models)-1]
		}
		b.record(ctx, store.Extraction{
			Kind: store.KindQuestion, UserID: user.ID, Model: model, Failed: err != nil,
			InputTokens: ans.Spend.Input, OutputTokens: ans.Spend.Output, CacheReadTokens: ans.Spend.CacheRead,
			CacheWriteTokens: ans.Spend.CacheWrite, USD: ans.Spend.USD,
		})
	}

	outcome := "answered"
	if err != nil {
		outcome = "failed: " + err.Error()
	}
	log.Printf("user %d: question in %d steps, $%.4f, %s", user.ID, ans.Steps, ans.Spend.USD, outcome)

	switch {
	case errors.Is(err, ask.ErrBusy):
		return reply{text: "Ainda estou na pergunta anterior."}
	case err != nil:
		return reply{text: "⚠️ Não consegui responder: " + questionError(err)}
	}

	answer := []rune(ans.Text)
	if len(answer) > maxAnswerRunes {
		answer = append(answer[:maxAnswerRunes], []rune("…")...)
	}
	out := escape(string(answer)) + "\n\n<i>≈ US$ " + dollars(ans.Spend.USD, 4) + "</i>"
	if ans.Reset {
		out = "<i>(a conversa ficou longa: recomecei do zero)</i>\n\n" + out
	}
	return reply{text: out}
}

func questionError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "declined"):
		return "o modelo se recusou a responder."
	case strings.Contains(msg, "ran out of tokens"):
		return "a resposta ficou longa demais. Tente uma pergunta mais específica."
	case strings.Contains(msg, "no answer after"):
		return "não cheguei a uma resposta. Tente perguntar de outro jeito."
	}
	return "o serviço falhou. Tente de novo em instantes."
}
