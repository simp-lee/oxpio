package edit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	stdhtml "html"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	internalconfig "github.com/simp-lee/oxpio/internal/config"
	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/resourcepath"
)

// ResourceUsage identifies one local resource reference in a Vault document.
type ResourceUsage struct {
	Document  string `json:"document"`
	Field     string `json:"field"`
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
	Line      int    `json:"line"`
}

// ResourceUsageConflictError prevents deleting a resource that still has
// source-side references.
type ResourceUsageConflictError struct {
	Path   string
	Usages []ResourceUsage
}

func (err *ResourceUsageConflictError) Error() string {
	documents := make(map[string]struct{}, len(err.Usages))
	for _, usage := range err.Usages {
		documents[usage.Document] = struct{}{}
	}
	return fmt.Sprintf("resource %q is used by %d document(s)", err.Path, len(documents))
}

type resourceReference struct {
	ResourceUsage
	start int
	end   int
	mode  resourceReferenceMode
}

type resourceReferenceMode uint8

const (
	resourceReferenceMarkdown resourceReferenceMode = iota
	resourceReferenceFrontmatter
	resourceReferenceHTML
	resourceReferenceCSS
	resourceReferenceWiki
)

type resourceResolver struct {
	exact       map[string]string
	canonical   map[string]string
	conflicts   map[string][]string
	attachments string
}

func (resolver resourceResolver) lookup(value string) model.PathLookupResult {
	if exact := resolver.exact[normalizeResourceLookupPath(value)]; exact != "" {
		return model.PathLookupResult{Path: exact}
	}
	canonical := model.CanonicalResourceLookupPath(value)
	if ambiguous := resolver.conflicts[canonical]; len(ambiguous) > 0 {
		return model.PathLookupResult{Ambiguous: append([]string(nil), ambiguous...)}
	}
	return model.PathLookupResult{Path: resolver.canonical[canonical]}
}

func normalizeResourceLookupPath(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.TrimPrefix(value, "/")
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return cleaned
}

