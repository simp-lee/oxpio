package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishIsTheRegisteredFormalOutputCommand(t *testing.T) {
	cmd := newRootCommand(testCommandDependencies())
	publish, _, err := cmd.Find([]string{"publish"})
	if err != nil {
		t.Fatal(err)
	}
	if publish == nil || publish.Name() != "publish" {
		t.Fatalf("publish command = %#v", publish)
	}
}

func TestBuildAndLegacyEditorNamesAreNotPublicCommands(t *testing.T) {
	for _, name := range []string{"build", "edit --setup"} {
		args := strings.Fields(name)
		_, _, err := executeForTest(t, testCommandDependencies(), args)
		if err == nil {
			t.Fatalf("executeForTest(%v) succeeded", args)
		}
	}
}

func TestPublishRendersSectionAndArticleReadingFlow(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeValidateFile(t, vault, "oxpio.yaml", "title: Reading Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeValidateFile(t, vault, "guide/_index.md", "---\ntitle: Guide\npublish: true\n---\nGuide\n")
	writeValidateFile(t, vault, "guide/01-first.md", "---\ntitle: First\npublish: true\ntype: doc\nslug: first\n---\nFirst body\n")
	writeValidateFile(t, vault, "guide/02-second.md", "---\ntitle: Second\npublish: true\ntype: doc\nslug: second\n---\nSecond body\n")
	if _, stderr, err := executeForTest(t, defaultCommandDependencies(), []string{"publish", "--vault", vault, "--output", output}); err != nil || stderr != "" {
		t.Fatalf("publish error = %v; stderr=%q", err, stderr)
	}
	section := string(readCLIOutputFile(t, output, "guide/index.html"))
	if first, second := strings.Index(section, "href=/guide/first/"), strings.Index(section, "href=/guide/second/"); first < 0 || second < 0 || first >= second {
		t.Fatalf("article order = %s", section)
	}
	first := string(readCLIOutputFile(t, output, "guide/first/index.html"))
	if !strings.Contains(first, "1 of 2") || !strings.Contains(first, "href=/guide/second/") || strings.Contains(first, "class=previous") {
		t.Fatalf("first reading flow = %s", first)
	}
	second := string(readCLIOutputFile(t, output, "guide/second/index.html"))
	if !strings.Contains(second, "2 of 2") || !strings.Contains(second, "href=/guide/first/") || strings.Contains(second, "class=next") {
		t.Fatalf("second reading flow = %s", second)
	}
}

func TestPublishFailurePreservesExistingOutput(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nBefore\n")
	if _, _, err := executeForTest(t, defaultCommandDependencies(), []string{"publish", "--vault", vault, "--output", output}); err != nil {
		t.Fatal(err)
	}
	before := snapshotCLIOutput(t, output)
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nAfter\n")
	writeValidateFile(t, vault, "broken.md", "not strict frontmatter\n")
	if _, _, err := executeForTest(t, defaultCommandDependencies(), []string{"publish", "--vault", vault, "--output", output}); err == nil {
		t.Fatal("publish succeeded for invalid source")
	}
	assertCLIOutputUnchanged(t, output, before, "publish failure")
	if _, err := os.Stat(filepath.Join(output, "index.html")); err != nil {
		t.Fatal(err)
	}
}

func readCLIOutputFile(t *testing.T, root, relPath string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relPath)))
	if err != nil {
		t.Fatalf("read output %q: %v", relPath, err)
	}
	return data
}
