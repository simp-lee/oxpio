package vault

import (
	"path"
	"sort"
	"strings"

	"github.com/simp-lee/oxpio/internal/model"
)

const tagSlugRoot = "tags"

func buildTagIndex(notes []*model.Note) map[string]*model.Tag {
	tags := make(map[string]*model.Tag)
	if len(notes) == 0 {
		return tags
	}

	noteByPath := make(map[string]*model.Note, len(notes))
	for _, note := range notes {
		if note == nil || note.RelPath == "" {
			continue
		}

		noteByPath[note.RelPath] = note

		seen := make(map[string]struct{}, len(note.Tags))
		for _, tagName := range note.Tags {
			for _, aggregate := range tagAggregates(tagName) {
				if aggregate == "" {
					continue
				}
				if _, ok := seen[aggregate]; ok {
					continue
				}
				seen[aggregate] = struct{}{}
				addTagMembership(tags, aggregate, note.RelPath)
			}
		}
	}

	sortTagIndex(tags, noteByPath)
	return tags
}

func mergeNoteTags(existing []string, inline []string) []string {
	merged := make([]string, 0, len(existing)+len(inline))
	seen := make(map[string]struct{}, len(existing)+len(inline))

	for _, values := range [][]string{existing, inline} {
		for _, value := range values {
			normalized := normalizeTag(value)
			if normalized == "" {
				continue
			}
			if _, ok := seen[normalized]; ok {
				continue
			}
			seen[normalized] = struct{}{}
			merged = append(merged, normalized)
		}
	}

	return merged
}

func normalizeTag(value string) string {
	return model.NormalizeTagName(value)
}

func tagAggregates(tagName string) []string {
	normalized := normalizeTag(tagName)
	if normalized == "" {
		return nil
	}

	segments := strings.Split(normalized, "/")
	aggregates := make([]string, 0, len(segments))
	for index := range segments {
		aggregates = append(aggregates, strings.Join(segments[:index+1], "/"))
	}

	return aggregates
}

func addTagMembership(tags map[string]*model.Tag, tagName string, relPath string) {
	if tagName == "" || relPath == "" {
		return
	}

	tagPath := tagPath(tagName)
	if tagPath == "" {
		return
	}

	tag := tags[tagName]
	if tag == nil {
		tag = &model.Tag{Name: tagName, Slug: tagPath}
		tags[tagName] = tag
	}

	for _, existing := range tag.Notes {
		if existing == relPath {
			return
		}
	}
	tag.Notes = append(tag.Notes, relPath)
}

func sortTagIndex(tags map[string]*model.Tag, noteByPath map[string]*model.Note) {
	for _, tag := range tags {
		sort.Slice(tag.Notes, func(i int, j int) bool {
			return lessTagNote(tag.Notes[i], tag.Notes[j], noteByPath)
		})
	}
}

func lessTagNote(left string, right string, noteByPath map[string]*model.Note) bool {
	leftNote := noteByPath[left]
	rightNote := noteByPath[right]
	if leftNote != nil || rightNote != nil {
		return model.LessCollectionNote(leftNote, rightNote)
	}
	return left < right
}

func tagPath(tagName string) string {
	normalized := normalizeTag(tagName)
	if normalized == "" {
		return ""
	}

	return path.Join(tagSlugRoot, normalized)
}
