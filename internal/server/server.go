package server

import (
	"bytes"
	"fmt"
	stdhtml "html"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	internalfsutil "github.com/simp-lee/obsite/internal/fsutil"
	"github.com/simp-lee/obsite/internal/slug"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// DefaultPort is the default local preview port for obsite serve.
const DefaultPort = 8080

const (
	liveReloadEndpoint   = "/_livereload"
	liveReloadQueryParam = "obsite-live-reload"
)

var liveReloadScript = []byte(`<script data-obsite-livereload>(function(){if(!window.EventSource){return;}var source=new EventSource("/_livereload?obsite-live-reload=1");source.onmessage=function(event){if(event.data==="reload"){source.close();window.location.reload();}};})();</script>`)

// Server serves a generated Obsite output directory over HTTP.
type Server struct {
	outputPath     string
	fileServer     http.Handler
	realOutputPath string
	port           int
	notFoundPath   string
	basePath       string
	basePathMu     sync.RWMutex
	liveReload     *liveReloadHub
}

type liveReloadHub struct {
	mu      sync.Mutex
	clients map[chan string]struct{}
}

type bufferedResponseWriter struct {
	header     http.Header
	body       bytes.Buffer
	statusCode int
}

// New validates the generated output directory and constructs a preview server.
func New(outputPath string, port int) (*Server, error) {
	normalizedOutputPath, err := normalizeOutputPath(outputPath)
	if err != nil {
		return nil, err
	}
	realOutputPath, err := filepath.EvalSymlinks(normalizedOutputPath)
	if err != nil {
		return nil, fmt.Errorf("resolve output path symlinks %q: %w", normalizedOutputPath, err)
	}
	normalizedPort := normalizePort(port)
	if err := validatePort(normalizedPort); err != nil {
		return nil, err
	}

	server := &Server{
		outputPath:     normalizedOutputPath,
		fileServer:     http.FileServer(http.Dir(normalizedOutputPath)),
		realOutputPath: realOutputPath,
		port:           normalizedPort,
		basePath:       detectOutputBasePath(normalizedOutputPath),
	}

	notFoundPath := filepath.Join(normalizedOutputPath, "404.html")
	if resolvedNotFoundPath, info, err := server.resolveExistingOutputPath(notFoundPath); err == nil && !info.IsDir() {
		server.notFoundPath = resolvedNotFoundPath
	}

	return server, nil
}

// Addr returns the listen address for the preview server.
func (s *Server) Addr() string {
	if s == nil {
		return ""
	}

	return fmt.Sprintf(":%d", s.port)
}

// ListenAndServe starts the preview server.
func (s *Server) ListenAndServe() error {
	if s == nil {
		return fmt.Errorf("server is nil")
	}

	return http.ListenAndServe(s.Addr(), s)
}

// EnableLiveReload turns on the SSE endpoint and HTML script injection used by watch mode.
func (s *Server) EnableLiveReload() {
	if s == nil || s.liveReload != nil {
		return
	}

	s.liveReload = newLiveReloadHub()
}

// NotifyReload broadcasts a livereload event to connected preview clients.
func (s *Server) NotifyReload() {
	if s == nil || s.liveReload == nil {
		return
	}

	s.liveReload.Reload()
}

// ServeHTTP handles clean-URL fallbacks before delegating to the underlying file server.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil {
		http.Error(w, "server is nil", http.StatusInternalServerError)
		return
	}

	requestPath := r.URL.EscapedPath()
	if requestPath == "" {
		requestPath = r.URL.Path
	}
	cleanPath, _ := cleanRequestPath(requestPath)
	if isEditControlPath(cleanPath) {
		http.NotFound(w, r)
		return
	}
	if cleanPath == s.externalOutputPath(liveReloadEndpoint) && s.liveReload != nil && shouldServeLiveReloadRequest(r) {
		s.serveLiveReload(w, r)
		return
	}

	servePath, redirectPath := s.resolvePath(requestPath)
	if redirectPath != "" {
		if rawQuery := r.URL.RawQuery; rawQuery != "" {
			redirectPath += "?" + rawQuery
		}
		http.Redirect(w, r, redirectPath, http.StatusMovedPermanently)
		return
	}

	if servePath != "" {
		s.serveOutput(w, r, servePath)
		return
	}

	if s.notFoundPath == "" {
		http.NotFound(w, r)
		return
	}

	s.serveNotFound(w, r)
}

