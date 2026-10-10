package agent

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	internalapplication "github.com/simp-lee/oxpio/internal/application"
	"github.com/simp-lee/oxpio/internal/model"
)

func TestGrepUsesNativeLineAndPermissionSemantics(t *testing.T) {
	vault := t.TempDir()
	writeQueryFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeQueryFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeQueryFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: doc\ntags: [\"#Docs\"]\n---\nAlpha beta\nAlpha\n")
	writeQueryFile(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\n---\n")
	writeQueryFile(t, vault, "private/draft.md", "---\ntitle: Draft\npublish: true\ntype: doc\ntags: [secret]\n---\nAlpha secret\n")
	snapshot, err := internalapplication.OpenSnapshot(vault, filepath.Join(vault, "public"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Grep(snapshot, GrepOptions{Query: "alpha", Scope: "source", MaxResults: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2 || result.Matches[0].Path != "article.md" || result.Truncated {
		t.Fatalf("matches = %#v", result.Matches)
	}
	limited, err := Grep(snapshot, GrepOptions{Query: "alpha", Scope: "source", MaxResults: 1})
	if err != nil || len(limited.Matches) != 1 || !limited.Truncated {
		t.Fatalf("limited = %#v, %v", limited, err)
	}
	exact, err := Grep(snapshot, GrepOptions{Query: "beta", Scope: "source", MaxResults: 1})
	if err != nil || len(exact.Matches) != 1 || exact.Truncated {
		t.Fatalf("exact limit = %#v, %v", exact, err)
	}
	var resourceErr GrepError
	if _, err := Grep(snapshot, GrepOptions{Query: "alpha", MaxScanBytes: 1}); err == nil || !errors.As(err, &resourceErr) || resourceErr.Code != "RESOURCE_LIMIT" {
		t.Fatalf("resource error = %v", err)
	}
	filtered, err := Grep(snapshot, GrepOptions{Query: "alpha", Scope: "source", Paths: []string{"article.md", "private"}, Tags: []string{"#docs"}, Types: []string{"doc"}, IncludeUnpublished: true})
	if err != nil || len(filtered.Matches) != 2 || filtered.Options.Paths[0] != "article.md" {
		t.Fatalf("filtered = %#v, %v", filtered, err)
	}
	root, err := Grep(snapshot, GrepOptions{Query: "alpha", Scope: "source", Paths: []string{"."}})
	if err != nil || len(root.Options.Paths) != 1 || root.Options.Paths[0] != "." {
		t.Fatalf("root options = %#v, %v", root.Options, err)
	}
	all, err := Grep(snapshot, GrepOptions{Query: "Alpha secret", Scope: "source", IncludeUnpublished: true})
	if err != nil || len(all.Matches) != 1 || all.Matches[0].Path != "private/draft.md" {
		t.Fatalf("unpublished matches = %#v, %v", all.Matches, err)
	}
	var hiddenErr GrepError
	if _, err := Grep(snapshot, GrepOptions{Query: "secret", File: "draft.md"}); err == nil || !errors.As(err, &hiddenErr) || hiddenErr.Code != "PERMISSION_DENIED" {
		t.Fatalf("hidden file error = %v", err)
	}
	if _, err := Grep(snapshot, GrepOptions{Query: "secret", Tags: []string{"secret"}}); err == nil || !errors.As(err, &hiddenErr) || hiddenErr.Code != "PERMISSION_DENIED" {
		t.Fatalf("hidden tag error = %v", err)
	}
	if _, err := Grep(snapshot, GrepOptions{Query: "alpha", File: "_index.md"}); err == nil || !errors.As(err, &hiddenErr) || hiddenErr.Code != "PERMISSION_DENIED" {
		t.Fatalf("mixed visible/hidden file error = %v", err)
	}
	if strings.Contains(result.Matches[0].Path, string(filepath.Separator)+"private") {
		t.Fatal("absolute or hidden path leaked")
	}
}

func TestGrepRejectsInvalidSourceBytes(t *testing.T) {
	for _, source := range [][]byte{{'x', 0xff}, {'x', 0}} {
		snapshot := internalapplication.Snapshot{Catalog: &model.SourceCatalog{Entries: []model.SourceCatalogEntry{{RelPath: "doc.md", Kind: "article", EffectivePublish: true, RawSource: source}}}}
		var sourceErr GrepError
		if _, err := Grep(snapshot, GrepOptions{Query: "x"}); err == nil || !errors.As(err, &sourceErr) || sourceErr.Code != "INVALID_SOURCE" {
			t.Fatalf("source %#v error = %v", source, err)
		}
	}
}

func TestGrepPreservesCRLFAndUTF8ByteColumns(t *testing.T) {
	lines := splitGrepLines([]byte("前\r\n界alpha\r\n"))
	if len(lines) != 2 || lines[0].text != "前" || lines[1].text != "界alpha" {
		t.Fatalf("lines = %#v", lines)
	}
	loneCR := splitGrepLines([]byte("x\r"))
	if len(loneCR) != 1 || loneCR[0].text != "x\r" {
		t.Fatalf("lone CR lines = %#v", loneCR)
	}
	matcher, err := compileGrepMatcher(GrepOptions{Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	match := lineMatch("doc.md", "source", 2, matcher.FindStringIndex(lines[1].text), lines[1].text)
	if match.Column == nil || *match.Column != len("界")+1 || match.LineText != "界alpha" {
		t.Fatalf("column = %#v", match.Column)
	}
}

func TestGrepRejectsExplicitHiddenRootScope(t *testing.T) {
	snapshot := internalapplication.Snapshot{Catalog: &model.SourceCatalog{Entries: []model.SourceCatalogEntry{{RelPath: "_index.md", Kind: "section", SectionPath: ".", EffectivePublish: false}}}}
	var grepErr GrepError
	if _, err := Grep(snapshot, GrepOptions{Query: "x", Paths: []string{"."}}); err == nil || !errors.As(err, &grepErr) || grepErr.Code != "PERMISSION_DENIED" {
		t.Fatalf("hidden root error = %v", err)
	}
}

func TestGrepRejectsZeroWidthRegexAndMergesOptions(t *testing.T) {
	vault := t.TempDir()
	writeQueryFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeQueryFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeQueryFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: doc\n---\nzero width\n")
	snapshot, err := internalapplication.OpenSnapshot(vault, filepath.Join(vault, "public"))
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"^", `\b`, `a*`} {
		var grepErr GrepError
		if _, err := Grep(snapshot, GrepOptions{Query: query, Regex: true}); err == nil || !errors.As(err, &grepErr) || grepErr.Code != "REGEX_ZERO_WIDTH" {
			t.Fatalf("zero-width error for %q = %v", query, err)
		}
	}
	if _, err := Grep(snapshot, GrepOptions{Query: "zero", Context: 1, Before: 1}); err == nil {
		t.Fatal("context and before were accepted together")
	}
	body, err := Grep(snapshot, GrepOptions{Query: "zero", Scope: "body"})
	if err != nil || len(body.Matches) != 1 || body.Matches[0].Line == nil {
		t.Fatalf("body = %#v, %v", body, err)
	}
	title, err := Grep(snapshot, GrepOptions{Query: "article", Scope: "title"})
	if err != nil || len(title.Matches) != 1 || title.Matches[0].SourceKind != "title" || title.Matches[0].Line != nil {
		t.Fatalf("title = %#v, %v", title, err)
	}
	metadata, err := Grep(snapshot, GrepOptions{Query: "doc", Scope: "metadata"})
	if err != nil || len(metadata.Matches) != 1 || metadata.Matches[0].Field == nil || *metadata.Matches[0].Field != "type" {
		t.Fatalf("metadata = %#v, %v", metadata, err)
	}
	pathMatches, err := Grep(snapshot, GrepOptions{Query: "article", Scope: "path"})
	if err != nil || len(pathMatches.Matches) != 1 || pathMatches.Matches[0].Line != nil {
		t.Fatalf("path = %#v, %v", pathMatches, err)
	}
	withContext, err := Grep(snapshot, GrepOptions{Query: "zero", Context: 1})
	if err != nil || len(withContext.Contexts) != 1 || withContext.Contexts[0].StartLine < 1 {
		t.Fatalf("context = %#v, %v", withContext, err)
	}
}
