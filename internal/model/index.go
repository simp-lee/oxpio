package model

import (
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/simp-lee/oxpio/internal/slug"
)

// VaultIndex is the immutable handoff between pass 1 indexing and pass 2 rendering.
// Pass 2 must clone any per-render note state instead of mutating notes held here.
//
// Exact asset and resource sets remain the source of truth. Derived canonical
// fallback tables are populated eagerly by the strict index builder and are
// also rebuilt lazily on first lookup so callers cannot observe a
// half-initialized handoff object.
type VaultIndex struct {
	AttachmentFolderPath string
	Notes                map[string]*Note
	Sections             map[string]*Section
	SectionsBySource     map[string]*Section
	SectionsByRoute      map[string]*Section
	NoteBySlug           map[string]*Note
	NoteByName           map[string][]*Note
	AliasByName          map[string][]*Note
	Tags                 map[string]*Tag
	Assets               map[string]*Asset
	AssetLookup          map[string]string
	Resources            map[string]string
	ResourceBaseNames    map[string]string
	ResourceVersions     map[string]string
	Unpublished          UnpublishedLookup

	lookupMu                  sync.RWMutex
	assetLookupReady          bool
	assetLookupConflicts      map[string][]string
	resourceLookupReady       bool
	resourcePathLookup        map[string]string
	resourcePathConflicts     map[string][]string
	resourceBaseNameConflicts map[string][]string
}

// UnpublishedLookup retains enough metadata for later path/name/alias resolution
// after unpublished notes are removed from the public lookup tables.
type UnpublishedLookup struct {
	Notes       map[string]*Note
	NoteByName  map[string][]*Note
	AliasByName map[string][]*Note
}

// LinkGraph stores forward and backward note relationships.
type LinkGraph struct {
	Forward  map[string][]string
	Backward map[string][]string
}

// PathLookupResult describes an exact or canonical resource lookup.
//
// Path is populated when lookup succeeds. Ambiguous is populated when canonical
// fallback matched multiple vault-relative paths and lookup intentionally
// refuses to choose a winner.
type PathLookupResult struct {
	Path      string
	Ambiguous []string
}

func (idx *VaultIndex) SetResources(resourceFiles []string) {
	if idx == nil {
		return
	}

	rawPaths := normalizedLookupPaths(resourceFiles)
	exact := BuildExactLookupPaths(rawPaths)
	pathLookup, pathConflicts := BuildCanonicalLookupPaths(rawPaths)
	baseLookup, baseConflicts := BuildCanonicalLookupNames(rawPaths)

	idx.lookupMu.Lock()
	defer idx.lookupMu.Unlock()

	idx.Resources = exact
	idx.resourcePathLookup = pathLookup
	idx.resourcePathConflicts = pathConflicts
	idx.ResourceBaseNames = baseLookup
	idx.resourceBaseNameConflicts = baseConflicts
	idx.resourceLookupReady = true
}

func (idx *VaultIndex) SetAssets(assets map[string]*Asset) {
	if idx == nil {
		return
	}

	rawPaths := assetLookupPaths(assets)
	lookup, conflicts := BuildCanonicalLookupPaths(rawPaths)

	idx.lookupMu.Lock()
	defer idx.lookupMu.Unlock()

	idx.Assets = assets
	idx.AssetLookup = lookup
	idx.assetLookupConflicts = conflicts
	idx.assetLookupReady = true
}

func (idx *VaultIndex) ResolveAssetPath(relPath string) string {
	return idx.LookupAssetPath(relPath).Path
}

func (idx *VaultIndex) ResolveResourcePath(relPath string) string {
	return idx.LookupResourcePath(relPath).Path
}

func (idx *VaultIndex) HasResource(relPath string) bool {
	return idx.ResolveResourcePath(relPath) != ""
}

func (idx *VaultIndex) ResolveResourceBaseName(name string) string {
	return idx.LookupResourceBaseName(name).Path
}

func (idx *VaultIndex) HasResourceBaseName(name string) bool {
	return idx.ResolveResourceBaseName(name) != ""
}

