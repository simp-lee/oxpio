package build

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalanalyze "github.com/simp-lee/oxpio/internal/analyze"
)

func TestNormalBuildRejectsMissingMarkdownAssetsAndPreservesPreviousOutput(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "site")
	writeStrictFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeStrictFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: page\n---\nPublished body\n")
	if _, err := BuildWithOptions(vault, output, Options{}); err != nil {
		t.Fatal(err)
	}
	before := readBuildOutputFile(t, output, "article/index.html")

	writeStrictFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: page\n---\n![Missing](missing.png)\n\n[Attachment](missing.pdf)\n\n![[missing-embed.png]]\n\n![[missing%2Epng]]\n\n![Outside](../outside.png)\n\n[Root attachment](/missing-root.pdf)\n\n[Fragment attachment](missing-fragment.pdf#page=2)\n\n[Windows attachment](C:/assets/manual.pdf)\n\n[Malformed attachment](missing%ZZ.pdf)\n")
	result, err := BuildWithOptions(vault, output, Options{})
	if err == nil {
		t.Fatal("normal build error = nil, want unresolved local assets to fail")
	}
	if result == nil || result.ErrorCount != 9 || result.WarningCount != 0 {
		t.Fatalf("build result = %#v, want nine unresolved asset errors", result)
	}
	for _, item := range result.Diagnostics {
		if item.Severity != "error" || item.Kind != "unresolved_asset" {
			t.Fatalf("diagnostic = %#v, want unresolved_asset error", item)
		}
	}
	after := readBuildOutputFile(t, output, "article/index.html")
	if !bytes.Equal(before, after) {
		t.Fatal("previous output changed after Markdown asset validation failure")
	}
}

func TestStrictBuildPreservesEveryPublishedByteForInputFailureClasses(t *testing.T) {
	type failureCase struct {
		name   string
		mutate func(t *testing.T, vault string)
	}
	cases := []failureCase{
		{name: "configuration", mutate: func(t *testing.T, vault string) {
			appendStrictFile(t, filepath.Join(vault, "oxpio.yaml"), "\nunknownConfig: true\n")
		}},
		{name: "link", mutate: func(t *testing.T, vault string) {
			writeStrictFile(t, vault, "guide/article.md", "---\ntitle: Feature Article\npublish: true\ntype: page\n---\n![missing](missing.png)\n")
		}},
		{name: "banner", mutate: func(t *testing.T, vault string) {
			writeStrictFile(t, vault, "guide/_index.md", "---\ntitle: Guide\npublish: true\nbanner: missing-banner.png\nbannerAlt: Missing\n---\nGuide\n")
		}},
		{name: "cover", mutate: func(t *testing.T, vault string) {
			writeStrictFile(t, vault, "guide/article.md", "---\ntitle: Feature Article\npublish: true\ntype: page\ncover: images/cover.png\n---\nArticle\n")
			if err := os.WriteFile(filepath.Join(vault, "images", "cover.png"), []byte("not an image"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "metadata", mutate: func(t *testing.T, vault string) {
			writeStrictFile(t, vault, "guide/article.md", "---\ntitle: Feature Article\npublish: true\ntype: page\nunknownMetadata: value\n---\nArticle\n")
		}},
		{name: "section", mutate: func(t *testing.T, vault string) {
			if err := os.Remove(filepath.Join(vault, "guide", "_index.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "version", mutate: func(t *testing.T, vault string) {
			appendStrictFile(t, filepath.Join(vault, "oxpio.yaml"), "\nversions:\n  root: docs\n  default: v1\n  entries:\n    - id: v1\n      label: Version 1\n      source: missing\n")
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			vault := copyFixtureVault(t, "feature-vault")
			output := filepath.Join(t.TempDir(), "site")
			if _, err := BuildWithOptions(vault, output, Options{}); err != nil {
				t.Fatal(err)
			}
			before := strictOutputBytes(t, output)
			testCase.mutate(t, vault)
			if _, err := BuildWithOptions(vault, output, Options{}); err == nil {
				t.Fatal("invalid input unexpectedly built")
			}
			after := strictOutputBytes(t, output)
			if len(before) != len(after) {
				t.Fatalf("published file count changed: %d -> %d", len(before), len(after))
			}
			for name, data := range before {
				if !bytes.Equal(data, after[name]) {
					t.Fatalf("published output %q changed after %s failure", name, testCase.name)
				}
			}
			matches, err := filepath.Glob(filepath.Join(filepath.Dir(output), ".site-oxpio-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 0 {
				t.Fatalf("transaction residue after %s failure: %v", testCase.name, matches)
			}
		})
	}
}

func TestStrictBuildPreservesOutputAndCleansStageWhenSocialGenerationFails(t *testing.T) {
	vault := copyFixtureVault(t, "feature-vault")
	output := filepath.Join(t.TempDir(), "site")
	if _, err := BuildWithOptions(vault, output, Options{}); err != nil {
		t.Fatal(err)
	}
	before := strictOutputBytes(t, output)
	analysis, err := internalanalyze.AnalyzeWithOutput(vault, output)
	if err != nil {
		t.Fatal(err)
	}
	// Inject a generator-only failure after valid analysis. Unlike a corrupt
	// cover fixture, this reaches Generate after staging has already begun.
	articles := analysis.Plan.Plan.Articles
	target := articles[len(articles)-1].RelPath
	analysis.Plan.Index.Notes[target].Frontmatter.Title = ""
	if _, err := buildStrictSite(analysis.Plan, vault, output, nil); err == nil || !strings.Contains(err.Error(), "generate social card") {
		t.Fatalf("build error = %v, want social generation failure", err)
	}
	after := strictOutputBytes(t, output)
	if len(after) != len(before) {
		t.Fatalf("published file count changed: %d -> %d", len(before), len(after))
	}
	for name, data := range before {
		if !bytes.Equal(data, after[name]) {
			t.Fatalf("published output %q changed after social generation failure", name)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(output))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "site" {
		t.Fatalf("social generation left temporary output: %v", entries)
	}
}

func appendStrictFile(t *testing.T, path, content string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, content...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStrictBuildPreservesPreviousOutputOnAssetFailure(t *testing.T) {
	vault := copyFixtureVault(t, "feature-vault")
	output := filepath.Join(t.TempDir(), "site")
	if _, err := BuildWithOptions(vault, output, Options{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(output, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "images", "cover.png"), []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildWithOptions(vault, output, Options{}); err == nil {
		t.Fatal("build error = nil")
	}
	after, err := os.ReadFile(filepath.Join(output, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("previous output changed after asset validation failure")
	}
}
