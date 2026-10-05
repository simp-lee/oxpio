package social

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestGenerateHasFixedCanonicalInputAndPNGSnapshot(t *testing.T) {
	order := 7
	result, err := Generate(Input{
		CanonicalURL: "https://example.test/docs/start/", SiteTitle: "OXPIO", Title: "Deterministic title", Context: "Docs / v1",
		Description: "A description", Date: "2026-04-06T00:00:00Z", Updated: "2026-04-07T00:00:00Z",
		Tags: []string{"guide", "go"}, Aliases: []string{"start"}, Slug: "start", Type: "doc", Order: &order,
		Author: "Alice", Reviewed: "2026-04-08T00:00:00Z", Status: "stable", Audience: "developers",
		ProductVersion: "v1", Series: "getting-started",
	})
	if err != nil {
		t.Fatal(err)
	}
	const wantJSON = `{"canonicalURL":"https://example.test/docs/start/","siteTitle":"OXPIO","title":"Deterministic title","context":"Docs / v1","description":"A description","date":"2026-04-06T00:00:00Z","updated":"2026-04-07T00:00:00Z","tags":["guide","go"],"aliases":["start"],"slug":"start","type":"doc","order":7,"author":"Alice","reviewed":"2026-04-08T00:00:00Z","status":"stable","audience":"developers","productVersion":"v1","series":"getting-started","generator":"oxpio-social-card-v2"}`
	const wantPNGHash = "7d192fe729bcd4a558ab89208997c0b240ec2dd05a2163897d8b8519cbf1e12d"
	if string(result.CanonicalJSON) != wantJSON {
		t.Fatalf("canonical JSON = %s, want %s", result.CanonicalJSON, wantJSON)
	}
	hash := sha256.Sum256(result.PNG)
	if got := hex.EncodeToString(hash[:]); got != wantPNGHash {
		t.Fatalf("PNG hash = %s, want %s", got, wantPNGHash)
	}
	if want := "assets/social/2647f899c632ffe86c58d0dc5bea3d840e2aaec648cbcac1706defb3f772b7ea/c1c19b1f3bca09f1e42fd386dc4d27d06d5a5f62e90a6bd7d66cbec2f9bc76ef-" + wantPNGHash + ".png"; result.Path != want {
		t.Fatalf("path = %s, want %s", result.Path, want)
	}
}

