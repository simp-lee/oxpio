package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func TestLoadForBuildRequiresStrictNavigationAndRejectsLegacyFields(t *testing.T) {
	for name, content := range map[string]string{
		"missing navigation": "title: Site\nbaseURL: https://example.test/\n",
		"legacy default":     "title: Site\nbaseURL: https://example.test/\nnavigation: []\ndefaultPublish: true\n",
		"unknown field":      "title: Site\nbaseURL: https://example.test/\nnavigation: []\nsearch: {}\n",
		"null navigation":    "title: Site\nbaseURL: https://example.test/\nnavigation: null\n",
		"duplicate key":      "title: Site\nbaseURL: https://example.test/\nnavigation: []\ntitle: Other\n",
	} {
		t.Run(name, func(t *testing.T) {
			vault := writeConfigVault(t, content)
			if _, err := LoadForBuild(vault); err == nil {
				t.Fatal("LoadForBuild() error = nil, want strict schema error")
			}
		})
	}
}

func TestLoadForBuildRejectsExplicitEmptyTargetsAndTemplates(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "navigation keys are mutually exclusive even when empty",
			content: "navigation:\n  - name: Home\n    section: .\n    url: ''\n",
			want:    "navigation[0] must contain exactly one of url or section",
		},
		{
			name:    "empty navigation URL",
			content: "navigation:\n  - name: Home\n    url: '  '\n",
			want:    "navigation[0].url must be non-empty",
		},
		{
			name:    "empty navigation section",
			content: "navigation:\n  - name: Home\n    section: ''\n",
			want:    "navigation[0].section must be non-empty",
		},
		{
			name:    "empty edit template",
			content: "navigation: []\nsource:\n  editURL: ''\n",
			want:    "source.editURL must be non-empty",
		},
		{
			name:    "empty view template",
			content: "navigation: []\nsource:\n  viewURL: '  '\n",
			want:    "source.viewURL must be non-empty",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := "title: Site\nbaseURL: https://example.test/\n" + test.content
			_, err := LoadForBuild(writeConfigVault(t, content))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadForBuild() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadForBuildRejectsScalarTypeCoercion(t *testing.T) {
	for _, test := range []struct {
		name     string
		content  string
		field    string
		typeName string
	}{
		{name: "legacy boolean spelling", content: "related:\n  enabled: yes\n", field: "related.enabled", typeName: "boolean"},
		{name: "floating point integer", content: "related:\n  count: 1.9\n", field: "related.count", typeName: "integer"},
		{name: "numeric navigation name", content: "navigation:\n  - name: 123\n    section: .\n", field: "navigation[0].name", typeName: "string"},
		{name: "wrong sequence tag", content: "navigation: !!str []\n", field: "navigation", typeName: "sequence"},
		{name: "wrong mapping tag", content: "source: !!seq {}\n", field: "source", typeName: "mapping"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := "title: Site\nbaseURL: https://example.test/\nnavigation: []\n"
			if strings.HasPrefix(test.content, "navigation:") {
				base = "title: Site\nbaseURL: https://example.test/\n"
			}
			_, err := LoadForBuild(writeConfigVault(t, base+test.content))
			if err == nil || !strings.Contains(err.Error(), test.field) || !strings.Contains(err.Error(), test.typeName) {
				t.Fatalf("LoadForBuild() error = %v, want %s YAML %s rejection", err, test.field, test.typeName)
			}
		})
	}
}

func TestLoadForBuildNormalizesRevisedConfig(t *testing.T) {
	vault := writeConfigVault(t, `title: Site
baseURL: https://example.test/base
language: fr
description: Public site
defaultImg: https://images.example.test/card.png
navigation:
  - name: Home
    section: .
  - name: Repository
    url: https://git.example.test:8443/source/:path?ref=main
source:
  editURL: https://git.example.test/edit/:path
  viewURL: https://git.example.test/view/:path
versions:
  root: docs
  default: v1
  entries:
    - id: v1
      label: Version 1
      source: v1
sidebar:
  enabled: true
related:
  enabled: true
  count: 7
`)
	cfg, err := LoadForBuild(vault)
	if err != nil {
		t.Fatalf("LoadForBuild() error = %v", err)
	}
	if cfg.Title != "Site" || cfg.BaseURL != "https://example.test/base/" || cfg.Language != "fr" || !cfg.DefaultImgExternal {
		t.Fatalf("normalized metadata = %#v", cfg)
	}
	if len(cfg.Navigation) != 2 || cfg.Navigation[0].Section != "." || cfg.Source.ViewURL == "" {
		t.Fatalf("normalized navigation/source = %#v / %#v", cfg.Navigation, cfg.Source)
	}
	if cfg.Versions == nil || cfg.Versions.Entries[0].ID != "v1" || !cfg.Sidebar.Enabled || cfg.Related.Count != 7 {
		t.Fatalf("normalized features = %#v", cfg)
	}
}

