package build

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictBuildCoversBannerLayoutExclusionAndIndependentCards(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "site")
	writeStrictFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\nbanner: images/banner.png\nbannerAlt: Home banner\n---\nHome\n")
	article := "---\ntitle: Same article\npublish: true\ntype: page\nbanner: images/banner.png\nbannerAlt: Article banner\n---\nArticle\n"
	writeStrictFile(t, vault, "one.md", article)
	writeStrictFile(t, vault, "two.md", strings.Replace(article, "banner: images/banner.png\nbannerAlt: Article banner\n", "", 1))
	writeStrictFile(t, vault, "images/banner.png", string(acceptanceBannerPNG(t)))

	if _, err := BuildWithOptions(vault, output, Options{}); err != nil {
		t.Fatal(err)
	}

	style := string(readBuildOutputFile(t, output, "style.css"))
	for _, contract := range []string{"aspect-ratio:16/5", "max-width:56rem", "max-height:280px", "object-fit:cover"} {
		if !strings.Contains(style, contract) {
			t.Fatalf("published stylesheet missing banner contract %q", contract)
		}
	}

	home := string(readBuildOutputFile(t, output, "index.html"))
	if strings.Contains(home, "og:image") || strings.Contains(home, "twitter:image") {
		t.Fatal("section banner implicitly received a social card")
	}
	one := string(readBuildOutputFile(t, output, "one/index.html"))
	if !strings.Contains(one, `class=page-banner`) || !strings.Contains(one, `alt="Article banner"`) || !strings.Contains(one, "og:image") {
		t.Fatalf("bannered article output missing expected banner/card markup: %s", one)
	}
	two := string(readBuildOutputFile(t, output, "two/index.html"))
	if strings.Contains(two, "page-banner") {
		t.Fatal("article without a banner received banner markup")
	}

	cards, err := filepath.Glob(filepath.Join(output, "assets", "social", "*", "*.png"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 {
		t.Fatalf("social card count = %d, want two article cards", len(cards))
	}
	first, err := os.ReadFile(cards[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(cards[1])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("banner-only article changed card pixels; banner was used as a cover")
	}
	if filepath.Dir(cards[0]) == filepath.Dir(cards[1]) {
		t.Fatal("articles with independent canonical URLs shared a social-card URL namespace")
	}
	bannerCards, err := filepath.Glob(filepath.Join(output, "assets", "banner.*.png"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bannerCards) != 1 {
		t.Fatalf("banner asset count = %d, want one shared planned banner", len(bannerCards))
	}
}

func TestStrictBuildPublishesSocialCardsForPNGJPEGAndWebPCovers(t *testing.T) {
	for _, format := range []string{"png", "jpeg", "webp"} {
		t.Run(format, func(t *testing.T) {
			vault := t.TempDir()
			output := filepath.Join(t.TempDir(), "site")
			writeStrictFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
			writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
			article := "---\ntitle: Article\npublish: true\ntype: page\ncover: images/cover." + format + "\n---\nArticle\n"
			writeStrictFile(t, vault, "article.md", article)
			writeStrictFile(t, vault, "images/cover."+format, string(strictAcceptanceCover(t, format)))
			if _, err := BuildWithOptions(vault, output, Options{}); err != nil {
				t.Fatal(err)
			}
			cards, err := filepath.Glob(filepath.Join(output, "assets", "social", "*", "*.png"))
			if err != nil {
				t.Fatal(err)
			}
			if len(cards) != 1 {
				t.Fatalf("social card count = %d, want one", len(cards))
			}
			if _, err := png.DecodeConfig(mustOpenBuildTestFile(t, cards[0])); err != nil {
				t.Fatalf("social card is not a PNG: %v", err)
			}
			articleHTML := string(readBuildOutputFile(t, output, "article/index.html"))
			relCardPath, err := filepath.Rel(output, cards[0])
			if err != nil {
				t.Fatal(err)
			}
			wantCardURL := "https://example.test/" + filepath.ToSlash(relCardPath)
			if got := strictMetaContent(articleHTML, `property="og:image"`); got != wantCardURL {
				t.Fatalf("og:image = %q, want %q", got, wantCardURL)
			}
			if got := strictMetaContent(articleHTML, `name=twitter:image`); got != wantCardURL {
				t.Fatalf("twitter:image = %q, want %q", got, wantCardURL)
			}

			noCoverVault := t.TempDir()
			noCoverOutput := filepath.Join(t.TempDir(), "site")
			writeStrictFile(t, noCoverVault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
			writeStrictFile(t, noCoverVault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
			writeStrictFile(t, noCoverVault, "article.md", strings.Replace(article, "cover: images/cover."+format+"\n", "", 1))
			if _, err := BuildWithOptions(noCoverVault, noCoverOutput, Options{}); err != nil {
				t.Fatal(err)
			}
			noCoverCards, err := filepath.Glob(filepath.Join(noCoverOutput, "assets", "social", "*", "*.png"))
			if err != nil || len(noCoverCards) != 1 {
				t.Fatalf("no-cover social cards = %v, err=%v", noCoverCards, err)
			}
			withCoverBytes, err := os.ReadFile(cards[0])
			if err != nil {
				t.Fatal(err)
			}
			noCoverBytes, err := os.ReadFile(noCoverCards[0])
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(withCoverBytes, noCoverBytes) {
				t.Fatalf("%s cover was accepted but did not affect the built card", format)
			}
		})
	}
}

func mustOpenBuildTestFile(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func strictAcceptanceCover(t *testing.T, format string) []byte {
	t.Helper()
	name := format
	if format == "jpeg" {
		name = "jpg"
	}
	data, err := os.ReadFile(filepath.Join("..", "social", "testdata", "cover."+name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func strictMetaContent(html, attribute string) string {
	start := strings.Index(html, attribute)
	if start < 0 {
		return ""
	}
	content := strings.Index(html[start:], `content="`)
	if content < 0 {
		return ""
	}
	content += start + len(`content="`)
	end := strings.IndexByte(html[content:], '"')
	if end < 0 {
		return ""
	}
	return html[content : content+end]
}

func acceptanceBannerPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 5))
	for y := 0; y < 5; y++ {
		for x := 0; x < 16; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 12), G: uint8(y * 30), B: 180, A: 255})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