func TestGenerateEnforcesFixedColorsAndNoCoverGeometry(t *testing.T) {
	result, err := Generate(Input{CanonicalURL: "https://example.test/no-cover/", SiteTitle: "Site", Title: "Title"})
	if err != nil {
		t.Fatal(err)
	}
	const wantJSON = `{"canonicalURL":"https://example.test/no-cover/","siteTitle":"Site","title":"Title","context":"","generator":"oxpio-social-card-v2"}`
	if string(result.CanonicalJSON) != wantJSON {
		t.Fatalf("canonical JSON = %s, want %s", result.CanonicalJSON, wantJSON)
	}
	decoded, err := png.Decode(bytes.NewReader(result.PNG))
	if err != nil {
		t.Fatal(err)
	}
	for point, want := range map[image.Point]color.RGBA{
		{0, 0}:    {R: 0x0f, G: 0x17, B: 0x2a, A: 0xff},
		{72, 72}:  {R: 0x38, G: 0xbd, B: 0xf8, A: 0xff},
		{83, 557}: {R: 0x38, G: 0xbd, B: 0xf8, A: 0xff},
		{84, 72}:  {R: 0x0f, G: 0x17, B: 0x2a, A: 0xff},
	} {
		if got := decoded.At(point.X, point.Y); got != want {
			t.Fatalf("pixel %v = %#v, want %#v", point, got, want)
		}
	}
	cover := image.NewRGBA(image.Rect(0, 0, 1, 1))
	cover.Set(0, 0, color.RGBA{R: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, cover); err != nil {
		t.Fatal(err)
	}
	withCover, err := Generate(Input{CanonicalURL: "https://example.test/cover/", SiteTitle: "Site", Title: "Title", Cover: encoded.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	coverImage, err := png.Decode(bytes.NewReader(withCover.PNG))
	if err != nil {
		t.Fatal(err)
	}
	if got := coverImage.At(767, 0); got != (color.RGBA{R: 0x0f, G: 0x17, B: 0x2a, A: 0xff}) {
		t.Fatalf("cover boundary pixel = %#v", got)
	}
	if got := coverImage.At(768, 0); got != (color.RGBA{R: 211, G: 4, B: 7, A: 0xff}) {
		t.Fatalf("cover overlay pixel = %#v", got)
	}
}

func TestGenerateTruncatesLongGraphemeTextDeterministically(t *testing.T) {
	input := Input{CanonicalURL: "https://example.test/long/", SiteTitle: "Site", Title: "这是一个非常长的标题😀这是一个非常长的标题😀这是一个非常长的标题😀", Context: "这是一个很长的上下文", Author: "作者作者作者作者作者", Date: "2026-04-06", Status: "stable"}
	first, err := Generate(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.PNG, second.PNG) {
		t.Fatal("long grapheme input was not stable PNG output")
	}
	const wantHash = "cdb80c357b8a65d3523e31005609f3cc9c4a944f76b24380af7181047c699316"
	if first.PNGHash != wantHash {
		t.Fatalf("long grapheme PNG hash = %s, want %s", first.PNGHash, wantHash)
	}
	if !bytes.HasPrefix(first.PNG, []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		t.Fatal("long grapheme output is not a PNG")
	}
}

func TestGenerateIsDeterministicAndContentAddressed(t *testing.T) {
	input := Input{CanonicalURL: "https://example.test/guide/start/", SiteTitle: "Site", Title: "A deterministic card", Context: "Guide", Author: "Alice", Date: "2026-04-05", Status: "stable"}
	first, err := Generate(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.PNG, second.PNG) || first.Path != second.Path || !bytes.Equal(first.CanonicalJSON, second.CanonicalJSON) {
		t.Fatal("same input produced different card output")
	}
	decoded, err := png.Decode(bytes.NewReader(first.PNG))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != 1200 || decoded.Bounds().Dy() != 630 {
		t.Fatalf("bounds = %v", decoded.Bounds())
	}
	if first.Path == "" || first.InputHash == "" || first.PNGHash == "" {
		t.Fatalf("result = %#v", first)
	}
}

func TestGenerateSupportsEmbeddedCJKFallbackText(t *testing.T) {
	first, err := Generate(Input{CanonicalURL: "https://example.test/cjk/", SiteTitle: "OXPIO", Title: "中文文档"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(Input{CanonicalURL: "https://example.test/cjk/", SiteTitle: "OXPIO", Title: "汉字指南"})
	if err != nil {
		t.Fatal(err)
	}
	firstImage, err := png.Decode(bytes.NewReader(first.PNG))
	if err != nil {
		t.Fatal(err)
	}
	secondImage, err := png.Decode(bytes.NewReader(second.PNG))
	if err != nil {
		t.Fatal(err)
	}

	// Compare only the title area to exclude the fixed accent bar and other
	// decorations. Different CJK titles must produce different drawn pixels.
	different := false
	for y := 160; y < 390 && !different; y++ {
		for x := 96; x < 696; x++ {
			if firstImage.At(x, y) != secondImage.At(x, y) {
				different = true
				break
			}
		}
	}
	if !different {
		t.Fatal("distinct CJK titles produced identical title pixels")
	}
}

func TestGenerateDistinguishesUnsupportedUnicodeTitles(t *testing.T) {
	grinning, err := Generate(Input{CanonicalURL: "https://example.test/emoji/", SiteTitle: "Site", Title: "😀"})
	if err != nil {
		t.Fatal(err)
	}
	unicorn, err := Generate(Input{CanonicalURL: "https://example.test/emoji/", SiteTitle: "Site", Title: "🦄"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(grinning.PNG, unicorn.PNG) || grinning.PNGHash == unicorn.PNGHash {
		t.Fatal("distinct unsupported Unicode titles produced the same PNG")
	}
}

func TestGenerateUsesCoverAndRejectsInvalidCover(t *testing.T) {
	cover := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			cover.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, cover); err != nil {
		t.Fatal(err)
	}
	withCover, err := Generate(Input{CanonicalURL: "https://example.test/a/", SiteTitle: "Site", Title: "Title", Cover: encoded.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	withoutCover, err := Generate(Input{CanonicalURL: "https://example.test/a/", SiteTitle: "Site", Title: "Title"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(withCover.PNG, withoutCover.PNG) || withCover.Path == withoutCover.Path {
		t.Fatal("cover did not affect card output")
	}
	if _, err := Generate(Input{CanonicalURL: "https://example.test/a/", SiteTitle: "Site", Title: "Title", Cover: []byte("not an image")}); err == nil {
		t.Fatal("invalid cover accepted")
	}
}
