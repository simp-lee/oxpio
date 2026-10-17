package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	internalapplication "github.com/simp-lee/oxpio/internal/application"
	"github.com/simp-lee/oxpio/internal/model"
)

const MaxReadBytes = 8 << 20

type Source struct {
	Path             string `json:"path"`
	Route            string `json:"route"`
	Title            string `json:"title"`
	Kind             string `json:"kind"`
	Type             string `json:"type,omitempty"`
	Publish          bool   `json:"publish"`
	EffectivePublish bool   `json:"effectivePublish"`
	Section          string `json:"section"`
	Version          string `json:"version,omitempty"`
}

type ReadResult struct {
	Source     Source `json:"source"`
	Content    string `json:"content"`
	Hash       string `json:"hash"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
	TotalLines int    `json:"totalLines"`
	EOF        bool   `json:"eof"`
}

type FindResult struct {
	Items []Source `json:"items"`
}

type TagResult struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
}

func Read(snapshot internalapplication.Snapshot, relPath string, includeUnpublished bool, offset, limit int) (ReadResult, error) {
	if offset < 0 || limit < 0 || limit > 10000 {
		return ReadResult{}, errors.New("offset must be >= 0 and limit must be between 1 and 10000")
	}
	entry, data, err := snapshot.ReadSource(relPath, includeUnpublished)
	if err != nil {
		return ReadResult{}, err
	}
	if len(data) > MaxReadBytes {
		return ReadResult{}, fmt.Errorf("source exceeds the %d-byte read limit", MaxReadBytes)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return ReadResult{}, errors.New("source must be UTF-8 without NUL bytes")
	}
	lines := logicalLines(data)
	if offset > len(lines) {
		return ReadResult{}, errors.New("offset exceeds the source line count")
	}
	end := len(lines)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	content := bytes.Join(lines[offset:end], nil)
	return ReadResult{Source: sourceDTO(entry), Content: string(content), Hash: hash(data), Offset: offset, Limit: end - offset, TotalLines: len(lines), EOF: end == len(lines)}, nil
}

func Find(snapshot internalapplication.Snapshot, includeUnpublished bool, pathPrefix, kind, section string, publish *bool) (FindResult, error) {
	var err error
	pathPrefix, err = normalizeFindPath(pathPrefix)
	if err != nil {
		return FindResult{}, err
	}
	if kind != "" && kind != "all" && kind != "section" && kind != "article" {
		return FindResult{}, fmt.Errorf("--kind must be section, article, or all")
	}
	section, err = normalizeFindPath(section)
	if err != nil {
		return FindResult{}, fmt.Errorf("--section: %w", err)
	}
	if !includeUnpublished {
		if publish != nil && !*publish {
			return FindResult{}, internalapplication.ErrHiddenSource
		}
		if pathPrefix != "" {
			if entry, entryErr := snapshot.Entry(pathPrefix, true); entryErr == nil && !entry.EffectivePublish {
				return FindResult{}, internalapplication.ErrHiddenSource
			}
		}
		if pathPrefix != "" && !snapshot.DirectoryVisibleForQuery(pathPrefix) {
			return FindResult{}, internalapplication.ErrHiddenSource
		}
		if section != "" && !snapshot.DirectoryVisibleForQuery(section) {
			return FindResult{}, internalapplication.ErrHiddenSource
		}
	}
	items := make([]Source, 0)
	for _, entry := range snapshot.Entries(includeUnpublished) {
		if pathPrefix != "" && entry.RelPath != pathPrefix && !strings.HasPrefix(entry.RelPath, pathPrefix+"/") {
			continue
		}
		if kind != "" && kind != "all" && entry.Kind != kind {
			continue
		}
		if section != "" && entry.SectionPath != section && !strings.HasPrefix(entry.SectionPath, section+"/") {
			continue
		}
		if publish != nil && entry.EffectivePublish != *publish {
			continue
		}
		items = append(items, sourceDTO(entry))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	return FindResult{Items: items}, nil
}

func normalizeFindPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" || value == "." {
		return "", nil
	}
	return internalapplication.NormalizeDirectoryPath(value)
}

func Tags(snapshot internalapplication.Snapshot, includeUnpublished bool) []TagResult {
	byName := make(map[string][]string)
	visible := make(map[string]bool)
	for _, entry := range snapshot.Entries(includeUnpublished) {
		visible[entry.RelPath] = true
	}
	if snapshot.Analysis.Plan != nil {
		for _, note := range snapshot.Analysis.Plan.Sources.AllArticles {
			if note == nil || !visible[note.RelPath] {
				continue
			}
			seen := make(map[string]struct{})
			for _, raw := range note.Tags {
				name := model.NormalizeTagName(raw)
				if name == "" {
					continue
				}
				if _, ok := seen[name]; ok {
					continue
				}
				seen[name] = struct{}{}
				byName[name] = append(byName[name], note.RelPath)
			}
		}
	}
	result := make([]TagResult, 0, len(byName))
	for name, paths := range byName {
		sort.Strings(paths)
		result = append(result, TagResult{Name: name, Paths: paths})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func sourceDTO(entry model.SourceCatalogEntry) Source {
	return Source{Path: entry.RelPath, Route: entry.Route, Title: entry.Title, Kind: entry.Kind, Type: entry.Type, Publish: entry.Publish, EffectivePublish: entry.EffectivePublish, Section: entry.SectionPath, Version: entry.VersionID}
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func logicalLines(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	if len(lines) > 1 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}
