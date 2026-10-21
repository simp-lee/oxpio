package edit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	internalapplication "github.com/simp-lee/oxpio/internal/application"
	internalserver "github.com/simp-lee/oxpio/internal/server"
	xhtml "golang.org/x/net/html"
)

const (
	previewLifetime           = 30 * time.Minute
	previewContentStartMarker = "oxpio-content-start-7c4f7e1b6d2a"
	previewContentEndMarker   = "oxpio-content-end-7c4f7e1b6d2a"
)

type editorPreview struct {
	static      *internalserver.Server
	root        string
	contentHTML []byte
	expires     time.Time
}

func (s *Server) servePreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := s.sessionForRequest(r); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	requestPath := r.URL.EscapedPath()
	if requestPath == "" {
		requestPath = r.URL.Path
	}
	controlPath := strings.TrimPrefix(requestPath, s.controlPrefix())
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
	if len(parts) == 1 && r.Method == http.MethodDelete {
		if !s.authorizeMutation(w, r) {
			return
		}
		if err := s.discardPublishedPreview(token); err != nil {
			http.Error(w, fmt.Sprintf("preview cleanup failed: %v", err), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	preview, cleanupErr := s.lookupPreview(token)
	if cleanupErr != nil {
		http.Error(w, fmt.Sprintf("preview cleanup failed: %v", cleanupErr), http.StatusInternalServerError)
		return
	}
	if preview == nil || preview.static == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(parts) == 2 && parts[1] == "content" {
		servePreviewBytes(w, r, preview.contentHTML, "text/html; charset=utf-8")
		return
	}
	escapedRelPath := "/"
	if len(parts) == 2 && parts[1] != "" {
		escapedRelPath = "/" + parts[1]
	}
	relPath, err := url.PathUnescape(escapedRelPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	clone := r.Clone(r.Context())
	if clone.Method == http.MethodHead {
		clone.Method = http.MethodGet
	}
	clone.URL.Path = relPath
	clone.URL.RawPath = escapedRelPath
	recorder := httptest.NewRecorder()
	preview.static.ServeHTTP(recorder, clone)
	body := append([]byte(nil), recorder.Body.Bytes()...)
	contentType := strings.ToLower(recorder.Header().Get("Content-Type"))
	prefix := s.controlPrefix() + "preview/" + token
	rewritten := false
	if recorder.Code >= 200 && recorder.Code < 300 && recorder.Code != http.StatusPartialContent {
		switch {
		case strings.Contains(contentType, "text/html"):
			body = rewritePreviewHTMLAtWithBase(body, prefix, previewDocumentBasePath(escapedRelPath), s.baseURL)
			rewritten = true
		case strings.Contains(contentType, "text/css"):
			body = rewritePreviewCSSURLs(body, prefix, s.baseURL)
			rewritten = true
		}
	}
	for name, values := range recorder.Header() {
		w.Header()[name] = append([]string(nil), values...)
	}
	if rewritten {
		w.Header().Del("Accept-Ranges")
		w.Header().Del("Content-Range")
	}
	if location := recorder.Header().Get("Location"); location != "" {
		if rewritten, ok := rewritePreviewLocation(location, prefix, s.baseURL); ok {
			w.Header().Set("Location", rewritten)
		} else {
			w.Header().Del("Location")
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
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

func renderContentPreviewHTML(fullPage []byte) ([]byte, error) {
	document, err := xhtml.Parse(bytes.NewReader(fullPage))
	if err != nil {
		return nil, err
	}
	content := findPreviewElement(document, func(node *xhtml.Node) bool {
		for _, attribute := range node.Attr {
			if attribute.Key == "data-page-content" {
				return true
			}
		}
		return false
	})
	contentStart, contentEnd := (*xhtml.Node)(nil), (*xhtml.Node)(nil)
	if content == nil {
		contentStart = findPreviewElement(document, func(node *xhtml.Node) bool {
			return node.Type == xhtml.CommentNode && node.Data == previewContentStartMarker
		})
		if contentStart != nil {
			contentEnd = findPreviewSibling(contentStart, func(node *xhtml.Node) bool {
				return node.Type == xhtml.CommentNode && node.Data == previewContentEndMarker
			})
		}
		if contentStart == nil || contentEnd == nil || contentStart.Parent != contentEnd.Parent || !previewNodeFollows(contentStart, contentEnd) {
			return nil, errors.New("content preview marker not found")
		}
	}
	contentRoot := content
	if contentRoot == nil {
		contentRoot = contentStart
	}
	for parent := contentRoot.Parent; parent != nil; parent = parent.Parent {
		if parent.Type == xhtml.ElementNode && (parent.Data == "article" || parent.Data == "section") {
			contentRoot = parent
			break
		}
	}
	if content == nil && contentRoot == contentStart {
		contentRoot = &xhtml.Node{Type: xhtml.ElementNode, Data: "div", Attr: []xhtml.Attribute{{Key: "data-page-content", Val: ""}}}
		for child := contentStart.NextSibling; child != nil && child != contentEnd; child = child.NextSibling {
			contentRoot.AppendChild(clonePreviewNode(child))
		}
	}
	htmlRoot := findPreviewElement(document, func(node *xhtml.Node) bool { return node.Type == xhtml.ElementNode && node.Data == "html" })
	headRoot := findPreviewElement(document, func(node *xhtml.Node) bool { return node.Type == xhtml.ElementNode && node.Data == "head" })
	bodyRoot := findPreviewElement(document, func(node *xhtml.Node) bool { return node.Type == xhtml.ElementNode && node.Data == "body" })
	if htmlRoot == nil || headRoot == nil || bodyRoot == nil {
		return nil, errors.New("content preview document is incomplete")
	}

	previewDocument := &xhtml.Node{Type: xhtml.DocumentNode}
	previewDocument.AppendChild(&xhtml.Node{Type: xhtml.DoctypeNode, Data: "html"})
	previewHTML := &xhtml.Node{Type: xhtml.ElementNode, Data: "html", Attr: append([]xhtml.Attribute(nil), htmlRoot.Attr...)}
	removePreviewRuntimeMarker(previewHTML, "data-oxpio-sidebar")
	removePreviewRuntimeMarker(previewHTML, "data-oxpio-popover")
	previewDocument.AppendChild(previewHTML)
	previewHead := &xhtml.Node{Type: xhtml.ElementNode, Data: "head", Attr: append([]xhtml.Attribute(nil), headRoot.Attr...)}
	for child := headRoot.FirstChild; child != nil; child = child.NextSibling {
		previewHead.AppendChild(clonePreviewNode(child))
	}
	previewHTML.AppendChild(previewHead)
	previewBody := &xhtml.Node{Type: xhtml.ElementNode, Data: "body", Attr: append([]xhtml.Attribute(nil), bodyRoot.Attr...)}
	previewBody.Attr = append(previewBody.Attr, xhtml.Attribute{Key: "data-oxpio-content-preview", Val: ""})
	previewMain := &xhtml.Node{Type: xhtml.ElementNode, Data: "main", Attr: []xhtml.Attribute{{Key: "class", Val: "site-main content-preview-main"}}}
	previewContent := &xhtml.Node{Type: xhtml.ElementNode, Data: "div", Attr: []xhtml.Attribute{{Key: "class", Val: "site-content"}}}
	contentClone := clonePreviewNode(contentRoot)
	if content == nil {
		wrapPreviewContentMarkers(contentClone)
	}
	previewContent.AppendChild(contentClone)
	previewMain.AppendChild(previewContent)
	previewBody.AppendChild(previewMain)
	appendPreviewScripts(previewBody, bodyRoot, contentRoot, contentStart, contentEnd)
	previewHTML.AppendChild(previewBody)

	var rendered bytes.Buffer
	if err := xhtml.Render(&rendered, previewDocument); err != nil {
		return nil, err
	}
	return rendered.Bytes(), nil
}

func removePreviewRuntimeMarker(node *xhtml.Node, key string) {
	if node == nil {
		return
	}
	for index := len(node.Attr) - 1; index >= 0; index-- {
		if node.Attr[index].Key == key {
			node.Attr = append(node.Attr[:index], node.Attr[index+1:]...)
		}
	}
}

func previewNodeFollows(start, end *xhtml.Node) bool {
	for node := start.NextSibling; node != nil; node = node.NextSibling {
		if node == end {
			return true
		}
	}
	return false
}

func wrapPreviewContentMarkers(root *xhtml.Node) {
	start := findPreviewElement(root, func(node *xhtml.Node) bool {
		return node.Type == xhtml.CommentNode && node.Data == previewContentStartMarker
	})
	end := findPreviewSibling(start, func(node *xhtml.Node) bool {
		return node.Type == xhtml.CommentNode && node.Data == previewContentEndMarker
	})
	if start == nil || end == nil || start.Parent != end.Parent || !previewNodeFollows(start, end) {
		return
	}
	parent := start.Parent
	first := start.NextSibling
	parent.RemoveChild(start)
	wrapper := &xhtml.Node{Type: xhtml.ElementNode, Data: "div", Attr: []xhtml.Attribute{{Key: "data-page-content", Val: ""}}}
	for child := first; child != nil && child != end; {
		next := child.NextSibling
		parent.RemoveChild(child)
		wrapper.AppendChild(child)
		child = next
	}
	parent.InsertBefore(wrapper, end)
	parent.RemoveChild(end)
}

func findPreviewSibling(start *xhtml.Node, match func(*xhtml.Node) bool) *xhtml.Node {
	if start == nil {
		return nil
	}
	for node := start.NextSibling; node != nil; node = node.NextSibling {
		if match(node) {
			return node
		}
	}
	return nil
}

func findPreviewElement(root *xhtml.Node, match func(*xhtml.Node) bool) *xhtml.Node {
	if root == nil {
		return nil
	}
	if match(root) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := findPreviewElement(child, match); found != nil {
			return found
		}
	}
	return nil
}

func clonePreviewNode(node *xhtml.Node) *xhtml.Node {
	clone := &xhtml.Node{Type: node.Type, DataAtom: node.DataAtom, Namespace: node.Namespace, Data: node.Data, Attr: append([]xhtml.Attribute(nil), node.Attr...)}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		clone.AppendChild(clonePreviewNode(child))
	}
	return clone
}

func appendPreviewScripts(parent, original, excluded, contentStart, contentEnd *xhtml.Node) {
	if original == nil || original == excluded || previewNodeWithinRange(original, contentStart, contentEnd) {
		return
	}
	if original.Type == xhtml.ElementNode && original.Data == "script" {
		parent.AppendChild(clonePreviewNode(original))
		return
	}
	for child := original.FirstChild; child != nil; child = child.NextSibling {
		appendPreviewScripts(parent, child, excluded, contentStart, contentEnd)
	}
}

func previewNodeWithinRange(node, start, end *xhtml.Node) bool {
	if node == nil || start == nil || end == nil || start.Parent == nil || start.Parent != end.Parent {
		return false
	}
	parent := start.Parent
	direct := node
	for direct.Parent != nil && direct.Parent != parent {
		direct = direct.Parent
	}
	if direct.Parent != parent {
		return false
	}
	for child := start.NextSibling; child != nil && child != end; child = child.NextSibling {
		if child == direct {
			return true
		}
	}
	return false
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
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil || len(fields) != 2 {
		http.Error(w, "invalid preview request", http.StatusBadRequest)
		return
	}
	for _, name := range []string{"path", "source"} {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) || len(bytes.TrimSpace(value)) == 0 || bytes.TrimSpace(value)[0] != '"' {
			http.Error(w, "invalid preview request", http.StatusBadRequest)
			return
		}
	}
	previewPath, err := internalapplication.NormalizeSourcePath(request.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entry := s.catalogEntryByRelPath(previewPath)
	if entry == nil {
		if !strings.EqualFold(path.Ext(previewPath), ".md") || strings.EqualFold(path.Base(previewPath), "_index.md") {
			http.NotFound(w, r)
			return
		}
		parent := s.catalogEntryByRelPath(path.Join(path.Dir(previewPath), "_index.md"))
		if parent == nil || parent.Kind != "section" {
			http.NotFound(w, r)
			return
		}
	} else if entry.Kind != "article" && entry.Kind != "section" {
		http.NotFound(w, r)
		return
	}
	overlay, err := s.previewOverlay(previewPath, []byte(request.Source))
	if err != nil {
		writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	root, err := os.MkdirTemp("", ".oxpio-preview-*")
	if err != nil {
		http.Error(w, "could not create preview workspace", http.StatusInternalServerError)
		return
	}
	if cleanupErr := s.trackPreviewRoot(root); cleanupErr != nil {
		http.Error(w, previewFailureMessage("editor is closed", cleanupErr), http.StatusServiceUnavailable)
		return
	}
	stageOutput := filepath.Join(root, "public")
	built, buildErr := internalapplication.BuildCandidate(s.vault, stageOutput, internalapplication.CandidateBuildOptions{
		AllowWarnings:     true,
		SourceOverlay:     overlay,
		SourceOutputPath:  s.output,
		PreviewSourcePath: previewPath,
	})
	if buildErr != nil {
		cleanupErr := s.discardUnpublishedPreview(nil, root)
		if built != nil {
			message := previewFailureMessage(s.editorDiagnosticText(buildErr.Error()), cleanupErr)
			writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]any{"error": message, "diagnostics": s.editorDiagnostics(built.Diagnostics)})
		} else {
			http.Error(w, previewFailureMessage(buildErr.Error(), cleanupErr), http.StatusUnprocessableEntity)
		}
		return
	}
	if r.Context().Err() != nil {
		_ = s.discardUnpublishedPreview(nil, root)
		return
	}
	previewCatalog := internalapplication.NewCatalog(built.Catalog)
	previewEntry, previewEntryErr := previewCatalog.EntryByRelPath(previewPath, true)
	if previewEntryErr != nil || previewEntry.Route == "" {
		cleanupErr := s.discardUnpublishedPreview(nil, root)
		writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]string{"error": previewFailureMessage("source has no preview route; publish its parent section or repair its frontmatter", cleanupErr)})
		return
	}
	if r.Context().Err() != nil {
		_ = s.discardUnpublishedPreview(nil, root)
		return
	}
	s.previewMu.Lock()
	if s.closed {
		s.previewMu.Unlock()
		cleanupErr := s.discardUnpublishedPreview(nil, root)
		http.Error(w, previewFailureMessage("editor is closed", cleanupErr), http.StatusServiceUnavailable)
		return
	}
	static, err := internalserver.New(stageOutput, 0)
	if err == nil {
		s.previewRoots[root] = static
	}
	s.previewMu.Unlock()
	if err != nil {
		cleanupErr := s.discardUnpublishedPreview(nil, root)
		http.Error(w, previewFailureMessage("could not create preview server", cleanupErr), http.StatusInternalServerError)
		return
	}
	token, err := s.newToken()
	if err != nil {
		cleanupErr := s.discardUnpublishedPreview(static, root)
		http.Error(w, previewFailureMessage("could not create preview token", cleanupErr), http.StatusInternalServerError)
		return
	}
	basePath := ""
	if built.Catalog != nil {
		basePath = strings.TrimSuffix(built.Catalog.BasePath, "/")
	}
	fullURL := s.controlPrefix() + "preview/" + token + basePath + previewEntry.Route
	fullPage, err := renderPreviewPage(static, previewSitePath(basePath, previewEntry.Route))
	if err != nil {
		cleanupErr := s.discardUnpublishedPreview(static, root)
		http.Error(w, previewFailureMessage("could not render preview page", cleanupErr), http.StatusInternalServerError)
		return
	}
	contentPage, err := renderContentPreviewHTML(fullPage)
	if err != nil {
		cleanupErr := s.discardUnpublishedPreview(static, root)
		http.Error(w, previewFailureMessage("could not render content preview", cleanupErr), http.StatusInternalServerError)
		return
	}
	contentHTML := rewritePreviewHTMLAtWithBase(contentPage, s.controlPrefix()+"preview/"+token, previewSitePath(basePath, previewEntry.Route), s.baseURL)
	if r.Context().Err() != nil {
		_ = s.discardUnpublishedPreview(static, root)
		return
	}
	s.previewMu.Lock()
	if r.Context().Err() != nil {
		s.previewMu.Unlock()
		_ = s.discardUnpublishedPreview(static, root)
		return
	}
	if s.closed {
		s.previewMu.Unlock()
		cleanupErr := s.discardUnpublishedPreview(static, root)
		http.Error(w, previewFailureMessage("editor is closed", cleanupErr), http.StatusServiceUnavailable)
		return
	}
	now := s.now()
	if err := s.cleanupPreviewsLocked(now); err != nil {
		s.previewMu.Unlock()
		cleanupErr := errors.Join(err, s.discardUnpublishedPreview(static, root))
		http.Error(w, previewFailureMessage("could not clean up expired preview", cleanupErr), http.StatusInternalServerError)
		return
	}
	if r.Context().Err() != nil {
		s.previewMu.Unlock()
		_ = s.discardUnpublishedPreview(static, root)
		return
	}
	delete(s.previewRoots, root)
	s.previews[token] = &editorPreview{static: static, root: root, contentHTML: contentHTML, expires: now.Add(previewLifetime)}
	if r.Context().Err() != nil {
		s.previewMu.Unlock()
		_ = s.discardPublishedPreview(token)
		return
	}
	s.previewMu.Unlock()
	response := map[string]any{
		"url":          fullURL,
		"fullURL":      fullURL,
		"contentURL":   s.controlPrefix() + "preview/" + token + "/content",
		"warningCount": built.WarningCount,
		"diagnostics":  s.editorDiagnostics(built.Diagnostics),
	}
	writeJSON(w, response)
	if oldToken := strings.TrimSpace(r.Header.Get("X-OXPIO-Preview-Token")); oldToken != "" && oldToken != token {
		_ = s.discardPublishedPreview(oldToken)
	}
	if r.Context().Err() != nil {
		_ = s.discardPublishedPreview(token)
	}
}

func (s *Server) discardPublishedPreview(token string) error {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	preview := s.previews[token]
	if preview == nil {
		return nil
	}
	if err := discardPreview(preview.static, preview.root); err != nil {
		return err
	}
	delete(s.previews, token)
	return nil
}

func (s *Server) previewOverlay(relPath string, source []byte) (map[string][]byte, error) {
	result := map[string][]byte{relPath: append([]byte(nil), source...)}
	directory := path.Dir(strings.ReplaceAll(relPath, "\\", "/"))
	for {
		sectionPath := path.Join(directory, "_index.md")
		if _, alreadyOverlaid := result[sectionPath]; !alreadyOverlaid {
			if entry := s.catalogEntryByRelPath(sectionPath); entry != nil && entry.Kind == "section" {
				result[sectionPath] = append([]byte(nil), entry.RawSource...)
			}
		}
		if directory == "." {
			break
		}
		directory = path.Dir(directory)
	}
	return result, nil
}

func (s *Server) trackPreviewRoot(root string) error {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	if s.closed {
		cleanupErr := discardPreview(nil, root)
		if cleanupErr != nil {
			if s.previewRoots == nil {
				s.previewRoots = make(map[string]*internalserver.Server)
			}
			s.previewRoots[root] = nil
		}
		return cleanupErr
	}
	if s.previewRoots == nil {
		s.previewRoots = make(map[string]*internalserver.Server)
	}
	s.previewRoots[root] = nil
	return nil
}

func (s *Server) discardUnpublishedPreview(static *internalserver.Server, root string) error {
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	cleanupErr := discardPreview(static, root)
	if cleanupErr == nil {
		delete(s.previewRoots, root)
	} else {
		if s.previewRoots == nil {
			s.previewRoots = make(map[string]*internalserver.Server)
		}
		s.previewRoots[root] = static
	}
	return cleanupErr
}

func (s *Server) lookupPreview(token string) (*editorPreview, error) {
	now := s.now()
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	if cleanupErr := s.cleanupPreviewsLocked(now); cleanupErr != nil {
		return nil, cleanupErr
	}
	return s.previews[token], nil
}

func (s *Server) cleanupPreviewsLocked(now time.Time) error {
	var cleanupErr error
	for token, preview := range s.previews {
		if preview == nil {
			delete(s.previews, token)
			continue
		}
		if now.After(preview.expires) {
			if err := discardPreview(preview.static, preview.root); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
				continue
			}
			delete(s.previews, token)
		}
	}
	return cleanupErr
}

