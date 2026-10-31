package telegram

import (
	"testing"

	"github.com/DeLucca990/argos-bot/internal/cost"
)

func TestUsageLine(t *testing.T) {
	s := cost.Spend{Input: 1020, CacheRead: 12100, Output: 2114, USD: 0.01234}
	if got, want := usageLine(s, ""), "<i>≈ US$ 0,0123 · 15.234 tokens: 1.020 entrada, 12.100 cache lido, 2.114 saída</i>"; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	s = cost.Spend{Input: 5, CacheWrite: 1000000, Output: 5, Unpriced: []string{"claude-future-9"}}
	if got, want := usageLine(s, "leitura (claude-future-9)"), "<i>leitura (claude-future-9): ≈ US$ 0,0000 (sem preço para claude-future-9) · 1.000.010 tokens: 5 entrada, 1.000.000 cache gravado, 5 saída</i>"; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
