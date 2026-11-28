package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/DeLucca990/argos/internal/receipt"
	"github.com/DeLucca990/argos/internal/store"
)

const (
	monthlyCatchUpDays = 7
	weeklyCatchUp      = 48 * time.Hour
	digestTop          = 5
	digestTick         = 10 * time.Minute
)

type digestRun struct {
	name string
	kind store.DigestKind
	key  string
	p    period
	text func(ctx context.Context, userID int64) (string, int, error)
}

type digestSpan struct {
	cur, prev      period
	title, against string
	days           int
}

func monthlyDue(now time.Time, at DailyAt) (period, bool) {
	if now.Day() > monthlyCatchUpDays {
		return period{}, false
	}
	if now.Day() == 1 && now.Hour()*60+now.Minute() < at.Hour*60+at.Minute {
		return period{}, false
	}
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	return monthPeriod(first.AddDate(0, -1, 0)), true
}

func weeklyDue(now time.Time, at DailyAt) (period, bool) {
	monday := startOfDay(now).AddDate(0, 0, -((int(now.Weekday()) + 6) % 7))
	due := time.Date(monday.Year(), monday.Month(), monday.Day()+6, at.Hour, at.Minute, 0, 0, now.Location())
	if now.Before(due) {
		monday, due = monday.AddDate(0, 0, -7), due.AddDate(0, 0, -7)
	}
	if now.Sub(due) >= weeklyCatchUp {
		return period{}, false
	}
	return weekPeriod(monday), true
}