func (s *Server) resolvePath(requestPath string) (servePath string, redirectPath string) {
	if isUnsafePreviewRequestPath(requestPath) {
		return "", ""
	}

	cleanPath, _ := cleanRequestPath(requestPath)
	outputPath, ok := s.outputPathForRequest(cleanPath)
	if !ok {
		return "", ""
	}
	decodedOutputPath, err := url.PathUnescape(outputPath)
	if err != nil {
		return "", ""
	}
	resolvedPath := s.outputPath
	decodedCleanPath := path.Clean("/" + strings.TrimPrefix(decodedOutputPath, "/"))
	if decodedCleanPath != "/" {
		_, candidate, err := internalfsutil.ResolveOutputURLPath(s.outputPath, strings.TrimPrefix(outputPath, "/"))
		if err != nil {
			return "", ""
		}
		resolvedPath = candidate
	}
	_, info, err := s.resolveExistingOutputPath(resolvedPath)
	if err != nil {
		return "", ""
	}

	if info.IsDir() {
		_, indexInfo, indexErr := s.resolveExistingOutputPath(filepath.Join(resolvedPath, "index.html"))
		if indexErr != nil || indexInfo.IsDir() {
			return "", ""
		}

		canonicalOutputPath := canonicalPreviewOutputPath(decodedOutputPath, true)
		canonicalPath := s.externalOutputPath(canonicalOutputPath)
		if requestPath != canonicalPath {
			return "", canonicalPath
		}

		return canonicalOutputPath, ""
	}

	canonicalOutputPath := canonicalPreviewOutputPath(decodedOutputPath, false)
	canonicalPath := s.externalOutputPath(canonicalOutputPath)
	if requestPath != canonicalPath {
		return "", canonicalPath
	}

	return canonicalOutputPath, ""
}

func (s *Server) serveNotFound(w http.ResponseWriter, r *http.Request) {
	body, err := os.ReadFile(s.notFoundPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	basePath := s.currentBasePath()
	body = injectPreviewBaseHrefAt(body, basePath)
	if s.liveReload != nil {
		body = injectLiveReloadScriptAt(body, basePath)
	}
	if len(body) == 0 {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusNotFound)
	if r.Method == http.MethodHead {
		return
	}

	_, _ = w.Write(body)
}

func (s *Server) serveLiveReload(w http.ResponseWriter, r *http.Request) {
	if s.liveReload == nil {
		http.Error(w, "live reload is unavailable", http.StatusServiceUnavailable)
		return
	}

	s.liveReload.ServeHTTP(w, r)
}

func shouldServeLiveReloadRequest(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}

	if r.URL.Query().Get(liveReloadQueryParam) == "1" {
		return true
	}

	accept := strings.ToLower(strings.TrimSpace(r.Header.Get("Accept")))
	return strings.Contains(accept, "text/event-stream")
}

