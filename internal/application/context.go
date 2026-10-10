package application

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	internalanalyze "github.com/simp-lee/oxpio/internal/analyze"
	internalbuild "github.com/simp-lee/oxpio/internal/build"
	internalconfig "github.com/simp-lee/oxpio/internal/config"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
)

// Context is the versioned, path-safe capability description exposed to Agents.
type Context struct {
	Schema             string         `json:"schema"`
	APIVersion         string         `json:"apiVersion"`
	Vault              string         `json:"vault"`
	Theme              string         `json:"theme"`
	AvailableThemes    []ThemeSummary `json:"availableThemes"`
	ContentKinds       []string       `json:"contentKinds"`
	Sections           int            `json:"sections"`
	Sources            int            `json:"sources"`
	IncludeUnpublished bool           `json:"includeUnpublished"`
	WritableDomains    []string       `json:"writableDomains"`
	PublishPolicy      string         `json:"publishPolicy"`
	Capabilities       []string       `json:"capabilities"`
}

// ThemeSummary is a safe theme identity summary; it never contains a filesystem path.
type ThemeSummary struct {
	ID         string `json:"id"`
	APIVersion int    `json:"apiVersion"`
	Selected   bool   `json:"selected"`
}

// StaticContext returns capabilities that do not require a Vault.
func StaticContext() Context {
	return Context{
		Schema:          "oxpio.agent/context/v1",
		APIVersion:      "oxpio.theme.vm/v1",
		Theme:           internalconfig.ThemeBaseID,
		AvailableThemes: []ThemeSummary{{ID: internalconfig.ThemeBaseID, APIVersion: 1, Selected: true}},
		ContentKinds:    []string{"section", "article", "doc", "post", "page"},
		WritableDomains: []string{"Markdown source", "theme files", "attachments.directory"},
		PublishPolicy:   "source mutations do not publish; publish is explicit",
		Capabilities:    []string{"find", "ls", "read", "grep", "tags", "write", "edit", "validate", "publish", "theme"},
	}
}

// LoadContext builds a dynamic capability description without exposing absolute paths.
func LoadContext(vaultPath, outputPath string, includeUnpublished bool) (Context, error) {
	resolvedVault, err := internalfsutil.ResolveVaultPath(vaultPath)
	if err != nil {
		return Context{}, err
	}
	if outputPath == "" {
		outputPath = filepath.Join(resolvedVault, "public")
	}
	boundary, err := internalfsutil.ResolveVaultOutput(resolvedVault, outputPath)
	if err != nil {
		return Context{}, err
	}
	cfg, err := internalconfig.LoadForBuildWithOutput(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return Context{}, err
	}
	analysis, err := internalanalyze.AnalyzeWithOutput(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return Context{}, err
	}
	if analysis.Plan == nil {
		return Context{}, fmt.Errorf("normalized analysis is unavailable")
	}
	catalog := internalbuild.SourceCatalogForPlan(analysis.Plan)
	if catalog == nil {
		return Context{}, fmt.Errorf("normalized source catalog is unavailable")
	}
	themes, err := discoverThemes(boundary.VaultPath, cfg.Theme)
	if err != nil {
		return Context{}, err
	}
	sections := 0
	sources := 0
	for _, entry := range catalog.Entries {
		if !includeUnpublished && !entry.EffectivePublish {
			continue
		}
		sources++
		if entry.Kind == "section" {
			sections++
		}
	}
	vaultName := filepath.Base(resolvedVault)
	if vaultName == string(filepath.Separator) || vaultName == "." || vaultName == "" {
		vaultName = "<vault>"
	}
	return Context{
		Schema:             "oxpio.agent/context/v1",
		APIVersion:         "oxpio.theme.vm/v1",
		Vault:              vaultName,
		Theme:              cfg.Theme,
		AvailableThemes:    themes,
		ContentKinds:       []string{"section", "article", "doc", "post", "page"},
		Sections:           sections,
		Sources:            sources,
		IncludeUnpublished: includeUnpublished,
		WritableDomains:    []string{"Markdown source", "theme files", "attachments.directory"},
		PublishPolicy:      "source mutations do not publish; publish is explicit",
		Capabilities:       []string{"find", "ls", "read", "grep", "tags", "write", "edit", "validate", "publish", "theme"},
	}, nil
}

func discoverThemes(vaultPath, selected string) ([]ThemeSummary, error) {
	result := []ThemeSummary{{ID: internalconfig.ThemeBaseID, APIVersion: 1, Selected: selected == internalconfig.ThemeBaseID}}
	root := filepath.Join(vaultPath, filepath.FromSlash(internalconfig.ThemeRootRelPath))
	if _, _, err := internalfsutil.InspectContainedDirectory(vaultPath, root); err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, fmt.Errorf("inspect themes: %w", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, fmt.Errorf("read themes: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == internalconfig.ThemeBaseID || !internalfsutil.IsPortableSitePath(entry.Name()) || strings.ContainsAny(entry.Name(), `/\\`) {
			continue
		}
		result = append(result, ThemeSummary{ID: entry.Name(), APIVersion: 1, Selected: entry.Name() == selected})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
