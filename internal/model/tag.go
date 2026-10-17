package model

import (
	"strings"

	"github.com/simp-lee/oxpio/internal/slug"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// CanonicalAliasName returns the identity used to deduplicate and resolve
// aliases. The displayed alias text remains the first trimmed source value.
func CanonicalAliasName(value string) string {
	return cases.Fold().String(norm.NFKC.String(strings.TrimSpace(value)))
}

// NormalizeTagName applies the shared canonical tag contract used by indexing and rendering.
func NormalizeTagName(value string) string {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "#")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return ""
	}

	rawSegments := strings.Split(trimmed, "/")
	segments := make([]string, 0, len(rawSegments))
	for _, segment := range rawSegments {
		normalized, ok := normalizeTagNameSegment(segment)
		if !ok {
			continue
		}
		segments = append(segments, normalized)
	}
	if len(segments) == 0 {
		return ""
	}

	return strings.Join(segments, "/")
}

func normalizeTagNameSegment(segment string) (string, bool) {
	segment = strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(segment)), " "))
	if segment == "" {
		return "", false
	}

	normalized, err := slug.Generate(&segment, "")
	if err != nil {
		return "", false
	}
	return normalized, true
}
