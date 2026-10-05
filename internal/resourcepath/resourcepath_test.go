package resourcepath

import (
	"reflect"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func TestIsLocalTargetDistinguishesLocalResourcesFromExternalDestinations(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"missing.png":                    true,
		"../outside.png":                 true,
		"/assets/manual.pdf#page=2":      true,
		`C:\\assets\\manual.pdf`:         true,
		"https://example.test/image.png": false,
		"//cdn.example.test/image.png":   false,
		"data:image/png;base64,AA":       false,
		"#local-fragment":                false,
		"":                               false,
	}
	for target, want := range tests {
		target, want := target, want
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			if got := IsLocalTarget(target); got != want {
				t.Fatalf("IsLocalTarget(%q) = %v, want %v", target, got, want)
			}
		})
	}
}

func TestCandidatePathsIncludeAttachmentFolderFallback(t *testing.T) {
	t.Parallel()

	note := &model.Note{RelPath: "notes/current.md"}
	idx := &model.VaultIndex{AttachmentFolderPath: "assets/uploads"}

	got := CandidatePathsWithAttachmentFolder(note, idx.AttachmentFolderPath, "diagram.png")
	want := []string{"notes/diagram.png", "diagram.png", "assets/uploads/diagram.png"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CandidatePathsWithAttachmentFolder() = %#v, want %#v", got, want)
	}
}

func TestCandidatePathsDecodeEscapedFilenameDelimitersOnce(t *testing.T) {
	t.Parallel()

	note := &model.Note{RelPath: "notes/current.md"}
	got := CandidatePathsWithAttachmentFolder(note, "", "./diagram%23v1.png")
	want := []string{"notes/diagram#v1.png"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CandidatePathsWithAttachmentFolder() = %#v, want %#v", got, want)
	}
}