func TestNormalizeSiteConfigPreservesEncodedBasePathTrailingSlash(t *testing.T) {
	for _, value := range []string{"https://example.test/文档/", "https://example.test/docs%20x/"} {
		t.Run(value, func(t *testing.T) {
			cfg, err := NormalizeSiteConfig(model.SiteConfig{Title: "Site", BaseURL: value})
			if err != nil {
				t.Fatalf("NormalizeSiteConfig(%q) error = %v", value, err)
			}
			if !strings.HasSuffix(cfg.BaseURL, "/") {
				t.Fatalf("BaseURL = %q, want trailing slash", cfg.BaseURL)
			}
		})
	}
}

func TestNormalizeSiteConfigRejectsEmptyBaseURLQueryOrFragment(t *testing.T) {
	t.Parallel()

	for _, baseURL := range []string{"https://example.test/docs/?", "https://example.test/docs/#"} {
		_, err := NormalizeSiteConfig(model.SiteConfig{Title: "Site", BaseURL: baseURL})
		if err == nil || !strings.Contains(err.Error(), "must not include query or fragment") {
			t.Fatalf("NormalizeSiteConfig(%q) error = %v, want query or fragment validation error", baseURL, err)
		}
	}
}

func TestNormalizeSiteConfigValidatesStrictURLAndVersionRules(t *testing.T) {
	for _, value := range []string{
		"https://example.test/a%2Fb",
		"https://example.test/a/%2e%2e/b",
		"https://example.test/a%5Cb",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := NormalizeSiteConfig(model.SiteConfig{Title: "Site", BaseURL: value})
			if err == nil || !strings.Contains(err.Error(), "encoded separators or dot segments") {
				t.Fatalf("NormalizeSiteConfig(%q) error = %v", value, err)
			}
		})
	}

	_, err := NormalizeSiteConfig(structuredConfig(model.SourceConfig{ViewURL: "https://example.test/view/:path/:branch"}, nil))
	if err == nil || !strings.Contains(err.Error(), "unknown template placeholder") {
		t.Fatalf("source error = %v", err)
	}
	versions := &model.VersionsConfig{Root: "docs", Default: "v1", Entries: []model.VersionEntry{
		{ID: "v1", Label: "one", Source: "v1"}, {ID: "v2", Label: "two", Source: "v1/chapter"},
	}}
	_, err = NormalizeSiteConfig(structuredConfig(model.SourceConfig{}, versions))
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("version error = %v", err)
	}
}

func TestInitialStrictYAMLIsStrictAndBuildConfigurable(t *testing.T) {
	content := InitialStrictYAML()
	if !strings.Contains(content, "navigation: []") || !strings.Contains(content, "source: {}") {
		t.Fatalf("InitialStrictYAML() = %q", content)
	}
	if strings.Contains(content, "defaultPublish") || strings.Contains(content, "timeline:") {
		t.Fatalf("InitialStrictYAML() contains superseded fields: %q", content)
	}
	cfg, err := LoadForBuild(writeConfigVault(t, content))
	if err != nil {
		t.Fatalf("LoadForBuild(InitialStrictYAML()) error = %v", err)
	}
	if cfg.Title != "My OXPIO Site" || cfg.BaseURL != "https://example.com/" {
		t.Fatalf("initial config = %#v", cfg)
	}
}

func TestNormalizeSiteConfigSupportsOnlyExplicitDefaultImageModes(t *testing.T) {
	for _, tt := range []struct {
		name     string
		value    string
		external bool
	}{
		{name: "empty", value: ""},
		{name: "external", value: "https://images.example.test/hero.png?size=2#card", external: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := NormalizeSiteConfig(model.SiteConfig{Title: "Site", BaseURL: "https://example.test/", DefaultImg: tt.value})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.DefaultImgExternal != tt.external {
				t.Fatalf("DefaultImgExternal = %t, want %t", cfg.DefaultImgExternal, tt.external)
			}
		})
	}
	for _, value := range []string{"images/hero.png?v=1", "images/hero.png#card", `images\\hero.png`, "/images/hero.png", "../hero.png", "data:image/png;base64,AA", "https://user:secret@example.test/card.png"} {
		t.Run(value, func(t *testing.T) {
			_, err := NormalizeSiteConfig(model.SiteConfig{Title: "Site", BaseURL: "https://example.test/", DefaultImg: value})
			if err == nil || !strings.Contains(err.Error(), "defaultImg") {
				t.Fatalf("NormalizeSiteConfig(%q) error = %v", value, err)
			}
		})
	}
}

func structuredConfig(source model.SourceConfig, versions *model.VersionsConfig) model.SiteConfig {
	cfg := Defaults()
	cfg.Title, cfg.BaseURL = "Site", "https://example.test/"
	cfg.Navigation = []model.NavigationItem{{Name: "Home", Section: "."}}
	cfg.Source, cfg.Versions = source, versions
	return cfg
}

func writeConfigVault(t *testing.T, content string) string {
	t.Helper()
	vault := t.TempDir()
	writeConfigTestFile(t, vault, Filename, strings.TrimSpace(content)+"\n")
	return vault
}

func writeConfigTestFile(t *testing.T, root string, relPath string, content string) {
	t.Helper()
	filePath := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