func discardPreview(static *internalserver.Server, root string) error {
	var cleanupErr error
	if static != nil {
		cleanupErr = errors.Join(cleanupErr, static.Close())
	}
	if strings.TrimSpace(root) != "" {
		cleanupErr = errors.Join(cleanupErr, os.RemoveAll(root))
	}
	return cleanupErr
}

func previewFailureMessage(message string, cleanupErr error) string {
	if cleanupErr == nil {
		return message
	}
	return fmt.Sprintf("%s: cleanup preview: %v", message, cleanupErr)
}

var (
	previewAbsoluteURLAttributePattern            = regexp.MustCompile(`(?i)((?:xlink:href|href|src|action|poster|data|data-src)\s*=\s*)(["'])(https?://[^"']*)`)
	previewUnquotedAbsoluteURLAttributePattern    = regexp.MustCompile(`(?i)((?:xlink:href|href|src|action|poster|data|data-src)\s*=\s*)(https?://[^\s"'=<>` + "`" + `]+)`)
	previewAbsoluteSrcsetAttributePattern         = regexp.MustCompile(`(?i)((?:srcset|imagesrcset)\s*=\s*)(["'])([^"']*)`)
	previewUnquotedAbsoluteSrcsetAttributePattern = regexp.MustCompile(`(?i)((?:srcset|imagesrcset)\s*=\s*)([^\s"'=<>` + "`" + `]+)`)
	previewStyleDoubleAttributePattern            = regexp.MustCompile(`(?is)(style\s*=\s*")(.*?)"`)
	previewStyleSingleAttributePattern            = regexp.MustCompile(`(?is)(style\s*=\s*')(.*?)'`)
	previewStyleUnquotedAttributePattern          = regexp.MustCompile(`(?is)(style\s*=\s*)([^\s"'=<>` + "`" + `]+)`)
	previewURLAttributePattern                    = regexp.MustCompile(`(?i)((?:xlink:href|href|src|action|poster|data|data-src)\s*=\s*)(["'])(/[^"']*)`)
	previewUnquotedURLAttributePattern            = regexp.MustCompile(`(?i)((?:xlink:href|href|src|action|poster|data|data-src)\s*=\s*)(/[^\s"'=<>` + "`" + `]+)`)
	previewRelativeURLAttributePattern            = regexp.MustCompile(`(?i)((?:xlink:href|href|src|action|poster|data|data-src)\s*=\s*)(["'])([^"']*)`)
	previewUnquotedRelativeURLAttributePattern    = regexp.MustCompile(`(?i)((?:xlink:href|href|src|action|poster|data|data-src)\s*=\s*)([^\s"'=<>` + "`" + `]+)`)
	previewSrcsetAttributePattern                 = regexp.MustCompile(`(?i)((?:srcset|imagesrcset)\s*=\s*)(["'])(/[^"']*)`)
	previewUnquotedSrcsetAttributePattern         = regexp.MustCompile(`(?i)((?:srcset|imagesrcset)\s*=\s*)(/[^\s"'=<>` + "`" + `]+)`)
	previewRelativeSrcsetAttributePattern         = regexp.MustCompile(`(?i)((?:srcset|imagesrcset)\s*=\s*)(["'])([^"']*)`)
	previewUnquotedRelativeSrcsetAttributePattern = regexp.MustCompile(`(?i)((?:srcset|imagesrcset)\s*=\s*)([^\s"'=<>` + "`" + `]+)`)
)

