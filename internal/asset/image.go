package asset

import (
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/simp-lee/oxpio/internal/model"
)

// HasImageExtension reports whether the destination ends with a supported image extension.
func HasImageExtension(value string) bool {
	cleaned, _ := splitDestinationSuffix(strings.TrimSpace(value))
	if decoded, err := url.PathUnescape(cleaned); err == nil {
		cleaned = decoded
	}
	ext := strings.ToLower(path.Ext(strings.TrimSpace(cleaned)))
	switch ext {
	case ".apng", ".avif", ".bmp", ".gif", ".heic", ".jpeg", ".jpg", ".jfif", ".pjp", ".pjpeg", ".png", ".svg", ".webp":
		return true
	default:
		return false
	}
}

// CandidatePaths expands a destination into the ordered vault-relative candidates
// that pass 1 and pass 2 should probe.
func CandidatePaths(note *model.Note, attachmentFolderPath string, rawDestination string) []string {
	normalized := normalizeDestination(rawDestination)
	if normalized == "" {
		return nil
	}

	candidates := make([]string, 0, 3)
	appendUniqueCandidate(&candidates, resolveVaultPath(note, normalized))
	if shouldProbeVaultRoot(normalized) {
		appendUniqueCandidate(&candidates, normalizePath(normalized))
	}
	if isBarePath(normalized) {
		appendUniqueCandidate(&candidates, attachmentFolderPathCandidate(attachmentFolderPath, normalized))
	}

	return candidates
}

// VaultRelativePathCandidate returns an exact vault-relative path candidate for
// explicit destinations that already contain path segments.
func VaultRelativePathCandidate(rawDestination string) string {
	normalized := normalizeDestination(rawDestination)
	normalized = strings.TrimPrefix(normalized, "/")
	if normalized == "" || !strings.Contains(normalized, "/") {
		return ""
	}
	if strings.HasPrefix(normalized, "./") || strings.HasPrefix(normalized, "../") {
		return ""
	}

	return normalizePath(normalized)
}

func splitDestinationSuffix(value string) (string, string) {
	index := strings.IndexAny(value, "?#")
	if index < 0 {
		return value, ""
	}

	return value[:index], value[index:]
}

func appendUniqueCandidate(candidates *[]string, candidate string) {
	if candidates == nil || candidate == "" {
		return
	}
	if slices.Contains(*candidates, candidate) {
		return
	}
	*candidates = append(*candidates, candidate)
}

func normalizeDestination(rawDestination string) string {
	trimmed := strings.TrimSpace(rawDestination)
	if trimmed == "" || shouldKeepDestination(trimmed) {
		return ""
	}

	baseDestination, _ := splitDestinationSuffix(trimmed)
	baseDestination = strings.ReplaceAll(strings.TrimSpace(baseDestination), "\\", "/")
	if baseDestination == "" {
		return ""
	}
	if decoded, err := url.PathUnescape(baseDestination); err == nil {
		baseDestination = decoded
	}

	baseDestination = strings.ReplaceAll(strings.TrimSpace(baseDestination), "\\", "/")
	if strings.HasPrefix(baseDestination, "//") {
		return ""
	}
	return baseDestination
}

func resolveVaultPath(note *model.Note, normalized string) string {
	if normalized == "" {
		return ""
	}

	var resolved string
	switch {
	case strings.HasPrefix(normalized, "/"):
		resolved = normalizePath(normalized)
	case note == nil || strings.TrimSpace(note.RelPath) == "":
		if isBarePath(normalized) {
			resolved = normalizePath(normalized)
		}
	default:
		noteDir := path.Dir(strings.ReplaceAll(note.RelPath, "\\", "/"))
		resolved = normalizePath(path.Join(noteDir, normalized))
	}

	if isOutsideVaultPath(resolved) {
		return ""
	}

	return resolved
}

func shouldProbeVaultRoot(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	return value != "" && (strings.HasPrefix(value, "/") || isBarePath(value))
}

func attachmentFolderPathCandidate(attachmentFolderPath string, target string) string {
	normalizedTarget := normalizePath(target)
	if normalizedTarget == "" {
		return ""
	}

	attachmentFolderPath = strings.TrimSpace(strings.ReplaceAll(attachmentFolderPath, "\\", "/"))
	if attachmentFolderPath == "" || attachmentFolderPath == "." {
		return normalizedTarget
	}

	return normalizePath(path.Join(attachmentFolderPath, normalizedTarget))
}

func isBarePath(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	return value != "" && !strings.Contains(value, "/")
}

func normalizePath(value string) string {
	cleaned := strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" {
		return ""
	}

	cleaned = path.Clean(cleaned)
	if cleaned == "." {
		return ""
	}

	return cleaned
}

func isOutsideVaultPath(value string) bool {
	return value == ".." || strings.HasPrefix(value, "../")
}

func shouldKeepDestination(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
		return true
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return false
	}

	return parsed.Scheme != ""
}
