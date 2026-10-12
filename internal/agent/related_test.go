package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	internalapplication "github.com/simp-lee/oxpio/internal/application"
)

func TestRelatedIsIndependentOfPageRenderingSwitch(t *testing.T) {
	vault := t.TempDir()
	writeQueryFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nrelated:\n  enabled: false\n  count: 5\n")
	writeQueryFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeQueryFile(t, vault, "alpha.md", "---\ntitle: Alpha\npublish: true\ntype: doc\ntags: [shared]\n---\ndatabase protocol shared\n")
	writeQueryFile(t, vault, "beta.md", "---\ntitle: Beta\npublish: true\ntype: doc\ntags: [shared]\n---\ndatabase protocol shared\n")

	first, err := internalapplication.OpenSnapshot(vault, "")
	if err != nil {
		t.Fatal(err)
	}
	withoutPageRendering, err := Related(first, "alpha.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutPageRendering.Candidates) != 1 {
		t.Fatalf("related candidates with page rendering disabled = %#v", withoutPageRendering.Candidates)
	}
	candidate := withoutPageRendering.Candidates[0]
	if candidate.Path != "beta.md" || candidate.Score != 1.15 || !reflect.DeepEqual(candidate.Reasons, []string{"content", "tag"}) {
		t.Fatalf("candidate = %#v", candidate)
	}
	if candidate.Signals.Content != 1 || candidate.Signals.Link != 0 || candidate.Signals.Tag != 0.15 {
		t.Fatalf("candidate signals = %#v", candidate.Signals)
	}
	candidateJSON, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	var candidateFields map[string]json.RawMessage
	if err := json.Unmarshal(candidateJSON, &candidateFields); err != nil {
		t.Fatal(err)
	}
	if _, ok := candidateFields["version"]; !ok {
		t.Fatalf("unversioned candidate omitted version field: %s", candidateJSON)
	}

	writeQueryFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nrelated:\n  enabled: true\n  count: 5\n")
	second, err := internalapplication.OpenSnapshot(vault, "")
	if err != nil {
		t.Fatal(err)
	}
	withPageRendering, err := Related(second, "alpha.md")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withoutPageRendering, withPageRendering) {
		t.Fatalf("related result changed with related.enabled: disabled=%#v enabled=%#v", withoutPageRendering, withPageRendering)
	}
}

func TestRelatedCountDoesNotChangeExactSearchResults(t *testing.T) {
	vault := t.TempDir()
	writeQueryFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nrelated:\n  enabled: false\n  count: 1\n")
	writeQueryFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	for _, name := range []string{"alpha", "beta", "gamma"} {
		writeQueryFile(t, vault, name+".md", "---\ntitle: "+name+"\npublish: true\ntype: doc\ntags: [shared]\n---\ndatabase protocol shared\n")
	}
	first, err := internalapplication.OpenSnapshot(vault, "")
	if err != nil {
		t.Fatal(err)
	}
	firstRelated, err := Related(first, "alpha.md")
	if err != nil {
		t.Fatal(err)
	}
	firstGrep, err := Grep(first, GrepOptions{Query: "database", Scope: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstRelated.Candidates) != 1 {
		t.Fatalf("count=1 related candidates = %d, want 1", len(firstRelated.Candidates))
	}

	writeQueryFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nrelated:\n  enabled: true\n  count: 5\n")
	second, err := internalapplication.OpenSnapshot(vault, "")
	if err != nil {
		t.Fatal(err)
	}
	secondRelated, err := Related(second, "alpha.md")
	if err != nil {
		t.Fatal(err)
	}
	secondGrep, err := Grep(second, GrepOptions{Query: "database", Scope: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondRelated.Candidates) != 2 {
		t.Fatalf("count=5 related candidates = %d, want 2", len(secondRelated.Candidates))
	}
	if !reflect.DeepEqual(firstGrep, secondGrep) {
		t.Fatalf("grep changed when only related config changed: before=%#v after=%#v", firstGrep, secondGrep)
	}
}

func TestRelatedRejectsSectionsAndHiddenArticles(t *testing.T) {
	vault := t.TempDir()
	writeQueryFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeQueryFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeQueryFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: doc\n---\n")
	writeQueryFile(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\n---\n")
	writeQueryFile(t, vault, "private/secret.md", "---\ntitle: Secret\npublish: true\ntype: doc\n---\n")
	snapshot, err := internalapplication.OpenSnapshot(vault, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Related(snapshot, "_index.md"); err == nil {
		t.Fatal("Related accepted a section source")
	}
	if _, err := Related(snapshot, "private/secret.md"); err != internalapplication.ErrHiddenSource {
		t.Fatalf("Related hidden error = %v", err)
	}
}
