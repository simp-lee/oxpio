package build

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictBuildIncludesInlineHashtagsInSocialCardIdentity(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "site")
	writeStrictFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	article := "---\ntitle: Article\npublish: true\ntype: page\n---\nBody without tags.\n"
	writeStrictFile(t, vault, "article.md", article)

	first, err := BuildWithOptions(vault, output, Options{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	firstArticle := first.Index.Notes["article.md"]
	if firstArticle == nil || firstArticle.SocialImage == "" {
		t.Fatalf("first article = %#v, want generated social image", firstArticle)
	}

	writeStrictFile(t, vault, "article.md", strings.Replace(article, "Body without tags.", "Body with #inline.", 1))
	second, err := BuildWithOptions(vault, output, Options{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	secondArticle := second.Index.Notes["article.md"]
	if secondArticle == nil || secondArticle.SocialImage == "" {
		t.Fatalf("second article = %#v, want generated social image", secondArticle)
	}
	foundInlineTag := false
	for _, tag := range secondArticle.Tags {
		if tag == "inline" {
			foundInlineTag = true
			break
		}
	}
	if !foundInlineTag {
		t.Fatalf("article tags = %#v, want inline hashtag", secondArticle.Tags)
	}
	if firstArticle.SocialImage == secondArticle.SocialImage {
		t.Fatalf("social image path did not change after adding inline hashtag: %q", secondArticle.SocialImage)
	}
}

func TestStrictBuildPublishesOneIndependentSocialCardPerArticle(t *testing.T) {
	vault := copyFixtureVault(t, "feature-vault")
	output := filepath.Join(t.TempDir(), "site")
	result, err := BuildWithOptions(vault, output, Options{})
	if err != nil {
		t.Fatal(err)
	}
	cards := make([]string, 0)
	if err := filepath.Walk(output, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.IsDir() && filepath.Dir(filepath.Dir(path)) == filepath.Join(output, "assets", "social") {
			cards = append(cards, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(cards) != result.NotePages {
		t.Fatalf("social card count = %d, article pages = %d", len(cards), result.NotePages)
	}
	for _, card := range cards {
		file, err := os.Open(card)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := png.DecodeConfig(file)
		_ = file.Close()
		if err != nil {
			t.Fatalf("decode social card %q: %v", card, err)
		}
		if decoded.Width != 1200 || decoded.Height != 630 {
			t.Fatalf("social card %q dimensions = %dx%d", card, decoded.Width, decoded.Height)
		}
	}
}
