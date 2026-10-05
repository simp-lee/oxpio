package edit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	internalbuild "github.com/simp-lee/oxpio/internal/build"
	internalserver "github.com/simp-lee/oxpio/internal/server"
	xhtml "golang.org/x/net/html"
)

const previewLifetime = 30 * time.Minute

type editorPreview struct {
	static      *internalserver.Server
	root        string
	contentHTML []byte
	expires     time.Time
}

type contentPreviewPage struct {
	Language    string
	Title       string
	CSSURL      string
	BaseURL     string
	RuntimeURL  string
	KatexCSSURL string
	Math        bool
	Mermaid     bool
	Content     template.HTML
}

var contentPreviewTemplate = template.Must(template.ParseFS(editorWeb, "web/content-preview.html"))

func (s *Server) servePreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := s.sessionForRequest(r); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	controlPath := strings.TrimPrefix(r.URL.Path, controlPrefix)
	if controlPath == "preview" && r.Method == http.MethodPost {
		s.createPreview(w, r)
		return
	}
	if !strings.HasPrefix(controlPath, "preview/") {
		http.NotFound(w, r)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(controlPath, "preview/"), "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	token := parts[0]
	preview := s.lookupPreview(token)
	if preview == nil || preview.static == nil {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "content" {
		servePreviewBytes(w, r, preview.contentHTML, "text/html; charset=utf-8")
		return
	}
	if len(parts) == 2 && parts[1] == "content.css" {
		data, err := editorWeb.ReadFile("web/content-preview.css")
		if err != nil {
			http.Error(w, "preview stylesheet unavailable", http.StatusInternalServerError)
			return
		}
		servePreviewBytes(w, r, data, "text/css; charset=utf-8")
		return
	}
	relPath := "/"
	if len(parts) == 2 && parts[1] != "" {
		relPath = "/" + parts[1]
	}
	clone := r.Clone(r.Context())
	clone.URL.Path = relPath
	clone.URL.RawPath = ""
	recorder := httptest.NewRecorder()
	preview.static.ServeHTTP(recorder, clone)
	body := append([]byte(nil), recorder.Body.Bytes()...)
	contentType := strings.ToLower(recorder.Header().Get("Content-Type"))
	if strings.Contains(contentType, "text/html") && recorder.Code >= 200 && recorder.Code < 300 {
		body = rewritePreviewHTML(body, "/_oxpio/preview/"+token)
	}
	for name, values := range recorder.Header() {
		w.Header()[name] = append([]string(nil), values...)
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodHead {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	}
	w.WriteHeader(recorder.Code)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func servePreviewBytes(w http.ResponseWriter, r *http.Request, data []byte, contentType string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func renderPreviewPage(static *internalserver.Server, requestPath string) ([]byte, error) {
	if static == nil {
		return nil, fmt.Errorf("preview server is nil")
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, requestPath, nil)
	static.ServeHTTP(recorder, request)
	if recorder.Code < http.StatusOK || recorder.Code >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("preview page returned HTTP %d", recorder.Code)
	}
	if !strings.Contains(strings.ToLower(recorder.Header().Get("Content-Type")), "text/html") {
		return nil, fmt.Errorf("preview page returned %q", recorder.Header().Get("Content-Type"))
	}
	return append([]byte(nil), recorder.Body.Bytes()...), nil
}

func previewSitePath(basePath, route string) string {
	base := strings.Trim(basePath, "/")
	relative := strings.Trim(route, "/")
	requestPath := "/"
	if base != "" || relative != "" {
		requestPath = "/" + strings.Trim(strings.Join([]string{base, relative}, "/"), "/")
	}
	if strings.HasSuffix(route, "/") && requestPath != "/" {
		requestPath += "/"
	}
	return requestPath
}

func renderContentPreview(fullPage []byte, token, title, pagePath string) ([]byte, error) {
	document, err := xhtml.Parse(bytes.NewReader(fullPage))
	if err != nil {
		return nil, fmt.Errorf("parse rendered page: %w", err)
	}
	contentNode := findPreviewNodeWithClass(document, "entry-content")
	if contentNode == nil {
		return nil, fmt.Errorf("rendered page has no entry content")
	}
	var content bytes.Buffer
	for child := contentNode.FirstChild; child != nil; child = child.NextSibling {
		if err := xhtml.Render(&content, child); err != nil {
			return nil, fmt.Errorf("render entry content: %w", err)
		}
	}
	contentHTML := rewritePreviewHTML(content.Bytes(), "/_oxpio/preview/"+token)
	math := strings.Contains(string(contentHTML), "data-oxpio-math-source")
	mermaid := findPreviewNodeWithClass(contentNode, "mermaid") != nil
	prefix := "/_oxpio/preview/" + token
	page := contentPreviewPage{
		Language: languageFromPreviewDocument(document),
		Title:    title,
		CSSURL:   prefix + "/content.css",
		BaseURL:  prefix + pagePath,
		Math:     math,
		Mermaid:  mermaid,
		Content:  template.HTML(contentHTML),
	}
	if page.Language == "" {
		page.Language = "en"
	}
	if math || mermaid {
		page.RuntimeURL = previewResourceURL(prefix, previewDocumentResource(document, "runtime"))
	}
	if math {
		page.KatexCSSURL = previewResourceURL(prefix, previewDocumentResource(document, "katex"))
	}
	var output bytes.Buffer
	if err := contentPreviewTemplate.Execute(&output, page); err != nil {
		return nil, fmt.Errorf("execute content preview template: %w", err)
	}
	return output.Bytes(), nil
}

func findPreviewNodeWithClass(root *xhtml.Node, className string) *xhtml.Node {
	if root == nil {
		return nil
	}
	if previewNodeHasClass(root, className) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := findPreviewNodeWithClass(child, className); found != nil {
			return found
		}
	}
	return nil
}

func previewNodeHasClass(node *xhtml.Node, className string) bool {
	classes := previewNodeAttribute(node, "class")
	for _, value := range strings.Fields(classes) {
		if value == className {
			return true
		}
	}
	return false
}

func previewNodeAttribute(node *xhtml.Node, name string) string {
	if node == nil {
		return ""
	}
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, name) {
			return attribute.Val
		}
	}
	return ""
}