func (s *Server) serveOutput(w http.ResponseWriter, r *http.Request, servePath string) {
	req := r.Clone(r.Context())
	decodedServePath, err := url.PathUnescape(servePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	req.URL.Path = decodedServePath
	req.URL.RawPath = servePath

	filePath := ""
	if s.outputPath != "" {
		filePath, err = s.resolveOutputFilePath(servePath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	serve := func(writer http.ResponseWriter, request *http.Request) {
		if s.outputPath == "" && s.fileServer != nil {
			s.fileServer.ServeHTTP(writer, request)
			return
		}
		http.ServeFile(writer, request, filePath)
	}
	if s.liveReload == nil || !shouldBufferInjectedResponse(req, servePath) {
		serve(w, req)
		return
	}
	s.serveInjectedResponse(w, req, serve)
}

func shouldBufferInjectedResponse(r *http.Request, servePath string) bool {
	if r == nil || requestHasRange(r) {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}

	return isHTMLCandidatePath(servePath)
}

func isHTMLCandidatePath(servePath string) bool {
	if servePath == "" {
		return false
	}
	if strings.HasSuffix(servePath, "/") {
		return true
	}

	return strings.EqualFold(path.Ext(servePath), ".html")
}

func (s *Server) serveInjectedResponse(w http.ResponseWriter, r *http.Request, serve func(http.ResponseWriter, *http.Request)) {
	recorder := newBufferedResponseWriter()
	serveRequest := r.Clone(r.Context())
	serveRequest.Header = r.Header.Clone()
	serveRequest.Header.Del("If-Modified-Since")
	serveRequest.Header.Del("If-None-Match")
	if r.Method == http.MethodHead {
		serveRequest.Method = http.MethodGet
	}
	serve(recorder, serveRequest)

	statusCode := recorder.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}

	body := recorder.body.Bytes()
	headers := cloneHeaders(recorder.Header())
	headers.Set("Cache-Control", "no-store")
	if statusCode != http.StatusPartialContent && !requestHasRange(r) && shouldInjectLiveReload(headers, body) {
		body = injectLiveReloadScriptAt(body, s.currentBasePath())
		headers.Set("Content-Length", strconv.Itoa(len(body)))
		clearRangeHeaders(headers)
	}

	copyHeaders(w.Header(), headers)
	w.WriteHeader(statusCode)
	if r.Method == http.MethodHead {
		return
	}

	_, _ = w.Write(body)
}

func (s *Server) resolveOutputFilePath(servePath string) (string, error) {
	if s == nil || s.outputPath == "" {
		return "", os.ErrNotExist
	}
	candidate := s.outputPath
	if servePath != "/" {
		_, resolvedCandidate, err := internalfsutil.ResolveOutputURLPath(s.outputPath, strings.TrimPrefix(servePath, "/"))
		if err != nil {
			return "", err
		}
		candidate = resolvedCandidate
	}
	if strings.HasSuffix(servePath, "/") {
		candidate = filepath.Join(candidate, "index.html")
	}
	resolvedPath, info, err := s.resolveExistingOutputPath(candidate)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", os.ErrNotExist
	}
	return resolvedPath, nil
}

func (s *Server) resolveExistingOutputPath(candidate string) (string, os.FileInfo, error) {
	if s == nil || strings.TrimSpace(candidate) == "" {
		return "", nil, os.ErrNotExist
	}
	if !pathWithinRoot(s.outputPath, candidate) {
		return "", nil, os.ErrPermission
	}

	info, err := os.Stat(candidate)
	if err != nil {
		return "", nil, err
	}

	resolvedPath, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", nil, err
	}
	if !pathWithinRoot(s.realOutputPath, resolvedPath) {
		return "", nil, os.ErrPermission
	}

	return resolvedPath, info, nil
}

func requestHasRange(r *http.Request) bool {
	if r == nil {
		return false
	}

	return strings.TrimSpace(r.Header.Get("Range")) != ""
}

func clearRangeHeaders(headers http.Header) {
	if headers == nil {
		return
	}

	headers.Del("Accept-Ranges")
	headers.Del("Content-Range")
}

