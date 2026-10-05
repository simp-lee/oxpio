package build

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStrictBuildBacklinksIncludeStandardLinksFromPagesAndEmbeds(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", `title: Link Graph
baseURL: https://example.test/
navigation: []
related:
  enabled: false
`)
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeStrictFile(t, vault, "child.md", "---\ntitle: Child\npublish: true\ntype: page\n---\nChild\n")
	writeStrictFile(t, vault, "direct.md", "---\ntitle: Direct\npublish: true\ntype: page\n---\n[Child](child.md)\n")
	writeStrictFile(t, vault, "embedded.md", "---\ntitle: Embedded\npublish: true\ntype: page\n---\n[Child](child.md)\n")
	writeStrictFile(t, vault, "host.md", "---\ntitle: Host\npublish: true\ntype: page\n---\n![[Embedded]]\n")

	output := filepath.Join(t.TempDir(), "site")
	result, err := BuildWithOptions(vault, output, Options{})
	if err != nil {
		t.Fatalf("BuildWithOptions() error = %v", err)
	}
	wantSources := []string{"direct.md", "embedded.md", "host.md"}
	if got := result.Graph.Backward["child.md"]; !reflect.DeepEqual(got, wantSources) {
		t.Fatalf("child backlinks = %#v, want %#v", got, wantSources)
	}

	childPage := string(readBuildOutputFile(t, output, "child/index.html"))
	for _, title := range []string{"Direct", "Embedded", "Host"} {
		if !strings.Contains(childPage, ">"+title+"</a>") {
			t.Fatalf("child page missing %q backlink: %s", title, childPage)
		}
	}
}
