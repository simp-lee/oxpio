package agent

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	internalapplication "github.com/simp-lee/oxpio/internal/application"
	"github.com/simp-lee/oxpio/internal/model"
	internalsearch "github.com/simp-lee/oxpio/internal/search"
)

const (
	DefaultMaxResults    = 1000
	DefaultMaxLineLength = 1 << 20
	DefaultMaxScanBytes  = 1 << 30
)

type GrepOptions struct {
	Query              string
	Regex              bool
	CaseSensitive      bool
	Scope              string
	Context            int
	Before             int
	After              int
	ContextSet         bool
	BeforeSet          bool
	AfterSet           bool
	MaxResults         int
	MaxLineLength      int
	MaxScanBytes       int64
	Path               string
	PathSet            bool
	Paths              []string
	File               string
	Files              []string
	Tag                string
	Tags               []string
	Section            string
	SectionSet         bool
	Sections           []string
	Type               string
	Types              []string
	Status             string
	Statuses           []string
	Publish            *bool
	Publishes          []bool
	IncludeUnpublished bool
}

type GrepResult struct {
	Query     string            `json:"query"`
	Scope     string            `json:"scope"`
	Options   GrepQueryMetadata `json:"options"`
	Matches   []GrepMatch       `json:"matches"`
	Contexts  []ContextWindow   `json:"contexts"`
	Truncated bool              `json:"truncated"`
}

type GrepQueryMetadata struct {
	Regex              bool     `json:"regex"`
	CaseSensitive      bool     `json:"caseSensitive"`
	Context            int      `json:"context"`
	Before             int      `json:"before"`
	After              int      `json:"after"`
	MaxResults         int      `json:"maxResults"`
	MaxLineLength      int      `json:"maxLineLength"`
	MaxScanBytes       int64    `json:"maxScanBytes"`
	Paths              []string `json:"paths"`
	Files              []string `json:"files"`
	Tags               []string `json:"tags"`
	Sections           []string `json:"sections"`
	Types              []string `json:"types"`
	Statuses           []string `json:"statuses"`
	Publishes          []bool   `json:"publishes"`
	IncludeUnpublished bool     `json:"includeUnpublished"`
}

type GrepMatch struct {
	Path        string     `json:"path"`
	SourceKind  string     `json:"sourceKind"`
	Line        *int       `json:"line"`
	Column      *int       `json:"column"`
	Range       *ByteRange `json:"range"`
	MatchedText string     `json:"matchedText"`
	LineText    string     `json:"lineText,omitempty"`
	Before      []string   `json:"before"`
	After       []string   `json:"after"`
	Field       *string    `json:"field"`
	sortValue   string
	sortStart   int
	sortEnd     int
}

type ByteRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type ContextWindow struct {
	Path       string   `json:"path"`
	SourceKind string   `json:"sourceKind"`
	StartLine  int      `json:"startLine"`
	Lines      []string `json:"lines"`
}

type GrepError struct {
	Code     string
	Category string
	Message  string
	Exit     int
	Cause    error
}

func (e GrepError) Error() string { return e.Message }
func (e GrepError) Unwrap() error { return e.Cause }
func (e GrepError) ExitCode() int { return e.Exit }
func (e GrepError) Diagnostic() Diagnostic {
	return Diagnostic{Code: e.Code, Severity: "error", Category: e.Category, Message: e.Message}
}

func grepError(code, category, message string, exit int) error {
	return GrepError{Code: code, Category: category, Message: message, Exit: exit}
}

