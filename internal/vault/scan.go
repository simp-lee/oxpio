package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/publishpath"
)

const (
	obsidianConfigDir = ".obsidian"
	obsidianAppJSON   = ".obsidian/app.json"
)

// ScanResult is the Step 11 handoff for later frontmatter parsing and index building.
// It captures the candidate Markdown files, candidate resources, and the normalized
// Obsidian attachment folder setting when present.
type ScanResult struct {
	VaultPath            string
	AttachmentFolderPath string
	MarkdownFiles        []string
	ResourceFiles        []string
	// OverlayMarkdown and OverlayDeleted are candidate-only inputs used by
	// edit transactions; they never write or replace vault files.
	OverlayMarkdown map[string][]byte
	OverlayDeleted  map[string]bool

	markdownSet       map[string]struct{}
	resourceSet       map[string]string
	resourceLookup    map[string]string
	resourceConflicts map[string][]string
}

// ScanOptions carries the already-resolved output exclusion shared with build and watch.
type ScanOptions struct {
	OutputPath      string
	OverlayMarkdown map[string][]byte
	OverlayDeleted  map[string]bool
}

// Scan walks a vault once without an output exclusion.
func Scan(vaultPath string) (ScanResult, error) {
	return ScanWithOptions(vaultPath, ScanOptions{})
}

// ScanWithOptions walks a vault once and returns the Markdown and resource
// candidates needed by later phases. Hidden entries, node_modules, the resolved
// output, all .obsidian content except the separately-read app.json, symlinks,
// and non-regular files are excluded. Overlay keys are normalized before
// merging; malformed keys fail the scan and reserved/output paths are ignored.
// attachmentFolderPath is preserved as normalized metadata only and does not
// relax scan boundaries.
func ScanWithOptions(vaultPath string, options ScanOptions) (ScanResult, error) {
	absVaultPath, err := normalizeVaultPath(vaultPath)
	if err != nil {
		return ScanResult{}, err
	}

	excludedOutput := filepath.Clean(strings.TrimSpace(options.OutputPath))
	if excludedOutput == "." || !internalfsutil.PathWithinRoot(absVaultPath, excludedOutput) {
		excludedOutput = ""
	}

	overlayMarkdown, err := normalizeOverlayMarkdown(absVaultPath, excludedOutput, options.OverlayMarkdown)
	if err != nil {
		return ScanResult{}, err
	}
	overlayDeleted, err := normalizeOverlayDeleted(absVaultPath, excludedOutput, options.OverlayDeleted)
	if err != nil {
		return ScanResult{}, err
	}

	attachmentFolderPath, err := readAttachmentFolderPath(absVaultPath, excludedOutput)
	if err != nil {
		return ScanResult{}, err
	}

	result := ScanResult{
		VaultPath:            absVaultPath,
		AttachmentFolderPath: attachmentFolderPath,
		OverlayMarkdown:      overlayMarkdown,
		OverlayDeleted:       overlayDeleted,
		markdownSet:          make(map[string]struct{}),
	}

	err = filepath.WalkDir(absVaultPath, func(currentPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(absVaultPath, currentPath)
		if err != nil {
			return fmt.Errorf("compute relative path for %q: %w", currentPath, err)
		}
		relPath = filepath.ToSlash(relPath)
		if relPath == "." {
			return nil
		}
		if excludedOutput != "" && internalfsutil.SamePath(currentPath, excludedOutput) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		isSymlink, err := isSymlinkEntry(entry)
		if err != nil {
			return fmt.Errorf("inspect %q: %w", currentPath, err)
		}
		if isSymlink {
			return nil
		}

		name := entry.Name()
		if entry.IsDir() {
			if shouldSkipPath(relPath) {
				return fs.SkipDir
			}
			return nil
		}

		if shouldSkipPath(relPath) {
			return nil
		}

		isRegular, err := isRegularFileEntry(entry)
		if err != nil {
			return fmt.Errorf("inspect %q: %w", currentPath, err)
		}
		if !isRegular {
			return nil
		}

		if isMarkdownFile(name) {
			result.markdownSet[relPath] = struct{}{}
			result.MarkdownFiles = append(result.MarkdownFiles, relPath)
			return nil
		}

		result.ResourceFiles = append(result.ResourceFiles, relPath)
		return nil
	})
	if err != nil {
		return ScanResult{}, fmt.Errorf("scan vault %q: %w", absVaultPath, err)
	}

	for relPath := range result.OverlayMarkdown {
		if result.OverlayDeleted[relPath] || !isMarkdownFile(path.Base(relPath)) || shouldSkipPath(relPath) {
			continue
		}
		if _, exists := result.markdownSet[relPath]; exists {
			continue
		}
		result.markdownSet[relPath] = struct{}{}
		result.MarkdownFiles = append(result.MarkdownFiles, relPath)
	}
	for relPath := range result.OverlayDeleted {
		if _, exists := result.markdownSet[relPath]; !exists {
			continue
		}
		delete(result.markdownSet, relPath)
		for index, candidate := range result.MarkdownFiles {
			if candidate == relPath {
				result.MarkdownFiles = append(result.MarkdownFiles[:index], result.MarkdownFiles[index+1:]...)
				break
			}
		}
	}
	sort.Strings(result.MarkdownFiles)
	sort.Strings(result.ResourceFiles)
	result.resourceSet = model.BuildExactLookupPaths(result.ResourceFiles)
	result.resourceLookup, result.resourceConflicts = model.BuildCanonicalLookupPaths(result.ResourceFiles)

	return result, nil
}

