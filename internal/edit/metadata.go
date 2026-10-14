package edit

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/vault"
	"gopkg.in/yaml.v3"
)

// editorFrontmatter is the form-facing representation of OXPIO's strict
// metadata. It deliberately contains no YAML details; the source-mode editor
// remains available for fields and syntax that are not represented here.
type editorFrontmatter struct {
	Title          string   `json:"title"`
	Publish        bool     `json:"publish"`
	Type           string   `json:"type,omitempty"`
	Description    string   `json:"description,omitempty"`
	Date           string   `json:"date,omitempty"`
	Updated        string   `json:"updated,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Aliases        []string `json:"aliases,omitempty"`
	Slug           string   `json:"slug,omitempty"`
	Order          *int     `json:"order,omitempty"`
	Author         string   `json:"author,omitempty"`
	Reviewed       string   `json:"reviewed,omitempty"`
	Status         string   `json:"status,omitempty"`
	Audience       string   `json:"audience,omitempty"`
	ProductVersion string   `json:"productVersion,omitempty"`
	Series         string   `json:"series,omitempty"`
	Cover          string   `json:"cover,omitempty"`
	Banner         string   `json:"banner,omitempty"`
	BannerAlt      string   `json:"bannerAlt,omitempty"`
}

type editorFrontmatterRequest struct {
	Path    string             `json:"path"`
	Source  *string            `json:"source"`
	Body    *string            `json:"body"`
	Fields  *editorFrontmatter `json:"fields"`
	Changed []string           `json:"changed,omitempty"`
}

type editorDocument struct {
	Frontmatter editorFrontmatter
	Body        []byte
	HasBlock    bool
	BodyStart   int
	CloseStart  int
	CloseEnd    int
}

func parseEditorDocument(content []byte, sourcePath ...string) (editorDocument, error) {
	document := editorDocument{Body: append([]byte(nil), content...)}
	path := "article.md"
	if len(sourcePath) > 0 && sourcePath[0] != "" {
		path = sourcePath[0]
	}
	parsed, err := vault.ParseStrictSource(path, content)
	if err != nil {
		return document, err
	}
	document.HasBlock = parsed.HasFrontmatter
	document.BodyStart = parsed.BodyStart
	document.CloseStart = parsed.CloseStart
	document.CloseEnd = parsed.CloseEnd
	document.Body = append([]byte(nil), parsed.Body...)
	document.Frontmatter = editorFrontmatterFromSource(parsed.Source)
	return document, nil
}

func editorFrontmatterFromSource(source model.PlannedSource) editorFrontmatter {
	if source.Article != nil {
		value := source.Article.Frontmatter
		return editorFrontmatter{
			Title: value.Title, Publish: value.Publish != nil && *value.Publish, Type: value.Type,
			Description: value.Description, Date: editorTime(value.Date), Updated: editorTime(value.Updated),
			Tags: append([]string(nil), value.Tags...), Aliases: append([]string(nil), value.Aliases...),
			Slug: value.Slug, Order: cloneEditorInt(value.Order), Author: value.Author,
			Reviewed: editorTime(value.Reviewed), Status: value.Status, Audience: value.Audience,
			ProductVersion: value.ProductVersion, Series: value.Series, Cover: value.Cover,
			Banner: value.Banner, BannerAlt: value.BannerAlt,
		}
	}
	if source.Section != nil {
		value := source.Section.Frontmatter
		return editorFrontmatter{Title: value.Title, Publish: value.Publish != nil && *value.Publish, Description: value.Description, Order: cloneEditorInt(value.Order), Banner: value.Banner, BannerAlt: value.BannerAlt}
	}
	return editorFrontmatter{}
}

func cloneEditorInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func editorTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	value = value.UTC()
	if value.Hour() == 0 && value.Minute() == 0 && value.Second() == 0 && value.Nanosecond() == 0 {
		return value.Format("2006-01-02")
	}
	return value.Format(time.RFC3339Nano)
}

func editorMetadataPath(kind string) string {
	if kind == "section" {
		return "_index.md"
	}
	return "article.md"
}

type editorLine struct {
	start int
	end   int
	text  string
}

func editorSourceLines(content []byte) []editorLine {
	lines := make([]editorLine, 0)
	for start := 0; start < len(content); {
		lineEnd := start
		for lineEnd < len(content) && content[lineEnd] != '\r' && content[lineEnd] != '\n' {
			lineEnd++
		}
		end := lineEnd
		if end < len(content) {
			if content[end] == '\r' && end+1 < len(content) && content[end+1] == '\n' {
				end += 2
			} else {
				end++
			}
		}
		lines = append(lines, editorLine{start: start, end: end, text: string(content[start:lineEnd])})
		start = end
	}
	return lines
}

func applyEditorFrontmatter(content, body []byte, fields editorFrontmatter, kind string, changedFields ...[]string) ([]byte, error) {
	absent, err := vault.IsFrontmatterAbsent(content)
	if err != nil {
		return nil, err
	}
	if absent {
		block, err := editorFrontmatterBlock(fields, kind)
		if err != nil {
			return nil, err
		}
		return append(block, normalizeEditorBody(body)...), nil
	}
	document, err := parseEditorDocument(content, editorMetadataPath(kind))
	if err != nil {
		return nil, err
	}
	updated := append([]byte(nil), content...)
	if body != nil {
		normalizedBody := normalizeEditorBody(body)
		updated = append(append([]byte(nil), content[:document.CloseEnd]...), normalizedBody...)
		document, err = parseEditorDocument(updated, editorMetadataPath(kind))
		if err != nil {
			return nil, err
		}
	}
	block := append([]byte(nil), updated[document.BodyStart:document.CloseStart]...)
	updates := editorFieldUpdates(fields, kind, editorFieldSelection(changedFields...))
	for _, update := range updates {
		block, err = replaceEditorField(block, update.key, update.lines)
		if err != nil {
			return nil, err
		}
	}
	return append(append(append([]byte(nil), updated[:document.BodyStart]...), block...), updated[document.CloseStart:]...), nil
}

type editorFieldUpdate struct {
	key   string
	lines []string
}

func editorFieldUpdates(fields editorFrontmatter, kind string, selected ...map[string]bool) []editorFieldUpdate {
	var selectedFields map[string]bool
	if len(selected) > 0 {
		selectedFields = selected[0]
	}
	updates := make([]editorFieldUpdate, 0, 19)
	if editorFieldSelected(selectedFields, "title") {
		updates = append(updates, editorFieldUpdate{key: "title", lines: editorYAMLField("title", fields.Title)})
	}
	if editorFieldSelected(selectedFields, "publish") {
		updates = append(updates, editorFieldUpdate{key: "publish", lines: editorYAMLField("publish", fields.Publish)})
	}
	if kind != "section" && editorFieldSelected(selectedFields, "type") {
		updates = append(updates, editorFieldUpdate{key: "type", lines: editorYAMLField("type", fields.Type)})
	}
	optional := []struct {
		key   string
		value any
	}{
		{"description", fields.Description},
		{"date", fields.Date},
		{"updated", fields.Updated},
		{"reviewed", fields.Reviewed},
		{"tags", fields.Tags},
		{"aliases", fields.Aliases},
		{"slug", fields.Slug},
		{"order", fields.Order},
		{"author", fields.Author},
		{"status", fields.Status},
		{"audience", fields.Audience},
		{"productVersion", fields.ProductVersion},
		{"series", fields.Series},
		{"cover", fields.Cover},
		{"banner", fields.Banner},
		{"bannerAlt", fields.BannerAlt},
	}
	for _, field := range optional {
		if !editorFieldSelected(selectedFields, field.key) {
			continue
		}
		var lines []string
		switch value := field.value.(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				lines = editorYAMLField(field.key, value)
			}
		case []string:
			if len(value) > 0 {
				lines = editorYAMLField(field.key, value)
			}
		case *int:
			if value != nil {
				lines = editorYAMLField(field.key, *value)
			}
		}
		updates = append(updates, editorFieldUpdate{key: field.key, lines: lines})
	}
	return updates
}

func editorFieldSelection(changed ...[]string) map[string]bool {
	if len(changed) == 0 || len(changed[0]) == 0 {
		return nil
	}
	selected := make(map[string]bool, len(changed[0]))
	for _, field := range changed[0] {
		selected[field] = true
	}
	return selected
}

func editorFieldSelected(selected map[string]bool, field string) bool {
	return selected == nil || selected[field]
}

func editorFrontmatterBlock(fields editorFrontmatter, kind string) ([]byte, error) {
	updates := editorFieldUpdates(fields, kind)
	var builder strings.Builder
	builder.WriteString("---\n")
	for _, update := range updates {
		if len(update.lines) == 0 {
			continue
		}
		for _, line := range update.lines {
			builder.WriteString(line)
			builder.WriteByte('\n')
		}
	}
	builder.WriteString("---\n")
	return []byte(builder.String()), nil
}

func editorYAMLField(key string, value any) []string {
	data, err := yaml.Marshal(map[string]any{key: value})
	if err != nil {
		return []string{key + ": " + strconv.Quote(fmt.Sprint(value))}
	}
	text := strings.TrimSuffix(string(data), "\n")
	return strings.Split(text, "\n")
}

func replaceEditorField(block []byte, key string, replacement []string) ([]byte, error) {
	lines := editorSourceLines(block)
	start := -1
	for index, line := range lines {
		if editorTopLevelField(line.text) == key {
			start = index
			break
		}
	}
	separator := editorPreferredLineEnding(block)
	if start < 0 {
		if len(replacement) == 0 {
			return block, nil
		}
		updated := append([]byte(nil), block...)
		if len(updated) > 0 && !hasEditorLineEnding(updated) {
			updated = append(updated, separator...)
		}
		return append(updated, []byte(strings.Join(editorLinesWithNewlines(replacement, separator), ""))...), nil
	}
	end := start + 1
	indentless := editorFieldHasEmptyValue(lines[start].text)
	for end < len(lines) {
		line := lines[end].text
		if indentless && editorIndentlessSequenceItem(line) {
			end++
			continue
		}
		if indentless && line == "" {
			next := end + 1
			for next < len(lines) && lines[next].text == "" {
				next++
			}
			if next < len(lines) && editorIndentlessSequenceItem(lines[next].text) {
				end++
				continue
			}
		}
		if line == "" {
			next := end + 1
			for next < len(lines) && lines[next].text == "" {
				next++
			}
			if next >= len(lines) || (len(lines[next].text) == 0 || (lines[next].text[0] != ' ' && lines[next].text[0] != '\t')) {
				break
			}
			end++
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			break
		}
		end++
	}
	replacementLines := editorLinesWithNewlines(replacement, separator)
	if len(replacementLines) > 0 {
		if comment := editorInlineComment(lines[start].text); comment != "" {
			replacementLines[0] = strings.TrimSuffix(replacementLines[0], separator) + " " + comment + separator
		}
	}
	before := block[:lines[start].start]
	after := block[lines[end-1].end:]
	result := make([]byte, 0, len(before)+len(after))
	result = append(result, before...)
	result = append(result, []byte(strings.Join(replacementLines, ""))...)
	result = append(result, after...)
	return result, nil
}

func editorLinesWithNewlines(lines []string, separator string) []string {
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		result = append(result, line+separator)
	}
	return result
}

func editorPreferredLineEnding(content []byte) string {
	for index, character := range content {
		switch character {
		case '\r':
			if index+1 < len(content) && content[index+1] == '\n' {
				return "\r\n"
			}
			return "\r"
		case '\n':
			return "\n"
		}
	}
	return "\n"
}

func hasEditorLineEnding(content []byte) bool {
	return bytes.ContainsAny(content, "\r\n")
}

func editorFieldHasEmptyValue(line string) bool {
	colon := editorYAMLKeyColon(line)
	if colon < 0 {
		return false
	}
	value := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(line[colon+1:], "\n"), "\r"))
	return value == "" || strings.HasPrefix(value, "#")
}

func editorIndentlessSequenceItem(line string) bool {
	return len(line) > 0 && line[0] == '-' && (len(line) == 1 || line[1] == ' ' || line[1] == '\t')
}

func editorTopLevelField(line string) string {
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") {
		return ""
	}
	colon := editorYAMLKeyColon(line)
	if colon <= 0 {
		return ""
	}
	key := strings.TrimSpace(line[:colon])
	if len(key) >= 2 && ((key[0] == '"' && key[len(key)-1] == '"') || (key[0] == '\'' && key[len(key)-1] == '\'')) {
		var decoded string
		if err := yaml.Unmarshal([]byte(key), &decoded); err == nil {
			return decoded
		}
	}
	if strings.ContainsAny(key, " \t#") {
		return ""
	}
	return key
}

func editorYAMLKeyColon(line string) int {
	var quote byte
	escaped := false
	for index := 0; index < len(line); index++ {
		character := line[index]
		if quote == '"' {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == quote {
				quote = 0
			}
			continue
		}
		if quote == '\'' {
			if character == quote {
				if index+1 < len(line) && line[index+1] == quote {
					index++
				} else {
					quote = 0
				}
			}
			continue
		}
		switch character {
		case '"', '\'':
			quote = character
		case ':':
			return index
		}
	}
	return -1
}

func editorInlineComment(line string) string {
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	var quote byte
	escaped := false
	for index := 0; index < len(line); index++ {
		character := line[index]
		if quote == '"' {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == quote {
				quote = 0
			}
			continue
		}
		if quote == '\'' {
			if character == quote {
				if index+1 < len(line) && line[index+1] == quote {
					index++
				} else {
					quote = 0
				}
			}
			continue
		}
		if character == '"' || character == '\'' {
			quote = character
			continue
		}
		if character == '#' && (index == 0 || line[index-1] == ' ' || line[index-1] == '\t') {
			return strings.TrimSpace(line[index:])
		}
	}
	return ""
}

func normalizeEditorBody(body []byte) []byte {
	if len(body) == 0 || body[0] == '\n' || body[0] == '\r' {
		return append([]byte(nil), body...)
	}
	return append([]byte{'\n'}, body...)
}
