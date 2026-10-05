package config

import (
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func TestStrictConfigRejectsLegacyAndRequiresNavigation(t *testing.T) {
	for name, content := range map[string]string{
		"missing navigation": "title: Site\nbaseURL: https://example.test/\n",
		"legacy default":     "title: Site\nbaseURL: https://example.test/\nnavigation: []\ndefaultPublish: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadForBuild(writeConfigVault(t, content)); err == nil {
				t.Fatal("LoadForBuild() error = nil")
			}
		})
	}
}

func TestStrictConfigNormalizesNavigationSourceAndVersions(t *testing.T) {
	vault := writeConfigVault(t, `title: Site
baseURL: https://example.test/base
navigation:
  - name: Home
    section: .
  - name: Repository
    url: https://git.example.test:8443/source/:path?ref=main
source:
  editURL: https://git.example.test/edit/:path
versions:
  root: docs
  default: v1
  entries:
    - id: v1
      label: Version 1
      source: v1
`)
	cfg, err := LoadForBuild(vault)
	if err != nil {
		t.Fatalf("LoadForBuild() error = %v", err)
	}
	if len(cfg.Navigation) != 2 || cfg.Navigation[0].Section != "." || cfg.Source.EditURL == "" || cfg.Versions == nil {
		t.Fatalf("normalized config = %#v", cfg)
	}
}

func TestStrictConfigRejectsUnknownSourcePlaceholderAndOverlappingVersions(t *testing.T) {
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