var (
	frontmatterDocumentPattern        = regexp.MustCompile(`^(?:\x{FEFF})?---(?:\r\n|\r|\n)[\s\S]*?(?:\r\n|\r|\n)(?:---|\.\.\.)(?:(?:\r\n|\r|\n)|$)`)
	frontmatterResourcePattern        = regexp.MustCompile(`(?mi)^([ \t]*)(cover|banner)[ \t]*:[ \t]*(?:"([^"]*)"|'([^']*)'|([^#\r\n]*?))[ \t]*(?:#.*)?(?:\r\n|\r|\n|$)`)
	configResourcePattern             = regexp.MustCompile(`(?mi)^([ \t]*)defaultImg[ \t]*:[ \t]*(?:"([^"]*)"|'([^']*)'|([^#\r\n]*?))[ \t]*(?:#.*)?(?:\r\n|\r|\n|$)`)
	markdownResourcePattern           = regexp.MustCompile(`!?(?:\[[^\]\r\n]*\])\(\s*(?:<([^>\r\n]+)>|([^\s\r\n]+))`)
	markdownDefinitionResourcePattern = regexp.MustCompile(`(?mi)^[ \t]*\[[^\]\r\n]+\]:[ \t]*(?:<([^>\r\n]+)>|([^\s\r\n]+))`)
	wikiResourcePattern               = regexp.MustCompile(`!\[\[([^\]\r\n|#]+)(?:[|#][^\]\r\n]*)?\]\]`)
	htmlResourcePattern               = regexp.MustCompile(`(?i)\b(src|href|poster|background|data|xlink:href)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	htmlSrcsetPattern                 = regexp.MustCompile(`(?i)\b(srcset|imagesrcset)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	htmlStylePattern                  = regexp.MustCompile(`(?i)\bstyle\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	cssResourcePattern                = regexp.MustCompile(`(?i)\burl\(\s*(?:"([^"]*)"|'([^']*)'|([^)]*?))\s*\)`)
	cssImportPattern                  = regexp.MustCompile(`(?i)@import\s*(?:"([^"]*)"|'([^']*)')`)
	htmlCommentPattern                = regexp.MustCompile(`(?s)<!--.*?-->|/\*.*?\*/`)
	markdownFencePattern              = regexp.MustCompile("(?ms)^ {0,3}(?:```|~~~).*?^ {0,3}(?:```|~~~)[ \\t]*(?:\\r?\\n|$)")
	markdownInlineCodePattern         = regexp.MustCompile("`+[^`\\r\\n]*`+")
)

// ResourceUsages scans source-side Markdown, frontmatter, HTML, and CSS
// references to one Vault-relative resource.
func ResourceUsages(vault, target string) ([]ResourceUsage, error) {
	references, err := scanResourceReferences(vault, target)
	if err != nil {
		return nil, err
	}
	usages := make([]ResourceUsage, 0, len(references))
	for _, reference := range references {
		usages = append(usages, reference.ResourceUsage)
	}
	return usages, nil
}

func ResourceUsageCounts(vault string) (map[string]int, error) {
	references, err := scanResourceReferences(vault, "")
	if err != nil {
		return nil, err
	}
	resolver, err := newResourceResolver(vault)
	if err != nil {
		return nil, err
	}
	documents := make(map[string]map[string]struct{})
	for _, reference := range references {
		target := resolveResourceReference(resolver, reference.Document, reference.mode, reference.Reference)
		if target == "" {
			continue
		}
		if documents[target] == nil {
			documents[target] = make(map[string]struct{})
		}
		documents[target][reference.Document] = struct{}{}
	}
	counts := make(map[string]int, len(documents))
	for target, usages := range documents {
		counts[target] = len(usages)
	}
	return counts, nil
}

func newResourceResolver(vault string) (resourceResolver, error) {
	root, err := filepath.Abs(vault)
	if err != nil {
		return resourceResolver{}, err
	}
	root = filepath.Clean(root)
	resources := make([]string, 0)
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if fileManagerPathHidden(rel) {
				return filepath.SkipDir
			}
			if _, markerErr := os.Stat(filepath.Join(current, ".oxpio-output")); markerErr == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() || fileManagerPathHidden(rel) || strings.EqualFold(path.Ext(rel), ".md") {
			return nil
		}
		resources = append(resources, rel)
		return nil
	})
	if err != nil {
		return resourceResolver{}, fmt.Errorf("scan resources: %w", err)
	}
	exact := model.BuildExactLookupPaths(resources)
	canonical, conflicts := model.BuildCanonicalLookupPaths(resources)
	attachments, attachmentErr := internalconfig.ConfiguredAttachmentDirectory(root)
	if attachmentErr != nil {
		attachments = ""
	}
	return resourceResolver{exact: exact, canonical: canonical, conflicts: conflicts, attachments: attachments}, nil
}

func resourceRenameUpdates(vault, source, destination string) (map[string][]byte, map[string]string, []ResourceUsage, error) {
	references, err := scanResourceReferences(vault, source)
	if err != nil {
		return nil, nil, nil, err
	}
	updates := make(map[string][]byte)
	beforeHashes := make(map[string]string)
	byDocument := make(map[string][]resourceReference)
	for _, reference := range references {
		byDocument[reference.Document] = append(byDocument[reference.Document], reference)
	}
	for document, documentReferences := range byDocument {
		data, err := os.ReadFile(filepath.Join(vault, filepath.FromSlash(document)))
		if err != nil {
			return nil, nil, nil, fmt.Errorf("read resource reference document %q: %w", document, err)
		}
		updated := append([]byte(nil), data...)
		sort.Slice(documentReferences, func(i, j int) bool { return documentReferences[i].start > documentReferences[j].start })
		for _, reference := range documentReferences {
			replacement := renamedResourceReference(document, reference, source, destination)
			updated = append(updated[:reference.start], append([]byte(replacement), updated[reference.end:]...)...)
		}
		if string(updated) == string(data) {
			continue
		}
		updates[document] = updated
		beforeHashes[document] = hashResourceBytes(data)
	}
	usages := make([]ResourceUsage, 0, len(references))
	for _, reference := range references {
		usages = append(usages, reference.ResourceUsage)
	}
	return updates, beforeHashes, usages, nil
}