func Grep(snapshot internalapplication.Snapshot, options GrepOptions) (GrepResult, error) {
	if options.Query == "" || strings.ContainsAny(options.Query, "\r\n") || !utf8.ValidString(options.Query) {
		return GrepResult{}, grepError("INVALID_QUERY", "parameter", "query must be a non-empty single-line UTF-8 string", 2)
	}
	if options.Scope == "" {
		options.Scope = "source"
	}
	if !validGrepScope(options.Scope) {
		return GrepResult{}, grepError("INVALID_SCOPE", "parameter", "--scope must be source, body, title, path, metadata, or all", 2)
	}
	if options.Context < 0 || options.Context > 20 || options.Before < 0 || options.Before > 20 || options.After < 0 || options.After > 20 {
		return GrepResult{}, grepError("INVALID_CONTEXT", "parameter", "context, before, and after must be between 0 and 20", 2)
	}
	if (options.Context != 0 || options.ContextSet) && (options.Before != 0 || options.After != 0 || options.BeforeSet || options.AfterSet) {
		return GrepResult{}, grepError("CONTEXT_CONFLICT", "parameter", "--context is mutually exclusive with --before and --after", 2)
	}
	if options.MaxResults == 0 {
		options.MaxResults = DefaultMaxResults
	}
	if options.MaxLineLength == 0 {
		options.MaxLineLength = DefaultMaxLineLength
	}
	if options.MaxScanBytes == 0 {
		options.MaxScanBytes = DefaultMaxScanBytes
	}
	if options.MaxResults < 1 || options.MaxResults > 10000 || options.MaxLineLength < 1 || options.MaxLineLength > 8<<20 || options.MaxScanBytes < 1 || options.MaxScanBytes > 10<<30 {
		return GrepResult{}, grepError("INVALID_LIMIT", "parameter", "grep limits are outside the supported range", 2)
	}
	matcher, err := compileGrepMatcher(options)
	if err != nil {
		return GrepResult{}, err
	}
	pathPrefixes, err := normalizeGrepDirectories(options.Path, options.Paths)
	if err != nil {
		return GrepResult{}, grepError("INVALID_PATH", "parameter", "--path is not a normalized Vault-relative directory", 2)
	}
	sectionPrefixes, err := normalizeGrepDirectories(options.Section, options.Sections)
	if err != nil {
		return GrepResult{}, grepError("INVALID_PATH", "parameter", "--section is not a normalized Vault-relative directory", 2)
	}
	if !options.IncludeUnpublished {
		if (options.PathSet || options.Path != "" || len(options.Paths) > 0) && !snapshot.DirectoryVisibleForQuery(grepRootOrPrefix(pathPrefixes)) {
			return GrepResult{}, GrepError{Code: "PERMISSION_DENIED", Category: "permission", Message: "source is not available in the selected read scope", Exit: 4, Cause: internalapplication.ErrHiddenSource}
		}
		if (options.SectionSet || options.Section != "" || len(options.Sections) > 0) && !snapshot.DirectoryVisibleForQuery(grepRootOrPrefix(sectionPrefixes)) {
			return GrepResult{}, GrepError{Code: "PERMISSION_DENIED", Category: "permission", Message: "source is not available in the selected read scope", Exit: 4, Cause: internalapplication.ErrHiddenSource}
		}
		for _, prefix := range append(append([]string{}, pathPrefixes...), sectionPrefixes...) {
			if prefix != "" && !snapshot.DirectoryVisibleForQuery(prefix) {
				return GrepResult{}, GrepError{Code: "PERMISSION_DENIED", Category: "permission", Message: "source is not available in the selected read scope", Exit: 4, Cause: internalapplication.ErrHiddenSource}
			}
		}
	}
	tags := append([]string{}, options.Tags...)
	if options.Tag != "" {
		tags = append(tags, options.Tag)
	}
	for index, tag := range tags {
		tags[index] = model.NormalizeTagName(tag)
		if tags[index] == "" {
			return GrepResult{}, grepError("INVALID_TAG", "parameter", "--tag normalizes to an empty tag", 2)
		}
	}
	types := append([]string{}, options.Types...)
	if options.Type != "" {
		types = append(types, options.Type)
	}
	for _, value := range types {
		if value != "doc" && value != "post" && value != "page" {
			return GrepResult{}, grepError("INVALID_TYPE", "parameter", "--type must be doc, post, or page", 2)
		}
	}
	statuses := append([]string{}, options.Statuses...)
	if options.Status != "" {
		statuses = append(statuses, options.Status)
	}
	for _, value := range statuses {
		if value != "stable" && value != "experimental" && value != "deprecated" {
			return GrepResult{}, grepError("INVALID_STATUS", "parameter", "--status must be stable, experimental, or deprecated", 2)
		}
	}
	files := append([]string{}, options.Files...)
	if options.File != "" {
		files = append(files, options.File)
	}
	for _, value := range files {
		if value == "" || path.Base(value) != value || strings.ContainsAny(value, "\\/\x00\r\n") {
			return GrepResult{}, grepError("INVALID_FILE", "parameter", "--file must be a single basename", 2)
		}
	}
	publishes := append([]bool{}, options.Publishes...)
	if options.Publish != nil {
		publishes = append(publishes, *options.Publish)
	}
	if !options.IncludeUnpublished {
		for _, value := range publishes {
			if !value {
				return GrepResult{}, GrepError{Code: "PERMISSION_DENIED", Category: "permission", Message: "source is not available in the selected read scope", Exit: 4, Cause: internalapplication.ErrHiddenSource}
			}
		}
	}

	metadata := grepMetadata(snapshot)
	if !options.IncludeUnpublished && len(files)+len(tags)+len(types)+len(statuses) > 0 {
		hidden := false
		for _, entry := range snapshot.Entries(true) {
			entryMetadata := metadata[entry.RelPath]
			if !grepEntryMatches(entry, pathPrefixes, files, tags, sectionPrefixes, types, statuses, publishes, entryMetadata) {
				continue
			}
			if !entry.EffectivePublish {
				hidden = true
			}
		}
		if hidden {
			return GrepResult{}, GrepError{Code: "PERMISSION_DENIED", Category: "permission", Message: "source is not available in the selected read scope", Exit: 4, Cause: internalapplication.ErrHiddenSource}
		}
	}
	entries := snapshot.Entries(options.IncludeUnpublished)
	result := GrepResult{
		Query: options.Query, Scope: options.Scope,
		Options: GrepQueryMetadata{Regex: options.Regex, CaseSensitive: options.CaseSensitive, Context: options.Context, Before: options.Before, After: options.After, MaxResults: options.MaxResults, MaxLineLength: options.MaxLineLength, MaxScanBytes: options.MaxScanBytes, Paths: displayGrepDirectories(pathPrefixes), Files: append([]string(nil), files...), Tags: append([]string(nil), tags...), Sections: displayGrepDirectories(sectionPrefixes), Types: append([]string(nil), types...), Statuses: append([]string(nil), statuses...), Publishes: append([]bool(nil), publishes...), IncludeUnpublished: options.IncludeUnpublished},
		Matches: make([]GrepMatch, 0), Contexts: make([]ContextWindow, 0),
	}
	var scanned int64
	scannedLines := make(map[string][]grepLine)
	for _, entry := range entries {
		entryMetadata := metadata[entry.RelPath]
		entryMetadata.fields = append(append([]grepMetadataField(nil), entryMetadata.fields...), grepMetadataField{"publish", fmt.Sprintf("%t", entry.EffectivePublish)})
		if !grepEntryMatches(entry, pathPrefixes, files, tags, sectionPrefixes, types, statuses, publishes, entryMetadata) {
			continue
		}
		_, source, err := snapshot.ReadSource(entry.RelPath, options.IncludeUnpublished)
		if err != nil {
			return GrepResult{}, err
		}
		if !utf8.Valid(source) || bytes.IndexByte(source, 0) >= 0 {
			return GrepResult{}, grepError("INVALID_SOURCE", "source", "source is not valid UTF-8 Markdown", 1)
		}
		if scanned+int64(len(source)) > options.MaxScanBytes {
			return GrepResult{}, grepError("RESOURCE_LIMIT", "resource", "max scan bytes exceeded", 1)
		}
		scanned += int64(len(source))
		lines := splitGrepLines(source)
		if options.Context != 0 || options.Before != 0 || options.After != 0 {
			scannedLines[entry.RelPath] = lines
		}
		for _, line := range lines {
			if len(line.bytes) > options.MaxLineLength {
				return GrepResult{}, grepError("RESOURCE_LIMIT", "resource", "line exceeds max-line-length", 1)
			}
		}
		if options.Scope == "source" || options.Scope == "body" || options.Scope == "all" {
			start := 0
			if options.Scope == "body" && entryMetadata.bodyStart > 0 {
				start = entryMetadata.bodyStart - 1
			}
			kind := "source"
			if options.Scope == "body" {
				kind = "body"
			}
			for index := start; index < len(lines); index++ {
				forEachGrepMatch(matcher, lines[index].text, func(match []int) {
					appendBoundedGrepMatch(&result, lineMatch(entry.RelPath, kind, index+1, match, lines[index].text), options.MaxResults)
				})
			}
		}
		if options.Scope == "title" || options.Scope == "metadata" || options.Scope == "all" {
			appendMetadataMatches(&result, entry, entryMetadata, matcher, options.Scope == "all" || options.Scope == "metadata", options.Scope != "metadata", options.MaxResults)
		}
		if options.Scope == "path" || options.Scope == "all" {
			forEachGrepMatch(matcher, entry.RelPath, func(match []int) {
				appendBoundedGrepMatch(&result, lineMatch(entry.RelPath, "path", 0, match, entry.RelPath), options.MaxResults)
			})
		}
	}
	sortGrepMatches(result.Matches)
	result.Matches = deduplicateGrepMatches(result.Matches)
	if len(result.Matches) > options.MaxResults {
		result.Truncated = true
		result.Matches = result.Matches[:options.MaxResults]
	}
	if options.Context != 0 || options.Before != 0 || options.After != 0 {
		result.Contexts = mergeGrepContexts(result.Matches, scannedLines, options.Context, options.Before, options.After)
	}
	for index := range result.Matches {
		if result.Matches[index].Line == nil || result.Matches[index].SourceKind == "title" || result.Matches[index].SourceKind == "path" || result.Matches[index].SourceKind == "metadata" {
			result.Matches[index].Range = nil
		}
	}
	return result, nil
}