var outputBasePathPattern = regexp.MustCompile(`data-obsite-base-path=(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)

func detectOutputBasePath(outputPath string) string {
	data, err := os.ReadFile(filepath.Join(outputPath, "index.html"))
	if err != nil {
		return "/"
	}
	match := outputBasePathPattern.FindSubmatch(data)
	if len(match) != 4 {
		return "/"
	}
	candidate := ""
	for _, value := range match[1:] {
		if len(value) > 0 {
			candidate = strings.TrimSpace(stdhtml.UnescapeString(string(value)))
			break
		}
	}
	if candidate == "" || !strings.HasPrefix(candidate, "/") || strings.ContainsAny(candidate, "\\\"<>") {
		return "/"
	}
	cleaned, _ := cleanRequestPath(candidate)
	return ensureDirectoryPath(cleaned)
}

// RefreshBasePath reloads the base path embedded in the current root page.
// Watch mode calls it after a successful configuration rebuild.
func (s *Server) RefreshBasePath() {
	if s == nil {
		return
	}
	basePath := detectOutputBasePath(s.outputPath)
	s.basePathMu.Lock()
	s.basePath = basePath
	s.basePathMu.Unlock()
}

// BasePath returns the configured public output prefix.
func (s *Server) BasePath() string {
	return s.currentBasePath()
}

func (s *Server) currentBasePath() string {
	if s == nil {
		return "/"
	}
	s.basePathMu.RLock()
	defer s.basePathMu.RUnlock()
	return s.basePath
}

func (s *Server) outputPathForRequest(cleanPath string) (string, bool) {
	if s == nil {
		return "", false
	}
	base := s.currentBasePath()
	if base == "" || base == "/" {
		return cleanPath, true
	}
	baseRoot := strings.TrimSuffix(base, "/")
	if cleanPath == baseRoot {
		return "/", true
	}
	if !strings.HasPrefix(cleanPath, base) {
		return "", false
	}
	relative := strings.TrimPrefix(cleanPath, baseRoot)
	if relative == "" {
		return "/", true
	}
	if !strings.HasPrefix(relative, "/") {
		return "", false
	}
	return relative, true
}

func (s *Server) externalOutputPath(outputPath string) string {
	basePath := s.currentBasePath()
	if basePath == "" || basePath == "/" {
		return outputPath
	}
	return strings.TrimSuffix(basePath, "/") + outputPath
}

func normalizeOutputPath(outputPath string) (string, error) {
	trimmedPath := strings.TrimSpace(outputPath)
	if trimmedPath == "" {
		return "", fmt.Errorf("output path is required")
	}
	normalizedPath := filepath.Clean(trimmedPath)

	info, err := os.Stat(normalizedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("output path %q does not exist", normalizedPath)
		}

		return "", fmt.Errorf("stat output path %q: %w", normalizedPath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("output path %q is not a directory", normalizedPath)
	}

	absPath, err := filepath.Abs(normalizedPath)
	if err != nil {
		return "", fmt.Errorf("resolve output path %q: %w", normalizedPath, err)
	}

	return absPath, nil
}

func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}

	return nil
}

func isUnsafePreviewRequestPath(requestPath string) bool {
	normalized := normalizePreviewRequestPath(requestPath)
	if normalized == "" {
		return false
	}

	trimmed := strings.TrimLeft(normalized, "/")
	return hasWindowsDriveRequestPrefix(trimmed) || hasUNCRequestPrefix(normalized)
}

func normalizePreviewRequestPath(requestPath string) string {
	return strings.ReplaceAll(strings.TrimSpace(requestPath), `\`, "/")
}

func hasWindowsDriveRequestPrefix(value string) bool {
	if len(value) < 3 || value[1] != ':' {
		return false
	}

	first := value[0]
	if (first < 'A' || first > 'Z') && (first < 'a' || first > 'z') {
		return false
	}

	return value[2] == '/'
}

func hasUNCRequestPrefix(value string) bool {
	if !strings.HasPrefix(value, "//") {
		return false
	}

	trimmed := strings.TrimLeft(value, "/")
	firstSep := strings.IndexByte(trimmed, '/')
	if firstSep <= 0 {
		return false
	}

	share := trimmed[firstSep+1:]
	if share == "" {
		return false
	}

	if secondSep := strings.IndexByte(share, '/'); secondSep >= 0 {
		return secondSep > 0
	}

	return true
}

func pathWithinRoot(root string, candidate string) bool {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(candidate) == "" {
		return false
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}

	rel, err := filepath.Rel(absRoot, absCandidate)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}

	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func normalizePort(port int) int {
	if port == 0 {
		return DefaultPort
	}

	return port
}

func injectPreviewBaseHref(body []byte) []byte {
	return injectPreviewBaseHrefAt(body, "/")
}

func injectPreviewBaseHrefAt(body []byte, basePath string) []byte {
	if len(body) == 0 {
		return body
	}
	basePath = ensureDirectoryPath(basePath)
	baseTag := []byte(`<base href="` + stdhtml.EscapeString(basePath) + `">`)

	insertAt, baseStart, baseEnd, hasBase, ok := previewBaseRewriteRange(body)
	if !ok {
		return body
	}

	if hasBase {
		withBase := make([]byte, 0, len(body)-(baseEnd-baseStart)+len(baseTag))
		withBase = append(withBase, body[:baseStart]...)
		withBase = append(withBase, baseTag...)
		withBase = append(withBase, body[baseEnd:]...)

		return withBase
	}

	withBase := make([]byte, 0, len(body)+len(baseTag)+1)
	withBase = append(withBase, body[:insertAt]...)
	withBase = append(withBase, '\n')
	withBase = append(withBase, baseTag...)
	withBase = append(withBase, body[insertAt:]...)

	return withBase
}

func previewBaseRewriteRange(body []byte) (insertAt int, baseStart int, baseEnd int, hasBase bool, ok bool) {
	tokenizer := xhtml.NewTokenizer(bytes.NewReader(body))
	offset := 0
	inHead := false
	implicitHead := false
	templateDepth := 0
	insertAt = -1

	for {
		tokenType := tokenizer.Next()
		raw := tokenizer.Raw()
		start := offset
		end := start + len(raw)
		offset = end

		switch tokenType {
		case xhtml.ErrorToken:
			return insertAt, 0, 0, false, insertAt >= 0
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			token := tokenizer.Token()
			if token.DataAtom == atom.Html || strings.EqualFold(token.Data, "html") {
				if insertAt < 0 {
					insertAt = end
				}
				if !inHead {
					implicitHead = true
				}
				continue
			}
			if token.DataAtom == atom.Head || strings.EqualFold(token.Data, "head") {
				insertAt = end
				if tokenType == xhtml.SelfClosingTagToken {
					return insertAt, 0, 0, false, true
				}
				inHead = true
				implicitHead = false
				continue
			}
			if !inHead && !implicitHead {
				continue
			}

			if implicitHead && templateDepth == 0 && (token.DataAtom == atom.Body || strings.EqualFold(token.Data, "body")) {
				return insertAt, 0, 0, false, insertAt >= 0
			}
			if implicitHead && templateDepth == 0 && !isImplicitHeadElement(token) {
				if insertAt < 0 {
					insertAt = start
				}
				return insertAt, 0, 0, false, insertAt >= 0
			}

			if token.DataAtom == atom.Template || strings.EqualFold(token.Data, "template") {
				if tokenType != xhtml.SelfClosingTagToken {
					templateDepth++
				}
				continue
			}

			if templateDepth == 0 && (token.DataAtom == atom.Base || strings.EqualFold(token.Data, "base")) {
				return insertAt, start, end, true, true
			}
		case xhtml.EndTagToken:
			token := tokenizer.Token()
			if token.DataAtom == atom.Template || strings.EqualFold(token.Data, "template") {
				if templateDepth > 0 {
					templateDepth--
				}
				continue
			}

			if inHead && templateDepth == 0 && (token.DataAtom == atom.Head || strings.EqualFold(token.Data, "head")) {
				return insertAt, 0, 0, false, true
			}
			if implicitHead && templateDepth == 0 && (token.DataAtom == atom.Html || strings.EqualFold(token.Data, "html")) {
				return insertAt, 0, 0, false, insertAt >= 0
			}
		}
	}
}

func isImplicitHeadElement(token xhtml.Token) bool {
	name := strings.ToLower(strings.TrimSpace(token.Data))
	switch name {
	case "base", "basefont", "bgsound", "link", "meta", "noframes", "noscript", "script", "style", "template", "title":
		return true
	default:
		return false
	}
}

func injectLiveReloadScriptAt(body []byte, basePath string) []byte {
	if len(body) == 0 {
		return body
	}

	lowerBody := bytes.ToLower(body)
	if containsLiveReloadScriptTag(lowerBody) {
		return body
	}

	insertAt := bytes.LastIndex(lowerBody, []byte("</body>"))
	if insertAt == -1 {
		insertAt = bytes.LastIndex(lowerBody, []byte("</html>"))
	}
	if insertAt == -1 {
		insertAt = len(body)
	}

	withScript := make([]byte, 0, len(body)+len(liveReloadScript)+1)
	withScript = append(withScript, body[:insertAt]...)
	if insertAt > 0 && body[insertAt-1] != '\n' {
		withScript = append(withScript, '\n')
	}
	withScript = append(withScript, liveReloadScriptAt(basePath)...)
	if insertAt < len(body) {
		withScript = append(withScript, body[insertAt:]...)
	}

	return withScript
}

func liveReloadScriptAt(basePath string) []byte {
	basePath = ensureDirectoryPath(basePath)
	endpoint := basePath + strings.TrimPrefix(liveReloadEndpoint, "/")
	return []byte(`<script data-obsite-livereload>(function(){if(!window.EventSource){return;}var source=new EventSource("` + endpoint + `?obsite-live-reload=1");source.onmessage=function(event){if(event.data==="reload"){source.close();window.location.reload();}};})();</script>`)
}

func containsLiveReloadScriptTag(lowerBody []byte) bool {
	const liveReloadAttribute = "data-obsite-livereload"

	searchStart := 0
	for {
		attributeOffset := bytes.Index(lowerBody[searchStart:], []byte(liveReloadAttribute))
		if attributeOffset == -1 {
			return false
		}
		attributeOffset += searchStart

		tagStart := bytes.LastIndexByte(lowerBody[:attributeOffset], '<')
		if tagStart == -1 {
			searchStart = attributeOffset + len(liveReloadAttribute)
			continue
		}
		if bytes.LastIndexByte(lowerBody[:attributeOffset], '>') > tagStart {
			searchStart = attributeOffset + len(liveReloadAttribute)
			continue
		}

		tagPrefix := bytes.TrimSpace(lowerBody[tagStart+1 : attributeOffset])
		if bytes.HasPrefix(tagPrefix, []byte("script")) {
			return true
		}

		searchStart = attributeOffset + len(liveReloadAttribute)
	}
}

func shouldInjectLiveReload(headers http.Header, body []byte) bool {
	if len(body) == 0 {
		return false
	}

	contentType := strings.ToLower(strings.TrimSpace(headers.Get("Content-Type")))
	if contentType == "" {
		contentType = strings.ToLower(http.DetectContentType(body))
	}

	return strings.HasPrefix(contentType, "text/html")
}

func canonicalPreviewOutputPath(decodedPath string, directory bool) string {
	cleaned := path.Clean("/" + strings.TrimPrefix(decodedPath, "/"))
	if cleaned == "/" {
		return "/"
	}
	segments := strings.Split(strings.Trim(cleaned, "/"), "/")
	for index, segment := range segments {
		segments[index] = slug.EncodeSegment(segment)
	}
	encoded := "/" + strings.Join(segments, "/")
	if directory {
		return ensureDirectoryPath(encoded)
	}
	return encoded
}

func isEditControlPath(cleanPath string) bool {
	return cleanPath == "/_obsite" || strings.HasPrefix(cleanPath, "/_obsite/")
}

func cleanRequestPath(requestPath string) (cleanPath string, hasTrailingSlash bool) {
	if requestPath == "" {
		return "/", true
	}

	hasTrailingSlash = requestPath == "/" || strings.HasSuffix(requestPath, "/")
	cleanPath = path.Clean("/" + strings.TrimPrefix(requestPath, "/"))
	if cleanPath == "." {
		cleanPath = "/"
	}

	return cleanPath, hasTrailingSlash
}

func ensureDirectoryPath(cleanPath string) string {
	if cleanPath == "/" {
		return "/"
	}

	return ensureTrailingSlash(cleanPath)
}

func ensureTrailingSlash(path string) string {
	if path == "/" || strings.HasSuffix(path, "/") {
		return path
	}

	return path + "/"
}

func newLiveReloadHub() *liveReloadHub {
	return &liveReloadHub{clients: make(map[chan string]struct{})}
}

func (h *liveReloadHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is unsupported", http.StatusInternalServerError)
		return
	}

	client := make(chan string, 1)
	h.register(client)
	defer h.unregister(client)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case message := <-client:
			if _, err := fmt.Fprintf(w, "data: %s\n\n", message); err != nil {
				return
			}
			flusher.Flush()
		case <-pingTicker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (h *liveReloadHub) Reload() {
	h.broadcast("reload")
}

func (h *liveReloadHub) register(client chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[client] = struct{}{}
}

func (h *liveReloadHub) unregister(client chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[client]; !ok {
		return
	}
	delete(h.clients, client)
	close(client)
}

func (h *liveReloadHub) broadcast(message string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		select {
		case client <- message:
		default:
		}
	}
}

func newBufferedResponseWriter() *bufferedResponseWriter {
	return &bufferedResponseWriter{header: make(http.Header)}
}

func (w *bufferedResponseWriter) Header() http.Header {
	return w.header
}

func (w *bufferedResponseWriter) Write(body []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	return w.body.Write(body)
}

func (w *bufferedResponseWriter) WriteHeader(statusCode int) {
	if w.statusCode != 0 {
		return
	}
	w.statusCode = statusCode
}

func copyHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func cloneHeaders(src http.Header) http.Header {
	if src == nil {
		return make(http.Header)
	}

	cloned := make(http.Header, len(src))
	copyHeaders(cloned, src)
	return cloned
}