func TestResolveIndexedAssetPathSupportsDecodedAndAttachmentFolderTargets(t *testing.T) {
	t.Parallel()

	note := &model.Note{RelPath: "notes/current.md"}
	idx := &model.VaultIndex{
		AttachmentFolderPath: "assets/uploads",
		Assets: map[string]*model.Asset{
			"images/My Chart.png":       {SrcPath: "images/My Chart.png"},
			"assets/uploads/poster.png": {SrcPath: "assets/uploads/poster.png"},
		},
	}

	tests := []struct {
		name   string
		target string
		want   string
	}{
		{
			name:   "decoded note relative target",
			target: "../images/My%20Chart.png?raw=1#frag",
			want:   "images/My Chart.png",
		},
		{
			name:   "attachment folder fallback",
			target: "poster.png",
			want:   "assets/uploads/poster.png",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := ResolveIndexedAssetPath(note, idx, tt.target); got != tt.want {
				t.Fatalf("ResolveIndexedAssetPath(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

func TestResolveIndexedAssetPathKeepsExplicitRelativeMissesUnresolved(t *testing.T) {
	t.Parallel()

	note := &model.Note{RelPath: "notes/current.md"}
	idx := &model.VaultIndex{
		Assets: map[string]*model.Asset{
			"poster.png":        {SrcPath: "poster.png"},
			"images/poster.png": {SrcPath: "images/poster.png"},
		},
	}

	for _, target := range []string{"./poster.png", "images/poster.png"} {
		target := target
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			lookup := LookupIndexedAssetPath(note, idx, target)
			if lookup.Path != "" {
				t.Fatalf("LookupIndexedAssetPath(%q).Path = %q, want empty when note-relative lookup misses", target, lookup.Path)
			}
			if len(lookup.Ambiguous) != 0 {
				t.Fatalf("LookupIndexedAssetPath(%q).Ambiguous = %#v, want no fallback ambiguity", target, lookup.Ambiguous)
			}
			if got := ResolveIndexedAssetPath(note, idx, target); got != "" {
				t.Fatalf("ResolveIndexedAssetPath(%q) = %q, want empty when explicit relative lookup misses", target, got)
			}
		})
	}
}

func TestLookupIndexedImageEmbedAssetPathSupportsVaultRelativeSlashPathFallback(t *testing.T) {
	t.Parallel()

	note := &model.Note{RelPath: "notes/deep/current.md"}
	idx := &model.VaultIndex{
		Assets: map[string]*model.Asset{
			"assets/diagram.png": {SrcPath: "assets/diagram.png"},
		},
	}

	if got := ResolveIndexedAssetPath(note, idx, "assets/diagram.png"); got != "" {
		t.Fatalf("ResolveIndexedAssetPath(%q) = %q, want empty so non-embed resource lookup semantics stay unchanged", "assets/diagram.png", got)
	}

	lookup := LookupIndexedImageEmbedAssetPath(note, idx, "assets/diagram.png")
	if lookup.Path != "assets/diagram.png" {
		t.Fatalf("LookupIndexedImageEmbedAssetPath(%q).Path = %q, want %q", "assets/diagram.png", lookup.Path, "assets/diagram.png")
	}
	if len(lookup.Ambiguous) != 0 {
		t.Fatalf("LookupIndexedImageEmbedAssetPath(%q).Ambiguous = %#v, want no ambiguity", "assets/diagram.png", lookup.Ambiguous)
	}
	if got := ResolveIndexedImageEmbedAssetPath(note, idx, "assets/diagram.png"); got != "assets/diagram.png" {
		t.Fatalf("ResolveIndexedImageEmbedAssetPath(%q) = %q, want %q", "assets/diagram.png", got, "assets/diagram.png")
	}
	if got := ResolveIndexedImageEmbedAssetPath(note, idx, "./diagram.png"); got != "" {
		t.Fatalf("ResolveIndexedImageEmbedAssetPath(%q) = %q, want empty when explicit relative targets miss", "./diagram.png", got)
	}
	if got := ResolveIndexedImageEmbedAssetPath(note, idx, "../assets/diagram.png"); got != "" {
		t.Fatalf("ResolveIndexedImageEmbedAssetPath(%q) = %q, want empty when parent-relative targets miss", "../assets/diagram.png", got)
	}
}

func TestResolveIndexedAssetPathCanonicalizesUnicodeLookupKeys(t *testing.T) {
	t.Parallel()

	note := &model.Note{RelPath: "notes/current.md"}
	idx := &model.VaultIndex{
		AttachmentFolderPath: "assets/uploads",
		Assets: map[string]*model.Asset{
			"images/Cafe\u0301 Chart.png":          {SrcPath: "images/Cafe\u0301 Chart.png"},
			"assets/uploads/Cafe\u0301 Poster.png": {SrcPath: "assets/uploads/Cafe\u0301 Poster.png"},
		},
	}

	tests := []struct {
		name   string
		target string
		want   string
	}{
		{
			name:   "standard markdown image path",
			target: "../images/Café Chart.png?raw=1#frag",
			want:   "images/Cafe\u0301 Chart.png",
		},
		{
			name:   "image embed target",
			target: "Café Poster.png",
			want:   "assets/uploads/Cafe\u0301 Poster.png",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := ResolveIndexedAssetPath(note, idx, tt.target); got != tt.want {
				t.Fatalf("ResolveIndexedAssetPath(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

func TestLookupIndexedAssetPathRefusesCanonicalUnicodeCollisions(t *testing.T) {
	t.Parallel()

	note := &model.Note{RelPath: "notes/current.md"}
	idx := &model.VaultIndex{
		Assets: map[string]*model.Asset{
			"images/Cafe\u0301 Chart.png": {SrcPath: "images/Cafe\u0301 Chart.png"},
			"images/Café Chart.png":       {SrcPath: "images/Café Chart.png"},
		},
	}

	lookup := LookupIndexedAssetPath(note, idx, "../images/CAFÉ Chart.png?raw=1#frag")
	if lookup.Path != "" {
		t.Fatalf("LookupIndexedAssetPath().Path = %q, want empty when canonical fallback is ambiguous", lookup.Path)
	}
	want := []string{"images/Cafe\u0301 Chart.png", "images/Café Chart.png"}
	if !reflect.DeepEqual(lookup.Ambiguous, want) {
		t.Fatalf("LookupIndexedAssetPath().Ambiguous = %#v, want %#v", lookup.Ambiguous, want)
	}
	if got := ResolveIndexedAssetPath(note, idx, "../images/CAFÉ Chart.png?raw=1#frag"); got != "" {
		t.Fatalf("ResolveIndexedAssetPath() = %q, want empty when canonical fallback is ambiguous", got)
	}
}

func TestResolvePathSupportsAttachmentFolderFallbackWithoutIndex(t *testing.T) {
	t.Parallel()

	note := &model.Note{RelPath: "notes/current.md"}

	lookup := LookupPath(note, "assets/uploads", "poster.png", func(candidate string) model.PathLookupResult {
		if candidate == "assets/uploads/poster.png" {
			return model.PathLookupResult{Path: candidate}
		}
		return model.PathLookupResult{}
	})
	if lookup.Path != "assets/uploads/poster.png" {
		t.Fatalf("LookupPath().Path = %q, want %q", lookup.Path, "assets/uploads/poster.png")
	}
}
