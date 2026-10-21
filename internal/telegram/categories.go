package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/DeLucca990/argos-bot/internal/extract"
	"github.com/DeLucca990/argos-bot/internal/store"
)

const (
	maxLabelRunes = 32
	maxHintRunes  = 120
	maxEmojiRunes = 10
	usageDays     = 90
)

const categoryUsage = "/categoria 🏋️ Academia — mensalidade, personal"

type catalog []store.Category

func (b *Bot) categories(ctx context.Context, userID int64) (catalog, error) {
	cats, err := b.store.Categories(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("reading the categories: %w", err)
	}
	return cats, nil
}

func (c catalog) of(slug string) store.Category {
	for _, cat := range c {
		if cat.Slug == slug {
			return cat
		}
	}
	return store.Category{Slug: slug, Label: slug}
}

func (c catalog) has(slug string) bool {
	for _, cat := range c {
		if cat.Slug == slug {
			return true
		}
	}
	return false
}

func (c catalog) byID(id int64) (store.Category, bool) {
	for _, cat := range c {
		if cat.ID == id {
			return cat, true
		}
	}
	return store.Category{}, false
}

func (c catalog) forModel(keep string) []extract.Category {
	out := make([]extract.Category, 0, len(c))
	for _, cat := range c {
		if !cat.Archived || cat.Slug == keep {
			out = append(out, extract.Category{Slug: cat.Slug, Label: cat.Label, Hint: cat.Hint})
		}
	}
	return out
}

func (b *Bot) byUse(ctx context.Context, userID int64, cats catalog) catalog {
	use, err := b.store.CategoryUse(ctx, userID, startOfDay(b.now()).AddDate(0, 0, -usageDays))
	if err != nil {
		log.Printf("user %d: reading which categories are used: %v", userID, err)
	}
	var active catalog
	for _, c := range cats {
		if !c.Archived {
			active = append(active, c)
		}
	}
	sort.SliceStable(active, func(i, j int) bool {
		if last := active[j].Slug == store.FallbackCategory; last != (active[i].Slug == store.FallbackCategory) {
			return last
		}
		return use[active[i].ID] > use[active[j].ID]
	})
	return active
}

func (b *Bot) saveCategory(ctx context.Context, userID int64, arg string) (string, error) {
	c, err := parseCategory(arg)
	if err != nil {
		return "", err
	}
	saved, created, err := b.store.SaveCategory(ctx, userID, c)
	switch {
	case errors.Is(err, store.ErrSharedCategory):
		return saved.Emoji + " <b>" + html.EscapeString(saved.Label) + "</b> é uma categoria padrão, igual para todos, " +
			"e não muda. Para uma sua, use outro nome.", nil
	case errors.Is(err, store.ErrCategoryLimit):
		return "", usageError{fmt.Sprintf("Já são %d categorias, contando as padrão: é o máximo.", store.MaxCategories)}
	case err != nil:
		return "", err
	}

	name := saved.Emoji + " <b>" + html.EscapeString(saved.Label) + "</b>"
	hint := ""
	if saved.Hint != "" {
		hint = "\n<i>" + html.EscapeString(saved.Hint) + "</i>"
	}
	switch {
	case created:
		return "✅ Criei a categoria " + name + "." + hint + "\n\nOs próximos comprovantes já podem cair nela. " +
			"Para mudar a de um gasto, toque em 🏷️ Categoria no cartão.", nil
	case c.Emoji == "" && c.Hint == "":
		return "A categoria " + name + " já existe." + hint + "\n\nPara mudar o emoji ou a dica: " +
			html.EscapeString("/categoria 💪 "+saved.Label+" — o que entra nela"), nil
	}
	return "✏️ Atualizei " + name + "." + hint, nil
}

func parseCategory(arg string) (store.Category, error) {
	usage := usageError{"Mande o emoji, o nome e, se quiser, uma dica do que entra nela: " + categoryUsage}
	name, hint := strings.TrimSpace(arg), ""
	for _, sep := range []string{"—", "–", " - "} {
		if before, after, found := strings.Cut(name, sep); found {
			name, hint = before, after
			break
		}
	}
	name, hint = strings.Join(strings.Fields(name), " "), strings.Join(strings.Fields(hint), " ")

	var emoji string
	first := strings.IndexFunc(name, wordRune)
	if first < 0 {
		return store.Category{}, usage
	}
	if lead := name[:first]; isEmoji(lead) {
		emoji, name = lead, name[first:]
	} else {
		last := strings.LastIndexFunc(name, wordRune)
		_, size := utf8.DecodeRuneInString(name[last:])
		if trail := name[last+size:]; isEmoji(trail) {
			emoji, name = trail, name[:last+size]
		}
	}
	emoji = strings.Join(strings.Fields(emoji), "")
	name = strings.TrimSpace(name)

	switch {
	case len([]rune(emoji)) > maxEmojiRunes:
		return store.Category{}, usageError{"Use um emoji só."}
	case len([]rune(name)) > maxLabelRunes:
		return store.Category{}, usageError{fmt.Sprintf("O nome pode ter até %d caracteres.", maxLabelRunes)}
	case len([]rune(hint)) > maxHintRunes:
		return store.Category{}, usageError{fmt.Sprintf("A dica pode ter até %d caracteres.", maxHintRunes)}
	}
	runes := []rune(name)
	runes[0] = unicode.ToUpper(runes[0])
	return store.Category{Label: string(runes), Emoji: emoji, Hint: hint}, nil
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func isEmoji(s string) bool {
	symbol := false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.So, r):
			symbol = true
		case unicode.In(r, unicode.Sk, unicode.Mn, unicode.Me, unicode.Cf, unicode.Zs):
		default:
			return false
		}
	}
	return symbol
}
