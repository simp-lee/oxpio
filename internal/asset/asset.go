package asset

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/model"
	internalslug "github.com/simp-lee/oxpio/internal/slug"
)

const outputDirPrefix = "assets"

var errUnsupportedAssetSource = errors.New("asset source must be a regular non-symlink file inside the vault")

func planAssetDestinations(vaultRoot string, assets map[string]*model.Asset, reservedOutputKeys map[string]struct{}) map[string]string {
	return planAssetDestinationsWithOverrides(vaultRoot, assets, reservedOutputKeys, nil)
}

func planAssetDestinationsWithOverrides(vaultRoot string, assets map[string]*model.Asset, reservedOutputKeys map[string]struct{}, overrides map[string][]byte) map[string]string {
	grouped := make(map[string][]string)

	for key, asset := range assets {
		srcPath := normalizeAssetSource(key, asset)
		if srcPath == "" {
			continue
		}
		groupKey := plainAssetKey(srcPath)
		grouped[groupKey] = append(grouped[groupKey], srcPath)
	}

	planned := make(map[string]string, len(assets))
	groupKeys := make([]string, 0, len(grouped))
	for groupKey := range grouped {
		groupKeys = append(groupKeys, groupKey)
	}
	sort.Strings(groupKeys)

	for _, groupKey := range groupKeys {
		sources := grouped[groupKey]
		sort.Strings(sources)
		hashed := hashCollisionPathsWithOverrides(vaultRoot, groupKey, sources, overrides)

		if len(sources) == 1 {
			planned[sources[0]] = avoidReservedOutputPath(hashed[sources[0]], reservedOutputKeys)
			continue
		}

		for _, srcPath := range sources {
			planned[srcPath] = avoidReservedOutputPath(hashed[srcPath], reservedOutputKeys)
		}
	}

	return planned
}

func hashCollisionPaths(vaultRoot string, groupKey string, sources []string) map[string]string {
	return hashCollisionPathsWithOverrides(vaultRoot, groupKey, sources, nil)
}

func hashCollisionPathsWithOverrides(vaultRoot string, groupKey string, sources []string, overrides map[string][]byte) map[string]string {
	hashes := make(map[string]string, len(sources))
	for _, srcPath := range sources {
		hashValue, err := assetHashWithOverrides(vaultRoot, srcPath, overrides)
		if err != nil {
			hashValue = missingAssetHash(srcPath)
		}
		hashes[srcPath] = hashValue
	}

	planned := make(map[string]string, len(sources))
	baseName := path.Base(groupKey)
	if decoded, err := url.PathUnescape(baseName); err == nil {
		baseName = decoded
	}
	for _, srcPath := range sources {
		planned[srcPath] = hashedAssetPathForBase(baseName, hashes[srcPath])
	}

	return planned
}

// PlanData allocates an explicitly discovered, contained input using the same
// content-addressed destinations as collected Markdown resources. The caller
// owns source admission and must keep the emitted data immutable; this does not
// make internal inputs discoverable through Markdown or the vault inventory.
func PlanData(srcPath string, data []byte) *model.PlannedAsset {
	hash := sha256.Sum256(data)
	return &model.PlannedAsset{
		Asset: model.Asset{SrcPath: srcPath, DstPath: hashedAssetPath(srcPath, hex.EncodeToString(hash[:]))},
		Data:  data,
	}
}

// DestinationCollisionError describes two sources that the asset planner
// assigned to the same output destination.
type DestinationCollisionError struct {
	Destination    string
	FirstSource    string
	SecondSource   string
	DifferentBytes bool
}

func (err *DestinationCollisionError) Error() string {
	if err == nil {
		return "asset destination collision"
	}
	if err.DifferentBytes {
		return fmt.Sprintf("asset destination %q is claimed by %q and %q with different content", err.Destination, err.FirstSource, err.SecondSource)
	}
	return fmt.Sprintf("asset destination %q is claimed by distinct sources %q and %q", err.Destination, err.FirstSource, err.SecondSource)
}

