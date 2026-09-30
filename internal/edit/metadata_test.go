package edit

import (
	"strings"
	"testing"
)

func TestApplyEditorFrontmatterPreservesUnknownFieldsAndReplacesBody(t *testing.T) {
	source := []byte("---\n# keep this comment\ntitle: Old\npublish: false\ntype: doc\ncustom: keep\n---\n\nOld body\n")
	order := 2
	updated, err := applyEditorFrontmatter(source, []byte("\nNew body\n"), editorFrontmatter{
		Title:       "New title",
		Publish:     true,
		Type:        "doc",
		Description: "Description",
		Tags:        []string{"one", "two"},
		Order:       &order,
	}, "article")
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)
	for _, want := range []string{"# keep this comment", "custom: keep", "title: New title", "publish: true", "description: Description", "New body"} {
		if !strings.Contains(text, want) {
			t.Fatalf("updated source missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Old body") || strings.Count(text, "---") != 2 {
		t.Fatalf("old body or duplicate frontmatter remained:\n%s", text)
	}
}

func TestParseEditorDocumentReturnsFormFieldsAndBody(t *testing.T) {
	document, err := parseEditorDocument([]byte("---\ntitle: Guide\npublish: true\ntype: post\ntags: [go, web]\ndate: 2026-01-02\n---\n\nBody\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !document.HasBlock || document.Frontmatter.Title != "Guide" || !document.Frontmatter.Publish || document.Frontmatter.Type != "post" {
		t.Fatalf("document = %#v", document)
	}
	if len(document.Frontmatter.Tags) != 2 || document.Frontmatter.Date != "2026-01-02" || string(document.Body) != "\nBody\n" {
		t.Fatalf("parsed metadata/body = %#v, %q", document.Frontmatter, document.Body)
	}
}

func TestApplyEditorFrontmatterUpdatesOnlyChangedFields(t *testing.T) {
	source := []byte("---\ntitle: \"Quoted title\"\npublish: true\ntype: doc\ntags:\n  - one\n---\n\nBody\n")
	updated, err := applyEditorFrontmatter(source, nil, editorFrontmatter{Description: "Updated"}, "article", []string{"description"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)
	for _, want := range []string{"title: \"Quoted title\"", "tags:\n  - one", "description: Updated"} {
		if !strings.Contains(text, want) {
			t.Fatalf("updated source missing %q:\n%s", want, text)
		}
	}
}

func TestApplyEditorFrontmatterCreatesBlockForBodyOnlySource(t *testing.T) {
	updated, err := applyEditorFrontmatter([]byte("Body"), []byte("Body"), editorFrontmatter{Title: "Page", Publish: false, Type: "page"}, "article")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(updated), "---\ntitle: Page\npublish: false\ntype: page\n---\n\nBody"; got != want {
		t.Fatalf("updated = %q, want %q", got, want)
	}
}

func TestApplyEditorFrontmatterUpdatesQuotedAndFlowFields(t *testing.T) {
	source := []byte("---\n\"title\": \"Old title\"\ntags: [one, two] # keep this comment\n---\nBody\n")
	updated, err := applyEditorFrontmatter(source, nil, editorFrontmatter{Title: "New title", Tags: []string{"three"}}, "article", []string{"title", "tags"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)
	for _, want := range []string{"title: New title", "tags:", "- three", "# keep this comment"} {
		if !strings.Contains(text, want) {
			t.Fatalf("updated source missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Old title") || strings.Contains(text, "one, two") {
		t.Fatalf("old YAML values remained:\n%s", text)
	}
}
