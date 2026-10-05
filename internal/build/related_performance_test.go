package build

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"testing"

	internalanalyze "github.com/simp-lee/oxpio/internal/analyze"
	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/recommend"
	"github.com/simp-lee/oxpio/internal/render"
	"github.com/simp-lee/oxpio/internal/siteplan"
	"github.com/simp-lee/oxpio/internal/testutil/relatedfixture"
)

const relatedEndToEndRSSHelperEnv = "OXPIO_RELATED_END_TO_END_RSS_HELPER"

func TestRelatedPerformanceBudgets(t *testing.T) {
	if os.Getenv("OXPIO_RELATED_PERF") != "1" {
		t.Skip("set OXPIO_RELATED_PERF=1 on the fixed acceptance host")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"end-to-end", "rss"} {
		command := exec.Command(filepath.Join(root, "test", "verify-related-benchmarks.sh"), mode)
		command.Dir = root
		command.Env = os.Environ()
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s performance verification error = %v\n%s", mode, err, output)
		}
		t.Logf("%s", output)
	}
}

func BenchmarkRelatedEndToEndWarm(b *testing.B) {
	cases := []struct {
		name  string
		kind  string
		count int
	}{
		{name: "mixed-5000", kind: relatedfixture.CaseMixed, count: 5000},
	}
	for _, current := range cases {
		b.Run(current.name, func(b *testing.B) {
			vault := writeRelatedBuildFixture(b, current.kind, current.count)
			if _, err := recommend.Tokenize("分布式数据库一致性协议"); err != nil {
				b.Fatal(err)
			}
			outputs := b.TempDir()
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				output := filepath.Join(outputs, strconv.Itoa(iteration))
				result, err := BuildWithOptions(vault, output, Options{Concurrency: 4})
				if err != nil {
					b.Fatalf("BuildWithOptions() error = %v; result = %#v", err, result)
				}
				b.StopTimer()
				validateRelatedBuildResult(b, result, output, current.count)
				runtime.KeepAlive(result)
				b.StartTimer()
			}
		})
	}
}

func TestRSSHelperIsolation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("VmHWM is available only on Linux")
	}
	if specification := os.Getenv(relatedEndToEndRSSHelperEnv); specification != "" {
		kind, count, err := parseRelatedBuildFixtureSpecification(specification)
		if err != nil {
			t.Fatal(err)
		}
		vault := writeRelatedBuildFixture(t, kind, count)
		if _, err := recommend.Tokenize("分布式数据库一致性协议"); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(t.TempDir(), "site")
		result, err := BuildWithOptions(vault, output, Options{Concurrency: 4})
		if err != nil {
			t.Fatalf("BuildWithOptions() error = %v; result = %#v", err, result)
		}
		validateRelatedBuildResult(t, result, output, count)
		peak, err := readBuildVmHWM()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("RELATED_END_TO_END_RSS %s %d %d\n", kind, count, peak)
		runtime.KeepAlive(result)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestRSSHelperIsolation$", "-test.count=1")
	command.Env = append(os.Environ(), relatedEndToEndRSSHelperEnv+"="+relatedfixture.CaseMixed+":20")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("RSS helper error = %v\n%s", err, output)
	}
	if strings.Count(string(output), "RELATED_END_TO_END_RSS mixed 20 ") != 1 {
		t.Fatalf("RSS helper output = %q, want exactly one validated build case", output)
	}
}