func languageFromPreviewDocument(document *xhtml.Node) string {
	htmlNode := findPreviewElement(document, "html")
	return previewNodeAttribute(htmlNode, "lang")
}

func previewDocumentResource(document *xhtml.Node, kind string) string {
	var resource string
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if node == nil || resource != "" {
			return
		}
		switch strings.ToLower(node.Data) {
		case "script":
			if kind == "runtime" {
				candidate := previewNodeAttribute(node, "src")
				if strings.Contains(candidate, "/assets/oxpio/runtime.") {
					resource = candidate
				}
			}
		case "link":
			if kind == "katex" && strings.Contains(strings.ToLower(previewNodeAttribute(node, "href")), "katex.min.css") {
				resource = previewNodeAttribute(node, "href")
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	return resource
}

func findPreviewElement(root *xhtml.Node, elementName string) *xhtml.Node {
	if root == nil {
		return nil
	}
	if strings.EqualFold(root.Data, elementName) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := findPreviewElement(child, elementName); found != nil {
			return found
		}
	}
	return nil
}

func previewResourceURL(prefix, resource string) string {
	if resource == "" || strings.HasPrefix(resource, "//") || !strings.HasPrefix(resource, "/") || strings.HasPrefix(resource, prefix) {
		return resource
	}
	return prefix + resource
}

func (s *Server) createPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeMutation(w, r) {
		return
	}
	data, err := readSourceBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	var request struct {
		Path   string `json:"path"`
		Source string `json:"source"`
	}
	if err := jsonUnmarshal(data, &request); err != nil {
		http.Error(w, "invalid preview request", http.StatusBadRequest)
		return
	}
	entry := s.catalogEntryByRelPath(request.Path)
	if entry == nil || (entry.Kind != "article" && entry.Kind != "section") {
		http.NotFound(w, r)
		return
	}
	overlay, err := s.previewOverlay(entry.RelPath, []byte(request.Source))
	if err != nil {
		writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	root, err := os.MkdirTemp("", ".oxpio-preview-*")
	if err != nil {
		http.Error(w, "could not create preview workspace", http.StatusInternalServerError)
		return
	}
	stageOutput := filepath.Join(root, "public")
	built, buildErr := internalbuild.BuildWithOptions(s.vault, stageOutput, internalbuild.Options{
		SourceOverlay:    overlay,
		SourceOutputPath: s.output,
	})
	if buildErr != nil {
		_ = os.RemoveAll(root)
		if built != nil {
			writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]any{"error": s.editorDiagnosticText(buildErr.Error()), "diagnostics": s.editorDiagnostics(built.Diagnostics)})
		} else {
			http.Error(w, buildErr.Error(), http.StatusUnprocessableEntity)
		}
		return
	}
	previewEntry := catalogEntry(built.Catalog, entry.RelPath)
	if previewEntry == nil || previewEntry.Route == "" {
		_ = os.RemoveAll(root)
		writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]string{"error": "source has no preview route; publish its parent section or repair its frontmatter"})
		return
	}
	static, err := internalserver.New(stageOutput, 0)
	if err != nil {
		_ = os.RemoveAll(root)
		http.Error(w, "could not create preview server", http.StatusInternalServerError)
		return
	}
	token, err := newToken()
	if err != nil {
		_ = os.RemoveAll(root)
		http.Error(w, "could not create preview token", http.StatusInternalServerError)
		return
	}
	basePath := ""
	if built.Catalog != nil {
		basePath = strings.TrimSuffix(built.Catalog.BasePath, "/")
	}
	fullURL := "/_oxpio/preview/" + token + basePath + previewEntry.Route
	fullPage, err := renderPreviewPage(static, previewSitePath(basePath, previewEntry.Route))
	if err != nil {
		_ = os.RemoveAll(root)
		http.Error(w, "could not render preview page", http.StatusInternalServerError)
		return
	}
	contentHTML, err := renderContentPreview(fullPage, token, previewEntry.Title, previewSitePath(basePath, previewEntry.Route))
	if err != nil {
		_ = os.RemoveAll(root)
		http.Error(w, "could not render content preview", http.StatusInternalServerError)
		return
	}
	s.previewMu.Lock()
	s.cleanupPreviewsLocked(time.Now())
	s.previews[token] = &editorPreview{static: static, root: root, contentHTML: contentHTML, expires: time.Now().Add(previewLifetime)}
	s.previewMu.Unlock()
	response := map[string]any{
		"url":          fullURL,
		"fullURL":      fullURL,
		"contentURL":   "/_oxpio/preview/" + token + "/content",
		"warningCount": built.WarningCount,
		"diagnostics":  s.editorDiagnostics(built.Diagnostics),
	}
	writeJSON(w, response)
}

