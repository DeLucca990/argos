package telegram

import (
	"html"
	"strings"
	"unicode"

	"github.com/DeLucca990/argos-bot/internal/store"
)

type command struct {
	name        string
	description string
}

var commands = []command{
	{name: "categorias", description: "As categorias de gasto"},
	{name: "help", description: "O que este bot sabe fazer"},
}

func parseCommand(text string) (name, target, rest string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", "", ""
	}
	name = text[1:]
	if i := strings.IndexFunc(name, unicode.IsSpace); i >= 0 {
		name, rest = name[:i], strings.TrimSpace(name[i:])
	}
	name, target, _ = strings.Cut(name, "@")
	return strings.ToLower(name), target, rest
}

func helpText() string {
	var b strings.Builder
	b.WriteString("<b>Argos</b>\n\n")
	b.WriteString("Você está autorizado. Ainda não leio comprovantes nem registro gastos — " +
		"isso chega nas próximas versões.\n\n")
	for _, c := range commands {
		b.WriteString("/" + c.name + " — " + html.EscapeString(c.description) + "\n")
	}
	return b.String()
}

func categoriesText(cats []store.Category) string {
	var b strings.Builder
	b.WriteString("<b>Categorias</b>\n\n")
	for _, c := range cats {
		b.WriteString(c.Emoji + " " + html.EscapeString(c.Label) + "\n")
	}
	return b.String()
}
