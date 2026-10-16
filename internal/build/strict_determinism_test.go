package build

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
)

func TestStrictBuildIsByteStableAcrossConcurrentBuilds(t *testing.T) {
	vault := makeStrictVersionedMetadataVault(t, false, false)
	roots := make([]string, 4)
	buildResults := make([]*BuildResult, len(roots))
	for index := range roots {
		roots[index] = filepath.Join(t.TempDir(), "site")
	}
	type completion struct {
		index  int
		result *BuildResult
		err    error
	}
	completed := make(chan completion, len(roots))
	for index, root := range roots {
		index, root := index, root
		go func() {
			result, err := BuildWithOptions(vault, root, Options{})
			completed <- completion{index: index, result: result, err: err}
		}()
	}
	for range roots {
		item := <-completed
		if item.err != nil {
			t.Fatal(item.err)
		}
		buildResults[item.index] = item.result
	}
	wantBytes := strictOutputBytes(t, roots[0])
	wantURLs := strictOutputURLs(buildResults[0])
	for index, root := range roots[1:] {
		compareStrictOutputBytes(t, wantBytes, strictOutputBytes(t, root))
		compareStrictURLValues(t, wantURLs, strictOutputURLs(buildResults[index+1]))
	}
}

func TestStrictBuildVersionedMetadataDeterminismMatrix(t *testing.T) {
	type mode struct {
		name        string
		incremental bool
	}
	type inputOrder struct {
		name    string
		reverse bool
	}
	modes := []mode{{name: "full"}, {name: "incremental", incremental: true}}
	orders := []inputOrder{{name: "forward"}, {name: "reverse", reverse: true}}
	concurrencies := []int{1, 4}

	var wantBytes map[string][]byte
	var wantURLs map[string]string
	for _, buildMode := range modes {
		for _, order := range orders {
			for _, concurrency := range concurrencies {
				name := buildMode.name + "/" + order.name + "/workers-" + strconv.Itoa(concurrency)
				t.Run(name, func(t *testing.T) {
					vault := makeStrictVersionedMetadataVault(t, order.reverse, buildMode.incremental)
					output := filepath.Join(t.TempDir(), "site")
					var precursorStyle os.FileInfo
					if buildMode.incremental {
						seedConcurrency := 1
						if concurrency == 1 {
							seedConcurrency = 4
						}
						if _, err := BuildWithOptions(vault, output, Options{Concurrency: seedConcurrency}); err != nil {
							t.Fatalf("build incremental precursor: %v", err)
						}
						if loadStrictCacheManifest(vault) == nil {
							t.Fatal("incremental precursor did not produce a cache manifest")
						}
						if _, err := os.Stat(filepath.Join(output, "docs", "v1", "start-preview", "index.html")); err != nil {
							t.Fatalf("incremental precursor route is missing: %v", err)
						}
						var statErr error
						precursorStyle, statErr = os.Stat(filepath.Join(output, "style.css"))
						if statErr != nil {
							t.Fatalf("incremental precursor style.css is missing: %v", statErr)
						}
						writeStrictVersionedMetadataVault(t, vault, order.reverse, false)
					}

					result, err := BuildWithOptions(vault, output, Options{Concurrency: concurrency})
					if err != nil {
						t.Fatalf("BuildWithOptions(concurrency=%d): %v", concurrency, err)
					}
					gotBytes := strictOutputBytes(t, output)
					gotURLs := strictOutputURLs(result)
					assertStrictVersionedMetadataCoverage(t, result)
					if buildMode.incremental {
						currentStyle, statErr := os.Stat(filepath.Join(output, "style.css"))
						if statErr != nil {
							t.Fatalf("incremental style.css is missing after rebuild: %v", statErr)
						}
						if !os.SameFile(precursorStyle, currentStyle) {
							t.Fatal("incremental rebuild rewrote unrelated style.css output")
						}

						unchanged, unchangedErr := BuildWithOptions(vault, output, Options{Concurrency: concurrency})
						if unchangedErr != nil {
							t.Fatalf("unchanged incremental rebuild: %v", unchangedErr)
						}
						compareStrictOutputBytes(t, gotBytes, strictOutputBytes(t, output))
						compareStrictURLValues(t, gotURLs, strictOutputURLs(unchanged))
						unchangedStyle, statErr := os.Stat(filepath.Join(output, "style.css"))
						if statErr != nil {
							t.Fatalf("unchanged incremental style.css is missing: %v", statErr)
						}
						if !os.SameFile(currentStyle, unchangedStyle) {
							t.Fatal("unchanged incremental rebuild rewrote unrelated style.css output")
						}
					}
					if wantBytes == nil {
						wantBytes = gotBytes
						wantURLs = gotURLs
						return
					}
					compareStrictOutputBytes(t, wantBytes, gotBytes)
					compareStrictURLValues(t, wantURLs, gotURLs)
				})
			}
		}
	}
}