func (b *Bot) runDigests(ctx context.Context) {
	t := time.NewTicker(digestTick)
	defer t.Stop()
	for {
		b.sendDigests(ctx, b.now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (b *Bot) sendDigests(ctx context.Context, now time.Time) {
	if at := b.cfg.MonthlyDigestAt; at != nil {
		if p, due := monthlyDue(now, *at); due {
			b.deliverDigests(ctx, digestRun{name: "digest", kind: store.MonthDigest, key: p.slug, p: p,
				text: func(ctx context.Context, userID int64) (string, int, error) {
					return b.monthDigest(ctx, userID, p, now)
				}})
		}
	}
	if at := b.cfg.WeeklyDigestAt; at != nil {
		if p, due := weeklyDue(now, *at); due {
			b.deliverDigests(ctx, digestRun{name: "weekly digest", kind: store.WeekDigest, key: p.from.Format("2006-01-02"), p: p,
				text: func(ctx context.Context, userID int64) (string, int, error) { return b.weekDigest(ctx, userID, p) }})
		}
	}
}

func (b *Bot) deliverDigests(ctx context.Context, run digestRun) {
	users, err := b.store.Users(ctx)
	if err != nil {
		log.Printf("%s: listing the users: %v", run.name, err)
		return
	}
	for _, u := range users {
		if !b.cfg.AllowedUsers[u.TelegramID] {
			continue
		}
		if sent, err := b.store.DigestSent(ctx, u.ID, run.kind, run.key); err != nil || sent {
			if err != nil {
				log.Printf("%s: user %d, %s: %v", run.name, u.ID, run.key, err)
			}
			continue
		}

		text, count, err := run.text(ctx, u.ID)
		if err != nil {
			log.Printf("%s: user %d, %s: %v", run.name, u.ID, run.key, err)
			continue
		}
		outcome := "nothing to send, no expenses"
		if count > 0 {
			err := b.notify(ctx, u.TelegramID, text)
			switch {
			case errors.Is(err, bot.ErrorForbidden):
				outcome = "not delivered, the user blocked the bot"
			case err != nil:
				log.Printf("%s: user %d, %s: sending it: %v", run.name, u.ID, run.key, err)
				continue
			default:
				outcome = fmt.Sprintf("sent, %d expenses", count)
				b.digestChart(ctx, u, run.p)
			}
		}
		if err := b.store.MarkDigestSent(ctx, u.ID, run.kind, run.key); err != nil {
			log.Printf("%s: user %d, %s: recording it: %v", run.name, u.ID, run.key, err)
			continue
		}
		log.Printf("%s: user %d, %s: %s", run.name, u.ID, run.key, outcome)
	}
}

func (b *Bot) digestCommand(ctx context.Context, userID int64, arg string, now time.Time) (reply, error) {
	var p period
	if strings.TrimSpace(arg) == "" {
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		p = monthPeriod(first.AddDate(0, -1, 0))
	} else {
		var err error
		if p, err = parseMonth(arg, now); err != nil {
			return reply{}, usageError{err.Error()}
		}
	}
	text, count, err := b.monthDigest(ctx, userID, p, now)
	r := reply{text: text}
	if err == nil && count > 0 {
		r = b.withChart(ctx, userID, r, "pace", p)
	}
	return r, err
}

func (b *Bot) monthDigest(ctx context.Context, userID int64, p period, now time.Time) (string, int, error) {
	prevFirst := p.from.AddDate(0, -1, 0)
	span := digestSpan{cur: p, prev: monthPeriod(prevFirst), title: "Resumo de " + strings.ToLower(p.title[:1]) + p.title[1:],
		against: "em " + monthNames[prevFirst.Month()-1], days: int(p.to.Sub(p.from).Hours()/24 + 0.5)}
	if today := startOfDay(now); today.Before(p.to) && !today.Before(p.from) {
		span.days = int(today.Sub(p.from).Hours()/24+0.5) + 1
		span.cur.to = today.AddDate(0, 0, 1)
		span.prev.to = minTime(prevFirst.AddDate(0, 0, span.days), span.prev.to)
		span.title = p.title + ", até o dia " + fmt.Sprint(today.Day())
		span.against = "no mesmo período de " + monthNames[prevFirst.Month()-1]
	}
	return b.digest(ctx, userID, span)
}

func (b *Bot) weekDigest(ctx context.Context, userID int64, p period) (string, int, error) {
	return b.digest(ctx, userID, digestSpan{cur: p, prev: period{from: p.from.AddDate(0, 0, -7), to: p.from},
		title:   "Resumo da semana de " + p.from.Format("02/01") + " a " + p.to.AddDate(0, 0, -1).Format("02/01"),
		against: "na semana anterior", days: 7})
}

func (b *Bot) digest(ctx context.Context, userID int64, span digestSpan) (string, int, error) {
	p, prev, title, against, days := span.cur, span.prev, span.title, span.against, span.days
	cats, err := b.store.Totals(ctx, userID, store.Filter{From: p.from, To: p.to}, "category")
	if err != nil {
		return "", 0, err
	}
	total, count := sumGroups(cats)
	var s strings.Builder
	s.WriteString("📅 <b>" + html.EscapeString(title) + "</b>\n\n")
	if count == 0 {
		s.WriteString("Nenhum gasto registrado.")
		return s.String(), 0, nil
	}

	prevCats, err := b.store.Totals(ctx, userID, store.Filter{From: prev.from, To: prev.to}, "category")
	if err != nil {
		return "", 0, err
	}
	prevTotal, _ := sumGroups(prevCats)
	before := map[string]int64{}
	for _, g := range prevCats {
		before[g.Key] = g.TotalCents
	}

	fmt.Fprintf(&s, "💰 <b>%s</b> em %d %s · média de %s por dia\n", receipt.FormatBRL(total), count,
		plural(count, "gasto", "gastos"), receipt.FormatBRL(total/int64(max(days, 1))))
	switch delta := total - prevTotal; {
	case prevTotal == 0:
		s.WriteString("Sem gastos " + html.EscapeString(against) + " para comparar.\n")
	case delta > 0:
		fmt.Fprintf(&s, "↗️ %s a mais que %s (+%d%%)\n", receipt.FormatBRL(delta), html.EscapeString(against), percent(delta, prevTotal))
	case delta < 0:
		fmt.Fprintf(&s, "↘️ %s a menos que %s (−%d%%)\n", receipt.FormatBRL(-delta), html.EscapeString(against), percent(-delta, prevTotal))
	default:
		s.WriteString("O mesmo que " + html.EscapeString(against) + ".\n")
	}

	categories, err := b.categories(ctx, userID)
	if err != nil {
		return "", 0, err
	}
	byLabel := map[string]store.Category{}
	for _, c := range categories {
		byLabel[c.Label] = c
	}
	s.WriteString("\n<b>Por categoria</b>\n")
	for _, g := range cats {
		fmt.Fprintf(&s, "%s %s — %s (%d%%)", byLabel[g.Key].Emoji, html.EscapeString(g.Key), receipt.FormatBRL(g.TotalCents),
			percent(g.TotalCents, total))
		switch was, delta := before[g.Key], g.TotalCents-before[g.Key]; {
		case was == 0:
			s.WriteString(" · novo")
		case delta > 0:
			s.WriteString(" · ↗️ +" + receipt.FormatBRL(delta))
		case delta < 0:
			s.WriteString(" · ↘️ −" + receipt.FormatBRL(-delta))
		}
		s.WriteString("\n")
	}

	top, err := b.store.Find(ctx, userID, store.Filter{From: p.from, To: p.to}, true, digestTop)
	if err != nil {
		return "", 0, err
	}
	s.WriteString("\n<b>Os maiores</b>\n")
	for _, e := range top {
		s.WriteString("• " + e.SpentOn.Format("02/01") + " " + line(e, categories) + "\n")
	}

	pays, err := b.store.Totals(ctx, userID, store.Filter{From: p.from, To: p.to}, "payment_method")
	if err != nil {
		return "", 0, err
	}
	var parts []string
	for _, g := range pays {
		label := paymentLabels[g.Key]
		if label == "" {
			label = "não informado"
		}
		parts = append(parts, label+" "+receipt.FormatBRL(g.TotalCents))
	}
	s.WriteString("\n<b>Pagamento</b>\n" + strings.Join(parts, " · "))

	accounts, err := b.store.Totals(ctx, userID, store.Filter{From: p.from, To: p.to}, "account")
	if err != nil {
		return "", 0, err
	}
	parts, known := nil, false
	for _, g := range accounts {
		label := g.Key
		if label == "" {
			label = "não informada"
		} else {
			known = true
		}
		parts = append(parts, html.EscapeString(label)+" "+receipt.FormatBRL(g.TotalCents))
	}
	if known {
		s.WriteString("\n\n<b>Contas</b>\n" + strings.Join(parts, " · "))
	}
	return s.String(), count, nil
}

func (b *Bot) deliver(ctx context.Context, chatID int64, text string) error {
	disabled := true
	_, err := b.tg.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &disabled},
	})
	return err
}

func sumGroups(groups []store.Group) (int64, int) {
	var total int64
	count := 0
	for _, g := range groups {
		total, count = total+g.TotalCents, count+g.Count
	}
	return total, count
}

func percent(part, whole int64) int64 { return (part*100 + whole/2) / max(whole, 1) }

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