func (idx *VaultIndex) LookupAssetPath(relPath string) PathLookupResult {
	if idx == nil || len(idx.Assets) == 0 {
		return PathLookupResult{}
	}

	exactKey := normalizeLookupPathExact(relPath)
	if exactKey != "" {
		if asset, ok := idx.Assets[exactKey]; ok {
			if asset != nil && strings.TrimSpace(asset.SrcPath) != "" {
				return PathLookupResult{Path: asset.SrcPath}
			}
			return PathLookupResult{Path: exactKey}
		}
	}

	idx.ensureAssetLookup()

	canonicalKey := CanonicalResourceLookupPath(relPath)
	if canonicalKey == "" {
		return PathLookupResult{}
	}

	idx.lookupMu.RLock()
	defer idx.lookupMu.RUnlock()

	if ambiguous := idx.assetLookupConflicts[canonicalKey]; len(ambiguous) > 0 {
		return PathLookupResult{Ambiguous: cloneLookupPaths(ambiguous)}
	}

	return PathLookupResult{Path: idx.AssetLookup[canonicalKey]}
}

func (idx *VaultIndex) LookupResourcePath(relPath string) PathLookupResult {
	if idx == nil {
		return PathLookupResult{}
	}

	idx.ensureResourceLookup()

	exactKey := normalizeLookupPathExact(relPath)
	if exactKey != "" {
		idx.lookupMu.RLock()
		resolved := idx.Resources[exactKey]
		idx.lookupMu.RUnlock()
		if resolved != "" {
			return PathLookupResult{Path: resolved}
		}
	}

	canonicalKey := CanonicalResourceLookupPath(relPath)
	if canonicalKey == "" {
		return PathLookupResult{}
	}

	idx.lookupMu.RLock()
	defer idx.lookupMu.RUnlock()

	if ambiguous := idx.resourcePathConflicts[canonicalKey]; len(ambiguous) > 0 {
		return PathLookupResult{Ambiguous: cloneLookupPaths(ambiguous)}
	}

	return PathLookupResult{Path: idx.resourcePathLookup[canonicalKey]}
}

func (idx *VaultIndex) LookupResourceBaseName(name string) PathLookupResult {
	if idx == nil {
		return PathLookupResult{}
	}

	idx.ensureResourceLookup()

	canonicalKey := CanonicalResourceLookupName(name)
	if canonicalKey == "" {
		return PathLookupResult{}
	}

	idx.lookupMu.RLock()
	defer idx.lookupMu.RUnlock()

	if ambiguous := idx.resourceBaseNameConflicts[canonicalKey]; len(ambiguous) > 0 {
		return PathLookupResult{Ambiguous: cloneLookupPaths(ambiguous)}
	}

	return PathLookupResult{Path: idx.ResourceBaseNames[canonicalKey]}
}

// CanonicalResourceLookupPath returns the shared canonical form used for
// Unicode-stable resource and asset path lookup.
func CanonicalResourceLookupPath(value string) string {
	normalized := normalizeLookupPathExact(value)
	if normalized == "" {
		return ""
	}

	return slug.Canonicalize(normalized)
}

// CanonicalResourceLookupName returns the shared canonical form used for
// basename-only resource lookup.
func CanonicalResourceLookupName(value string) string {
	normalized := CanonicalResourceLookupPath(value)
	if normalized == "" {
		return ""
	}

	return slug.Canonicalize(path.Base(normalized))
}

func normalizeLookupPathExact(value string) string {
	normalized := strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	normalized = strings.TrimPrefix(normalized, "/")
	if normalized == "" {
		return ""
	}

	normalized = path.Clean(normalized)
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") {
		return ""
	}

	return normalized
}

func normalizedLookupPaths(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	rawPaths := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		rawPath := normalizeLookupPathExact(value)
		if rawPath == "" {
			continue
		}
		if _, ok := seen[rawPath]; ok {
			continue
		}
		seen[rawPath] = struct{}{}
		rawPaths = append(rawPaths, rawPath)
	}
	sort.Strings(rawPaths)
	return rawPaths
}

// BuildExactLookupPaths returns the normalized exact-path lookup table for the
// provided vault-relative paths.
func BuildExactLookupPaths(values []string) map[string]string {
	rawPaths := normalizedLookupPaths(values)
	if len(rawPaths) == 0 {
		return nil
	}

	lookup := make(map[string]string, len(rawPaths))
	for _, rawPath := range rawPaths {
		lookup[rawPath] = rawPath
	}

	return lookup
}

// BuildCanonicalLookupPaths returns the canonical-path fallback table plus any
// canonical collisions that must refuse fallback.
func BuildCanonicalLookupPaths(values []string) (map[string]string, map[string][]string) {
	rawPaths := normalizedLookupPaths(values)
	if len(rawPaths) == 0 {
		return nil, nil
	}

	grouped := make(map[string][]string, len(rawPaths))
	for _, rawPath := range rawPaths {
		canonicalKey := CanonicalResourceLookupPath(rawPath)
		if canonicalKey == "" {
			continue
		}
		grouped[canonicalKey] = append(grouped[canonicalKey], rawPath)
	}

	return splitCanonicalLookupGroups(grouped)
}

