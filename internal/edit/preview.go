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
	if len(parts) == 2 && parts[1] == "content" {
		servePreviewBytes(w, r, preview.contentHTML, "text/html; charset=utf-8")
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
		body = rewritePreviewHTML(body, s.controlPrefix()+"preview/"+token)
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
			return fullPage, nil
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
		return fullPage, nil
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
	contentHTML := rewritePreviewHTMLAt(contentPage, s.controlPrefix()+"preview/"+token, previewSitePath(basePath, previewEntry.Route))
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
	previewURLAttributePattern                    = regexp.MustCompile(`(?i)(\b(?:href|src|action|poster|data|data-src)\s*=\s*)(["'])(/[^"']*)`)
	previewUnquotedURLAttributePattern            = regexp.MustCompile(`(?i)(\b(?:href|src|action|poster|data|data-src)\s*=\s*)(/[^\s"'=<>` + "`" + `]+)`)
	previewRelativeURLAttributePattern            = regexp.MustCompile(`(?i)(\b(?:href|src|action|poster|data|data-src)\s*=\s*)(["'])([^"']*)`)
	previewUnquotedRelativeURLAttributePattern    = regexp.MustCompile(`(?i)(\b(?:href|src|action|poster|data|data-src)\s*=\s*)([^\s"'=<>` + "`" + `]+)`)
	previewSrcsetAttributePattern                 = regexp.MustCompile(`(?i)(\b(?:srcset|imagesrcset)\s*=\s*)(["'])(/[^"']*)`)
	previewUnquotedSrcsetAttributePattern         = regexp.MustCompile(`(?i)(\b(?:srcset|imagesrcset)\s*=\s*)(/[^\s"'=<>` + "`" + `]+)`)
	previewRelativeSrcsetAttributePattern         = regexp.MustCompile(`(?i)(\b(?:srcset|imagesrcset)\s*=\s*)(["'])([^"']*)`)
	previewUnquotedRelativeSrcsetAttributePattern = regexp.MustCompile(`(?i)(\b(?:srcset|imagesrcset)\s*=\s*)([^\s"'=<>` + "`" + `]+)`)
)

func rewritePreviewHTMLAt(body []byte, prefix, pagePath string) []byte {
	text := previewRelativeURLAttributePattern.ReplaceAllStringFunc(string(body), func(match string) string {
		parts := previewRelativeURLAttributePattern.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		return parts[1] + parts[2] + rewritePreviewRelativeURL(parts[3], prefix, pagePath)
	})
	text = previewUnquotedRelativeURLAttributePattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := previewUnquotedRelativeURLAttributePattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return parts[1] + rewritePreviewRelativeURL(parts[2], prefix, pagePath)
	})
	text = previewRelativeSrcsetAttributePattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := previewRelativeSrcsetAttributePattern.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		return parts[1] + parts[2] + rewritePreviewRelativeSrcset(parts[3], prefix, pagePath)
	})
	text = previewUnquotedRelativeSrcsetAttributePattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := previewUnquotedRelativeSrcsetAttributePattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return parts[1] + rewritePreviewRelativeSrcset(parts[2], prefix, pagePath)
	})
	return rewritePreviewHTML([]byte(text), prefix)
}

func rewritePreviewRelativeSrcset(value, prefix, pagePath string) string {
	candidates := strings.Split(value, ",")
	for index, candidate := range candidates {
		fields := strings.Fields(candidate)
		if len(fields) == 0 {
			continue
		}
		fields[0] = rewritePreviewRelativeURL(fields[0], prefix, pagePath)
		candidates[index] = strings.Join(fields, " ")
	}
	return strings.Join(candidates, ",")
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
	text := previewURLAttributePattern.ReplaceAllStringFunc(string(body), func(match string) string {
		parts := previewURLAttributePattern.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		return parts[1] + parts[2] + rewritePreviewRootURL(parts[3], prefix)
	})
	text = previewUnquotedURLAttributePattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := previewUnquotedURLAttributePattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		return parts[1] + rewritePreviewRootURL(parts[2], prefix)
	})
	text = previewSrcsetAttributePattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := previewSrcsetAttributePattern.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		candidates := strings.Split(parts[3], ",")
		for index, candidate := range candidates {
			fields := strings.Fields(candidate)
			if len(fields) == 0 || strings.HasPrefix(fields[0], "//") || !strings.HasPrefix(fields[0], "/") {
				continue
			}
			fields[0] = rewritePreviewRootURL(fields[0], prefix)
			candidates[index] = strings.Join(fields, " ")
		}
		return parts[1] + parts[2] + strings.Join(candidates, ",")
	})
	return []byte(previewUnquotedSrcsetAttributePattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := previewUnquotedSrcsetAttributePattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		candidates := strings.Split(parts[2], ",")
		for index, candidate := range candidates {
			fields := strings.Fields(candidate)
			if len(fields) == 0 || strings.HasPrefix(fields[0], "//") || !strings.HasPrefix(fields[0], "/") {
				continue
			}
			fields[0] = rewritePreviewRootURL(fields[0], prefix)
			candidates[index] = strings.Join(fields, " ")
		}
		return parts[1] + strings.Join(candidates, ",")
	}))
}

func rewritePreviewRootURL(value, prefix string) string {
	if strings.HasPrefix(value, "//") || value == prefix || strings.HasPrefix(value, prefix+"/") {
		return value
	}
	return prefix + value
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