func TestRelatedMemoryLifetimes(t *testing.T) {
	profileDir := os.Getenv("OXPIO_RELATED_PROFILE_DIR")
	if profileDir == "" {
		t.Skip("set OXPIO_RELATED_PROFILE_DIR to an external profile directory")
	}

	previousProfileRate := runtime.MemProfileRate
	runtime.MemProfileRate = 1
	defer func() { runtime.MemProfileRate = previousProfileRate }()
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}

	const count = 20
	vault := writeRelatedBuildFixture(t, relatedfixture.CaseMixed, count)
	output := filepath.Join(t.TempDir(), "site")
	analysis, err := internalanalyze.AnalyzeWithOutputAndConcurrency(vault, output, 4)
	if err != nil {
		t.Fatalf("AnalyzeWithOutputAndConcurrency() error = %v; diagnostics = %#v", err, analysis.Diagnostics)
	}
	if analysis.Plan == nil || len(analysis.Plan.RelatedSemantic) != count {
		t.Fatalf("related semantic inputs = %d, want %d before page output", relatedSemanticCount(analysis.Plan), count)
	}

	result, err := buildStrictSite(analysis.Plan, vault, output, nil, 4)
	if err != nil {
		t.Fatalf("buildStrictSite() error = %v; result = %#v", err, result)
	}
	if len(analysis.Plan.RelatedSemantic) != 0 {
		t.Fatalf("related semantic inputs retained after ranking = %d, want 0", len(analysis.Plan.RelatedSemantic))
	}
	validateRelatedBuildResult(t, result, output, count)

	runtime.GC()
	profilePath := filepath.Join(profileDir, "build-page-output.pprof")
	writeBuildHeapProfile(t, profilePath)
	info, err := os.Stat(profilePath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("profile %s missing/empty: %v", profilePath, err)
	}
	runtime.KeepAlive(analysis.Plan)
	runtime.KeepAlive(result)
}

func TestBuildStrictRelationsConsumesRelatedSemanticOwner(t *testing.T) {
	fixture, err := relatedfixture.Generate(relatedfixture.CaseMixed, 20)
	if err != nil {
		t.Fatal(err)
	}
	planned := &siteplan.Result{
		Plan:            &model.SitePlan{Config: model.SiteConfig{Related: model.RelatedConfig{Enabled: true, Count: 5}}},
		Index:           fixture.Index,
		RelatedSemantic: fixture.Semantics,
	}

	_, related, err := buildStrictRelations(planned, 4)
	if err != nil {
		t.Fatal(err)
	}
	if planned.RelatedSemantic != nil {
		t.Fatalf("RelatedSemantic = %#v, want consumed nil owner", planned.RelatedSemantic)
	}
	if len(related) != 20 {
		t.Fatalf("related result documents = %d, want 20", len(related))
	}
}