func previewDocumentBasePath(relPath string) string {
	if strings.HasSuffix(relPath, "/") {
		return relPath
	}
	dir := path.Dir(relPath)
	if dir == "/" {
		return dir
	}
	return dir + "/"
}

func rewritePreviewHTMLAt(body []byte, prefix, pagePath string) []byte {
	return rewritePreviewHTMLAtWithBase(body, prefix, pagePath, "")
}

func rewritePreviewHTMLAtWithBase(body []byte, prefix, pagePath, configuredBaseURL string) []byte {
	text, restored := protectPreviewCSSContexts(string(body), prefix, configuredBaseURL, pagePath)
	text = rewritePreviewConfiguredURLs(text, prefix, configuredBaseURL)
	text = rewritePreviewHTMLTags(text, func(attrName, tag string) string {
		if !isPreviewURLAttributeName(attrName) {
			return tag
		}
		tag = previewRelativeURLAttributePattern.ReplaceAllStringFunc(tag, func(match string) string {
			parts := previewRelativeURLAttributePattern.FindStringSubmatch(match)
			if len(parts) != 4 {
				return match
			}
			return parts[1] + parts[2] + rewritePreviewRelativeURL(parts[3], prefix, pagePath)
		})
		tag = previewUnquotedRelativeURLAttributePattern.ReplaceAllStringFunc(tag, func(match string) string {
			parts := previewUnquotedRelativeURLAttributePattern.FindStringSubmatch(match)
			if len(parts) != 3 {
				return match
			}
			return parts[1] + rewritePreviewRelativeURL(parts[2], prefix, pagePath)
		})
		tag = previewRelativeSrcsetAttributePattern.ReplaceAllStringFunc(tag, func(match string) string {
			parts := previewRelativeSrcsetAttributePattern.FindStringSubmatch(match)
			if len(parts) != 4 {
				return match
			}
			return parts[1] + parts[2] + rewritePreviewRelativeSrcset(parts[3], prefix, pagePath)
		})
		return previewUnquotedRelativeSrcsetAttributePattern.ReplaceAllStringFunc(tag, func(match string) string {
			parts := previewUnquotedRelativeSrcsetAttributePattern.FindStringSubmatch(match)
			if len(parts) != 3 {
				return match
			}
			return parts[1] + rewritePreviewRelativeSrcset(parts[2], prefix, pagePath)
		})
	})
	text = string(rewritePreviewHTMLUnprotectedAt([]byte(text), prefix, configuredBaseURL))
	for marker, value := range restored {
		text = strings.ReplaceAll(text, marker, value)
	}
	return []byte(text)
}