// ValidateDestinationCollisions checks the destinations produced by the asset
// planner. Distinct sources may share a destination only when they are not
// marked distinct and have identical content. overrides supplies bytes for
// explicitly planned inputs that are not read from vaultRoot.
func ValidateDestinationCollisions(vaultRoot string, assets map[string]*model.Asset, distinct map[string]bool, overrides map[string][]byte) error {
	sources := make([]string, 0, len(assets))
	for source, asset := range assets {
		if asset != nil && asset.DstPath != "" {
			sources = append(sources, source)
		}
	}
	sort.Strings(sources)

	owners := make(map[string]string, len(sources))
	hashes := make(map[string]string, len(sources))
	for _, source := range sources {
		asset := assets[source]
		data, ok := overrides[source]
		if !ok {
			_, readData, _, err := internalfsutil.ReadContainedRegularFile(vaultRoot, source)
			if err != nil {
				return fmt.Errorf("read asset %q: %w", source, err)
			}
			data = readData
		}
		hash := sha256.Sum256(data)
		hashValue := fmt.Sprintf("%x", hash)
		if owner, exists := owners[asset.DstPath]; exists {
			if (distinct[owner] || distinct[source]) && hashes[asset.DstPath] == hashValue {
				return &DestinationCollisionError{Destination: asset.DstPath, FirstSource: owner, SecondSource: source}
			}
			if hashes[asset.DstPath] != hashValue {
				return &DestinationCollisionError{Destination: asset.DstPath, FirstSource: owner, SecondSource: source, DifferentBytes: true}
			}
			continue
		}
		owners[asset.DstPath] = source
		hashes[asset.DstPath] = hashValue
	}
	return nil
}

func plainAssetPath(srcPath string) string {
	return path.Join(outputDirPrefix, encodeAssetSegment(path.Base(srcPath)))
}

func hashedAssetPath(srcPath string, suffix string) string {
	return hashedAssetPathForBase(path.Base(srcPath), suffix)
}

func hashedAssetPathForBase(baseName string, suffix string) string {
	baseName = path.Base(strings.TrimSpace(strings.ReplaceAll(baseName, "\\", "/")))
	baseName = strings.ToLower(baseName)
	if baseName == "" || baseName == "." || baseName == "/" {
		baseName = "asset"
	}
	ext := path.Ext(baseName)
	stem := strings.TrimSuffix(baseName, ext)
	if stem == "" {
		stem = baseName
	}

	return path.Join(outputDirPrefix, encodeAssetSegment(stem)+"."+suffix+encodeAssetSegment(ext))
}

func encodeAssetSegment(value string) string {
	const hex = "0123456789ABCDEF"
	var result strings.Builder
	for _, b := range []byte(value) {
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || b == '.' || b == '_' || b == '~' {
			result.WriteByte(b)
		} else {
			result.WriteByte('%')
			result.WriteByte(hex[b>>4])
			result.WriteByte(hex[b&0x0f])
		}
	}
	return result.String()
}

func normalizeAssetSource(key string, asset *model.Asset) string {
	if asset != nil && strings.TrimSpace(asset.SrcPath) != "" {
		if srcPath := normalizePublishableAssetPath(asset.SrcPath); srcPath != "" {
			return srcPath
		}
	}

	return normalizePublishableAssetPath(key)
}

func normalizeAssetPath(value string) string {
	if !isVaultRelativeAssetInput(value) {
		return ""
	}

	normalized := normalizePath(value)
	if normalized == "" || isOutsideVaultPath(normalized) {
		return ""
	}

	return normalized
}

func normalizePublishableAssetPath(value string) string {
	normalized := normalizeAssetPath(value)
	if normalized == "" || shouldSkipPublishableAssetPath(normalized) {
		return ""
	}

	return normalized
}

// IsPublishableAssetPath reports whether value is a vault-relative path that the
// asset pipeline accepts as a publishable input.
func IsPublishableAssetPath(value string) bool {
	return normalizePublishableAssetPath(value) != ""
}

func outputSitePath(value string) string {
	normalized := normalizePath(value)
	if normalized == "" || isOutsideVaultPath(normalized) {
		return ""
	}
	if normalized == outputDirPrefix || !strings.HasPrefix(normalized, outputDirPrefix+"/") {
		return ""
	}

	return normalized
}

func outputSiteKey(value string) string {
	normalized := outputSitePath(value)
	if normalized == "" {
		return ""
	}

	return internalslug.Canonicalize(normalized)
}

func normalizeReservedOutputKeys(reservedOutputPaths []string) map[string]struct{} {
	if len(reservedOutputPaths) == 0 {
		return nil
	}

	reserved := make(map[string]struct{}, len(reservedOutputPaths))
	for _, reservedOutputPath := range reservedOutputPaths {
		if key := outputSiteKey(reservedOutputPath); key != "" {
			reserved[key] = struct{}{}
		}
	}
	if len(reserved) == 0 {
		return nil
	}

	return reserved
}

