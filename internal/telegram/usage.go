package telegram

import (
	"strconv"
	"strings"

	"github.com/DeLucca990/argos/internal/cost"
)

func usageLine(s cost.Spend, label string) string {
	categories := []struct {
		tokens int64
		name   string
	}{
		{s.Input, "entrada"},
		{s.CacheRead, "cache lido"},
		{s.CacheWrite, "cache gravado"},
		{s.Output, "saída"},
	}

	var total int64
	var parts []string
	for _, c := range categories {
		if c.tokens > 0 {
			total += c.tokens
			parts = append(parts, thousands(c.tokens)+" "+c.name)
		}
	}

	line := "≈ US$ " + dollars(s.USD, 4)
	if len(s.Unpriced) > 0 {
		line += " (sem preço para " + strings.Join(s.Unpriced, ", ") + ")"
	}
	if total > 0 {
		line += " · " + thousands(total) + " tokens: " + strings.Join(parts, ", ")
	}
	if label != "" {
		line = label + ": " + line
	}
	return "<i>" + escape(line) + "</i>"
}

func thousands(n int64) string {
	digits := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(d)
	}
	return b.String()
}