func rewritePreviewHTMLTags(body string, rewrite func(string, string) string) string {
	var output strings.Builder
	output.Grow(len(body))
	for index := 0; index < len(body); {
		relativeStart := strings.IndexByte(body[index:], '<')
		if relativeStart < 0 {
			output.WriteString(body[index:])
			break
		}
		start := index + relativeStart
		output.WriteString(body[index:start])
		if strings.HasPrefix(body[start:], "<!--") {
			end := strings.Index(body[start+len("<!--"):], "-->")
			if end < 0 {
				output.WriteString(body[start:])
				break
			}
			end += start + len("<!--") + len("-->")
			output.WriteString(body[start:end])
			index = end
			continue
		}
		end := htmlTagEnd(body, start)
		if end < 0 {
			output.WriteString(body[start:])
			break
		}
		for _, rawName := range []string{"textarea", "title"} {
			if !htmlOpeningTagIs(body[start:end], rawName) {
				continue
			}
			closingRelative := indexFold(body[end:], "</"+rawName)
			if closingRelative < 0 {
				output.WriteString(body[start:])
				return output.String()
			}
			closingStart := end + closingRelative
			closingEnd := htmlTagEnd(body, closingStart)
			if closingEnd < 0 {
				output.WriteString(body[start:])
				return output.String()
			}
			output.WriteString(body[start:closingEnd])
			index = closingEnd
			goto next
		}
		output.WriteString(rewritePreviewHTMLTagAttributes(body[start:end], rewrite))
		index = end
		goto next
	next:
	}
	return output.String()
}

func rewritePreviewHTMLTagAttributes(tag string, rewrite func(string, string) string) string {
	if len(tag) < 3 || tag[0] != '<' || tag[1] == '/' {
		return tag
	}
	end := len(tag) - 1
	index := 1
	for index < end && tag[index] != '>' && tag[index] != ' ' && tag[index] != '\t' && tag[index] != '\r' && tag[index] != '\n' && tag[index] != '\f' {
		index++
	}
	var output strings.Builder
	output.Grow(len(tag))
	output.WriteString(tag[:index])
	for index < end {
		spaceStart := index
		for index < end && (tag[index] == ' ' || tag[index] == '\t' || tag[index] == '\r' || tag[index] == '\n' || tag[index] == '\f') {
			index++
		}
		output.WriteString(tag[spaceStart:index])
		if index >= end || tag[index] == '/' {
			output.WriteString(tag[index:])
			return output.String()
		}
		attributeStart := index
		for index < end && tag[index] != '=' && tag[index] != ' ' && tag[index] != '\t' && tag[index] != '\r' && tag[index] != '\n' && tag[index] != '\f' && tag[index] != '>' {
			index++
		}
		attributeNameEnd := index
		for index < end && (tag[index] == ' ' || tag[index] == '\t' || tag[index] == '\r' || tag[index] == '\n' || tag[index] == '\f') {
			index++
		}
		if index >= end || tag[index] != '=' {
			output.WriteString(tag[attributeStart:index])
			continue
		}
		index++
		for index < end && (tag[index] == ' ' || tag[index] == '\t' || tag[index] == '\r' || tag[index] == '\n' || tag[index] == '\f') {
			index++
		}
		if index < end && (tag[index] == '\'' || tag[index] == '"') {
			quote := tag[index]
			index++
			for index < end && tag[index] != quote {
				index++
			}
			if index < end {
				index++
			}
		} else {
			for index < end && tag[index] != ' ' && tag[index] != '\t' && tag[index] != '\r' && tag[index] != '\n' && tag[index] != '\f' && tag[index] != '>' {
				index++
			}
		}
		output.WriteString(rewrite(tag[attributeStart:attributeNameEnd], tag[attributeStart:index]))
	}
	output.WriteString(tag[end:])
	return output.String()
}

