package pathutil

import (
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func TestNormalizeSitePath(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty", value: "  ", want: ""},
		{name: "slashes and whitespace", value: ` /assets\images/hero.png `, want: "assets/images/hero.png"},
		{name: "clean path", value: "assets/./images/../hero.png", want: "assets/hero.png"},
		{name: "current directory", value: "/./", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeSitePath(tt.value); got != tt.want {
				t.Fatalf("NormalizeSitePath(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestRelativeToNoteOutput(t *testing.T) {
	tests := []struct {
		name string
		note *model.Note
		path string
		want string
	}{
		{
			name: "route",
			note: &model.Note{Route: "guide/topic"},
			path: `\assets\hero.png`,
			want: "../../assets/hero.png",
		},
		{
			name: "slug fallback",
			note: &model.Note{Slug: "topic"},
			path: "topic/index.html",
			want: "index.html",
		},
		{name: "nil note", path: "assets/hero.png", want: "assets/hero.png"},
		{name: "empty path", note: &model.Note{Route: "guide/topic"}, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RelativeToNoteOutput(tt.note, tt.path); got != tt.want {
				t.Fatalf("RelativeToNoteOutput(%#v, %q) = %q, want %q", tt.note, tt.path, got, tt.want)
			}
		})
	}
}
