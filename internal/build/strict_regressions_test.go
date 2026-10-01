package build

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStrictBuildResolvesSectionSourceLinksAndFragments(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "obsite.yaml", "title: Section Links\nbaseURL: https://example.test/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n## Welcome\n\n[Root](_index.md) [Root heading](_index.md#Welcome) [[_index#Welcome|Root wikilink]]\n\n[Markdown](guide/_index.md#Install)\n\n[[guide/_index#Install|Wikilink]]\n")
	writeStrictFile(t, vault, "guide/_index.md", "---\ntitle: Guide\npublish: true\n---\n## Install\n\n![[Embedded]]\n")
	writeStrictFile(t, vault, "guide/embedded.md", "---\ntitle: Embedded\npublish: true\ntype: page\n---\n[[guide/_index#Install|Back]]\n")

	output := filepath.Join(t.TempDir(), "site")
	result, err := BuildWithOptions(vault, output, Options{Strict: true})
	if err != nil {
		t.Fatalf("BuildWithOptions() error = %v; diagnostics = %#v", err, result.Diagnostics)
	}
	if result.WarningCount != 0 || result.ErrorCount != 0 {
		t.Fatalf("diagnostic counts = (%d warnings, %d errors), want zero; diagnostics = %#v", result.WarningCount, result.ErrorCount, result.Diagnostics)
	}
	section := result.Index.SectionsBySource["guide/_index.md"]
	if section == nil || section.RelPath != "guide" {
		t.Fatalf("SectionsBySource[guide/_index.md] = %#v, want guide section", section)
	}

	home := string(readBuildOutputFile(t, output, "index.html"))
	if got := strings.Count(home, `href=guide/#install`); got != 2 {
		t.Fatalf("section source heading link count = %d, want 2:\n%s", got, home)
	}
	if got := strings.Count(home, `href=#welcome`); got != 2 {
		t.Fatalf("root section source heading link count = %d, want 2:\n%s", got, home)
	}
	if !strings.Contains(home, `href=./>Root</a>`) {
		t.Fatalf("root section source landing link is not canonical:\n%s", home)
	}
	guide := string(readBuildOutputFile(t, output, "guide/index.html"))
	if !strings.Contains(guide, `href=#install>Back</a>`) {
		t.Fatalf("embedded section heading link lost its fragment:\n%s", guide)
	}
}

func TestStrictBuildUsesCollectionOrderForTagPages(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "obsite.yaml", "title: Tag Order\nbaseURL: https://example.test/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeStrictFile(t, vault, "b.md", "---\ntitle: B\npublish: true\ntype: doc\norder: 1\ntags: [shared]\n---\nB\n")
	writeStrictFile(t, vault, "a.md", "---\ntitle: A\npublish: true\ntype: doc\norder: 2\ntags: [shared]\n---\nA\n")

	output := filepath.Join(t.TempDir(), "site")
	result, err := BuildWithOptions(vault, output, Options{Strict: true})
	if err != nil {
		t.Fatalf("BuildWithOptions() error = %v; diagnostics = %#v", err, result.Diagnostics)
	}
	if got, want := result.Index.Tags["shared"].Notes, []string{"b.md", "a.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tag note order = %#v, want %#v", got, want)
	}

	tagPage := string(readBuildOutputFile(t, output, "tags/shared/index.html"))
	if b, a := strings.Index(tagPage, `class=listing-title>B</span>`), strings.Index(tagPage, `class=listing-title>A</span>`); b < 0 || a < 0 || b >= a {
		t.Fatalf("tag page does not use collection order B, A:\n%s", tagPage)
	}
}