func scanResourceReferences(vault, target string) ([]resourceReference, error) {
	root, err := filepath.Abs(vault)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	target = filepath.ToSlash(path.Clean(strings.TrimSpace(target)))
	if target == "." || target == "" {
		target = ""
	}
	resolver, err := newResourceResolver(root)
	if err != nil {
		return nil, err
	}
	references := make([]resourceReference, 0)
	configPath := filepath.Join(root, internalconfig.Filename)
	if data, readErr := os.ReadFile(configPath); readErr == nil {
		for _, reference := range parseResourceReferences(internalconfig.Filename, data) {
			if target == "" || resolveResourceReference(resolver, internalconfig.Filename, reference.mode, reference.Reference) == target {
				references = append(references, reference)
			}
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, fmt.Errorf("scan resource references: read config: %w", readErr)
	}
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if fileManagerPathHidden(rel) {
				return filepath.SkipDir
			}
			if _, markerErr := os.Stat(filepath.Join(current, ".oxpio-output")); markerErr == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() || fileManagerPathHidden(rel) {
			return nil
		}
		lower := strings.ToLower(path.Ext(rel))
		if lower != ".md" && lower != ".html" && lower != ".htm" && lower != ".css" {
			return nil
		}
		data, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		fileReferences := parseResourceReferences(rel, data)
		for _, reference := range fileReferences {
			if target == "" || resolveResourceReference(resolver, rel, reference.mode, reference.Reference) == target {
				references = append(references, reference)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan resource references: %w", err)
	}
	sort.Slice(references, func(i, j int) bool {
		left, right := references[i], references[j]
		if left.Document != right.Document {
			return left.Document < right.Document
		}
		if left.Line != right.Line {
			return left.Line < right.Line
		}
		return left.start < right.start
	})
	return references, nil
}

func parseResourceReferences(document string, data []byte) []resourceReference {
	text := string(data)
	refs := make([]resourceReference, 0)
	if strings.EqualFold(path.Base(document), internalconfig.Filename) {
		for _, match := range configResourcePattern.FindAllStringSubmatchIndex(text, -1) {
			valueStart, valueEnd, value := resourcePatternValue(match, text, []int{2, 3, 4})
			if valueStart < 0 || value == "" {
				continue
			}
			refs = appendResourceReference(&refs, document, "defaultImg", "config", value, valueStart, valueEnd, resourceReferenceFrontmatter, text)
		}
		return refs
	}
	frontmatterEnd := 0
	if strings.EqualFold(path.Ext(document), ".md") {
		frontmatterEnd = resourceFrontmatterEnd(text)
		if frontmatterEnd > 0 {
			for _, match := range frontmatterResourcePattern.FindAllStringSubmatchIndex(text[:frontmatterEnd], -1) {
				valueStart, valueEnd, value := resourcePatternValue(match, text, []int{3, 4, 5})
				if valueStart < 0 || value == "" {
					continue
				}
				field := strings.ToLower(text[match[4]:match[5]])
				refs = appendResourceReference(&refs, document, field, "frontmatter", value, valueStart, valueEnd, resourceReferenceFrontmatter, text)
			}
		}
	}
	body := text[frontmatterEnd:]
	bodyOffset := frontmatterEnd
	scanBody := maskResourceSyntax(body, path.Ext(document))
	if strings.EqualFold(path.Ext(document), ".css") {
		appendCSSResourceReferences(&refs, document, body, scanBody, bodyOffset, text)
		for _, match := range cssImportPattern.FindAllStringSubmatchIndex(scanBody, -1) {
			valueStart, valueEnd, value := resourcePatternValue(match, body, []int{1, 2})
			if valueStart < 0 || value == "" {
				continue
			}
			refs = appendResourceReference(&refs, document, "CSS import", "css-import", value, bodyOffset+valueStart, bodyOffset+valueEnd, resourceReferenceCSS, text)
		}
		return refs
	}
	for _, match := range markdownResourcePattern.FindAllStringSubmatchIndex(scanBody, -1) {
		valueStart, valueEnd, value := markdownResourcePatternValue(match, body)
		if valueStart < 0 || value == "" {
			continue
		}
		kind := "markdown-link"
		if body[match[0]] == '!' {
			kind = "markdown-image"
		}
		refs = appendResourceReference(&refs, document, kind, kind, value, bodyOffset+valueStart, bodyOffset+valueEnd, resourceReferenceMarkdown, text)
	}
	for _, match := range markdownDefinitionResourcePattern.FindAllStringSubmatchIndex(scanBody, -1) {
		valueStart, valueEnd, value := resourcePatternValue(match, body, []int{1, 2})
		if valueStart < 0 || value == "" {
			continue
		}
		refs = appendResourceReference(&refs, document, "markdown-link", "markdown-link", value, bodyOffset+valueStart, bodyOffset+valueEnd, resourceReferenceMarkdown, text)
	}
	for _, match := range wikiResourcePattern.FindAllStringSubmatchIndex(scanBody, -1) {
		valueStart, valueEnd, value := resourcePatternValue(match, body, []int{1})
		if valueStart < 0 || value == "" {
			continue
		}
		refs = appendResourceReference(&refs, document, "markdown-image", "markdown-image", value, bodyOffset+valueStart, bodyOffset+valueEnd, resourceReferenceWiki, text)
	}
	for _, match := range htmlResourcePattern.FindAllStringSubmatchIndex(scanBody, -1) {
		valueStart, valueEnd, value := resourcePatternValue(match, body, []int{2, 3, 4})
		if valueStart < 0 || value == "" {
			continue
		}
		attribute := strings.ToLower(body[match[2]:match[3]])
		refs = appendResourceReference(&refs, document, "HTML "+attribute, "html-"+attribute, value, bodyOffset+valueStart, bodyOffset+valueEnd, resourceReferenceHTML, text)
	}
	for _, match := range htmlSrcsetPattern.FindAllStringSubmatchIndex(scanBody, -1) {
		valueStart, valueEnd, value := resourcePatternValue(match, body, []int{2, 3, 4})
		if valueStart < 0 || value == "" {
			continue
		}
		attribute := strings.ToLower(body[match[2]:match[3]])
		appendSrcsetResourceReferences(&refs, document, attribute, value, bodyOffset+valueStart, text)
		_ = valueEnd
	}
	for _, match := range htmlStylePattern.FindAllStringSubmatchIndex(scanBody, -1) {
		valueStart, _, value := resourcePatternValue(match, body, []int{1, 2, 3})
		if valueStart < 0 || value == "" {
			continue
		}
		style := body[valueStart : valueStart+len(value)]
		appendCSSResourceReferences(&refs, document, style, maskResourceSyntax(style, ".css"), bodyOffset+valueStart, text)
	}
	return refs
}

func markdownResourcePatternValue(match []int, source string) (int, int, string) {
	start, end, value := resourcePatternValue(match, source, []int{1, 2})
	if start >= 0 && match[4] >= 0 && strings.HasSuffix(value, ")") {
		end--
		value = value[:len(value)-1]
	}
	return start, end, value
}

func appendCSSResourceReferences(target *[]resourceReference, document, source, masked string, offset int, fullSource string) {
	for _, match := range cssResourcePattern.FindAllStringSubmatchIndex(masked, -1) {
		valueStart, valueEnd, value := resourcePatternValue(match, source, []int{1, 2, 3})
		if valueStart < 0 || value == "" {
			continue
		}
		appendResourceReference(target, document, "CSS url", "css-url", value, offset+valueStart, offset+valueEnd, resourceReferenceCSS, fullSource)
	}
}

func appendSrcsetResourceReferences(target *[]resourceReference, document, attribute, value string, offset int, fullSource string) {
	for index := 0; index < len(value); {
		for index < len(value) && (value[index] == ',' || value[index] == ' ' || value[index] == '\t' || value[index] == '\r' || value[index] == '\n' || value[index] == '\f') {
			index++
		}
		start := index
		for index < len(value) && value[index] != ',' && value[index] != ' ' && value[index] != '\t' && value[index] != '\r' && value[index] != '\n' && value[index] != '\f' {
			index++
		}
		if start < index {
			reference := value[start:index]
			appendResourceReference(target, document, "HTML "+attribute, "html-"+attribute, reference, offset+start, offset+index, resourceReferenceHTML, fullSource)
		}
		for index < len(value) && value[index] != ',' {
			index++
		}
	}
}

func maskResourceSyntax(source, extension string) string {
	masked := source
	if strings.EqualFold(extension, ".md") {
		masked = maskMatches(masked, markdownFencePattern)
		masked = maskMatches(masked, markdownInlineCodePattern)
	}
	if strings.EqualFold(extension, ".html") || strings.EqualFold(extension, ".htm") || strings.EqualFold(extension, ".css") || strings.EqualFold(extension, ".md") {
		masked = maskMatches(masked, htmlCommentPattern)
	}
	return masked
}

func maskMatches(source string, pattern *regexp.Regexp) string {
	if source == "" || pattern == nil {
		return source
	}
	masked := []byte(source)
	for _, match := range pattern.FindAllStringIndex(source, -1) {
		for index := match[0]; index < match[1]; index++ {
			if masked[index] != '\r' && masked[index] != '\n' {
				masked[index] = ' '
			}
		}
	}
	return string(masked)
}

func appendResourceReference(target *[]resourceReference, document, field, kind, value string, start, end int, mode resourceReferenceMode, source string) []resourceReference {
	line := 1 + strings.Count(source[:start], "\n")
	*target = append(*target, resourceReference{ResourceUsage: ResourceUsage{Document: document, Field: field, Kind: kind, Reference: value, Line: line}, start: start, end: end, mode: mode})
	return *target
}

func resourcePatternValue(match []int, source string, groups []int) (int, int, string) {
	for _, group := range groups {
		start, end := match[group*2], match[group*2+1]
		if start < 0 {
			continue
		}
		value := source[start:end]
		if group == groups[len(groups)-1] {
			trimmed := strings.TrimSpace(value)
			leading := len(value) - len(strings.TrimLeft(value, " \t"))
			trailing := len(value) - len(strings.TrimRight(value, " \t"))
			start += leading
			end -= trailing
			value = trimmed
		}
		return start, end, value
	}
	return -1, -1, ""
}

func resourceFrontmatterEnd(source string) int {
	match := frontmatterDocumentPattern.FindStringIndex(source)
	if match == nil {
		return 0
	}
	return match[1]
}

func resolveResourceReference(resolver resourceResolver, document string, mode resourceReferenceMode, raw string) string {
	raw = strings.TrimSpace(stdhtml.UnescapeString(raw))
	if mode == resourceReferenceFrontmatter {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.Path == "" {
			return ""
		}
		decoded, err := url.PathUnescape(parsed.EscapedPath())
		if err != nil {
			return ""
		}
		return resolver.lookup(strings.TrimPrefix(decoded, "/")).Path
	}
	note := &model.Note{RelPath: document}
	if mode == resourceReferenceWiki {
		return resourcepath.LookupImageEmbedPath(note, resolver.attachments, raw, resolver.lookup).Path
	}
	return resourcepath.LookupPath(note, resolver.attachments, raw, resolver.lookup).Path
}

func renamedResourceReference(document string, reference resourceReference, source, destination string) string {
	original := reference.Reference
	_, suffix := splitResourceReferenceSuffix(original)
	if reference.mode == resourceReferenceFrontmatter {
		pathPart := destination
		if strings.HasPrefix(original, "/") {
			pathPart = "/" + strings.TrimPrefix(destination, "/")
		}
		return encodeResourcePathPreservingSpaces(pathPart) + suffix
	}
	directory := path.Dir(document)
	var pathPart string
	if reference.mode == resourceReferenceWiki && !strings.HasPrefix(original, "/") && !strings.Contains(original, "/") && path.Dir(source) == path.Dir(destination) {
		pathPart = path.Base(destination)
	} else {
		relative, err := filepath.Rel(filepath.FromSlash(directory), filepath.FromSlash(destination))
		if err != nil {
			relative = destination
		}
		pathPart = filepath.ToSlash(relative)
		if strings.HasPrefix(original, "/") {
			pathPart = "/" + destination
		}
	}
	if reference.mode == resourceReferenceWiki {
		return encodeResourcePathPreservingSpaces(pathPart) + suffix
	}
	return encodeResourcePath(pathPart) + suffix
}

func splitResourceReferenceSuffix(value string) (string, string) {
	index := strings.IndexAny(value, "?#")
	if index < 0 {
		return value, ""
	}
	return value[:index], value[index:]
}

func encodeResourcePathPreservingSpaces(value string) string {
	return strings.ReplaceAll(encodeResourcePath(value), "%20", " ")
}

func encodeResourcePath(value string) string {
	leading := strings.HasPrefix(value, "/")
	value = strings.TrimPrefix(value, "/")
	parts := strings.Split(value, "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	encoded := strings.Join(parts, "/")
	if leading {
		return "/" + encoded
	}
	return encoded
}

func hashResourceBytes(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
