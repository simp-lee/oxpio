package markdown

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/slug"
	gmhashtag "go.abhg.dev/goldmark/hashtag"
)

type renderHashtagResolver struct {
	index      *model.VaultIndex
	outputNote *model.Note
}

func newRenderHashtagResolver(idx *model.VaultIndex, outputNote *model.Note) gmhashtag.Resolver {
	if idx == nil {
		return nil
	}

	return renderHashtagResolver{index: idx, outputNote: outputNote}
}

func (r renderHashtagResolver) ResolveHashtag(node *gmhashtag.Node) ([]byte, error) {
	if node == nil || r.index == nil {
		return nil, nil
	}

	tagName := normalizeRenderedHashtag(string(node.Tag))
	if tagName == "" {
		return nil, nil
	}

	tag := r.index.Tags[tagName]
	if tag == nil || strings.TrimSpace(tag.Slug) == "" {
		return nil, nil
	}

	href := relativeDirectoryURLToNoteOutput(r.outputNote, slug.EncodePath(tag.Slug))
	if href == "" {
		return nil, nil
	}

	return []byte(href), nil
}

func normalizeRenderedHashtag(value string) string {
	return model.NormalizeTagName(value)
}

func relativeDirectoryURLToNoteOutput(note *model.Note, siteRelPath string) string {
	normalized := normalizeHashtagSitePath(siteRelPath)
	if normalized == "" {
		return ""
	}

	relativePath, err := filepath.Rel(hashtagNoteOutputDir(note), normalized)
	if err != nil {
		relativePath = normalized
	}

	clean := filepath.ToSlash(relativePath)
	if clean == "" || clean == "." {
		return "./"
	}
	if !strings.HasSuffix(clean, "/") {
		clean += "/"
	}
	return clean
}

func hashtagNoteOutputDir(note *model.Note) string {
	if note == nil {
		return "."
	}

	route := strings.Trim(strings.ReplaceAll(note.Route, "\\", "/"), "/")
	if route == "" {
		route = strings.Trim(strings.ReplaceAll(note.Slug, "\\", "/"), "/")
	}
	if route == "" {
		return "."
	}

	return path.Clean(route)
}

func normalizeHashtagSitePath(value string) string {
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
