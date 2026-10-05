package headingid

import (
	"strings"
	"unicode"

	"github.com/simp-lee/oxpio/internal/slug"
)

// CanonicalText applies the shared lowercase NFC + whitespace-collapsing
// contract used by heading ids and fragment lookup.
func CanonicalText(value string) string {
	normalized := NormalizeWhitespace(value)
	if normalized == "" {
		return ""
	}

	return NormalizeWhitespace(slug.Canonicalize(normalized))
}

// NormalizeWhitespace collapses internal whitespace runs to single spaces.
func NormalizeWhitespace(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

// Normalize returns the shared fragment/id slug form derived from visible heading text.
func Normalize(value string) string {
	value = CanonicalText(value)
	if value == "" {
		return "heading"
	}

	var builder strings.Builder
	lastHyphen := false

	for _, r := range value {
		switch {
		case isASCIIControl(r):
			continue
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			builder.WriteRune(r)
			lastHyphen = false
		case unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) || r == '_' || r == '-':
			if lastHyphen || builder.Len() == 0 {
				continue
			}
			builder.WriteByte('-')
			lastHyphen = true
		}
	}

	normalized := strings.Trim(builder.String(), "-")
	if normalized == "" {
		return "heading"
	}

	return normalized
}

func isASCIIControl(r rune) bool {
	return (r >= 0 && r < 0x20) || r == 0x7f
}