func avoidReservedOutputPath(dstPath string, reservedOutputKeys map[string]struct{}) string {
	if !isReservedOutputKey(dstPath, reservedOutputKeys) {
		return dstPath
	}

	ext := path.Ext(dstPath)
	stem := strings.TrimSuffix(dstPath, ext)
	for attempt := 1; ; attempt++ {
		candidate := fmt.Sprintf("%s-%d%s", stem, attempt, ext)
		if !isReservedOutputKey(candidate, reservedOutputKeys) {
			return candidate
		}
	}
}

func isReservedOutputKey(outputPath string, reservedOutputKeys map[string]struct{}) bool {
	if outputPath == "" || len(reservedOutputKeys) == 0 {
		return false
	}

	_, ok := reservedOutputKeys[outputSiteKey(outputPath)]
	return ok
}

func plainAssetKey(srcPath string) string {
	return outputSiteKey(plainAssetPath(srcPath))
}

func assetHash(vaultRoot string, srcPath string) (hashHex string, err error) {
	return assetHashWithOverrides(vaultRoot, srcPath, nil)
}

func assetHashWithOverrides(vaultRoot string, srcPath string, overrides map[string][]byte) (hashHex string, err error) {
	if data, ok := overrides[srcPath]; ok {
		hash := sha256.Sum256(data)
		return hex.EncodeToString(hash[:]), nil
	}
	if vaultRoot == "" {
		return missingAssetHash(srcPath), nil
	}

	_, file, _, err := openAssetSource(vaultRoot, srcPath)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	return fileHashHex(file)
}

func isVaultRelativeAssetInput(value string) bool {
	cleaned := strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if cleaned == "" {
		return false
	}
	if shouldKeepDestination(cleaned) {
		return false
	}
	if strings.HasPrefix(cleaned, "//") {
		return false
	}
	if len(cleaned) >= 2 && isASCIIAlpha(cleaned[0]) && cleaned[1] == ':' {
		return false
	}

	return true
}

func isASCIIAlpha(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func shouldSkipPublishableAssetPath(relPath string) bool {
	normalized := normalizePath(relPath)
	if normalized == "" || normalized == "." {
		return false
	}

	for _, segment := range strings.Split(normalized, "/") {
		switch segment {
		case "":
			continue
		case ".obsidian", ".oxpio", "node_modules":
			return true
		}
	}

	return false
}

func assetSourceInfo(vaultRoot string, srcPath string) (string, os.FileInfo, error) {
	vaultRoot = strings.TrimSpace(vaultRoot)
	if vaultRoot == "" || srcPath == "" {
		return "", nil, os.ErrNotExist
	}

	resolvedPath, info, err := internalfsutil.InspectContainedRegularFile(vaultRoot, filepath.FromSlash(srcPath))
	if err != nil {
		if errors.Is(err, internalfsutil.ErrUnsupportedRegularFileSource) ||
			errors.Is(err, internalfsutil.ErrPathOutsideRoot) ||
			errors.Is(err, internalfsutil.ErrSymlinkPath) {
			return "", nil, errUnsupportedAssetSource
		}
		return "", nil, err
	}
	return resolvedPath, info, nil
}

func missingAssetHash(srcPath string) string {
	sum := sha256.Sum256([]byte("missing:" + srcPath))
	return hex.EncodeToString(sum[:])
}

func openAssetSource(vaultRoot string, srcPath string) (string, *os.File, os.FileInfo, error) {
	resolvedPath, file, info, err := internalfsutil.OpenContainedRegularFile(vaultRoot, filepath.FromSlash(srcPath))
	if err != nil {
		if errors.Is(err, internalfsutil.ErrUnsupportedRegularFileSource) ||
			errors.Is(err, internalfsutil.ErrPathOutsideRoot) ||
			errors.Is(err, internalfsutil.ErrSymlinkPath) {
			return "", nil, nil, errUnsupportedAssetSource
		}
		return "", nil, nil, err
	}
	return resolvedPath, file, info, nil
}

func fileHashHex(reader io.Reader) (string, error) {
	hasher := sha256.New()
	if _, err := io.Copy(hasher, reader); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
