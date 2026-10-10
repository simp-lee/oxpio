package application

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	internalanalyze "github.com/simp-lee/oxpio/internal/analyze"
	internalbuild "github.com/simp-lee/oxpio/internal/build"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/model"
)

var (
	ErrHiddenSource  = errors.New("source is not available in the selected read scope")
	ErrInvalidSource = errors.New("source is not valid UTF-8 Markdown")
)

// Snapshot is the immutable normalized source handoff shared by read-only
// Agent queries. It contains no filesystem handles and never writes output.
type Snapshot struct {
	VaultPath  string
	OutputPath string
	Analysis   internalanalyze.Result
	Catalog    *model.SourceCatalog
}

// OpenSnapshot analyzes one Vault/output boundary and builds the canonical
// source catalog used by all query adapters.
func OpenSnapshot(vaultPath, outputPath string) (Snapshot, error) {
	if strings.TrimSpace(vaultPath) == "" {
		vaultPath = "."
	}
	if strings.TrimSpace(outputPath) == "" {
		outputPath = filepath.Join(vaultPath, "public")
	}
	boundary, err := internalfsutil.ResolveVaultOutput(vaultPath, outputPath)
	if err != nil {
		return Snapshot{}, err
	}
	analysis, err := internalanalyze.AnalyzeWithOutput(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		// Query failures are intentionally generic here: a malformed or hidden
		// source must not disclose its Vault-relative identity before scope
		// filtering can be applied. Invalid byte diagnostics are safe to expose
		// without a path and let the grep adapter return a stable source error.
		if invalidSourceAnalysis(err) {
			return Snapshot{}, ErrInvalidSource
		}
		return Snapshot{}, errors.New("normalized analysis failed")
	}
	if analysis.Plan == nil {
		return Snapshot{}, errors.New("normalized analysis is unavailable")
	}
	catalog := internalbuild.SourceCatalogForPlan(analysis.Plan)
	if catalog == nil {
		return Snapshot{}, errors.New("normalized source catalog is unavailable")
	}
	return Snapshot{VaultPath: boundary.VaultPath, OutputPath: boundary.OutputPath, Analysis: analysis, Catalog: catalog}, nil
}

