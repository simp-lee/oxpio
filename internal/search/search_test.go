package search

import (
	"errors"
	"testing"
)

func TestCompileUsesNativeNonOverlappingRE2AndRejectsNullablePatterns(t *testing.T) {
	matcher, err := Compile("Alpha", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(matcher.FindAllStringIndex("alpha ALPHA", -1)); got != 2 {
		t.Fatalf("matches = %d", got)
	}
	for _, pattern := range []string{"^", `$`, `\b`, `a*`} {
		if _, err := Compile(pattern, true, true); !errors.Is(err, ErrZeroWidth) {
			t.Fatalf("%q error = %v", pattern, err)
		}
	}
}
