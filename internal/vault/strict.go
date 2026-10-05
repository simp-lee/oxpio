package vault

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/slug"
	"gopkg.in/yaml.v3"
)

const maxInt32 = int64(1<<31 - 1)

var (
	utf8BOM                               = []byte{0xef, 0xbb, 0xbf}
	errMissingClosingFrontmatterDelimiter = errors.New("missing closing frontmatter delimiter")
)

var (
	strictDatePattern      = regexp.MustCompile(`^(?:[0-9]{4}-[0-9]{2}-[0-9]{2}|[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2}))$`)
	strictOrderPattern     = regexp.MustCompile(`^[0-9]+$`)
	frontmatterLinePattern = regexp.MustCompile(`\bline ([0-9]+)\b`)
)

func frontmatterSourceError(err error) error {
	if err == nil {
		return nil
	}
	message := frontmatterLinePattern.ReplaceAllStringFunc(err.Error(), func(value string) string {
		match := frontmatterLinePattern.FindStringSubmatch(value)
		if len(match) != 2 {
			return value
		}
		line, parseErr := strconv.Atoi(match[1])
		if parseErr != nil {
			return value
		}
		return fmt.Sprintf("line %d", line+1)
	})
	return errors.New(message)
}

// StrictFrontmatterResult is the schema-safe source handoff used by the
// section planner. It keeps _index.md out of the article list and rejects
// unknown fields.
type StrictFrontmatterResult struct {
	Sources     []model.PlannedSource
	Sections    []*model.SectionSource
	Articles    []*model.Note
	AllArticles []*model.Note
	Unpublished []*model.Note
}

// ParseStrictFrontmatter parses the complete vault source set according to the
// revised section/article contract.
func ParseStrictFrontmatter(scanResult ScanResult) (StrictFrontmatterResult, error) {
	result := StrictFrontmatterResult{
		Sources:     make([]model.PlannedSource, 0, len(scanResult.MarkdownFiles)),
		Sections:    make([]*model.SectionSource, 0),
		Articles:    make([]*model.Note, 0),
		AllArticles: make([]*model.Note, 0),
		Unpublished: make([]*model.Note, 0),
	}

	for _, relPath := range append([]string(nil), scanResult.MarkdownFiles...) {
		content, info, err := readStrictSource(scanResult, relPath)
		if err != nil {
			return StrictFrontmatterResult{}, fmt.Errorf("read %q: %w", relPath, err)
		}
		frontmatterData, body, bodyStartLine, present, err := splitFrontmatter(content)
		if err != nil {
			return StrictFrontmatterResult{}, fmt.Errorf("parse frontmatter %q: %w", relPath, err)
		}
		mapping, err := strictMapping(frontmatterData, present)
		if err != nil {
			return StrictFrontmatterResult{}, fmt.Errorf("parse frontmatter %q: %w", relPath, frontmatterSourceError(err))
		}
		if strings.EqualFold(path.Base(relPath), "_index.md") && path.Base(relPath) != "_index.md" {
			return StrictFrontmatterResult{}, fmt.Errorf("section source %q must use the exact filename _index.md", relPath)
		}
		if path.Base(relPath) == "_index.md" {
			section, err := decodeSectionSource(relPath, body, bodyStartLine, info, mapping)
			if err != nil {
				return StrictFrontmatterResult{}, fmt.Errorf("parse section %q: %w", relPath, frontmatterSourceError(err))
			}
			result.Sections = append(result.Sections, section)
			result.Sources = append(result.Sources, model.PlannedSource{RelPath: relPath, Section: section, Publish: section.Frontmatter.Publish != nil && *section.Frontmatter.Publish})
			continue
		}

		note, err := decodeStrictArticle(relPath, body, bodyStartLine, info, mapping)
		if err != nil {
			return StrictFrontmatterResult{}, fmt.Errorf("parse article %q: %w", relPath, frontmatterSourceError(err))
		}
		result.AllArticles = append(result.AllArticles, note)
		published := note.Frontmatter.Publish != nil && *note.Frontmatter.Publish
		result.Sources = append(result.Sources, model.PlannedSource{RelPath: relPath, Article: note, Publish: published})
		if published {
			result.Articles = append(result.Articles, note)
		} else {
			result.Unpublished = append(result.Unpublished, note)
		}
	}
	return result, nil
}