func invalidSourceAnalysis(err error) bool {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "oxpio.yaml\"") {
		return false
	}
	for _, marker := range []string{"utf-8", "utf8", "nul byte", "control character"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// Entries returns stable catalog entries visible to the requested scope.
func (snapshot Snapshot) Entries(includeUnpublished bool) []model.SourceCatalogEntry {
	if snapshot.Catalog == nil {
		return nil
	}
	entries := make([]model.SourceCatalogEntry, 0, len(snapshot.Catalog.Entries))
	for _, entry := range snapshot.Catalog.Entries {
		if !includeUnpublished && !entry.EffectivePublish {
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RelPath < entries[j].RelPath })
	return entries
}

// Entry resolves a Vault-relative source path after applying permission scope.
func (snapshot Snapshot) Entry(relPath string, includeUnpublished bool) (model.SourceCatalogEntry, error) {
	normalized, err := NormalizeSourcePath(relPath)
	if err != nil {
		return model.SourceCatalogEntry{}, err
	}
	for _, entry := range snapshot.Catalog.Entries {
		if entry.RelPath != normalized {
			continue
		}
		if !includeUnpublished && !entry.EffectivePublish {
			return model.SourceCatalogEntry{}, ErrHiddenSource
		}
		return entry, nil
	}
	return model.SourceCatalogEntry{}, fmt.Errorf("source %q was not found", normalized)
}

// ReadSource reads the original Markdown bytes for an authorized catalog entry.
func (snapshot Snapshot) ReadSource(relPath string, includeUnpublished bool) (model.SourceCatalogEntry, []byte, error) {
	entry, err := snapshot.Entry(relPath, includeUnpublished)
	if err != nil {
		return model.SourceCatalogEntry{}, nil, err
	}
	if len(entry.RawSource) == 0 {
		return model.SourceCatalogEntry{}, nil, errors.New("source snapshot is unavailable")
	}
	return entry, append([]byte(nil), entry.RawSource...), nil
}

// NormalizeSourcePath validates the public Vault-relative POSIX source path.
type DirectoryEntry struct {
	Path  string
	Kind  string
	Bytes int64
}

// ListDirectory enumerates one Vault-relative directory without reading source
// contents. Hidden source subtrees remain inaccessible unless explicitly
// requested by the caller.
func (snapshot Snapshot) ListDirectory(relPath string, includeUnpublished bool) ([]DirectoryEntry, error) {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		relPath = "."
	}
	if relPath != "." {
		if normalized, err := NormalizeDirectoryPath(relPath); err != nil {
			return nil, err
		} else {
			relPath = normalized
		}
		if !includeUnpublished && !snapshot.directoryAllowed(relPath) {
			return nil, ErrHiddenSource
		}
	}
	absolute := snapshot.VaultPath
	if relPath != "." {
		absolute = filepath.Join(snapshot.VaultPath, filepath.FromSlash(relPath))
	}
	if internalfsutil.PathWithinRootAfterSymlinks(snapshot.OutputPath, absolute) {
		return nil, errors.New("directory is inside the output boundary")
	}
	if relPath != "." {
		if _, _, err := internalfsutil.InspectContainedDirectory(snapshot.VaultPath, absolute); err != nil {
			return nil, err
		}
	} else if info, err := os.Stat(absolute); err != nil || !info.IsDir() {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("path is not a directory")
	}
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return nil, err
	}
	result := make([]DirectoryEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		child := name
		if relPath != "." {
			child = path.Join(relPath, name)
		}
		if child == ".oxpio" || strings.HasPrefix(child, ".oxpio/") || child == ".obsidian" || strings.HasPrefix(child, ".obsidian/") {
			continue
		}
		if entry.IsDir() {
			if !includeUnpublished && !snapshot.directoryAllowed(child) {
				continue
			}
			result = append(result, DirectoryEntry{Path: child, Kind: "directory"})
			continue
		}
		if !entry.Type().IsRegular() {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil, infoErr
		}
		if strings.HasSuffix(strings.ToLower(name), ".md") {
			found := false
			for _, source := range snapshot.Catalog.Entries {
				if source.RelPath == child {
					if includeUnpublished || source.EffectivePublish {
						result = append(result, DirectoryEntry{Path: child, Kind: source.Kind, Bytes: info.Size()})
					}
					found = true
					break
				}
			}
			if found {
				continue
			}
		}
		result = append(result, DirectoryEntry{Path: child, Kind: "file", Bytes: info.Size()})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

// DirectoryVisibleForQuery reports whether a relative directory can be
// enumerated without exposing a hidden source ancestor.
func (snapshot Snapshot) DirectoryVisibleForQuery(relPath string) bool {
	return snapshot.directoryAllowed(relPath)
}

func (snapshot Snapshot) directoryAllowed(relPath string) bool {
	if relPath == "." || relPath == "" {
		for _, entry := range snapshot.Catalog.Entries {
			if entry.Kind == "section" && entry.SectionPath == "." {
				return entry.EffectivePublish
			}
		}
		return true
	}
	for current := relPath; current != "." && current != ""; current = path.Dir(current) {
		if !snapshot.directoryVisible(current) {
			return false
		}
	}
	return true
}

func (snapshot Snapshot) directoryVisible(relPath string) bool {
	prefix := relPath + "/"
	seenHidden := false
	seenVisible := false
	for _, entry := range snapshot.Catalog.Entries {
		if entry.RelPath != relPath && !strings.HasPrefix(entry.RelPath, prefix) {
			continue
		}
		if entry.EffectivePublish {
			seenVisible = true
		} else {
			seenHidden = true
		}
	}
	if !seenVisible && !seenHidden {
		return true
	}
	return !seenHidden
}

// NormalizeDirectoryPath validates a Vault-relative POSIX directory path.
func NormalizeDirectoryPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return ".", nil
	}
	if value == "." {
		return value, nil
	}
	cleaned, err := normalizeVaultPath(value)
	if err != nil {
		return "", err
	}
	return cleaned, nil
}

func normalizeVaultPath(value string) (string, error) {
	if strings.HasPrefix(value, "/") || strings.Contains(value, `\`) || strings.ContainsAny(value, "?#\x00") {
		return "", errors.New("path must be a normalized Vault-relative POSIX path")
	}
	cleaned := path.Clean(value)
	if cleaned != value || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path must be a normalized Vault-relative POSIX path")
	}
	if cleaned == ".oxpio" || strings.HasPrefix(cleaned, ".oxpio/") || cleaned == ".obsidian" || strings.HasPrefix(cleaned, ".obsidian/") {
		return "", errors.New("path is outside the content source boundary")
	}
	if !internalfsutil.IsPortableSitePath(cleaned) {
		return "", errors.New("path contains a non-portable path segment")
	}
	return cleaned, nil
}

func NormalizeSourcePath(value string) (string, error) {
	cleaned, err := normalizeVaultPath(value)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(strings.ToLower(cleaned), ".md") {
		return "", errors.New("source path must name a Markdown file")
	}
	return cleaned, nil
}