func isPreviewURLAttributeName(name string) bool {
	switch strings.ToLower(name) {
	case "xlink:href", "href", "src", "action", "poster", "data", "data-src", "srcset", "imagesrcset":
		return true
	default:
		return false
	}
}

func protectPreviewHTMLRawElements(body string, preserve, protect func(string) string) string {
	var output strings.Builder
	output.Grow(len(body))
	for index := 0; index < len(body); {
		relativeStart := strings.IndexByte(body[index:], '<')
		if relativeStart < 0 {
			output.WriteString(body[index:])
			break
		}
		start := index + relativeStart
		output.WriteString(body[index:start])
		if strings.HasPrefix(body[start:], "<!--") {
			end := strings.Index(body[start+len("<!--"):], "-->")
			if end < 0 {
				output.WriteString(body[start:])
				break
			}
			end += start + len("<!--") + len("-->")
			output.WriteString(body[start:end])
			index = end
			continue
		}
		openingEnd := htmlTagEnd(body, start)
		if openingEnd < 0 {
			output.WriteString(body[start:])
			break
		}
		name := ""
		for _, candidate := range []string{"textarea", "title", "script", "style"} {
			if htmlOpeningTagIs(body[start:openingEnd], candidate) {
				name = candidate
				break
			}
		}
		if name == "" {
			output.WriteString(body[start:openingEnd])
			index = openingEnd
			continue
		}
		closingRelative := indexFold(body[openingEnd:], "</"+name)
		if closingRelative < 0 {
			output.WriteString(body[start:openingEnd])
			output.WriteString(preserve(body[openingEnd:]))
			return output.String()
		}
		closingStart := openingEnd + closingRelative
		closingEnd := htmlTagEnd(body, closingStart)
		if closingEnd < 0 {
			output.WriteString(body[start:openingEnd])
			output.WriteString(preserve(body[openingEnd:]))
			return output.String()
		}
		output.WriteString(body[start:openingEnd])
		if name == "style" {
			output.WriteString(protect(body[openingEnd:closingStart]))
		} else {
			output.WriteString(preserve(body[openingEnd:closingStart]))
		}
		output.WriteString(body[closingStart:closingEnd])
		index = closingEnd
	}
	return output.String()
}

func htmlOpeningTagIs(tag, name string) bool {
	if len(tag) <= len(name)+1 || tag[0] != '<' || tag[1] == '/' || !strings.EqualFold(tag[1:1+len(name)], name) {
		return false
	}
	boundary := tag[1+len(name)]
	return boundary == ' ' || boundary == '\t' || boundary == '\r' || boundary == '\n' || boundary == '\f' || boundary == '>'
}

func indexFold(value, needle string) int {
	for index := 0; index+len(needle) <= len(value); index++ {
		if !strings.EqualFold(value[index:index+len(needle)], needle) {
			continue
		}
		end := index + len(needle)
		if end == len(value) || value[end] == ' ' || value[end] == '\t' || value[end] == '\r' || value[end] == '\n' || value[end] == '\f' || value[end] == '>' {
			return index
		}
	}
	return -1
}

func htmlTagEnd(body string, start int) int {
	quote := byte(0)
	for index := start + 1; index < len(body); index++ {
		if quote != 0 {
			if body[index] == quote {
				quote = 0
			}
			continue
		}
		switch body[index] {
		case '\'', '"':
			quote = body[index]
		case '>':
			return index + 1
		}
	}
	return -1
}

func rewritePreviewConfiguredURLs(text, prefix, configuredBaseURL string) string {
	if strings.TrimSpace(configuredBaseURL) == "" {
		return text
	}
	return rewritePreviewHTMLTags(text, func(attrName, tag string) string {
		if !isPreviewURLAttributeName(attrName) {
			return tag
		}
		tag = previewAbsoluteURLAttributePattern.ReplaceAllStringFunc(tag, func(match string) string {
			parts := previewAbsoluteURLAttributePattern.FindStringSubmatch(match)
			if len(parts) != 4 {
				return match
			}
			return parts[1] + parts[2] + rewritePreviewConfiguredURL(parts[3], prefix, configuredBaseURL)
		})
		tag = previewUnquotedAbsoluteURLAttributePattern.ReplaceAllStringFunc(tag, func(match string) string {
			parts := previewUnquotedAbsoluteURLAttributePattern.FindStringSubmatch(match)
			if len(parts) != 3 {
				return match
			}
			return parts[1] + rewritePreviewConfiguredURL(parts[2], prefix, configuredBaseURL)
		})
		tag = previewAbsoluteSrcsetAttributePattern.ReplaceAllStringFunc(tag, func(match string) string {
			parts := previewAbsoluteSrcsetAttributePattern.FindStringSubmatch(match)
			if len(parts) != 4 {
				return match
			}
			return parts[1] + parts[2] + rewritePreviewConfiguredSrcset(parts[3], prefix, configuredBaseURL)
		})
		return previewUnquotedAbsoluteSrcsetAttributePattern.ReplaceAllStringFunc(tag, func(match string) string {
			parts := previewUnquotedAbsoluteSrcsetAttributePattern.FindStringSubmatch(match)
			if len(parts) != 3 {
				return match
			}
			return parts[1] + rewritePreviewConfiguredSrcset(parts[2], prefix, configuredBaseURL)
		})
	})
}

func protectPreviewCSSContexts(text, prefix, configuredBaseURL, pagePath string) (string, map[string]string) {
	restored := make(map[string]string)
	reserved := text
	nextMarker := 0
	markerFor := func(value string) string {
		for {
			marker := fmt.Sprintf("__oxpio_preview_css_%d__", nextMarker)
			nextMarker++
			if !strings.Contains(reserved, marker) {
				restored[marker] = value
				return marker
			}
		}
	}
	protect := func(css string) string {
		return markerFor(string(rewritePreviewCSSURLsAt([]byte(css), prefix, configuredBaseURL, pagePath)))
	}
	preserve := func(content string) string {
		return markerFor(content)
	}
	text = protectPreviewHTMLRawElements(text, preserve, protect)
	text = rewritePreviewHTMLTags(text, func(attrName, tag string) string {
		if !strings.EqualFold(attrName, "style") {
			return tag
		}
		tag = previewStyleDoubleAttributePattern.ReplaceAllStringFunc(tag, func(match string) string {
			parts := previewStyleDoubleAttributePattern.FindStringSubmatch(match)
			if len(parts) != 3 {
				return match
			}
			return parts[1] + protect(parts[2]) + `"`
		})
		tag = previewStyleSingleAttributePattern.ReplaceAllStringFunc(tag, func(match string) string {
			parts := previewStyleSingleAttributePattern.FindStringSubmatch(match)
			if len(parts) != 3 {
				return match
			}
			return parts[1] + protect(parts[2]) + "'"
		})
		return previewStyleUnquotedAttributePattern.ReplaceAllStringFunc(tag, func(match string) string {
			parts := previewStyleUnquotedAttributePattern.FindStringSubmatch(match)
			if len(parts) != 3 {
				return match
			}
			return parts[1] + protect(parts[2])
		})
	})
	return text, restored
}