func TestBuildStrictRelationsPartitionsRecommendationCorpusByVersion(t *testing.T) {
	index := &model.VaultIndex{Notes: make(map[string]*model.Note)}
	semantics := make([]model.RelatedSemanticDocument, 0, 4)
	for _, versionID := range []string{"v1", "v2"} {
		for _, name := range []string{"a", "b"} {
			relPath := "docs/" + versionID + "/" + name + ".md"
			index.Notes[relPath] = &model.Note{
				RelPath:    relPath,
				VersionID:  versionID,
				RawContent: []byte("database protocol"),
				Frontmatter: model.Frontmatter{
					Title: versionID + " " + name,
				},
			}
			semantics = append(semantics, model.RelatedSemanticDocument{
				RelPath: relPath,
				Title:   versionID + " " + name,
				Body:    "database protocol",
			})
		}
	}
	planned := &siteplan.Result{
		Plan: &model.SitePlan{Config: model.SiteConfig{Related: model.RelatedConfig{
			Enabled: true,
			Count:   3,
		}}},
		Index:           index,
		RelatedSemantic: semantics,
	}

	_, related, err := buildStrictRelations(planned, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(related) != len(index.Notes) {
		t.Fatalf("related result documents = %d, want %d", len(related), len(index.Notes))
	}
	for sourcePath, candidates := range related {
		source := index.Notes[sourcePath]
		if len(candidates) != 1 {
			t.Errorf("related[%q] = %d candidates, want the one same-version peer", sourcePath, len(candidates))
			continue
		}
		if candidates[0].VersionID != source.VersionID {
			t.Errorf("related[%q] candidate version = %q, want %q", sourcePath, candidates[0].VersionID, source.VersionID)
		}
	}
}

func writeRelatedBuildFixture(tb testing.TB, kind string, count int) string {
	tb.Helper()
	fixture, err := relatedfixture.Generate(kind, count)
	if err != nil {
		tb.Fatal(err)
	}

	vault := tb.TempDir()
	writeRelatedBuildFile(tb, vault, "oxpio.yaml", `title: Related Performance Garden
baseURL: https://related.example.test/
navigation: []
sidebar:
  enabled: false
popover:
  enabled: false
related:
  enabled: true
  count: 5
rss:
  enabled: false
timeline:
  enabled: false
`)
	writeRelatedBuildFile(tb, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nRelated performance fixture.\n")
	writeRelatedBuildFile(tb, vault, "notes/_index.md", "---\ntitle: Notes\npublish: true\n---\nGenerated notes.\n")
	for _, document := range fixture.Semantics {
		note := fixture.Index.Notes[document.RelPath]
		var content strings.Builder
		_, _ = fmt.Fprintf(&content, "---\ntitle: %s\npublish: true\ntype: page\n", document.Title)
		if note != nil && len(note.Tags) > 0 {
			content.WriteString("tags:\n")
			for _, tag := range note.Tags {
				_, _ = fmt.Fprintf(&content, "  - %s\n", tag)
			}
		}
		content.WriteString("---\n")
		content.WriteString(document.Body)
		content.WriteByte('\n')
		writeRelatedBuildFile(tb, vault, document.RelPath, content.String())
	}
	clear(fixture.Semantics)
	fixture = relatedfixture.Fixture{}
	runtime.GC()
	return vault
}

func writeRelatedBuildFile(tb testing.TB, root, relPath, content string) {
	tb.Helper()
	name := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		tb.Fatal(err)
	}
}

func validateRelatedBuildResult(tb testing.TB, result *BuildResult, output string, count int) {
	tb.Helper()
	if result == nil {
		tb.Fatal("build result is nil")
		return
	}
	if len(result.Diagnostics) != 0 {
		tb.Fatalf("build diagnostics = %#v, want none", result.Diagnostics)
	}
	if result.NotePages != count || result.Index == nil || len(result.Index.Notes) != count {
		tb.Fatalf("built notes = %d pages/%d indexed, want %d/%d", result.NotePages, indexedNoteCount(result), count, count)
	}
	if result.TagPages != 9 {
		tb.Fatalf("tag pages = %d, want 9", result.TagPages)
	}
	for _, tag := range []string{"topic-0", "language-0"} {
		if result.Index.Tags[tag] == nil {
			tb.Fatalf("representative tag %q missing from build index", tag)
		}
	}

	article := result.Index.Notes["notes/00000.md"]
	if article == nil {
		tb.Fatal("representative article notes/00000.md missing")
	}
	for _, tag := range []string{"topic-0", "language-0"} {
		if !slices.Contains(article.Tags, tag) {
			tb.Fatalf("representative article tags = %#v, want %q", article.Tags, tag)
		}
	}
	page, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(render.StrictRouteOutputPath(article.Route))))
	if err != nil {
		tb.Fatalf("read representative article: %v", err)
	}
	if !bytes.Contains(page, []byte(`class=related-articles`)) {
		tb.Fatalf("representative article has no related output: %s", page)
	}
	if article.SocialImage == "" {
		tb.Fatal("representative article has no social image path")
	}
	card, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(article.SocialImage)))
	if err != nil {
		tb.Fatalf("read representative social card: %v", err)
	}
	if !bytes.HasPrefix(card, []byte("\x89PNG\r\n\x1a\n")) {
		tb.Fatalf("representative social card is not PNG: %q", card[:min(len(card), 16)])
	}
}

func indexedNoteCount(result *BuildResult) int {
	if result == nil || result.Index == nil {
		return 0
	}
	return len(result.Index.Notes)
}

func relatedSemanticCount(result *siteplan.Result) int {
	if result == nil {
		return 0
	}
	return len(result.RelatedSemantic)
}

func parseRelatedBuildFixtureSpecification(value string) (string, int, error) {
	kind, countText, ok := strings.Cut(value, ":")
	if !ok {
		return "", 0, fmt.Errorf("invalid fixture specification %q", value)
	}
	count, err := strconv.Atoi(countText)
	if err != nil || count < 1 {
		return "", 0, fmt.Errorf("invalid fixture count %q", countText)
	}
	return kind, count, nil
}

func readBuildVmHWM() (value int64, err error) {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return 0, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "VmHWM:" {
			return strconv.ParseInt(fields[1], 10, 64)
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("VmHWM not found")
}

func writeBuildHeapProfile(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := pprof.WriteHeapProfile(file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
