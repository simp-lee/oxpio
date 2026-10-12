// Package theme implements the single OXPIO Go html/template theme compiler.
package theme

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	templateparse "text/template/parse"
	"time"
	"unicode/utf8"

	"github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/render/viewmodel"
	_ "golang.org/x/image/webp"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

const (
	APIVersion = 1
	VMVersion  = "oxpio.theme.vm/v1"
	BaseID     = "base"
)

//go:embed base/**
var baseFS embed.FS

var requiredLayouts = [...]string{"base", "section", "article", "tag", "timeline", "404"}
var staticThemeAssetURLPattern = regexp.MustCompile(`(?i)\b(?:src|href)\s*=\s*["'](/?assets/[^"'#?\s]+)`)
var themeAssetCallPattern = regexp.MustCompile(`\{\{\s*assetURL\s+"([^"]+)"\s*\}\}`)
var themeAssetBacktickPattern = regexp.MustCompile("\\{\\{\\s*assetURL\\s+`([^`]+)`\\s*\\}\\}")
var themeExternalURLPattern = regexp.MustCompile(`(?i)(?:https?:)?//`)
var themeExternalResourcePattern = regexp.MustCompile(`(?i)(?:href|src)\s*=\s*["'](?:https?:)?//|url\s*\(\s*(?:https?:)?//`)

// File describes one theme-domain regular file without exposing its host path.
type File struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Hash string `json:"sha256"`
	Size int    `json:"size"`
	Data []byte `json:"-"`
}

// Source is an immutable theme input snapshot.
type Source struct {
	ID         string
	APIVersion int
	Name       string
	Files      map[string][]byte
	Dirs       map[string]bool
	Embedded   bool
}

// Manifest is the stable public identity of a theme input.
type Manifest struct {
	APIVersion int    `json:"apiVersion"`
	Name       string `json:"name"`
	Hash       string `json:"hash"`
	Files      []File `json:"files"`
}

// BaseFiles returns a fresh copy of the embedded base theme.
func BaseFiles() (map[string][]byte, error) {
	return readFS(baseFS, "base")
}

// BaseSource returns the explicitly selected embedded theme.
func BaseSource() (Source, error) {
	files, err := BaseFiles()
	if err != nil {
		return Source{}, err
	}
	return sourceFromFiles(BaseID, files, true), nil
}

// LoadLocal reads only one selected local theme. Unselected themes are never
// discovered by this function.
func LoadLocal(vault, id string) (Source, error) {
	if !validID(id) || id == BaseID {
		return Source{}, fmt.Errorf("theme %q is not a valid local theme ID", id)
	}
	root := filepath.Join(vault, ".oxpio", "themes", id)
	current := vault
	for _, segment := range []string{".oxpio", "themes", id} {
		current = filepath.Join(current, segment)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return Source{}, fmt.Errorf("read selected theme %q: %w", id, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return Source{}, fmt.Errorf("selected theme %q must not cross a symbolic link", id)
		}
		if !info.IsDir() {
			return Source{}, fmt.Errorf("selected theme %q must be a regular directory", id)
		}
	}
	files := make(map[string][]byte)
	dirs := map[string]bool{".": true}
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, current)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if !safeRelativePath(rel) {
			return fmt.Errorf("theme path %q is not safe", rel)
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("theme path %q must not be a symbolic link", rel)
		}
		if entry.IsDir() {
			dirs[rel] = true
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("theme path %q must be a regular file", rel)
		}
		data, readErr := os.ReadFile(current)
		if readErr != nil {
			return readErr
		}
		if len(data) > 16*1024*1024 {
			return fmt.Errorf("theme file %q exceeds 16 MiB", rel)
		}
		if err := ValidateFileData(rel, data); err != nil {
			return err
		}
		files[rel] = data
		return nil
	})
	if err != nil {
		return Source{}, err
	}
	return sourceFromFiles(id, files, false, dirs), nil
}