// BuildCanonicalLookupNames returns the basename-only canonical fallback table
// plus any canonical collisions that must refuse fallback.
func BuildCanonicalLookupNames(values []string) (map[string]string, map[string][]string) {
	rawPaths := normalizedLookupPaths(values)
	if len(rawPaths) == 0 {
		return nil, nil
	}

	grouped := make(map[string][]string, len(rawPaths))
	for _, rawPath := range rawPaths {
		canonicalKey := CanonicalResourceLookupName(rawPath)
		if canonicalKey == "" {
			continue
		}
		grouped[canonicalKey] = append(grouped[canonicalKey], rawPath)
	}

	return splitCanonicalLookupGroups(grouped)
}

func splitCanonicalLookupGroups(grouped map[string][]string) (map[string]string, map[string][]string) {
	if len(grouped) == 0 {
		return nil, nil
	}

	lookup := make(map[string]string, len(grouped))
	conflicts := make(map[string][]string)
	for key, candidates := range grouped {
		sort.Strings(candidates)
		if len(candidates) == 1 {
			lookup[key] = candidates[0]
			continue
		}
		conflicts[key] = append([]string(nil), candidates...)
	}

	if len(lookup) == 0 {
		lookup = nil
	}
	if len(conflicts) == 0 {
		conflicts = nil
	}

	return lookup, conflicts
}

func (idx *VaultIndex) ensureAssetLookup() {
	if idx == nil {
		return
	}

	idx.lookupMu.RLock()
	ready := idx.assetLookupReady
	idx.lookupMu.RUnlock()
	if ready {
		return
	}

	idx.lookupMu.Lock()
	defer idx.lookupMu.Unlock()
	if idx.assetLookupReady {
		return
	}

	rawPaths := assetLookupPaths(idx.Assets)
	idx.AssetLookup, idx.assetLookupConflicts = BuildCanonicalLookupPaths(rawPaths)
	idx.assetLookupReady = true
}

func (idx *VaultIndex) ensureResourceLookup() {
	if idx == nil {
		return
	}

	idx.lookupMu.RLock()
	ready := idx.resourceLookupReady
	idx.lookupMu.RUnlock()
	if ready {
		return
	}

	idx.lookupMu.Lock()
	defer idx.lookupMu.Unlock()
	if idx.resourceLookupReady {
		return
	}

	rawPaths := resourceLookupPaths(idx.Resources)
	idx.Resources = BuildExactLookupPaths(rawPaths)
	idx.resourcePathLookup, idx.resourcePathConflicts = BuildCanonicalLookupPaths(rawPaths)
	idx.ResourceBaseNames, idx.resourceBaseNameConflicts = BuildCanonicalLookupNames(rawPaths)
	idx.resourceLookupReady = true
}

func assetLookupPaths(assets map[string]*Asset) []string {
	if len(assets) == 0 {
		return nil
	}

	rawPaths := make([]string, 0, len(assets))
	seen := make(map[string]struct{}, len(assets))
	for key, asset := range assets {
		rawPath := normalizeLookupPathExact(key)
		if asset != nil {
			if srcPath := normalizeLookupPathExact(asset.SrcPath); srcPath != "" {
				rawPath = srcPath
			}
		}
		if rawPath == "" {
			continue
		}
		if _, ok := seen[rawPath]; ok {
			continue
		}
		seen[rawPath] = struct{}{}
		rawPaths = append(rawPaths, rawPath)
	}
	if len(rawPaths) == 0 {
		return nil
	}

	sort.Strings(rawPaths)
	return rawPaths
}

func resourceLookupPaths(resources map[string]string) []string {
	if len(resources) == 0 {
		return nil
	}

	rawPaths := make([]string, 0, len(resources))
	seen := make(map[string]struct{}, len(resources))
	for key, value := range resources {
		rawPath := normalizeLookupPathExact(value)
		if rawPath == "" {
			rawPath = normalizeLookupPathExact(key)
		}
		if rawPath == "" {
			continue
		}
		if _, ok := seen[rawPath]; ok {
			continue
		}
		seen[rawPath] = struct{}{}
		rawPaths = append(rawPaths, rawPath)
	}
	if len(rawPaths) == 0 {
		return nil
	}

	sort.Strings(rawPaths)
	return rawPaths
}

func cloneLookupPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}

	return append([]string(nil), paths...)
}
