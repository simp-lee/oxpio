package recommend

import (
	"strings"
	"unicode"

	"github.com/simp-lee/oxpio/internal/recommend/chinese"
	"golang.org/x/text/unicode/norm"
)

// Tokenize normalizes and tokenizes mixed-language recommendation text.
func Tokenize(text string) ([]string, error) {
	return tokenize(text, chinese.Default)
}

func tokenize(text string, loadChinese func() (*chinese.Segmenter, error)) ([]string, error) {
	normalized := strings.ToLower(norm.NFKC.String(text))
	if strings.TrimSpace(normalized) == "" {
		return nil, nil
	}

	runes := []rune(normalized)
	tokens := make([]string, 0, 32)
	var segmenter *chinese.Segmenter
	for start := 0; start < len(runes); {
		if isHanRune(runes[start]) {
			end := start + 1
			for end < len(runes) && isHanRune(runes[end]) {
				end++
			}

			if segmenter == nil {
				loaded, err := loadChinese()
				if err != nil {
					return nil, err
				}
				segmenter = loaded
			}
			for _, token := range segmenter.Cut(string(runes[start:end]), true) {
				appendFilteredToken(&tokens, token, segmenter)
			}
			start = end
			continue
		}

		if !isNonHanAlphaNumeric(runes[start]) {
			start++
			continue
		}

		token, end := scanNonHanToken(runes, start)
		appendFilteredToken(&tokens, token, nil)
		start = end
	}

	return tokens, nil
}

func scanNonHanToken(runes []rune, start int) (string, int) {
	var token strings.Builder
	token.WriteRune(runes[start])
	end := start + 1
	for end < len(runes) {
		current := runes[end]
		if isNonHanAlphaNumeric(current) {
			token.WriteRune(current)
			end++
			continue
		}

		if isNonHanConnector(current) &&
			end+1 < len(runes) &&
			isNonHanAlphaNumeric(runes[end-1]) &&
			isNonHanAlphaNumeric(runes[end+1]) {
			token.WriteRune(current)
			end++
			continue
		}

		if current == '+' && isNonHanAlphaNumeric(runes[end-1]) {
			for end < len(runes) && runes[end] == '+' {
				token.WriteRune('+')
				end++
			}
			break
		}
		if current == '#' && isNonHanAlphaNumeric(runes[end-1]) {
			token.WriteRune('#')
			end++
		}
		break
	}
	return token.String(), end
}

func appendFilteredToken(dst *[]string, token string, segmenter *chinese.Segmenter) {
	if dst == nil || token == "" || isSingleHanToken(token) || isOXPIOStopword(token) {
		return
	}
	if segmenter != nil && segmenter.IsStopword(token) {
		return
	}
	*dst = append(*dst, token)
}

func isSingleHanToken(token string) bool {
	runes := []rune(token)
	return len(runes) == 1 && isHanRune(runes[0])
}

func isNonHanAlphaNumeric(r rune) bool {
	return !isHanRune(r) && (unicode.IsLetter(r) || unicode.IsNumber(r))
}

func isNonHanConnector(r rune) bool {
	switch r {
	case '.', '_', '-', '/', ':':
		return true
	default:
		return false
	}
}

func isHanRune(r rune) bool {
	return unicode.Is(unicode.Han, r)
}