func rewritePreviewCSSURLs(body []byte, prefix, configuredBaseURL string) []byte {
	return rewritePreviewCSSURLsAt(body, prefix, configuredBaseURL, "")
}

func rewritePreviewCSSURLsAt(body []byte, prefix, configuredBaseURL, pagePath string) []byte {
	if strings.TrimSpace(configuredBaseURL) == "" && pagePath == "" {
		return body
	}
	input := string(body)
	var output strings.Builder
	for index := 0; index < len(input); {
		if strings.HasPrefix(input[index:], "/*") {
			end := strings.Index(input[index+2:], "*/")
			if end < 0 {
				output.WriteString(input[index:])
				break
			}
			end += index + 4
			output.WriteString(input[index:end])
			index = end
			continue
		}
		if input[index] == '\'' || input[index] == '"' {
			end := cssStringEnd(input, index)
			output.WriteString(input[index:end])
			index = end
			continue
		}
		if strings.HasPrefix(input[index:], "&#39;") || strings.HasPrefix(input[index:], "&#34;") {
			entity := input[index : index+5]
			end := strings.Index(input[index+5:], entity)
			if end >= 0 {
				end += index + 10
				output.WriteString(input[index:end])
				index = end
				continue
			}
		}
		if cssTokenAt(input, index, "@import") {
			valueStart := skipCSSWhitespaceAndComments(input, index+len("@import"))
			if valueStart < len(input) && (input[valueStart] == '\'' || input[valueStart] == '"') {
				end := cssStringEnd(input, valueStart)
				if end > valueStart+1 {
					value := input[valueStart+1 : end-1]
					output.WriteString(input[index : valueStart+1])
					output.WriteString(rewritePreviewCSSResource(value, prefix, configuredBaseURL, pagePath))
					output.WriteByte(input[end-1])
					index = end
					continue
				}
			} else if strings.HasPrefix(input[valueStart:], "&#39;") || strings.HasPrefix(input[valueStart:], "&#34;") {
				entity := input[valueStart : valueStart+5]
				resourceStart := valueStart + len(entity)
				closing := strings.Index(input[resourceStart:], entity)
				if closing >= 0 {
					resourceEnd := resourceStart + closing
					output.WriteString(input[index:resourceStart])
					output.WriteString(rewritePreviewCSSResource(input[resourceStart:resourceEnd], prefix, configuredBaseURL, pagePath))
					output.WriteString(entity)
					index = resourceEnd + len(entity)
					continue
				}
			}
		}
		tokenLength := cssFunctionTokenLength(input, index, "image-set")
		if tokenLength == 0 {
			tokenLength = cssFunctionTokenLength(input, index, "-webkit-image-set")
		}
		if tokenLength > 0 {
			if replacement, end, ok := rewriteCSSImageSetFunction(input, index, tokenLength, prefix, configuredBaseURL, pagePath); ok {
				output.WriteString(replacement)
				index = end
				continue
			}
		}
		if tokenLength := cssURLTokenLength(input, index); tokenLength > 0 {
			if replacement, end, ok := rewriteCSSURLFunction(input, index, tokenLength, prefix, configuredBaseURL, pagePath); ok {
				output.WriteString(replacement)
				index = end
				continue
			}
		}
		output.WriteByte(input[index])
		index++
	}
	return []byte(output.String())
}

func cssTokenAt(value string, index int, token string) bool {
	if index < 0 || index+len(token) > len(value) || !strings.EqualFold(value[index:index+len(token)], token) {
		return false
	}
	if index > 0 && isCSSIdentifierBefore(value, index) {
		return false
	}
	end := index + len(token)
	return end == len(value) || !isCSSIdentifierAt(value, end)
}

func isCSSIdentifierRune(value rune) bool {
	return value == '_' || value == '-' || unicode.IsLetter(value) || unicode.IsDigit(value) || unicode.IsMark(value)
}

func isCSSIdentifierAt(value string, index int) bool {
	if index < 0 || index >= len(value) {
		return false
	}
	runeValue, _ := utf8.DecodeRuneInString(value[index:])
	return isCSSIdentifierRune(runeValue)
}

func isCSSIdentifierBefore(value string, index int) bool {
	if index <= 0 || index > len(value) {
		return false
	}
	runeValue, _ := utf8.DecodeLastRuneInString(value[:index])
	return isCSSIdentifierRune(runeValue)
}

func isCSSWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n' || value == '\f'
}

func skipCSSWhitespaceAndComments(value string, index int) int {
	for index < len(value) {
		for index < len(value) && isCSSWhitespace(value[index]) {
			index++
		}
		if !strings.HasPrefix(value[index:], "/*") {
			return index
		}
		end := strings.Index(value[index+2:], "*/")
		if end < 0 {
			return len(value)
		}
		index += end + 4
	}
	return index
}

func cssStringEnd(value string, start int) int {
	quote := value[start]
	for index := start + 1; index < len(value); index++ {
		if value[index] == '\\' {
			index++
			continue
		}
		if value[index] == quote {
			return index + 1
		}
	}
	return len(value)
}

func cssFunctionTokenLength(value string, index int, token string) int {
	if cssTokenAt(value, index, token) {
		return len(token)
	}
	return 0
}

func cssURLTokenLength(value string, index int) int {
	if cssTokenAt(value, index, "url") {
		return len("url")
	}
	if index+5 <= len(value) && strings.EqualFold(value[index:index+5], `u\72l`) {
		if index > 0 && isCSSIdentifierBefore(value, index) {
			return 0
		}
		end := index + 5
		if end == len(value) || !isCSSIdentifierAt(value, end) {
			return 5
		}
	}
	return 0
}

