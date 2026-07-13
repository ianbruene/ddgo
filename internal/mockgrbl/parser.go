package mockgrbl

import (
	"strconv"
	"strings"
	"unicode"
)

func NormalizeLine(s string) string {
	var b strings.Builder
	inParen := false
	for _, r := range s {
		if inParen {
			if r == ')' {
				inParen = false
			}
			continue
		}
		if r == '(' {
			inParen = true
			continue
		}
		if r == ';' {
			break
		}
		if r == '/' {
			continue
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			continue
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}
func parseWords(s string) map[byte]float64 {
	m := map[byte]float64{}
	for i := 0; i < len(s); {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || c == '$' {
			j := i + 1
			for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z') || s[j] == '$') {
				j++
			}
			if c != '$' {
				if v, e := strconv.ParseFloat(s[i+1:j], 64); e == nil {
					m[c] = v
				}
			}
			i = j
		} else {
			i++
		}
	}
	return m
}
