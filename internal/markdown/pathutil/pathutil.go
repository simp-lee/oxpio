// Package pathutil contains shared Markdown output-path helpers.
package pathutil

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/simp-lee/oxpio/internal/model"
)

// RelativeToNoteOutput returns siteRelPath relative to the output directory of note.
func RelativeToNoteOutput(note *model.Note, siteRelPath string) string {
	normalized := NormalizeSitePath(siteRelPath)
	if normalized == "" {
		return ""
	}

	relativePath, err := filepath.Rel(noteOutputDir(note), normalized)
	if err != nil {
		return normalized
	}

	return filepath.ToSlash(relativePath)
}

// NormalizeSitePath normalizes a site-relative path for output-path operations.
func NormalizeSitePath(value string) string {
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

func noteOutputDir(note *model.Note) string {
	if note == nil {
		return "."
	}

	output := note.Route
	if output == "" {
		output = note.Slug
	}
	output = strings.Trim(strings.ReplaceAll(output, "\\", "/"), "/")
	if output == "" {
		return "."
	}

	return path.Clean(output)
}
