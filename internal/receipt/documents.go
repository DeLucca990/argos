package receipt

import "strings"

func normalizeID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func ValidCNPJ(id string) bool {
	if len(id) != 14 || !digits(id[12:]) || same(id) {
		return false
	}
	return checkDigit(id[:12], []int{5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2}) == int(id[12]-'0') &&
		checkDigit(id[:13], []int{6, 5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2}) == int(id[13]-'0')
}

func ValidCPF(id string) bool {
	if len(id) != 11 || !digits(id) || same(id) {
		return false
	}
	return checkDigit(id[:9], []int{10, 9, 8, 7, 6, 5, 4, 3, 2}) == int(id[9]-'0') &&
		checkDigit(id[:10], []int{11, 10, 9, 8, 7, 6, 5, 4, 3, 2}) == int(id[10]-'0')
}

func ValidNFCeKey(key string) bool {
	if len(key) != 44 || !digits(key[43:]) {
		return false
	}
	weights := make([]int, 43)
	for i := range weights {
		weights[42-i] = 2 + i%8
	}
	return checkDigit(key[:43], weights) == int(key[43]-'0')
}

func checkDigit(s string, weights []int) int {
	sum := 0
	for i := range s {
		sum += int(s[i]-'0') * weights[i]
	}
	if r := sum % 11; r >= 2 {
		return 11 - r
	}
	return 0
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func same(s string) bool { return strings.Count(s, s[:1]) == len(s) }

func FormatTaxID(id string) string {
	switch len(id) {
	case 14:
		return id[:2] + "." + id[2:5] + "." + id[5:8] + "/" + id[8:12] + "-" + id[12:]
	case 11:
		return id[:3] + "." + id[3:6] + "." + id[6:9] + "-" + id[9:]
	}
	return id
}
