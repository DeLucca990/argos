package receipt

import (
	"fmt"
	"strconv"
	"strings"
)

func ParseCents(s string) (int64, error) {
	raw := s
	var b strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == '.' || r == ',' || r == '-' {
			b.WriteRune(r)
		}
	}
	s = b.String()
	negative := strings.HasPrefix(s, "-")
	s = strings.ReplaceAll(s, "-", "")
	if s == "" {
		return 0, fmt.Errorf("%q is not an amount", raw)
	}

	whole, frac := s, ""
	dots, commas := strings.Count(s, "."), strings.Count(s, ",")
	if i := strings.LastIndexAny(s, ".,"); i >= 0 && ((dots > 0 && commas > 0) || dots+commas == 1) {
		whole, frac = s[:i], s[i+1:]
	}
	whole = strings.NewReplacer(".", "", ",", "").Replace(whole)
	if whole == "" {
		whole = "0"
	}

	reais, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not an amount", raw)
	}
	cents := reais * 100
	switch {
	case len(frac) == 1:
		cents += int64(frac[0]-'0') * 10
	case len(frac) >= 2:
		c, _ := strconv.ParseInt(frac[:2], 10, 64)
		if len(frac) > 2 && frac[2] >= '5' {
			c++
		}
		cents += c
	}
	if negative {
		cents = -cents
	}
	return cents, nil
}

func ParseQuantity(s string) float64 {
	q, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", "."), 64)
	if err != nil || q <= 0 {
		return 1
	}
	return q
}

func FormatBRL(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	whole := strconv.FormatInt(cents/100, 10)
	var b strings.Builder
	for i, d := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(d)
	}
	return fmt.Sprintf("%sR$ %s,%02d", sign, b.String(), cents%100)
}

func FormatQuantity(q float64) string {
	return strings.ReplaceAll(strconv.FormatFloat(q, 'f', -1, 64), ".", ",")
}
