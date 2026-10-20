package cost

import (
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

type price struct {
	input, output, cacheRead float64
}

// USD per million tokens, list prices as of 2026-10-17.
var prices = map[string]price{
	"claude-fable-5-1":  {10, 50, 0.25},
	"claude-mythos-5-1": {10, 50, 0.25},
	"claude-fable-5":    {10, 50, 1.00},
	"claude-mythos-5":   {10, 50, 1.00},
	"claude-opus-5-5":   {4, 20, 0.20},
	"claude-opus-5":     {5, 25, 0.50},
	"claude-opus-4-8":   {5, 25, 0.50},
	"claude-opus-4-7":   {5, 25, 0.50},
	"claude-opus-4-6":   {5, 25, 0.50},
	"claude-opus-4-5":   {5, 25, 0.50},
	"claude-sonnet-5-5": {2, 10, 0.10},
	"claude-sonnet-5":   {2, 10, 0.20},
	"claude-sonnet-4-6": {3, 15, 0.30},
	"claude-sonnet-4-5": {3, 15, 0.30},
	"claude-haiku-5-5":  {0.10, 0.50, 0.01},
	"claude-haiku-4-5":  {1, 5, 0.10},
}

var longPrompts = map[string]struct {
	over  int64
	price price
}{
	"claude-haiku-5-5": {100_000, price{0.50, 2.50, 0.05}},
}

func priceOf(model string, prompt int64) (price, bool) {
	best := ""
	for id := range prices {
		if strings.HasPrefix(model, id) && len(id) > len(best) {
			best = id
		}
	}
	p, ok := prices[best]
	if long, has := longPrompts[best]; has && prompt > long.over {
		p = long.price
	}
	return p, ok
}

type Spend struct {
	Input, Output, CacheRead, CacheWrite int64
	USD                                  float64
	Models                               []string
	Unpriced                             []string
}

func (s *Spend) Add(o Spend) {
	s.Input += o.Input
	s.Output += o.Output
	s.CacheRead += o.CacheRead
	s.CacheWrite += o.CacheWrite
	s.USD += o.USD
	for _, m := range o.Models {
		if !slices.Contains(s.Models, m) {
			s.Models = append(s.Models, m)
		}
	}
	for _, m := range o.Unpriced {
		if !slices.Contains(s.Unpriced, m) {
			s.Unpriced = append(s.Unpriced, m)
		}
	}
}

func Of(resp *anthropic.BetaMessage) Spend {
	if len(resp.Usage.Iterations) == 0 {
		u := resp.Usage
		return priced(string(resp.Model), u.InputTokens, u.OutputTokens, u.CacheReadInputTokens,
			u.CacheCreationInputTokens, u.CacheCreation.Ephemeral5mInputTokens, u.CacheCreation.Ephemeral1hInputTokens)
	}

	var total Spend
	for _, it := range resp.Usage.Iterations {
		model := string(it.Model)
		if model == "" {
			model = string(resp.Model)
		}
		total.Add(priced(model, it.InputTokens, it.OutputTokens, it.CacheReadInputTokens,
			it.CacheCreationInputTokens, it.CacheCreation.Ephemeral5mInputTokens, it.CacheCreation.Ephemeral1hInputTokens))
	}
	return total
}

func priced(model string, input, output, cacheRead, cacheWrite, write5m, write1h int64) Spend {
	s := Spend{Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite, Models: []string{model}}

	p, ok := priceOf(model, input+cacheRead+cacheWrite)
	if !ok {
		s.Unpriced = []string{model}
		return s
	}
	if write5m+write1h == 0 {
		write5m = cacheWrite
	}
	s.USD = (float64(input)*p.input +
		float64(output)*p.output +
		float64(cacheRead)*p.cacheRead +
		float64(write5m)*p.input*1.25 +
		float64(write1h)*p.input*2) / 1e6
	return s
}
