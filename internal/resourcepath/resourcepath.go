// Package resourcepath centralizes shared resource target rules.
package resourcepath

import (
	"net/url"
	"strings"

	internalasset "github.com/simp-lee/oxpio/internal/asset"
	"github.com/simp-lee/oxpio/internal/model"
)

// LookupResult describes a shared resource lookup attempt.
type LookupResult = model.PathLookupResult

// LookupPath applies the shared note-relative and attachment-folder lookup rules
// against a caller-provided lookup that can surface canonical ambiguity.
func LookupPath(note *model.Note, attachmentFolderPath string, rawTarget string, lookup func(string) model.PathLookupResult) model.PathLookupResult {
	if lookup == nil {
		return model.PathLookupResult{}
	}

	ambiguous := model.PathLookupResult{}
	for _, candidate := range CandidatePathsWithAttachmentFolder(note, attachmentFolderPath, rawTarget) {
		result := lookup(candidate)
		if strings.TrimSpace(result.Path) != "" {
			return result
		}
		if len(ambiguous.Ambiguous) == 0 && len(result.Ambiguous) > 0 {
			ambiguous = result
		}
	}

	return ambiguous
}

// LookupImageEmbedPath applies the shared image-embed lookup rules, including
// an explicit vault-relative slash-path fallback after note-relative lookup misses.
func LookupImageEmbedPath(note *model.Note, attachmentFolderPath string, rawTarget string, lookup func(string) model.PathLookupResult) model.PathLookupResult {
	result := LookupPath(note, attachmentFolderPath, rawTarget, lookup)
	if result.Path != "" || len(result.Ambiguous) > 0 {
		return result
	}
	if lookup == nil {
		return result
	}

	explicitPath := internalasset.VaultRelativePathCandidate(rawTarget)
	if explicitPath == "" {
		return result
	}

	return lookup(explicitPath)
}

// ResolveIndexedImageEmbedAssetPath applies the indexed vault image-embed asset lookup rules.
func ResolveIndexedImageEmbedAssetPath(note *model.Note, idx *model.VaultIndex, rawTarget string) string {
	return LookupIndexedImageEmbedAssetPath(note, idx, rawTarget).Path
}

// LookupIndexedImageEmbedAssetPath applies the indexed vault image-embed asset
// lookup rules and can surface canonical ambiguity when fallback is refused.
func LookupIndexedImageEmbedAssetPath(note *model.Note, idx *model.VaultIndex, rawTarget string) model.PathLookupResult {
	if idx == nil {
		return model.PathLookupResult{}
	}

	return LookupImageEmbedPath(note, idx.AttachmentFolderPath, rawTarget, idx.LookupAssetPath)
}

// ResolveIndexedAssetPath applies the indexed vault asset lookup rules.
func ResolveIndexedAssetPath(note *model.Note, idx *model.VaultIndex, rawTarget string) string {
	return LookupIndexedAssetPath(note, idx, rawTarget).Path
}

// LookupIndexedAssetPath applies the indexed vault asset lookup rules and can
// surface canonical ambiguity when fallback is refused.
func LookupIndexedAssetPath(note *model.Note, idx *model.VaultIndex, rawTarget string) model.PathLookupResult {
	if idx == nil {
		return model.PathLookupResult{}
	}

	return LookupPath(note, idx.AttachmentFolderPath, rawTarget, idx.LookupAssetPath)
}

// CandidatePathsWithAttachmentFolder expands a resource target through the shared
// note-relative and attachment-folder rules.
func CandidatePathsWithAttachmentFolder(note *model.Note, attachmentFolderPath string, rawTarget string) []string {
	return internalasset.CandidatePaths(note, attachmentFolderPath, rawTarget)
}

// IsLocalTarget reports whether a Markdown resource target denotes a local
// path rather than an external URL, protocol-relative URL, or fragment.
func IsLocalTarget(rawTarget string) bool {
	target := strings.TrimSpace(rawTarget)
	if target == "" || strings.HasPrefix(target, "#") || isUNCPath(target) {
		return false
	}
	if isWindowsDrivePath(target) {
		return true
	}
	parsed, err := url.Parse(target)
	return err != nil || parsed.Scheme == ""
}

func isUNCPath(value string) bool {
	normalized := strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if strings.HasPrefix(normalized, "//") {
		return true
	}

	decoded, err := url.PathUnescape(normalized)
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.ReplaceAll(strings.TrimSpace(decoded), "\\", "/"), "//")
}

func isWindowsDrivePath(value string) bool {
	if len(value) < 3 || value[1] != ':' || value[2] != '/' && value[2] != '\\' {
		return false
	}
	return value[0] >= 'A' && value[0] <= 'Z' || value[0] >= 'a' && value[0] <= 'z'
}

// IsResourceAllowedForNote reports whether a resource belongs to the note's
// version scope. Empty scope means the resource is shared site content.
func IsResourceAllowedForNote(idx *model.VaultIndex, note *model.Note, resource string) bool {
	if idx == nil {
		return false
	}
	versionID := idx.ResourceVersions[resource]
	return versionID == "" || note == nil || note.VersionID == versionID
}

// LooksLikeImage reports whether a target resolves to a supported image type.
func LooksLikeImage(rawTarget string) bool {
	return internalasset.HasImageExtension(rawTarget)
}