type overlayFileInfo struct{}

func (overlayFileInfo) ModTime() time.Time { return time.Time{} }

func readStrictSource(scanResult ScanResult, relPath string) ([]byte, interface{ ModTime() time.Time }, error) {
	if data, ok := scanResult.OverlayMarkdown[relPath]; ok && !scanResult.OverlayDeleted[relPath] {
		return append([]byte(nil), data...), overlayFileInfo{}, nil
	}
	_, data, info, err := internalfsutil.ReadContainedRegularFile(scanResult.VaultPath, relPath)
	if err != nil {
		return nil, nil, err
	}
	return data, info, nil
}

func splitFrontmatter(content []byte) ([]byte, []byte, int, bool, error) {
	start := 0
	if bytes.HasPrefix(content, utf8BOM) {
		start = len(utf8BOM)
	}
	firstLine, next, ok := readLine(content, start)
	if !ok || !isFrontmatterDelimiter(firstLine) {
		return nil, content[start:], 1, false, nil
	}
	frontmatterStart, lineStart, lineNumber := next, next, 2
	for {
		line, nextLine, lineOK := readLine(content, lineStart)
		if !lineOK {
			if isLikelyFrontmatterBlock(content[frontmatterStart:], false) {
				return nil, nil, 0, false, errMissingClosingFrontmatterDelimiter
			}
			return nil, content[start:], 1, false, nil
		}
		if isFrontmatterClosingDelimiter(line) {
			block := content[frontmatterStart:lineStart]
			if isLikelyFrontmatterBlock(block, true) {
				return block, content[nextLine:], lineNumber + 1, true, nil
			}
			return nil, content[start:], 1, false, nil
		}
		if nextLine >= len(content) {
			if isLikelyFrontmatterBlock(content[frontmatterStart:], false) {
				return nil, nil, 0, false, errMissingClosingFrontmatterDelimiter
			}
			return nil, content[start:], 1, false, nil
		}
		lineStart, lineNumber = nextLine, lineNumber+1
	}
}

func readLine(content []byte, start int) ([]byte, int, bool) {
	if start >= len(content) {
		return nil, start, false
	}
	lineEnd := start
	for lineEnd < len(content) && content[lineEnd] != '\n' {
		lineEnd++
	}
	next := lineEnd
	if next < len(content) {
		next++
	}
	return bytes.TrimSuffix(content[start:lineEnd], []byte{'\r'}), next, true
}

func isFrontmatterDelimiter(line []byte) bool { return bytes.Equal(line, []byte("---")) }
func isFrontmatterClosingDelimiter(line []byte) bool {
	return isFrontmatterDelimiter(line) || bytes.Equal(line, []byte("..."))
}

func isLikelyFrontmatterBlock(block []byte, closed bool) bool {
	if len(bytes.TrimSpace(block)) == 0 {
		return closed
	}
	var document yaml.Node
	if err := yaml.Unmarshal(block, &document); err == nil {
		return len(document.Content) > 0 && document.Content[0].Kind == yaml.MappingNode
	}
	line, ok := firstNonBlankLine(block)
	return ok && isLikelyYAMLMappingLine(bytes.TrimSpace(line))
}

func firstNonBlankLine(content []byte) ([]byte, bool) {
	lineStart := 0
	for {
		line, next, ok := readLine(content, lineStart)
		if !ok {
			return nil, false
		}
		if len(bytes.TrimSpace(line)) != 0 {
			return line, true
		}
		if next >= len(content) {
			return nil, false
		}
		lineStart = next
	}
}

func isLikelyYAMLMappingLine(line []byte) bool {
	colon := bytes.IndexByte(line, ':')
	if colon <= 0 {
		return false
	}
	if colon == len(line)-1 {
		return true
	}
	switch line[colon+1] {
	case ' ', '\t', '[', '{', '|', '>', '&', '*', '!', '\'', '"':
		return true
	}
	return false
}