func rewriteCSSImageSetFunction(value string, start, tokenLength int, prefix, configuredBaseURL, pagePath string) (string, int, bool) {
	index := start + tokenLength
	index = skipCSSWhitespaceAndComments(value, index)
	if index >= len(value) || value[index] != '(' {
		return "", start, false
	}
	contentStart := index + 1
	last := start
	depth := 1
	var output strings.Builder
	for index = contentStart; index < len(value); {
		if strings.HasPrefix(value[index:], "/*") {
			commentEnd := strings.Index(value[index+2:], "*/")
			if commentEnd < 0 {
				return "", start, false
			}
			commentEnd += index + 4
			output.WriteString(value[last:commentEnd])
			last = commentEnd
			index = commentEnd
			continue
		}
		if value[index] == '\'' || value[index] == '"' {
			end := cssStringEnd(value, index)
			if end <= index+1 {
				return "", start, false
			}
			output.WriteString(value[last : index+1])
			resource := value[index+1 : end-1]
			if cssImageSetTypeArgument(value, index) {
				output.WriteString(resource)
			} else {
				output.WriteString(rewritePreviewCSSResource(resource, prefix, configuredBaseURL, pagePath))
			}
			output.WriteString(value[end-1 : end])
			last = end
			index = end
			continue
		}
		if strings.HasPrefix(value[index:], "&#39;") || strings.HasPrefix(value[index:], "&#34;") {
			entity := value[index : index+5]
			closing := strings.Index(value[index+5:], entity)
			if closing < 0 {
				return "", start, false
			}
			valueStart := index + len(entity)
			valueEnd := valueStart + closing
			output.WriteString(value[last:valueStart])
			resource := value[valueStart:valueEnd]
			if cssImageSetTypeArgument(value, index) {
				output.WriteString(resource)
			} else {
				output.WriteString(rewritePreviewCSSResource(resource, prefix, configuredBaseURL, pagePath))
			}
			output.WriteString(value[valueEnd : valueEnd+len(entity)])
			last = valueEnd + len(entity)
			index = last
			continue
		}
		if nestedTokenLength := cssURLTokenLength(value, index); nestedTokenLength > 0 {
			if replacement, end, ok := rewriteCSSURLFunction(value, index, nestedTokenLength, prefix, configuredBaseURL, pagePath); ok {
				output.WriteString(value[last:index])
				output.WriteString(replacement)
				last = end
				index = end
				continue
			}
		}
		switch value[index] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				output.WriteString(value[last : index+1])
				return output.String(), index + 1, true
			}
		}
		index++
	}
	return "", start, false
}

func cssImageSetTypeArgument(value string, quoteStart int) bool {
	before := strings.TrimRightFunc(value[:quoteStart], isCSSWhitespaceRune)
	for strings.HasSuffix(before, "*/") {
		commentStart := strings.LastIndex(before[:len(before)-2], "/*")
		if commentStart < 0 {
			break
		}
		before = strings.TrimRightFunc(before[:commentStart], isCSSWhitespaceRune)
	}
	if !strings.HasSuffix(before, "(") {
		return false
	}
	before = strings.TrimRightFunc(before[:len(before)-1], isCSSWhitespaceRune)
	end := len(before)
	index := end
	for index > 0 {
		runeValue, size := utf8.DecodeLastRuneInString(before[:index])
		if !isCSSIdentifierRune(runeValue) {
			break
		}
		index -= size
	}
	return end > index && strings.EqualFold(before[index:end], "type")
}

func isCSSWhitespaceRune(value rune) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n' || value == '\f'
}

func rewriteCSSURLFunction(value string, start, tokenLength int, prefix, configuredBaseURL, pagePath string) (string, int, bool) {
	index := start + tokenLength
	for index < len(value) && isCSSWhitespace(value[index]) {
		index++
	}
	if index >= len(value) || value[index] != '(' {
		return "", start, false
	}
	index++
	index = skipCSSWhitespaceAndComments(value, index)
	valueStart := index
	var valueEnd, end int
	quoteEntity := ""
	if index < len(value) && (value[index] == '\'' || value[index] == '"') {
		end = cssStringEnd(value, index)
		if end <= index+1 {
			return "", start, false
		}
		valueStart = index + 1
		valueEnd = end - 1
	} else if strings.HasPrefix(value[index:], "&#39;") || strings.HasPrefix(value[index:], "&#34;") {
		quoteEntity = value[index : index+5]
		valueStart = index + len(quoteEntity)
		closing := strings.Index(value[valueStart:], quoteEntity)
		if closing < 0 {
			return "", start, false
		}
		valueEnd = valueStart + closing
		end = valueEnd + len(quoteEntity)
	} else {
		for index < len(value) && value[index] != ')' && !isCSSWhitespace(value[index]) {
			index++
		}
		valueEnd = index
		end = index
	}
	for end < len(value) && isCSSWhitespace(value[end]) {
		end++
	}
	if end >= len(value) || value[end] != ')' {
		return "", start, false
	}
	end++
	replacement := value[start:valueStart] + rewritePreviewCSSResource(value[valueStart:valueEnd], prefix, configuredBaseURL, pagePath) + value[valueEnd:end]
	return replacement, end, true
}

func rewritePreviewCSSResource(value, prefix, configuredBaseURL, pagePath string) string {
	if rewritten := rewritePreviewConfiguredURL(value, prefix, configuredBaseURL); rewritten != value {
		return rewritten
	}
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		return rewritePreviewRootURLWithBase(value, prefix, configuredBaseURL)
	}
	if pagePath != "" {
		return rewritePreviewRelativeURL(value, prefix, pagePath)
	}
	return value
}

func rewritePreviewConfiguredSrcset(value, prefix, configuredBaseURL string) string {
	return rewritePreviewSrcsetCandidates(value, func(candidate string) string {
		return rewritePreviewConfiguredURL(candidate, prefix, configuredBaseURL)
	})
}

func rewritePreviewSrcsetCandidates(value string, rewrite func(string) string) string {
	candidates := strings.Split(value, ",")
	for index, candidate := range candidates {
		trimmed := strings.TrimLeft(candidate, " \t\r\n\f")
		if trimmed == "" {
			continue
		}
		leading := len(candidate) - len(trimmed)
		tokenEnd := strings.IndexAny(trimmed, " \t\r\n\f")
		if tokenEnd < 0 {
			tokenEnd = len(trimmed)
		}
		original := trimmed[:tokenEnd]
		rewritten := rewrite(original)
		if rewritten != original {
			candidates[index] = candidate[:leading] + rewritten + candidate[leading+tokenEnd:]
		}
	}
	return strings.Join(candidates, ",")
}

