package asset

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/simp-lee/oxpio/internal/model"
)

// AssetCollector records pass-2 asset registrations and returns the site-relative
// output path that renderers should use in generated HTML.
type AssetCollector struct {
	mu                 sync.Mutex
	vaultRoot          string
	assets             map[string]*model.Asset
	registeredByGroup  map[string][]string
	planned            map[string]string
	reservedOutputKeys map[string]struct{}
	overrides          map[string][]byte
	seededByGroup      map[string][]string
	inventoryByGroup   map[string][]string
	scanInventoryHook  func() error
}

// NewCollectorWithResourceFiles creates a collector that optionally reuses a
// caller-provided emitted-resource inventory instead of walking the vault.
// Passing nil keeps collision planning scoped to indexed assets plus later
// pass-2 registrations.
func NewCollectorWithResourceFiles(vaultRoot string, indexed map[string]*model.Asset, reservedOutputPaths []string, resourceFiles []string) (*AssetCollector, error) {
	return newCollectorWithReservedPaths(vaultRoot, indexed, reservedOutputPaths, resourceInventoryGroups(resourceFiles), nil)
}

// NewCollectorWithOverrides plans explicitly transformed assets from their
// emitted bytes while retaining the shared vault inventory and collision rules.
func NewCollectorWithOverrides(vaultRoot string, indexed map[string]*model.Asset, reservedOutputPaths []string, resourceFiles []string, overrides map[string][]byte) (*AssetCollector, error) {
	return newCollectorWithOverrides(vaultRoot, indexed, reservedOutputPaths, resourceInventoryGroups(resourceFiles), nil, overrides)
}

func newCollectorWithReservedPaths(vaultRoot string, indexed map[string]*model.Asset, reservedOutputPaths []string, inventoryByGroup map[string][]string, scanInventoryHook func() error) (*AssetCollector, error) {
	return newCollectorWithOverrides(vaultRoot, indexed, reservedOutputPaths, inventoryByGroup, scanInventoryHook, nil)
}

func newCollectorWithOverrides(vaultRoot string, indexed map[string]*model.Asset, reservedOutputPaths []string, inventoryByGroup map[string][]string, scanInventoryHook func() error, overrides map[string][]byte) (*AssetCollector, error) {
	collector := &AssetCollector{
		vaultRoot:          vaultRoot,
		assets:             make(map[string]*model.Asset),
		registeredByGroup:  make(map[string][]string),
		planned:            make(map[string]string),
		reservedOutputKeys: normalizeReservedOutputKeys(reservedOutputPaths),
		overrides:          overrides,
		seededByGroup:      make(map[string][]string),
		scanInventoryHook:  scanInventoryHook,
	}

	for srcPath, dstPath := range planAssetDestinationsWithOverrides(vaultRoot, indexed, collector.reservedOutputKeys, collector.overrides) {
		if srcPath == "" || dstPath == "" {
			continue
		}
		collector.planned[srcPath] = dstPath
		groupKey := plainAssetKey(srcPath)
		if groupKey != "" {
			collector.seededByGroup[groupKey] = append(collector.seededByGroup[groupKey], srcPath)
		}
	}

	for groupKey := range collector.seededByGroup {
		sort.Strings(collector.seededByGroup[groupKey])
	}
	if inventoryByGroup != nil {
		collector.inventoryByGroup = cloneInventoryByGroup(inventoryByGroup)
	} else {
		inventoryByGroup, err := collector.scanVaultInventory()
		if err != nil {
			return nil, fmt.Errorf("scan vault asset inventory: %w", err)
		}
		collector.inventoryByGroup = inventoryByGroup
	}

	return collector, nil
}

// Register records a vault-relative asset reference and returns the site-relative
// output path under assets/.
func (c *AssetCollector) Register(vaultRelPath string) string {
	if c == nil {
		return ""
	}

	srcPath := normalizePublishableAssetPath(vaultRelPath)
	if srcPath == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	asset := c.assets[srcPath]
	if asset == nil {
		asset = &model.Asset{SrcPath: srcPath}
		c.assets[srcPath] = asset
		groupKey := plainAssetKey(srcPath)
		if groupKey != "" {
			c.registeredByGroup[groupKey] = append(c.registeredByGroup[groupKey], srcPath)
		}
	}
	asset.RefCount++
	if asset.DstPath != "" {
		return asset.DstPath
	}
	asset.DstPath = c.registerSitePathLocked(srcPath)

	return asset.DstPath
}

