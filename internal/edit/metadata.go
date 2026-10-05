package edit

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

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

type editorLine struct {
	start int
	end   int
	text  string
}

func parseEditorDocument(content []byte) (editorDocument, error) {
	document := editorDocument{Body: append([]byte(nil), content...)}
	lines := editorSourceLines(content)
	if len(lines) == 0 {
		return document, nil
	}
	first := strings.TrimPrefix(strings.TrimSuffix(lines[0].text, "\r"), "\ufeff")
	if first != "---" {
		return document, nil
	}
	for index := 1; index < len(lines); index++ {
		line := strings.TrimSuffix(lines[index].text, "\r")
		if line != "---" && line != "..." {
			continue
		}
		document.HasBlock = true
		document.BodyStart = lines[0].end
		document.CloseStart = lines[index].start
		document.CloseEnd = lines[index].end
		document.Body = append([]byte(nil), content[document.CloseEnd:]...)
		block := content[document.BodyStart:document.CloseStart]
		frontmatter, err := parseEditorMapping(block)
		if err != nil {
			return document, err
		}
		document.Frontmatter = frontmatter
		return document, nil
	}
	return document, fmt.Errorf("missing closing frontmatter delimiter")
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

func parseEditorMapping(block []byte) (editorFrontmatter, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(block, &document); err != nil {
		return editorFrontmatter{}, err
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return editorFrontmatter{}, fmt.Errorf("frontmatter must be a YAML mapping")
	}
	result := editorFrontmatter{}
	mapping := document.Content[0]
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		key := mapping.Content[index].Value
		value := mapping.Content[index+1]
		switch key {
		case "title":
			result.Title = editorScalar(value)
		case "publish":
			parsed, err := strconv.ParseBool(editorScalar(value))
			if err != nil {
				return editorFrontmatter{}, fmt.Errorf("publish must be boolean")
			}
			result.Publish = parsed
		case "type":
			result.Type = editorScalar(value)
		case "description":
			result.Description = editorScalar(value)
		case "date":
			result.Date = editorScalar(value)
		case "updated":
			result.Updated = editorScalar(value)
		case "reviewed":
			result.Reviewed = editorScalar(value)
		case "tags":
			result.Tags = editorStringList(value)
		case "aliases":
			result.Aliases = editorStringList(value)
		case "slug":
			result.Slug = editorScalar(value)
		case "order":
			value := editorScalar(value)
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return editorFrontmatter{}, fmt.Errorf("order must be an integer")
			}
			result.Order = &parsed
		case "author":
			result.Author = editorScalar(value)
		case "status":
			result.Status = editorScalar(value)
		case "audience":
			result.Audience = editorScalar(value)
		case "productVersion":
			result.ProductVersion = editorScalar(value)
		case "series":
			result.Series = editorScalar(value)
		case "cover":
			result.Cover = editorScalar(value)
		case "banner":
			result.Banner = editorScalar(value)
		case "bannerAlt":
			result.BannerAlt = editorScalar(value)
		}
	}
	return result, nil
}

func editorScalar(node *yaml.Node) string {
	if node == nil || node.Tag == "!!null" {
		return ""
	}
	return node.Value
}

func editorStringList(node *yaml.Node) []string {
	if node == nil || node.Tag == "!!null" {
		return nil
	}
	if node.Kind != yaml.SequenceNode {
		if value := strings.TrimSpace(editorScalar(node)); value != "" {
			return []string{value}
		}
		return nil
	}
	result := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		if value := strings.TrimSpace(editorScalar(item)); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func applyEditorFrontmatter(content, body []byte, fields editorFrontmatter, kind string, changedFields ...[]string) ([]byte, error) {
	document, err := parseEditorDocument(content)
	if err != nil {
		return nil, err
	}
	if !document.HasBlock {
		block, err := editorFrontmatterBlock(fields, kind)
		if err != nil {
			return nil, err
		}
		return append(block, normalizeEditorBody(body)...), nil
	}
	updated := append([]byte(nil), content...)
	if body != nil {
		normalizedBody := normalizeEditorBody(body)
		updated = append(append([]byte(nil), content[:document.CloseEnd]...), normalizedBody...)
		document, err = parseEditorDocument(updated)
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