func assertStrictVersionedMetadataCoverage(t *testing.T, result *BuildResult) {
	t.Helper()
	if result == nil || result.Index == nil {
		t.Fatal("versioned determinism fixture produced no index")
	}
	coveredArticle := false
	for _, note := range result.Index.Notes {
		if note == nil || note.VersionID == "" || len(note.VersionRoutes) < 2 {
			continue
		}
		metadata := note.Frontmatter
		if metadata.Author != "" && metadata.Status != "" && metadata.Audience != "" && metadata.ProductVersion != "" && metadata.Series != "" && note.BannerURL != "" && note.CoverURL != "" && note.SocialImage != "" {
			coveredArticle = true
			break
		}
	}
	if !coveredArticle {
		t.Fatal("determinism fixture does not cover version routes, metadata, banner, cover, and social URLs")
	}
	for _, section := range result.Index.Sections {
		if section != nil && section.VersionID != "" && len(section.VersionRoutes) >= 2 && section.BannerURL != "" {
			return
		}
	}
	t.Fatal("determinism fixture does not cover versioned section and banner URLs")
}

func strictOutputURLs(result *BuildResult) map[string]string {
	values := make(map[string]string)
	if result == nil || result.Index == nil {
		return values
	}
	for source, asset := range result.Assets {
		if asset != nil {
			values["asset:"+source] = asset.DstPath
		}
	}
	for relPath, note := range result.Index.Notes {
		if note == nil {
			continue
		}
		prefix := "note:" + relPath + ":"
		values[prefix+"route"] = note.Route
		values[prefix+"social"] = note.SocialImage
		values[prefix+"banner"] = note.BannerURL
		values[prefix+"cover"] = note.CoverURL
		for version, route := range note.VersionRoutes {
			values[prefix+"version:"+version] = route
		}
	}
	for relPath, section := range result.Index.Sections {
		if section == nil {
			continue
		}
		prefix := "section:" + relPath + ":"
		values[prefix+"route"] = section.Route
		values[prefix+"banner"] = section.BannerURL
		for version, route := range section.VersionRoutes {
			values[prefix+"version:"+version] = route
		}
		for index, breadcrumb := range section.Breadcrumbs {
			values[prefix+"breadcrumb:"+strconv.Itoa(index)] = breadcrumb.URL
		}
	}
	return values
}

func compareStrictOutputBytes(t *testing.T, want, got map[string][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("output file counts differ: %d and %d", len(want), len(got))
	}
	for name, data := range want {
		other, ok := got[name]
		if !ok {
			t.Fatalf("output omitted file %q", name)
		}
		if !bytes.Equal(data, other) {
			t.Fatalf("output file %q differs", name)
		}
	}
}

func compareStrictURLValues(t *testing.T, want, got map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("URL value counts differ: %d and %d", len(want), len(got))
	}
	for name, value := range want {
		other, ok := got[name]
		if !ok {
			t.Fatalf("output omitted URL %q", name)
		}
		if other != value {
			t.Fatalf("output changed URL %q: %q and %q", name, value, other)
		}
	}
}

func strictOutputBytes(t *testing.T, root string) map[string][]byte {
	t.Helper()
	paths := make([]string, 0)
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			paths = append(paths, rel)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	result := make(map[string][]byte, len(paths))
	for _, rel := range paths {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		result[rel] = data
	}
	return result
}