func (s *Server) previewOverlay(relPath string, source []byte) (map[string][]byte, error) {
	result := map[string][]byte{}
	forcePublish := func(pathValue string, content []byte) error {
		updated, err := forceEditorPublish(content)
		if err != nil {
			return fmt.Errorf("preview %q: %w", pathValue, err)
		}
		result[pathValue] = updated
		return nil
	}
	if err := forcePublish(relPath, source); err != nil {
		return nil, err
	}
	directory := path.Dir(strings.ReplaceAll(relPath, "\\", "/"))
	for {
		sectionPath := path.Join(directory, "_index.md")
		if data, exists, err := readSource(s.vault, sectionPath); err == nil && exists {
			if err := forcePublish(sectionPath, data); err != nil {
				return nil, err
			}
		}
		if directory == "." {
			break
		}
		directory = path.Dir(directory)
	}
	return result, nil
}

func forceEditorPublish(content []byte) ([]byte, error) {
	document, err := parseEditorDocument(content)
	if err != nil {
		return nil, err
	}
	if !document.HasBlock {
		return nil, fmt.Errorf("frontmatter is required")
	}
	block, err := replaceEditorField(content[document.BodyStart:document.CloseStart], "publish", editorYAMLField("publish", true))
	if err != nil {
		return nil, err
	}
	return append(append(append([]byte(nil), content[:document.BodyStart]...), block...), content[document.CloseStart:]...), nil
}

func (s *Server) lookupPreview(token string) *editorPreview {
	now := time.Now()
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	s.cleanupPreviewsLocked(now)
	return s.previews[token]
}

func (s *Server) cleanupPreviewsLocked(now time.Time) {
	for token, preview := range s.previews {
		if preview == nil || now.After(preview.expires) {
			if preview != nil {
				_ = os.RemoveAll(preview.root)
			}
			delete(s.previews, token)
		}
	}
}

var (
	previewURLAttributePattern    = regexp.MustCompile(`(?i)(\b(?:href|src|action|poster|data-src)\s*=\s*)(["'])(/[^"']*)`)
	previewSrcsetAttributePattern = regexp.MustCompile(`(?i)(\bsrcset\s*=\s*)(["'])(/[^"']*)`)
)

func rewritePreviewHTML(body []byte, prefix string) []byte {
	text := previewURLAttributePattern.ReplaceAllStringFunc(string(body), func(match string) string {
		parts := previewURLAttributePattern.FindStringSubmatch(match)
		if len(parts) != 4 || strings.HasPrefix(parts[3], "//") || strings.HasPrefix(parts[3], prefix) {
			return match
		}
		return parts[1] + parts[2] + prefix + parts[3]
	})
	return []byte(previewSrcsetAttributePattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := previewSrcsetAttributePattern.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		candidates := strings.Split(parts[3], ",")
		for index, candidate := range candidates {
			fields := strings.Fields(candidate)
			if len(fields) == 0 || strings.HasPrefix(fields[0], "//") || !strings.HasPrefix(fields[0], "/") || strings.HasPrefix(fields[0], prefix) {
				continue
			}
			fields[0] = prefix + fields[0]
			candidates[index] = strings.Join(fields, " ")
		}
		return parts[1] + parts[2] + strings.Join(candidates, ",")
	}))
}

// jsonUnmarshal is kept local to the edit package so preview requests share
// the same bounded source-body handling as source mutations.
func jsonUnmarshal(data []byte, value any) error {
	return json.Unmarshal(data, value)
}