// Snapshot returns a stable copy of the pass-2 registrations.
func (c *AssetCollector) Snapshot() map[string]*model.Asset {
	if c == nil {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	snapshot := make(map[string]*model.Asset, len(c.assets))
	for srcPath, asset := range c.assets {
		if asset == nil {
			continue
		}
		cloned := *asset
		snapshot[srcPath] = &cloned
	}

	return snapshot
}

// PlanDestinations expands requested sources through the collector's
// inventory-aware basename groups, updates the collector's cached plan, and
// returns destinations for the requested sources.
func (c *AssetCollector) PlanDestinations(assets map[string]*model.Asset) map[string]string {
	if c == nil || len(assets) == 0 {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	expanded := make(map[string]*model.Asset, len(assets))
	requested := make(map[string]struct{}, len(assets))
	requestedGroups := make(map[string]struct{})
	for key, asset := range assets {
		srcPath := normalizeAssetSource(key, asset)
		if srcPath == "" {
			continue
		}

		requested[srcPath] = struct{}{}
		if groupKey := plainAssetKey(srcPath); groupKey != "" {
			requestedGroups[groupKey] = struct{}{}
		}
		existing := expanded[srcPath]
		if existing == nil {
			existing = &model.Asset{SrcPath: srcPath, DstPath: c.planned[srcPath]}
			expanded[srcPath] = existing
		}
		if asset != nil {
			existing.RefCount += asset.RefCount
			if dstPath := outputSitePath(asset.DstPath); dstPath != "" {
				existing.DstPath = dstPath
			}
		}
	}
	for groupKey := range requestedGroups {
		for _, candidate := range c.groupSourcesLocked(groupKey, "") {
			if expanded[candidate] != nil {
				continue
			}
			expanded[candidate] = &model.Asset{SrcPath: candidate, DstPath: c.planned[candidate]}
		}
	}
	if len(expanded) == 0 {
		return nil
	}

	planned := planAssetDestinationsWithOverrides(c.vaultRoot, expanded, c.reservedOutputKeys, c.overrides)
	filtered := make(map[string]string, len(requested))
	for srcPath, dstPath := range planned {
		if srcPath == "" || dstPath == "" {
			continue
		}
		c.planned[srcPath] = dstPath
		if asset := c.assets[srcPath]; asset != nil {
			asset.DstPath = dstPath
		}
		if _, ok := requested[srcPath]; ok {
			filtered[srcPath] = dstPath
		}
	}

	return filtered
}

func (c *AssetCollector) registerSitePathLocked(srcPath string) string {
	if asset := c.assets[srcPath]; asset != nil && asset.DstPath != "" {
		return asset.DstPath
	}
	if dstPath := c.planned[srcPath]; dstPath != "" {
		if outputSiteKey(dstPath) == plainAssetKey(srcPath) {
			c.planGroupLocked(srcPath)
			if replanned := c.planned[srcPath]; replanned != "" {
				return replanned
			}
		}
		return dstPath
	}
	if !c.sourceExists(srcPath) {
		dstPath := avoidReservedOutputPath(hashedAssetPath(srcPath, missingAssetHash(srcPath)), c.reservedOutputKeys)
		c.planned[srcPath] = dstPath
		return dstPath
	}

	c.planGroupLocked(srcPath)
	if dstPath := c.planned[srcPath]; dstPath != "" {
		return dstPath
	}

	hashValue, err := assetHashWithOverrides(c.vaultRoot, srcPath, c.overrides)
	if err != nil {
		hashValue = missingAssetHash(srcPath)
	}

	dstPath := avoidReservedOutputPath(hashedAssetPath(srcPath, hashValue), c.reservedOutputKeys)
	c.planned[srcPath] = dstPath
	return dstPath
}

func (c *AssetCollector) planGroupLocked(srcPath string) {
	groupKey := plainAssetKey(srcPath)
	if groupKey == "" {
		return
	}

	sources := c.groupSourcesLocked(groupKey, srcPath)
	if len(sources) == 0 {
		return
	}

	assets := make(map[string]*model.Asset, len(sources))
	for _, candidate := range sources {
		asset := &model.Asset{SrcPath: candidate}
		if dstPath := c.planned[candidate]; dstPath != "" {
			asset.DstPath = dstPath
		}
		assets[candidate] = asset
	}

	for candidate, dstPath := range planAssetDestinationsWithOverrides(c.vaultRoot, assets, c.reservedOutputKeys, c.overrides) {
		if candidate == "" || dstPath == "" {
			continue
		}
		c.planned[candidate] = dstPath
		if asset := c.assets[candidate]; asset != nil {
			asset.DstPath = dstPath
		}
	}
}

func (c *AssetCollector) groupSourcesLocked(groupKey string, srcPath string) []string {
	registered := c.registeredByGroup[groupKey]
	seeded := c.seededByGroup[groupKey]
	inventory := c.inventoryByGroup[groupKey]
	sources := make(map[string]struct{}, 1+len(registered)+len(seeded)+len(inventory))
	if srcPath != "" {
		sources[srcPath] = struct{}{}
	}
	for _, candidate := range registered {
		sources[candidate] = struct{}{}
	}
	for _, candidate := range seeded {
		sources[candidate] = struct{}{}
	}
	for _, candidate := range inventory {
		sources[candidate] = struct{}{}
	}

	ordered := make([]string, 0, len(sources))
	for candidate := range sources {
		ordered = append(ordered, candidate)
	}
	sort.Strings(ordered)
	return ordered
}

func (c *AssetCollector) scanVaultInventory() (map[string][]string, error) {
	if c == nil || strings.TrimSpace(c.vaultRoot) == "" {
		return nil, nil
	}
	if c.scanInventoryHook != nil {
		if err := c.scanInventoryHook(); err != nil {
			return nil, err
		}
	}

	groups := make(map[string][]string)
	seen := make(map[string]struct{})
	err := filepath.WalkDir(c.vaultRoot, func(currentPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(c.vaultRoot, currentPath)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)
		if relPath == "." {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipPublishableAssetPath(relPath) {
				return fs.SkipDir
			}
			return nil
		}
		if shouldSkipPublishableAssetPath(relPath) {
			return nil
		}
		if regular, err := isRegularInventoryEntry(entry); err != nil {
			return err
		} else if !regular {
			return nil
		}

		normalized := normalizePublishableAssetPath(relPath)
		if normalized == "" {
			return nil
		}
		if _, ok := seen[normalized]; ok {
			return nil
		}

		seen[normalized] = struct{}{}
		groupKey := plainAssetKey(normalized)
		if groupKey == "" {
			return nil
		}
		groups[groupKey] = append(groups[groupKey], normalized)
		return nil
	})
	if err != nil {
		return nil, err
	}

	for groupKey := range groups {
		sort.Strings(groups[groupKey])
	}

	return groups, nil
}

func isRegularInventoryEntry(entry fs.DirEntry) (bool, error) {
	if entry == nil {
		return false, nil
	}
	if entry.Type().IsRegular() {
		return true, nil
	}

	info, err := entry.Info()
	if err != nil {
		return false, err
	}

	return info.Mode().IsRegular(), nil
}

func (c *AssetCollector) sourceExists(srcPath string) bool {
	if c == nil || strings.TrimSpace(c.vaultRoot) == "" || srcPath == "" {
		return false
	}

	_, _, err := assetSourceInfo(c.vaultRoot, srcPath)
	return err == nil
}

func resourceInventoryGroups(resourceFiles []string) map[string][]string {
	groups := make(map[string][]string)
	if resourceFiles == nil {
		return groups
	}

	seen := make(map[string]struct{}, len(resourceFiles))
	for _, resourceFile := range resourceFiles {
		normalized := normalizePublishableAssetPath(resourceFile)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}

		groupKey := plainAssetKey(normalized)
		if groupKey == "" {
			continue
		}
		groups[groupKey] = append(groups[groupKey], normalized)
	}

	for groupKey := range groups {
		sort.Strings(groups[groupKey])
	}

	return groups
}

func cloneInventoryByGroup(groups map[string][]string) map[string][]string {
	cloned := make(map[string][]string, len(groups))
	for groupKey, paths := range groups {
		cloned[groupKey] = append([]string(nil), paths...)
	}
	return cloned
}