// LookupResourcePath returns the scanned vault-relative resource path that
// matches relPath after exact and canonical Unicode lookup.
func (r ScanResult) LookupResourcePath(relPath string) model.PathLookupResult {
	if exactKey := normalizeLookupPath(relPath); exactKey != "" {
		if resolved := r.resourceSet[exactKey]; resolved != "" {
			return model.PathLookupResult{Path: resolved}
		}
	}

	canonicalKey := model.CanonicalResourceLookupPath(relPath)
	if canonicalKey == "" {
		return model.PathLookupResult{}
	}
	if ambiguous := r.resourceConflicts[canonicalKey]; len(ambiguous) > 0 {
		return model.PathLookupResult{Ambiguous: append([]string(nil), ambiguous...)}
	}

	return model.PathLookupResult{Path: r.resourceLookup[canonicalKey]}
}

func normalizeOverlayMarkdown(vaultPath, outputPath string, input map[string][]byte) (map[string][]byte, error) {
	if len(input) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(input))
	for relPath := range input {
		keys = append(keys, relPath)
	}
	sort.Strings(keys)

	output := make(map[string][]byte, len(input))
	for _, rawPath := range keys {
		relPath, err := normalizeOverlayPath(rawPath)
		if err != nil {
			return nil, fmt.Errorf("overlay Markdown path %q: %w", rawPath, err)
		}
		resolvedRelPath, err := resolveOverlayPath(vaultPath, relPath)
		if err != nil {
			return nil, fmt.Errorf("overlay Markdown path %q: %w", rawPath, err)
		}
		if shouldSkipPath(resolvedRelPath) || shouldExcludeOverlayPath(vaultPath, outputPath, relPath) {
			continue
		}
		if _, exists := output[relPath]; exists {
			return nil, fmt.Errorf("overlay Markdown paths %q and %q normalize to the same path", rawPath, relPath)
		}
		output[relPath] = append([]byte(nil), input[rawPath]...)
	}
	if len(output) == 0 {
		return nil, nil
	}
	return output, nil
}

func normalizeOverlayDeleted(vaultPath, outputPath string, input map[string]bool) (map[string]bool, error) {
	if len(input) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(input))
	for relPath, deleted := range input {
		if deleted {
			keys = append(keys, relPath)
		}
	}
	sort.Strings(keys)

	output := make(map[string]bool, len(keys))
	for _, rawPath := range keys {
		relPath, err := normalizeOverlayPath(rawPath)
		if err != nil {
			return nil, fmt.Errorf("deleted overlay path %q: %w", rawPath, err)
		}
		resolvedRelPath, err := resolveOverlayPath(vaultPath, relPath)
		if err != nil {
			return nil, fmt.Errorf("deleted overlay path %q: %w", rawPath, err)
		}
		if shouldSkipPath(resolvedRelPath) || shouldExcludeOverlayPath(vaultPath, outputPath, relPath) {
			continue
		}
		if _, exists := output[relPath]; exists {
			return nil, fmt.Errorf("deleted overlay paths %q and %q normalize to the same path", rawPath, relPath)
		}
		output[relPath] = true
	}
	if len(output) == 0 {
		return nil, nil
	}
	return output, nil
}