type grepLine struct{ text, bytes string }

func splitGrepLines(source []byte) []grepLine {
	parts := bytes.Split(source, []byte{'\n'})
	lines := make([]grepLine, 0, len(parts))
	for index, part := range parts {
		if index < len(parts)-1 {
			part = bytes.TrimSuffix(part, []byte{'\r'})
		}
		lines = append(lines, grepLine{text: string(part), bytes: string(part)})
	}
	if len(lines) > 1 && lines[len(lines)-1].text == "" && bytes.HasSuffix(source, []byte{'\n'}) {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func compileGrepMatcher(options GrepOptions) (*regexp.Regexp, error) {
	matcher, err := internalsearch.Compile(options.Query, options.Regex, options.CaseSensitive)
	if err != nil {
		if errors.Is(err, internalsearch.ErrZeroWidth) {
			return nil, grepError("REGEX_ZERO_WIDTH", "parameter", err.Error(), 2)
		}
		return nil, grepError("INVALID_REGEX", "parameter", fmt.Sprintf("invalid RE2 pattern: %v", err), 2)
	}
	return matcher, nil
}

func validGrepScope(scope string) bool {
	switch scope {
	case "source", "body", "title", "path", "metadata", "all":
		return true
	}
	return false
}

func normalizeGrepDirectories(single string, multiple []string) ([]string, error) {
	values := append([]string{}, multiple...)
	if single != "" {
		values = append(values, single)
	}
	result := make([]string, 0, len(values))
	for index, value := range values {
		if strings.TrimSpace(value) == "" {
			if index < len(multiple) {
				return nil, fmt.Errorf("directory path must not be empty")
			}
			result = append(result, "")
			continue
		}
		if strings.TrimSpace(value) == "." {
			result = append(result, "")
			continue
		}
		normalized, err := internalapplication.NormalizeDirectoryPath(value)
		if err != nil {
			return nil, err
		}
		result = append(result, normalized)
	}
	return result, nil
}

type grepMetadataField struct {
	name, value string
}

type grepEntryMetadata struct {
	title, status string
	tags          []string
	bodyStart     int
	fields        []grepMetadataField
}

func grepMetadata(snapshot internalapplication.Snapshot) map[string]grepEntryMetadata {
	result := make(map[string]grepEntryMetadata)
	if snapshot.Analysis.Plan == nil {
		return result
	}
	for _, source := range snapshot.Analysis.Plan.Sources.Sources {
		if source.Article != nil {
			article := source.Article
			fields := []grepMetadataField{{"title", article.Frontmatter.Title}, {"description", article.Frontmatter.Description}, {"type", article.Frontmatter.Type}, {"status", article.Frontmatter.Status}, {"author", article.Frontmatter.Author}, {"audience", article.Frontmatter.Audience}, {"productVersion", article.Frontmatter.ProductVersion}, {"series", article.Frontmatter.Series}, {"slug", article.Slug}, {"route", article.Route}, {"section", article.SectionPath}, {"version", article.VersionID}, {"cover", article.Frontmatter.Cover}, {"banner", article.Frontmatter.Banner}, {"bannerAlt", article.Frontmatter.BannerAlt}, {"date", formatGrepTime(article.Frontmatter.Date)}, {"updated", formatGrepTime(article.Frontmatter.Updated)}, {"reviewed", formatGrepTime(article.Frontmatter.Reviewed)}}
			if article.Frontmatter.Order != nil {
				fields = append(fields, grepMetadataField{"order", fmt.Sprintf("%d", *article.Frontmatter.Order)})
			}
			for _, alias := range article.Frontmatter.Aliases {
				fields = append(fields, grepMetadataField{"alias", alias})
			}
			for _, tag := range article.Tags {
				fields = append(fields, grepMetadataField{"tag", model.NormalizeTagName(tag)})
			}
			result[source.RelPath] = grepEntryMetadata{title: article.Frontmatter.Title, status: article.Frontmatter.Status, tags: article.Tags, bodyStart: article.BodyStartLine, fields: nonEmptyGrepFields(fields)}
		} else if source.Section != nil {
			section := source.Section
			fields := []grepMetadataField{{"title", section.Frontmatter.Title}, {"description", section.Frontmatter.Description}, {"section", section.SectionPath}, {"banner", section.Frontmatter.Banner}, {"bannerAlt", section.Frontmatter.BannerAlt}}
			if section.Frontmatter.Order != nil {
				fields = append(fields, grepMetadataField{"order", fmt.Sprintf("%d", *section.Frontmatter.Order)})
			}
			result[source.RelPath] = grepEntryMetadata{title: section.Frontmatter.Title, bodyStart: section.BodyStartLine, fields: nonEmptyGrepFields(fields)}
		}
	}
	return result
}

func formatGrepTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

func nonEmptyGrepFields(fields []grepMetadataField) []grepMetadataField {
	result := make([]grepMetadataField, 0, len(fields))
	for _, field := range fields {
		if field.value != "" {
			result = append(result, field)
		}
	}
	return result
}

func grepEntryMatches(entry model.SourceCatalogEntry, paths, files, tags, sections, types, statuses []string, publishes []bool, metadata grepEntryMetadata) bool {
	if !matchesAnyPrefix(entry.RelPath, paths) || !matchesAnyFile(entry.RelPath, files) || !matchesAnyPrefix(entry.SectionPath, sections) || !matchesAny(entry.Type, types) || !matchesAny(metadata.status, statuses) {
		return false
	}
	if len(publishes) > 0 {
		found := false
		for _, value := range publishes {
			if entry.EffectivePublish == value {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(tags) > 0 {
		found := false
		for _, wanted := range tags {
			for _, actual := range metadata.tags {
				if model.NormalizeTagName(actual) == wanted {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func displayGrepDirectories(prefixes []string) []string {
	if prefixes == nil {
		return nil
	}
	result := append([]string(nil), prefixes...)
	for index, prefix := range result {
		if prefix == "" {
			result[index] = "."
		}
	}
	return result
}

func grepRootOrPrefix(prefixes []string) string {
	if len(prefixes) == 0 {
		return "."
	}
	return prefixes[0]
}

func matchesAny(value string, values []string) bool {
	if len(values) == 0 {
		return true
	}
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func matchesAnyPrefix(value string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		if prefix == "" || value == prefix || strings.HasPrefix(value, prefix+"/") {
			return true
		}
	}
	return false
}

func matchesAnyFile(value string, files []string) bool {
	if len(files) == 0 {
		return true
	}
	base := path.Base(value)
	for _, candidate := range files {
		if base == candidate {
			return true
		}
	}
	return false
}

func lineMatch(sourcePath, kind string, line int, indexes []int, text string) GrepMatch {
	matched := text[indexes[0]:indexes[1]]
	if line == 0 {
		return GrepMatch{Path: sourcePath, SourceKind: kind, MatchedText: matched, Before: []string{}, After: []string{}}
	}
	lineValue, columnValue := line, indexes[0]+1
	return GrepMatch{Path: sourcePath, SourceKind: kind, Line: &lineValue, Column: &columnValue, Range: &ByteRange{Start: indexes[0] + 1, End: indexes[1] + 1}, MatchedText: matched, LineText: text, Before: []string{}, After: []string{}}
}

func forEachGrepMatch(matcher *regexp.Regexp, text string, visit func([]int)) {
	for offset := 0; offset <= len(text); {
		indexes := matcher.FindStringIndex(text[offset:])
		if indexes == nil {
			return
		}
		absolute := []int{offset + indexes[0], offset + indexes[1]}
		visit(absolute)
		if indexes[1] <= indexes[0] {
			offset++
		} else {
			offset += indexes[1]
		}
	}
}

func appendBoundedGrepMatch(result *GrepResult, match GrepMatch, maxResults int) {
	result.Matches = append(result.Matches, match)
	if len(result.Matches) <= maxResults+1 {
		return
	}
	sortGrepMatches(result.Matches)
	result.Matches = deduplicateGrepMatches(result.Matches)
	if len(result.Matches) > maxResults+1 {
		result.Matches = result.Matches[:maxResults+1]
	}
}

func appendMetadataMatches(result *GrepResult, entry model.SourceCatalogEntry, metadata grepEntryMetadata, matcher *regexp.Regexp, includeAll, titleKind bool, maxResults int) {
	fields := metadata.fields
	if !includeAll {
		fields = make([]grepMetadataField, 0, 1)
		for _, field := range metadata.fields {
			if field.name == "title" {
				fields = append(fields, field)
			}
		}
	}
	for _, field := range fields {
		forEachGrepMatch(matcher, field.value, func(match []int) {
			name := field.name
			kind := "metadata"
			if field.name == "title" && titleKind {
				kind = "title"
			}
			appendBoundedGrepMatch(result, GrepMatch{Path: entry.RelPath, SourceKind: kind, MatchedText: field.value[match[0]:match[1]], Before: []string{}, After: []string{}, Field: &name, sortValue: field.value, sortStart: match[0], sortEnd: match[1]}, maxResults)
		})
	}
}

func deduplicateGrepMatches(matches []GrepMatch) []GrepMatch {
	seen := make(map[string]struct{}, len(matches))
	result := make([]GrepMatch, 0, len(matches))
	for _, match := range matches {
		line, column, start, end := "", "", "", ""
		if match.Line != nil {
			line = fmt.Sprintf("%d", *match.Line)
		}
		if match.Column != nil {
			column = fmt.Sprintf("%d", *match.Column)
		}
		if match.Range != nil {
			start, end = fmt.Sprintf("%d", match.Range.Start), fmt.Sprintf("%d", match.Range.End)
		}
		field := ""
		if match.Field != nil {
			field = *match.Field
		}
		key := strings.Join([]string{match.Path, match.SourceKind, line, column, start, end, field, match.MatchedText}, "\x00")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, match)
	}
	return result
}

func sortGrepMatches(matches []GrepMatch) {
	order := map[string]int{"source": 0, "body": 1, "title": 2, "path": 3, "metadata": 4}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}
		if order[matches[i].SourceKind] != order[matches[j].SourceKind] {
			return order[matches[i].SourceKind] < order[matches[j].SourceKind]
		}
		if matches[i].Line != nil && matches[j].Line != nil {
			if *matches[i].Line != *matches[j].Line {
				return *matches[i].Line < *matches[j].Line
			}
			if matches[i].Column != nil && matches[j].Column != nil && *matches[i].Column != *matches[j].Column {
				return *matches[i].Column < *matches[j].Column
			}
		}
		if matches[i].Field != nil && matches[j].Field != nil {
			if *matches[i].Field != *matches[j].Field {
				return *matches[i].Field < *matches[j].Field
			}
			if matches[i].sortValue != matches[j].sortValue {
				return matches[i].sortValue < matches[j].sortValue
			}
			if matches[i].sortStart != matches[j].sortStart {
				return matches[i].sortStart < matches[j].sortStart
			}
			if matches[i].sortEnd != matches[j].sortEnd {
				return matches[i].sortEnd < matches[j].sortEnd
			}
		}
		return matches[i].MatchedText < matches[j].MatchedText
	})
}

func mergeGrepContexts(matches []GrepMatch, lines map[string][]grepLine, context, before, after int) []ContextWindow {
	type interval struct {
		start, end int
		kind       string
	}
	byPath := make(map[string][]interval)
	for _, match := range matches {
		if match.Line == nil || (match.SourceKind != "source" && match.SourceKind != "body") {
			continue
		}
		beforeLines, afterLines := before, after
		if context != 0 {
			beforeLines, afterLines = context, context
		}
		line := *match.Line
		start, end := line-beforeLines, line+afterLines
		if start < 1 {
			start = 1
		}
		if end > len(lines[match.Path]) {
			end = len(lines[match.Path])
		}
		byPath[match.Path] = append(byPath[match.Path], interval{start: start, end: end, kind: match.SourceKind})
	}
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]ContextWindow, 0)
	for _, sourcePath := range paths {
		intervals := byPath[sourcePath]
		sort.Slice(intervals, func(i, j int) bool { return intervals[i].start < intervals[j].start })
		merged := make([]interval, 0, len(intervals))
		for _, current := range intervals {
			if len(merged) == 0 || current.start > merged[len(merged)-1].end+1 {
				merged = append(merged, current)
				continue
			}
			if current.end > merged[len(merged)-1].end {
				merged[len(merged)-1].end = current.end
			}
		}
		for _, window := range merged {
			values := make([]string, 0, window.end-window.start+1)
			for _, line := range lines[sourcePath][window.start-1 : window.end] {
				values = append(values, line.text)
			}
			result = append(result, ContextWindow{Path: sourcePath, SourceKind: window.kind, StartLine: window.start, Lines: values})
		}
	}
	return result
}
