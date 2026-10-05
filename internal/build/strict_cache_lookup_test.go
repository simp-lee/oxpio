package build

import (
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func TestStrictCacheLookupDigestsTrackRenderDependencies(t *testing.T) {
	note := &model.Note{
		RelPath:    "docs/target.md",
		Route:      "/docs/target/",
		RawContent: []byte("original body\n"),
		Frontmatter: model.Frontmatter{
			Title: "Target",
		},
	}
	section := &model.Section{
		RelPath:    "docs",
		SourcePath: "docs/_index.md",
		Route:      "/docs/",
		Title:      "Docs",
		RawContent: []byte("section body\n"),
	}
	index := &model.VaultIndex{
		Notes:    map[string]*model.Note{note.RelPath: note},
		Sections: map[string]*model.Section{section.RelPath: section},
	}

	original, err := strictCacheLookupDigests(index)
	if err != nil {
		t.Fatalf("strictCacheLookupDigests() error = %v", err)
	}

	note.RawContent = []byte("changed body\n")
	contentChanged, err := strictCacheLookupDigests(index)
	if err != nil {
		t.Fatalf("strictCacheLookupDigests() after content change error = %v", err)
	}
	if contentChanged.base != original.base {
		t.Fatal("base lookup digest changed for embed-only content")
	}
	if contentChanged.embedded == original.embedded {
		t.Fatal("embedded lookup digest did not change for note content")
	}

	note.Route = "/docs/renamed/"
	lookupChanged, err := strictCacheLookupDigests(index)
	if err != nil {
		t.Fatalf("strictCacheLookupDigests() after route change error = %v", err)
	}
	if lookupChanged.base == contentChanged.base {
		t.Fatal("base lookup digest did not change for a route change")
	}
	if lookupChanged.embedded == contentChanged.embedded {
		t.Fatal("embedded lookup digest did not change for a route change")
	}
}