func noteLookupName(relPath string) string {
	base := path.Base(strings.ReplaceAll(relPath, `\`, "/"))
	if ext := path.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return slug.Canonicalize(strings.TrimSpace(base))
}

func aliasLookupName(alias string) string { return slug.Canonicalize(strings.TrimSpace(alias)) }

func strictMapping(data []byte, present bool) (map[string]*yaml.Node, error) {
	if !present {
		return nil, fmt.Errorf("frontmatter is required")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("multiple YAML documents are not supported")
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("frontmatter must be a YAML mapping")
	}
	root := document.Content[0]
	fields := make(map[string]*yaml.Node, len(root.Content)/2)
	for index := 0; index+1 < len(root.Content); index += 2 {
		key, value := root.Content[index], root.Content[index+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return nil, fmt.Errorf("frontmatter key at line %d must be a string", key.Line)
		}
		if _, exists := fields[key.Value]; exists {
			return nil, fmt.Errorf("duplicate frontmatter key %q at line %d", key.Value, key.Line)
		}
		if value.Tag == "!!null" {
			return nil, fmt.Errorf("frontmatter field %q at line %d must not be null", key.Value, value.Line)
		}
		fields[key.Value] = value
	}
	return fields, nil
}

var articleFields = map[string]struct{}{
	"title": {}, "description": {}, "publish": {}, "date": {}, "updated": {},
	"tags": {}, "aliases": {}, "slug": {}, "type": {}, "order": {}, "author": {},
	"reviewed": {}, "status": {}, "audience": {}, "productVersion": {}, "series": {},
	"cover": {}, "banner": {}, "bannerAlt": {},
}

var sectionFields = map[string]struct{}{
	"title": {}, "description": {}, "publish": {}, "order": {}, "banner": {}, "bannerAlt": {},
}

func rejectUnknownFields(fields map[string]*yaml.Node, allowed map[string]struct{}) error {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	return fmt.Errorf("unknown frontmatter field %q at line %d", keys[0], fields[keys[0]].Line)
}

func decodeSectionSource(relPath string, body []byte, bodyStartLine int, info interface{ ModTime() time.Time }, fields map[string]*yaml.Node) (*model.SectionSource, error) {
	if err := rejectUnknownFields(fields, sectionFields); err != nil {
		return nil, err
	}
	title, err := requiredStrictString(fields, "title")
	if err != nil {
		return nil, err
	}
	publish, err := requiredStrictBool(fields, "publish")
	if err != nil {
		return nil, err
	}
	section := &model.SectionSource{
		RelPath: relPath, SectionPath: path.Dir(relPath), RawContent: append([]byte(nil), body...),
		BodyStartLine: bodyStartLine,
		Frontmatter:   model.SectionFrontmatter{Title: title, Publish: publish},
		FieldLines:    frontmatterFieldLines(fields),
	}
	if section.SectionPath == "." {
		section.SectionPath = "."
	}
	if value, ok := fields["description"]; ok {
		section.Frontmatter.Description, err = strictString(value, "description", false)
		if err != nil {
			return nil, err
		}
	}
	if value, ok := fields["order"]; ok {
		section.Frontmatter.Order, err = strictOrder(value, "order")
		if err != nil {
			return nil, err
		}
	}
	if value, ok := fields["banner"]; ok {
		section.Frontmatter.Banner, err = strictString(value, "banner", true)
		if err != nil {
			return nil, err
		}
	}
	if value, ok := fields["bannerAlt"]; ok {
		section.Frontmatter.BannerAlt, err = strictString(value, "bannerAlt", true)
		if err != nil {
			return nil, err
		}
	}
	_, hasBanner := fields["banner"]
	_, hasBannerAlt := fields["bannerAlt"]
	if hasBanner != hasBannerAlt {
		return nil, fmt.Errorf("banner and bannerAlt must be configured together")
	}
	return section, nil
}

func decodeStrictArticle(relPath string, body []byte, bodyStartLine int, info interface{ ModTime() time.Time }, fields map[string]*yaml.Node) (*model.Note, error) {
	if err := rejectUnknownFields(fields, articleFields); err != nil {
		return nil, err
	}
	title, err := requiredStrictString(fields, "title")
	if err != nil {
		return nil, err
	}
	publish, err := requiredStrictBool(fields, "publish")
	if err != nil {
		return nil, err
	}
	typeName, err := requiredStrictString(fields, "type")
	if err != nil {
		return nil, err
	}
	if typeName != "doc" && typeName != "post" && typeName != "page" {
		return nil, fmt.Errorf("type must be one of doc, post, or page")
	}
	note := &model.Note{RelPath: relPath, RawContent: append([]byte(nil), body...), BodyStartLine: bodyStartLine, FieldLines: frontmatterFieldLines(fields)}
	note.Frontmatter.Title, note.Frontmatter.Publish, note.Frontmatter.Type = title, publish, typeName
	if value, ok := fields["description"]; ok {
		note.Frontmatter.Description, err = strictString(value, "description", false)
		if err != nil {
			return nil, err
		}
	}
	if value, ok := fields["date"]; ok {
		note.Frontmatter.Date, err = strictTime(value, "date")
		if err != nil {
			return nil, err
		}
	}
	if typeName == "post" && note.Frontmatter.Date.IsZero() {
		return nil, fmt.Errorf("date is required for type=post")
	}
	if value, ok := fields["updated"]; ok {
		note.Frontmatter.Updated, err = strictTime(value, "updated")
		if err != nil {
			return nil, err
		}
	}
	if !note.Frontmatter.Updated.IsZero() && !note.Frontmatter.Date.IsZero() && note.Frontmatter.Updated.Before(note.Frontmatter.Date) {
		return nil, fmt.Errorf("updated must not be earlier than date")
	}
	if value, ok := fields["reviewed"]; ok {
		note.Frontmatter.Reviewed, err = strictTime(value, "reviewed")
		if err != nil {
			return nil, err
		}
	}
	if value, ok := fields["tags"]; ok {
		note.Frontmatter.Tags, err = strictStringList(value, "tags")
		if err != nil {
			return nil, err
		}
		note.Tags = append([]string(nil), note.Frontmatter.Tags...)
	}
	if value, ok := fields["aliases"]; ok {
		note.Frontmatter.Aliases, err = strictStringList(value, "aliases")
		if err != nil {
			return nil, err
		}
		note.Aliases = append([]string(nil), note.Frontmatter.Aliases...)
	}
	if value, ok := fields["slug"]; ok {
		note.Frontmatter.Slug, err = strictArticleSlug(value)
		if err != nil {
			return nil, err
		}
	}
	if value, ok := fields["order"]; ok {
		note.Frontmatter.Order, err = strictOrder(value, "order")
		if err != nil {
			return nil, err
		}
	}
	for _, field := range []string{"author", "status", "audience", "productVersion", "series", "cover", "banner", "bannerAlt"} {
		if value, ok := fields[field]; ok {
			text, textErr := strictString(value, field, true)
			if textErr != nil {
				return nil, textErr
			}
			switch field {
			case "author":
				note.Frontmatter.Author = text
			case "status":
				note.Frontmatter.Status = text
				if text != "stable" && text != "experimental" && text != "deprecated" {
					return nil, fmt.Errorf("status at line %d must be stable, experimental, or deprecated", value.Line)
				}
			case "audience":
				note.Frontmatter.Audience = text
			case "productVersion":
				note.Frontmatter.ProductVersion = text
			case "series":
				note.Frontmatter.Series = text
			case "cover":
				note.Frontmatter.Cover = text
			case "banner":
				note.Frontmatter.Banner = text
			case "bannerAlt":
				note.Frontmatter.BannerAlt = text
			}
		}
	}
	_, hasBanner := fields["banner"]
	_, hasBannerAlt := fields["bannerAlt"]
	if hasBanner != hasBannerAlt {
		return nil, fmt.Errorf("banner and bannerAlt must be configured together")
	}
	if !note.Frontmatter.Updated.IsZero() {
		note.LastModified = note.Frontmatter.Updated
	} else if !note.Frontmatter.Date.IsZero() {
		note.LastModified = note.Frontmatter.Date
	}
	return note, nil
}

func frontmatterFieldLines(fields map[string]*yaml.Node) map[string]int {
	lines := make(map[string]int, len(fields))
	for name, node := range fields {
		lines[name] = node.Line + 1 // opening frontmatter delimiter
	}
	return lines
}

func requiredStrictString(fields map[string]*yaml.Node, name string) (string, error) {
	value, ok := fields[name]
	if !ok {
		return "", fmt.Errorf("%s is required", name)
	}
	return strictString(value, name, true)
}

func requiredStrictBool(fields map[string]*yaml.Node, name string) (*bool, error) {
	value, ok := fields[name]
	if !ok {
		return nil, fmt.Errorf("%s is required", name)
	}
	if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" {
		return nil, fmt.Errorf("%s at line %d must be a boolean", name, value.Line)
	}
	parsed, err := strconv.ParseBool(strings.ToLower(value.Value))
	if err != nil {
		return nil, fmt.Errorf("%s at line %d must be a boolean", name, value.Line)
	}
	return &parsed, nil
}

func strictString(node *yaml.Node, name string, nonEmpty bool) (string, error) {
	if node == nil {
		return "", fmt.Errorf("%s must be a string", name)
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("%s at line %d must be a string", name, node.Line)
	}
	value := strings.TrimSpace(node.Value)
	if nonEmpty && value == "" {
		return "", fmt.Errorf("%s at line %d must be non-empty", name, node.Line)
	}
	return value, nil
}

func strictStringList(node *yaml.Node, name string) ([]string, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s at line %d must be a sequence of strings", name, node.Line)
	}
	values := make([]string, 0, len(node.Content))
	seen := make(map[string]struct{}, len(node.Content))
	for _, item := range node.Content {
		value, err := strictString(item, name, true)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("%s contains duplicate value %q", name, value)
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values, nil
}

func strictTime(node *yaml.Node, name string) (time.Time, error) {
	if node.Kind != yaml.ScalarNode || (node.Tag != "!!str" && node.Tag != "!!timestamp") || !strictDatePattern.MatchString(node.Value) {
		return time.Time{}, fmt.Errorf("%s at line %d must be RFC 3339 or YYYY-MM-DD", name, node.Line)
	}
	if len(node.Value) == len("2006-01-02") {
		value, err := time.Parse("2006-01-02", node.Value)
		if err != nil {
			return time.Time{}, fmt.Errorf("%s at line %d must be RFC 3339 or YYYY-MM-DD", name, node.Line)
		}
		return value.UTC(), nil
	}
	if !strictRFC3339Offset(node.Value) {
		return time.Time{}, fmt.Errorf("%s at line %d must be RFC 3339 or YYYY-MM-DD", name, node.Line)
	}
	value, err := time.Parse(time.RFC3339Nano, node.Value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s at line %d must be RFC 3339 or YYYY-MM-DD", name, node.Line)
	}
	return value.UTC(), nil
}

func strictRFC3339Offset(value string) bool {
	if strings.HasSuffix(value, "Z") {
		return true
	}
	if len(value) < len("+00:00") {
		return false
	}
	offset := value[len(value)-len("+00:00"):]
	if offset[0] != '+' && offset[0] != '-' || offset[3] != ':' {
		return false
	}
	hour := int(offset[1]-'0')*10 + int(offset[2]-'0')
	minute := int(offset[4]-'0')*10 + int(offset[5]-'0')
	return hour <= 23 && minute <= 59
}

func strictOrder(node *yaml.Node, name string) (*int, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!int" || !strictOrderPattern.MatchString(node.Value) {
		return nil, fmt.Errorf("%s at line %d must be a decimal integer from 0 to 2147483647", name, node.Line)
	}
	value, err := strconv.ParseInt(node.Value, 10, 32)
	if err != nil || value < 0 || value > maxInt32 {
		return nil, fmt.Errorf("%s at line %d must be a decimal integer from 0 to 2147483647", name, node.Line)
	}
	converted := int(value)
	return &converted, nil
}

func strictArticleSlug(node *yaml.Node) (string, error) {
	value, err := strictString(node, "slug", true)
	if err != nil {
		return "", err
	}
	normalized, err := slug.GenerateArticleSegment(&value, "")
	if err != nil {
		return "", fmt.Errorf("slug at line %d must be one non-empty Unicode path segment containing only letters, numbers, '-', '_' or '~'", node.Line)
	}
	return normalized, nil
}