func sourceFromFiles(id string, files map[string][]byte, embedded bool, dirs ...map[string]bool) Source {
	copied := make(map[string][]byte, len(files))
	for name, data := range files {
		copied[name] = append([]byte(nil), data...)
	}
	directorySet := map[string]bool{".": true}
	for name := range copied {
		for current := path.Dir(name); current != "." && current != "/"; current = path.Dir(current) {
			directorySet[current] = true
		}
	}
	if len(dirs) > 0 {
		for name, present := range dirs[0] {
			directorySet[name] = present
		}
	}
	return Source{ID: id, APIVersion: APIVersion, Name: id, Files: copied, Dirs: directorySet, Embedded: embedded}
}

func readFS(root fs.FS, prefix string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := fs.WalkDir(root, prefix, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(name, prefix+"/")] = data
		return nil
	})
	return files, err
}

func validID(id string) bool {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\\`) || !fsutil.IsPortableSitePath(id) {
		return false
	}
	return true
}

// ValidID reports whether a local theme identifier is portable and safe.
func ValidID(id string) bool { return validID(id) }

func safeRelativePath(value string) bool {
	if value == "" || value == "." || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || path.Clean(value) != value || strings.HasPrefix(value, "../") || value == ".." {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if !fsutil.IsPortableSitePath(segment) {
			return false
		}
	}
	return true
}

// ValidRelativePath reports whether a theme-domain file path is safe.
func ValidRelativePath(value string) bool { return safeRelativePath(value) }

// Validate checks the strict theme manifest, directory contract, templates,
// and the five page-kind execution entries. Resource URL checks happen through
// Compile when the site plan supplies its planned asset map.
func Validate(source Source) error {
	if !validID(source.ID) || source.ID != source.Name {
		return fmt.Errorf("theme name must equal its safe directory ID")
	}
	if len(source.Files) == 0 {
		return fmt.Errorf("theme %q must not be empty", source.ID)
	}
	if err := validateFileContract(source); err != nil {
		return err
	}
	if err := validateManifest(source); err != nil {
		return err
	}
	for _, directory := range []string{"layouts", "partials", "assets"} {
		if !source.Dirs[directory] {
			return fmt.Errorf("theme directory %q is required", directory)
		}
	}
	for _, layout := range requiredLayouts {
		if _, ok := source.Files[path.Join("layouts", layout+".html")]; !ok {
			return fmt.Errorf("missing required layout layouts/%s.html", layout)
		}
	}
	_, err := Compile(source, nil, "https://theme.invalid/")
	return err
}

// ValidateWithAssets additionally executes every public page entry against a
// deterministic empty View Model so missing template calls and planned asset
// references fail before output is opened.
func ValidationAssets(source Source) map[string]string {
	assets := make(map[string]string)
	for name := range source.Files {
		if strings.HasPrefix(name, "assets/") && strings.TrimPrefix(name, "assets/") != "" {
			assets[name] = name
		}
	}
	return assets
}

func validateThemeTemplateActions(name string, data []byte) error {
	allowed := map[string]bool{"siteURL": true, "routeURL": true, "assetURL": true, "formatDate": true, "emitJSONLD": true}
	funcs := map[string]any{}
	for identifier := range allowed {
		funcs[identifier] = func(...any) any { return nil }
	}
	for _, identifier := range []string{"and", "call", "eq", "ge", "gt", "index", "le", "len", "lt", "ne", "not", "or", "print", "printf", "println", "slice"} {
		funcs[identifier] = func(...any) any { return nil }
	}
	trees, err := templateparse.Parse(name, string(data), "{{", "}}", funcs)
	if err != nil {
		return fmt.Errorf("theme template %s is invalid: %w", name, err)
	}
	var visit func(templateparse.Node) error
	visit = func(node templateparse.Node) error {
		if node == nil {
			return nil
		}
		value := reflect.ValueOf(node)
		if value.Kind() == reflect.Ptr && value.IsNil() {
			return nil
		}
		switch node := node.(type) {
		case *templateparse.ListNode:
			for _, child := range node.Nodes {
				if err := visit(child); err != nil {
					return err
				}
			}
		case *templateparse.ActionNode:
			return visit(node.Pipe)
		case *templateparse.IfNode:
			if err := visit(node.Pipe); err != nil {
				return err
			}
			if err := visit(node.List); err != nil {
				return err
			}
			return visit(node.ElseList)
		case *templateparse.RangeNode:
			if err := visit(node.Pipe); err != nil {
				return err
			}
			if err := visit(node.List); err != nil {
				return err
			}
			return visit(node.ElseList)
		case *templateparse.WithNode:
			if err := visit(node.Pipe); err != nil {
				return err
			}
			if err := visit(node.List); err != nil {
				return err
			}
			return visit(node.ElseList)
		case *templateparse.TemplateNode:
			return visit(node.Pipe)
		case *templateparse.PipeNode:
			for _, command := range node.Cmds {
				if err := visit(command); err != nil {
					return err
				}
			}
		case *templateparse.CommandNode:
			for _, argument := range node.Args {
				if err := visit(argument); err != nil {
					return err
				}
			}
		case *templateparse.IdentifierNode:
			if !allowed[node.Ident] {
				return fmt.Errorf("theme template %s uses forbidden function %q", name, node.Ident)
			}
		case *templateparse.ChainNode:
			return visit(node.Node)
		}
		return nil
	}
	for _, tree := range trees {
		if err := visit(tree.Root); err != nil {
			return err
		}
	}
	return nil
}

func ValidateWithAssets(source Source, assets map[string]string, baseURL string) error {
	if err := Validate(source); err != nil {
		return err
	}
	names := make([]string, 0, len(source.Files))
	for name := range source.Files {
		if strings.HasPrefix(name, "layouts/") || strings.HasPrefix(name, "partials/") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		data := source.Files[name]
		if err := validateThemeTemplateActions(name, data); err != nil {
			return err
		}
		if themeExternalURLPattern.Match(data) {
			return fmt.Errorf("theme template %s contains an external resource URL", name)
		}
		if match := staticThemeAssetURLPattern.FindSubmatch(data); len(match) == 2 {
			return fmt.Errorf("theme template %s must use assetURL for local asset %q", name, string(match[1]))
		}
		for _, pattern := range []*regexp.Regexp{themeAssetCallPattern, themeAssetBacktickPattern} {
			for _, match := range pattern.FindAllSubmatch(data, -1) {
				resource := string(match[1])
				if assets[resource] == "" {
					return fmt.Errorf("theme template %s references unplanned asset %q", name, resource)
				}
			}
		}
	}
	renderer, err := Compile(source, assets, baseURL)
	if err != nil {
		return err
	}
	for _, kind := range requiredLayouts[1:] {
		if _, err := renderer.Render(kind, samplePage(kind)); err != nil {
			return fmt.Errorf("theme sample %s: %w", kind, err)
		}
	}
	return nil
}

// SamplePage returns the deterministic in-memory preview context for one page kind.
func SamplePage(kind string) viewmodel.Page { return samplePage(kind) }

func samplePage(kind string) viewmodel.Page {
	card := viewmodel.Card{Path: "guide/intro.md", URL: "/guide/intro/", Href: "/guide/intro/", Title: "Introduction", Summary: "Example"}
	page := viewmodel.Page{
		Schema: viewmodel.SchemaVersion, Kind: kind,
		Site:  viewmodel.Site{Title: "Example site", BaseURL: "https://example.test/", Author: "Author", Description: "Description", Language: "en"},
		Title: "Example", Description: "Description", URL: "/guide/", Href: "/guide/", Canonical: "https://example.test/guide/",
		Content: template.HTML("<p>Example content</p>"), HeadHTML: template.HTML("<meta name=description content=Description>"),
		Navigation:  []viewmodel.NavigationItem{{Name: "Home", URL: "/", Href: "/", Current: "page", Active: true}},
		Breadcrumbs: []viewmodel.Breadcrumb{{Name: "Home", URL: "/", Href: "/"}},
		Sidebar:     []viewmodel.SidebarNode{{Name: "Guide", URL: "/guide/", Href: "/guide/", Current: true}},
		Versions:    []viewmodel.Version{{ID: "v1", Label: "Version 1", URL: "/v1/", Href: "/v1/", Current: true}},
		Source:      &viewmodel.Source{Path: "guide/_index.md", EditURL: "https://source.test/edit/guide/_index.md", ViewURL: "https://source.test/view/guide/_index.md"},
		Entries:     []viewmodel.Card{card}, Pagination: viewmodel.Pagination{CurrentPage: 1, TotalPages: 1, Entries: []viewmodel.Card{card}},
		SEO: viewmodel.SEO{Title: "Example", Description: "Description", Canonical: "https://example.test/guide/", JSONLD: `{"@type":"Article"}`}, VersionID: "v1",
	}
	switch kind {
	case "section":
		page.Section = &viewmodel.Section{Path: "guide", URL: "/guide/", Href: "/guide/", Title: "Guide", Articles: []viewmodel.Card{card}}
	case "article":
		page.Article = &viewmodel.Article{Path: card.Path, URL: card.URL, Href: card.Href, Title: card.Title, Description: card.Summary, Type: "doc", Tags: []string{"example"}, Position: 1, Total: 1}
	case "tag":
		page.Title = "Tag: example"
		page.Tags = []string{"example"}
	case "timeline":
		page.Title = "Recent articles"
	case "404":
		page.NotFound = true
		page.Title = "Not found"
		page.Description, page.Canonical, page.SEO = "", "", viewmodel.SEO{}
		page.Source, page.Section, page.Article, page.Entries, page.Tags = nil, nil, nil, nil, nil
	}
	return page
}

// ValidateFileData validates one theme-domain file's declared encoding and
// the signatures of supported binary assets.
func ValidateFileData(name string, data []byte) error {
	kind, err := fileKind(name)
	if err != nil {
		return err
	}
	if kind == "text" {
		if !utf8.Valid(data) {
			return fmt.Errorf("theme text file %q is not valid UTF-8", name)
		}
		if strings.EqualFold(path.Ext(name), ".svg") {
			lower := strings.ToLower(string(data))
			if themeExternalResourcePattern.MatchString(lower) || strings.Contains(lower, "@import") || strings.Contains(lower, "url(data:") {
				return fmt.Errorf("theme SVG file %q contains an external resource", name)
			}
		}
		return nil
	}
	ext := strings.ToLower(path.Ext(name))
	valid := false
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		_, format, decodeErr := image.DecodeConfig(bytes.NewReader(data))
		if decodeErr == nil {
			want := map[string]bool{"png": ext == ".png", "jpeg": ext == ".jpg" || ext == ".jpeg", "gif": ext == ".gif", "webp": ext == ".webp"}
			valid = want[format]
		}
	case ".woff":
		valid = len(data) >= 4 && string(data[:4]) == "wOFF"
	case ".woff2":
		valid = len(data) >= 4 && string(data[:4]) == "wOF2"
	case ".ttf":
		valid = len(data) >= 4 && (string(data[:4]) == "\x00\x01\x00\x00" || string(data[:4]) == "true")
	case ".otf":
		valid = len(data) >= 4 && string(data[:4]) == "OTTO"
	}
	if !valid {
		return fmt.Errorf("theme binary file %q has an invalid %s signature", name, ext)
	}
	return nil
}

func validateFileContract(source Source) error {
	names := make([]string, 0, len(source.Files))
	for name := range source.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	identities := make(map[string]string, len(names))
	for _, name := range names {
		data := source.Files[name]
		if !safeRelativePath(name) {
			return fmt.Errorf("theme file path %q is not safe", name)
		}
		identity := strings.ToLower(norm.NFKC.String(name))
		if previous := identities[identity]; previous != "" && previous != name {
			return fmt.Errorf("theme file paths %q and %q collide on portable filesystems", previous, name)
		}
		identities[identity] = name
		if err := ValidateFileData(name, data); err != nil {
			return err
		}
	}
	return nil
}

func validateManifest(source Source) error {
	data, ok := source.Files["theme.yaml"]
	if !ok {
		return fmt.Errorf("theme.yaml is required")
	}
	var node yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&node); err != nil {
		return fmt.Errorf("parse theme.yaml: %w", err)
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("theme.yaml must contain one document")
	} else if err != io.EOF {
		return fmt.Errorf("parse theme.yaml: %w", err)
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("theme.yaml must be one mapping document")
	}
	root := node.Content[0]
	seen := map[string]bool{}
	var api int
	var name string
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if key.Value == "" || seen[key.Value] {
			return fmt.Errorf("theme.yaml contains duplicate key %q", key.Value)
		}
		seen[key.Value] = true
		if value.Tag == "!!null" {
			return fmt.Errorf("theme.yaml field %q must not be null", key.Value)
		}
		switch key.Value {
		case "apiVersion":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!int" || value.Value != "1" {
				return fmt.Errorf("theme.yaml apiVersion must be integer 1")
			}
			api = 1
		case "name":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || strings.TrimSpace(value.Value) == "" {
				return fmt.Errorf("theme.yaml name must be non-empty")
			}
			name = strings.TrimSpace(value.Value)
		default:
			return fmt.Errorf("theme.yaml unknown field %q", key.Value)
		}
	}
	if api != 1 || name != source.Name {
		return fmt.Errorf("theme.yaml must contain apiVersion: 1 and name: %s", source.Name)
	}
	return nil
}

// Compile parses one complete theme with the one documented function set.
func Compile(source Source, assets map[string]string, baseURL string) (*Renderer, error) {
	if err := validateFileContract(source); err != nil {
		return nil, err
	}
	if err := validateManifest(source); err != nil {
		return nil, err
	}
	funcs := template.FuncMap{
		"siteURL":  func(value string) (string, error) { return resolveThemeURL(baseURL, value, assets) },
		"routeURL": func(value string) (string, error) { return resolveThemeURL(baseURL, value, assets) },
		"assetURL": func(value string) (string, error) {
			if !safeRelativePath(value) {
				return "", fmt.Errorf("theme asset path %q is not normalized", value)
			}
			destination := assets[value]
			if destination == "" {
				return "", fmt.Errorf("theme asset %q was not planned", value)
			}
			return resolveURL(baseURL, "/"+strings.TrimPrefix(destination, "/"))
		},
		"formatDate": func(value, layout string) (string, error) {
			if strings.TrimSpace(layout) == "" {
				return "", fmt.Errorf("formatDate layout is required")
			}
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				if parsed, err = time.Parse("2006-01-02", value); err != nil {
					return "", fmt.Errorf("formatDate value is invalid")
				}
			}
			return parsed.UTC().Format(layout), nil
		},
		"emitJSONLD": func(value string) (template.JS, error) {
			canonical, err := canonicalJSONLD(value)
			if err != nil {
				return "", err
			}
			return template.JS(canonical), nil
		},
	}
	compiled := template.New("root").Funcs(funcs)
	names := make([]string, 0, len(source.Files))
	for name := range source.Files {
		if strings.HasPrefix(name, "layouts/") || strings.HasPrefix(name, "partials/") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.Contains(name, "..") || !strings.HasSuffix(name, ".html") {
			continue
		}
		if _, err := compiled.New(name).Parse(string(source.Files[name])); err != nil {
			return nil, fmt.Errorf("theme template %s: %w", name, err)
		}
	}
	for _, layout := range requiredLayouts {
		if compiled.Lookup(layout) == nil && compiled.Lookup("layouts/"+layout+".html") == nil {
			return nil, fmt.Errorf("theme layout %s is not executable", layout)
		}
		if layout == "base" && compiled.Lookup("base") == nil {
			return nil, fmt.Errorf("theme layout base must define template %q", "base")
		}
	}
	return &Renderer{source: source, templates: compiled, assets: assets, baseURL: baseURL}, nil
}

func resolveThemeURL(base, value string, _ map[string]string) (string, error) {
	assetPath := strings.TrimPrefix(value, "/")
	if strings.HasPrefix(assetPath, "assets/") && !strings.HasPrefix(assetPath, "assets/oxpio/") && !strings.HasPrefix(assetPath, "assets/oxpio-runtime/") {
		return "", fmt.Errorf("theme URL asset %q must use assetURL", value)
	}
	return resolveURL(base, value)
}

func resolveURL(base, value string) (string, error) {
	if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\\\x00") || strings.HasPrefix(value, "//") {
		return "", fmt.Errorf("theme URL input is invalid")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" {
		return "", fmt.Errorf("theme URL input must be a normalized relative route")
	}
	baseURL, err := url.Parse(base)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return "", fmt.Errorf("theme base URL is invalid")
	}
	if parsed.User != nil || parsed.Opaque != "" || parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == "" {
		return "", fmt.Errorf("theme URL input is invalid")
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	if strings.Contains(parsed.Path, "//") {
		return "", fmt.Errorf("theme URL path is not normalized")
	}
	segments := strings.Split(parsed.Path, "/")
	for index, segment := range segments {
		if segment == "." || segment == ".." || segment == "" && index != 0 && index != len(segments)-1 {
			return "", fmt.Errorf("theme URL path is not normalized")
		}
	}
	if !strings.HasPrefix(parsed.Path, "/") {
		parsed.Path = "/" + parsed.Path
	}
	basePath := strings.TrimSuffix(baseURL.Path, "/")
	if basePath == "/" {
		basePath = ""
	}
	parsed.Path = basePath + parsed.Path
	parsed.RawPath = ""
	return strings.TrimSuffix(baseURL.ResolveReference(parsed).String(), "#"), nil
}

// Renderer is the only production HTML renderer for theme pages.
type Renderer struct {
	source    Source
	templates *template.Template
	assets    map[string]string
	baseURL   string
}

func canonicalJSONLD(value string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("emitJSONLD value is not a canonical Article JSON object")
	}
	allowed := map[string]bool{
		"@context": true, "@type": true, "headline": true, "url": true,
		"description": true, "image": true, "author": true, "audience": true,
		"version": true, "isPartOf": true, "datePublished": true, "dateModified": true,
		"lastReviewed": true, "creativeWorkStatus": true, "keywords": true,
	}
	for name := range fields {
		if !allowed[name] {
			return nil, fmt.Errorf("emitJSONLD value contains unsupported field %q", name)
		}
	}
	var context, kind, headline, pageURL string
	for name, target := range map[string]*string{"@context": &context, "@type": &kind, "headline": &headline, "url": &pageURL} {
		if data, ok := fields[name]; !ok || json.Unmarshal(data, target) != nil || *target == "" {
			return nil, fmt.Errorf("emitJSONLD value is missing canonical field %q", name)
		}
	}
	if context != "https://schema.org" || kind != "Article" {
		return nil, fmt.Errorf("emitJSONLD value is not a canonical Article JSON object")
	}
	for _, name := range []string{"description", "image", "version", "isPartOf", "datePublished", "dateModified", "lastReviewed", "creativeWorkStatus"} {
		if data, ok := fields[name]; ok {
			var text string
			if json.Unmarshal(data, &text) != nil {
				return nil, fmt.Errorf("emitJSONLD field %q must be a string", name)
			}
		}
	}
	if data, ok := fields["keywords"]; ok {
		var keywords []string
		if json.Unmarshal(data, &keywords) != nil {
			return nil, fmt.Errorf("emitJSONLD field %q must be an array of strings", "keywords")
		}
	}
	for name, wantType := range map[string]string{"author": "Person", "audience": "Audience"} {
		if data, ok := fields[name]; ok {
			var object map[string]string
			if json.Unmarshal(data, &object) != nil || object["@type"] != wantType {
				return nil, fmt.Errorf("emitJSONLD field %q is not canonical", name)
			}
			if name == "author" && object["name"] == "" || name == "audience" && object["audienceType"] == "" {
				return nil, fmt.Errorf("emitJSONLD field %q is not canonical", name)
			}
		}
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return nil, fmt.Errorf("emitJSONLD value is not valid JSON")
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(canonical, []byte(value)) {
		return nil, fmt.Errorf("emitJSONLD value is not canonical JSON")
	}
	return canonical, nil
}

func (r *Renderer) Render(kind string, page viewmodel.Page) ([]byte, error) {
	if r == nil || r.templates == nil {
		return nil, fmt.Errorf("theme renderer is not compiled")
	}
	if !contains(requiredLayouts[:], kind) {
		return nil, fmt.Errorf("unknown theme page kind %q", kind)
	}
	name := "layouts/" + kind + ".html"
	if r.templates.Lookup(kind) != nil {
		name = kind
	}
	var output bytes.Buffer
	if err := r.templates.ExecuteTemplate(&output, name, page); err != nil {
		return nil, fmt.Errorf("render %s: %w", name, err)
	}
	return output.Bytes(), nil
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// Manifest returns a deterministic inventory and content hash.
func ManifestOf(source Source) Manifest {
	files := make([]File, 0, len(source.Files))
	hash := sha256.New()
	_, _ = hash.Write([]byte(fmt.Sprintf("apiVersion:%d\nname:%s\n", source.APIVersion, source.Name)))
	names := make([]string, 0, len(source.Files))
	for name := range source.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data := source.Files[name]
		digest := sha256.Sum256(data)
		kind := "text"
		if !isTextPath(name) {
			kind = "binary"
		}
		files = append(files, File{Path: name, Kind: kind, Hash: fmt.Sprintf("%x", digest), Size: len(data)})
		_, _ = hash.Write([]byte(name))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return Manifest{APIVersion: source.APIVersion, Name: source.Name, Hash: fmt.Sprintf("%x", hash.Sum(nil)), Files: files}
}

func fileKind(name string) (string, error) {
	base := path.Base(name)
	ext := strings.ToLower(path.Ext(name))
	for _, candidate := range []string{".html", ".css", ".js", ".yaml", ".yml", ".md", ".txt", ".json", ".svg"} {
		if ext == candidate {
			return "text", nil
		}
	}
	if base == "README" || base == "LICENSE" {
		return "text", nil
	}
	for _, candidate := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".woff", ".woff2", ".ttf", ".otf"} {
		if ext == candidate {
			return "binary", nil
		}
	}
	return "", fmt.Errorf("theme file %q has an unsupported extension", name)
}

func isTextPath(name string) bool {
	kind, err := fileKind(name)
	return err == nil && kind == "text"
}

// InitFiles returns the deterministic editable baseline and rewrites its name.
func InitFiles(name string) (map[string][]byte, error) {
	if !validID(name) || name == BaseID {
		return nil, fmt.Errorf("theme name %q is invalid", name)
	}
	files, err := BaseFiles()
	if err != nil {
		return nil, err
	}
	files["theme.yaml"] = []byte(fmt.Sprintf("apiVersion: 1\nname: %s\n", strconv.Quote(name)))
	return files, nil
}

// Files returns a deterministic public inventory.
func Files(source Source) []File { return ManifestOf(source).Files }

// FileKind validates and classifies a theme-domain file by its contract.
func FileKind(name string) (string, error) { return fileKind(name) }