func normalizeOverlayPath(rawPath string) (string, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", errors.New("path must be non-empty")
	}

	normalized := strings.ReplaceAll(trimmed, `\`, "/")
	if strings.ContainsRune(normalized, '\x00') {
		return "", errors.New("path must not contain NUL bytes")
	}
	if strings.HasPrefix(normalized, "/") || hasWindowsDrivePathPrefix(normalized) {
		return "", errors.New("path must be vault-relative")
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return "", errors.New("path must not contain traversal segments")
		}
	}

	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path must be a non-empty vault-relative path")
	}
	return cleaned, nil
}

func hasWindowsDrivePathPrefix(value string) bool {
	return len(value) >= 2 && isASCIILetter(value[0]) && value[1] == ':'
}

func resolveOverlayPath(vaultPath, relPath string) (string, error) {
	candidate := filepath.Join(vaultPath, filepath.FromSlash(relPath))
	resolved, err := internalfsutil.ResolvePathAfterSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if !internalfsutil.PathWithinRoot(vaultPath, resolved) {
		return "", errors.New("path must resolve inside the vault")
	}
	relative, err := filepath.Rel(vaultPath, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("path must resolve inside the vault")
	}
	return filepath.ToSlash(relative), nil
}

func shouldExcludeOverlayPath(vaultPath, outputPath, relPath string) bool {
	if shouldSkipPath(relPath) {
		return true
	}
	if outputPath == "" {
		return false
	}
	candidate := filepath.Join(vaultPath, filepath.FromSlash(relPath))
	return internalfsutil.PathWithinRootAfterSymlinks(outputPath, candidate)
}

func normalizeVaultPath(vaultPath string) (string, error) {
	return internalfsutil.ResolveVaultPath(vaultPath)
}

func readAttachmentFolderPath(vaultPath, outputPath string) (string, error) {
	appConfigPath := filepath.Join(vaultPath, filepath.FromSlash(obsidianAppJSON))
	if outputPath != "" && internalfsutil.PathWithinRoot(outputPath, appConfigPath) {
		return "", nil
	}

	configDirPath := filepath.Join(vaultPath, obsidianConfigDir)
	if _, _, err := internalfsutil.InspectContainedDirectory(vaultPath, configDirPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if errors.Is(err, internalfsutil.ErrSymlinkPath) {
			return "", fmt.Errorf("obsidian config path %q must not be a symbolic link", configDirPath)
		}
		if errors.Is(err, internalfsutil.ErrUnsupportedRegularFileSource) {
			return "", nil
		}
		return "", err
	}

	_, data, _, err := internalfsutil.ReadContainedRegularFile(vaultPath, appConfigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if errors.Is(err, internalfsutil.ErrSymlinkPath) {
			return "", fmt.Errorf("obsidian config path %q must not be a symbolic link", appConfigPath)
		}
		if errors.Is(err, internalfsutil.ErrUnsupportedRegularFileSource) {
			return "", fmt.Errorf("obsidian config path %q must be a regular file", appConfigPath)
		}
		return "", fmt.Errorf("read %q: %w", appConfigPath, err)
	}

	var config struct {
		AttachmentFolderPath string `json:"attachmentFolderPath"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("parse %q: %w", appConfigPath, err)
	}

	normalizedPath, err := normalizeAttachmentFolderPath(config.AttachmentFolderPath)
	if err != nil {
		return "", fmt.Errorf("normalize attachmentFolderPath from %q: %w", appConfigPath, err)
	}
	return normalizedPath, nil
}

func normalizeAttachmentFolderPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	normalized := strings.ReplaceAll(raw, `\`, "/")
	if hasWindowsDriveAbsolutePath(normalized) || hasDoubleSlashAbsolutePath(normalized) {
		return "", fmt.Errorf("attachmentFolderPath must stay inside the vault: %q", raw)
	}

	cleaned := path.Clean(normalized)
	if cleaned == "." {
		return ".", nil
	}

	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("attachmentFolderPath must stay inside the vault: %q", raw)
	}

	return cleaned, nil
}

func hasWindowsDriveAbsolutePath(cleaned string) bool {
	if len(cleaned) < 2 {
		return false
	}
	if !isASCIILetter(cleaned[0]) || cleaned[1] != ':' {
		return false
	}
	return len(cleaned) == 2 || cleaned[2] == '/'
}

func hasDoubleSlashAbsolutePath(cleaned string) bool {
	return strings.HasPrefix(cleaned, "//")
}

func isASCIILetter(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}

func shouldSkipPath(relPath string) bool {
	normalizedRelPath := normalizeLookupPath(relPath)
	if normalizedRelPath == "" || normalizedRelPath == "." {
		return false
	}
	return publishpath.IsReservedPath(normalizedRelPath)
}

func isSymlinkEntry(entry fs.DirEntry) (bool, error) {
	if entry.Type()&fs.ModeSymlink != 0 {
		return true, nil
	}
	if entry.Type().IsRegular() || entry.IsDir() {
		return false, nil
	}

	info, err := entry.Info()
	if err != nil {
		return false, err
	}
	return info.Mode()&fs.ModeSymlink != 0, nil
}

func isRegularFileEntry(entry fs.DirEntry) (bool, error) {
	if entry.Type().IsRegular() {
		return true, nil
	}
	if entry.Type()&fs.ModeSymlink != 0 || entry.IsDir() {
		return false, nil
	}

	info, err := entry.Info()
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

func isMarkdownFile(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".md")
}

func normalizeLookupPath(relPath string) string {
	trimmed := strings.TrimSpace(relPath)
	if trimmed == "" {
		return ""
	}

	normalized := strings.ReplaceAll(trimmed, `\`, "/")
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