func rewritePreviewConfiguredURL(value, prefix, configuredBaseURL string) string {
	base, err := url.Parse(strings.TrimSpace(configuredBaseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return value
	}
	target, err := url.Parse(value)
	if err != nil || !target.IsAbs() || target.Host == "" || !strings.EqualFold(target.Scheme, base.Scheme) || !strings.EqualFold(target.Host, base.Host) {
		return value
	}
	baseDecodedPath, err := url.PathUnescape(base.EscapedPath())
	if err != nil {
		return value
	}
	basePath := path.Clean(baseDecodedPath)
	if basePath == "." || basePath == "" {
		basePath = "/"
	}
	targetEscapedPath := target.EscapedPath()
	targetDecodedPath, err := url.PathUnescape(targetEscapedPath)
	if err != nil {
		return value
	}
	targetHasTrailingSlash := strings.HasSuffix(targetEscapedPath, "/") && targetEscapedPath != ""
	targetPath := path.Clean(targetDecodedPath)
	if targetPath == "." || targetPath == "" {
		targetPath = "/"
	}
	baseRoot := strings.TrimSuffix(basePath, "/")
	if baseRoot != "" && baseRoot != "/" && targetPath != baseRoot && !strings.HasPrefix(targetPath, baseRoot+"/") {
		return value
	}
	resolved := targetEscapedPath
	if targetDecodedPath != targetPath {
		resolved = (&url.URL{Path: targetPath}).EscapedPath()
	}
	if targetHasTrailingSlash && resolved != "/" && !strings.HasSuffix(resolved, "/") {
		resolved += "/"
	}
	if target.RawQuery != "" {
		resolved += "?" + target.RawQuery
	}
	if target.Fragment != "" {
		resolved += "#" + target.EscapedFragment()
	}
	return rewritePreviewRootURLWithBase(resolved, prefix, configuredBaseURL)
}

func rewritePreviewRelativeSrcset(value, prefix, pagePath string) string {
	return rewritePreviewSrcsetCandidates(value, func(candidate string) string {
		return rewritePreviewRelativeURL(candidate, prefix, pagePath)
	})
}

func rewritePreviewRelativeURL(value, prefix, pagePath string) string {
	if value == "" || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "#") || value == prefix || strings.HasPrefix(value, prefix+"/") {
		return value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Opaque != "" {
		return value
	}
	basePath := pagePath
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	if !strings.HasSuffix(basePath, "/") {
		basePath += "/"
	}
	base, err := url.Parse("https://preview.invalid" + basePath)
	if err != nil {
		return value
	}
	resolved := base.ResolveReference(parsed)
	resolvedPath := resolved.EscapedPath()
	if resolvedPath == "" {
		resolvedPath = "/"
	}
	if resolved.RawQuery != "" {
		resolvedPath += "?" + resolved.RawQuery
	}
	if resolved.Fragment != "" {
		resolvedPath += "#" + resolved.EscapedFragment()
	}
	return prefix + resolvedPath
}

func rewritePreviewHTML(body []byte, prefix string) []byte {
	return rewritePreviewHTMLWithBase(body, prefix, "")
}

func rewritePreviewHTMLWithBase(body []byte, prefix, configuredBaseURL string) []byte {
	text, restored := protectPreviewCSSContexts(string(body), prefix, configuredBaseURL, "")
	text = string(rewritePreviewHTMLUnprotectedAt([]byte(text), prefix, configuredBaseURL))
	for marker, value := range restored {
		text = strings.ReplaceAll(text, marker, value)
	}
	return []byte(text)
}

func rewritePreviewHTMLUnprotectedAt(body []byte, prefix, configuredBaseURL string) []byte {
	text := rewritePreviewHTMLTags(string(body), func(attrName, tag string) string {
		if !isPreviewURLAttributeName(attrName) {
			return tag
		}
		tag = previewURLAttributePattern.ReplaceAllStringFunc(tag, func(match string) string {
			parts := previewURLAttributePattern.FindStringSubmatch(match)
			if len(parts) != 4 {
				return match
			}
			return parts[1] + parts[2] + rewritePreviewRootURLWithBase(parts[3], prefix, configuredBaseURL)
		})
		tag = previewUnquotedURLAttributePattern.ReplaceAllStringFunc(tag, func(match string) string {
			parts := previewUnquotedURLAttributePattern.FindStringSubmatch(match)
			if len(parts) != 3 {
				return match
			}
			return parts[1] + rewritePreviewRootURLWithBase(parts[2], prefix, configuredBaseURL)
		})
		tag = previewSrcsetAttributePattern.ReplaceAllStringFunc(tag, func(match string) string {
			parts := previewSrcsetAttributePattern.FindStringSubmatch(match)
			if len(parts) != 4 {
				return match
			}
			return parts[1] + parts[2] + rewritePreviewSrcsetCandidates(parts[3], func(candidate string) string {
				if strings.HasPrefix(candidate, "//") || !strings.HasPrefix(candidate, "/") {
					return candidate
				}
				return rewritePreviewRootURLWithBase(candidate, prefix, configuredBaseURL)
			})
		})
		return previewUnquotedSrcsetAttributePattern.ReplaceAllStringFunc(tag, func(match string) string {
			parts := previewUnquotedSrcsetAttributePattern.FindStringSubmatch(match)
			if len(parts) != 3 {
				return match
			}
			return parts[1] + rewritePreviewSrcsetCandidates(parts[2], func(candidate string) string {
				if strings.HasPrefix(candidate, "//") || !strings.HasPrefix(candidate, "/") {
					return candidate
				}
				return rewritePreviewRootURLWithBase(candidate, prefix, configuredBaseURL)
			})
		})
	})
	return []byte(text)
}

func rewritePreviewLocation(value, prefix, configuredBaseURL string) (string, bool) {
	if rewritten := rewritePreviewConfiguredURL(value, prefix, configuredBaseURL); rewritten != value {
		return rewritten, true
	}
	parsed, err := url.Parse(value)
	if err == nil && (parsed.IsAbs() || parsed.Host != "" || strings.HasPrefix(value, "//")) {
		return "", false
	}
	if strings.HasPrefix(value, "/") {
		return rewritePreviewRootURLWithBase(value, prefix, configuredBaseURL), true
	}
	return rewritePreviewRelativeURL(value, prefix, "/"), true
}

func rewritePreviewRootURL(value, prefix string) string {
	if strings.HasPrefix(value, "//") || value == prefix || strings.HasPrefix(value, prefix+"/") {
		return value
	}
	return prefix + value
}

func rewritePreviewRootURLWithBase(value, prefix, configuredBaseURL string) string {
	if strings.TrimSpace(configuredBaseURL) == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return rewritePreviewRootURL(value, prefix)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return rewritePreviewRootURL(value, prefix)
	}
	decodedTarget, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return value
	}
	cleanTarget := path.Clean(decodedTarget)
	if cleanTarget == "." || cleanTarget == "" {
		cleanTarget = "/"
	}
	if cleanTarget == decodedTarget {
		return rewritePreviewRootURL(value, prefix)
	}
	base, err := url.Parse(strings.TrimSpace(configuredBaseURL))
	if err != nil {
		return value
	}
	decodedBase, err := url.PathUnescape(base.EscapedPath())
	if err != nil {
		return value
	}
	baseRoot := strings.TrimSuffix(path.Clean(decodedBase), "/")
	if baseRoot != "" && baseRoot != "/" && cleanTarget != baseRoot && !strings.HasPrefix(cleanTarget, baseRoot+"/") {
		return value
	}
	resolved := (&url.URL{Path: cleanTarget}).EscapedPath()
	if strings.HasSuffix(parsed.EscapedPath(), "/") && resolved != "/" && !strings.HasSuffix(resolved, "/") {
		resolved += "/"
	}
	if parsed.RawQuery != "" {
		resolved += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		resolved += "#" + parsed.EscapedFragment()
	}
	return rewritePreviewRootURL(resolved, prefix)
}

// jsonUnmarshal is kept local to the edit package so preview requests share
// the same bounded source-body handling as source mutations.
func jsonUnmarshal(data []byte, value any) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("invalid JSON: trailing data")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case json.Delim:
			switch value {
			case '{':
				seen := map[string]struct{}{}
				for decoder.More() {
					keyToken, keyErr := decoder.Token()
					if keyErr != nil {
						return keyErr
					}
					key, ok := keyToken.(string)
					if !ok {
						return errors.New("invalid JSON object key")
					}
					if _, exists := seen[key]; exists {
						return fmt.Errorf("duplicate JSON key %q", key)
					}
					seen[key] = struct{}{}
					if err := walk(); err != nil {
						return err
					}
				}
				_, err = decoder.Token()
				return err
			case '[':
				for decoder.More() {
					if err := walk(); err != nil {
						return err
					}
				}
				_, err = decoder.Token()
				return err
			}
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("invalid JSON: trailing data")
		}
		return err
	}
	return nil
}
