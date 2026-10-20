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
	usage       string
}

var commands = []command{
	{name: "hoje", description: "Os gastos de hoje"},
	{name: "semana", description: "Esta semana, por categoria"},
	{name: "mes", description: "O mês, por categoria", usage: "[2026-10 | outubro]"},
	{name: "ultimos", description: "Os últimos gastos", usage: "[quantos]"},
	{name: "desfazer", description: "Descarta o último gasto salvo"},
	{name: "exportar", description: "O mês numa planilha CSV", usage: "[2026-10 | tudo]"},
	{name: "resumo", description: "O resumo de um mês, comparado ao anterior", usage: "[mês]"},
	{name: "categorias", description: "As categorias de gasto"},
	{name: "cost", description: "Quanto as leituras custaram"},
	{name: "help", description: "O que este bot sabe fazer"},
}

func (c command) menuDescription() string {
	if c.usage == "" {
		return c.description
	}
	return c.description + " · " + c.usage
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
	b.WriteString("Mande um comprovante e eu registro o gasto:\n" +
		"• a foto de um cupom, nota ou comprovante\n" +
		"• um PDF, como a nota fiscal ou o comprovante do banco\n" +
		"• ou uma linha de texto: <i>almoço 35,90 no pix</i>\n\n" +
		"Eu salvo na hora e mostro o que li. Para corrigir, responda ao cartão: <i>foi 45,90 no crédito</i>.\n\n" +
		"E pergunte o que quiser sobre os gastos: <i>quanto gastei com mercado em setembro?</i>, " +
		"<i>qual foi meu maior gasto da semana?</i>\n\n")
	for _, c := range commands {
		line := "/" + c.name
		if c.usage != "" {
			line += " " + c.usage
		}
		b.WriteString(html.EscapeString(line) + " — " + html.EscapeString(c.description) + "\n")
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
