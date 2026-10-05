// Package edit provides the explicitly invoked, single-account editor server.
package edit

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/simp-lee/oxpio/internal/branding"
	internalconfig "github.com/simp-lee/oxpio/internal/config"
	"github.com/simp-lee/oxpio/internal/diag"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/model"
	internalserver "github.com/simp-lee/oxpio/internal/server"
	"golang.org/x/term"
)

const (
	sessionCookieName = "oxpio_session"
	csrfHeaderName    = "X-OXPIO-CSRF"
	controlPrefix     = "/_oxpio/"
	sessionLifetime   = 12 * time.Hour
)

// Server combines the read-only generated-site server with the isolated edit
// control boundary. It never writes editor state into generated output.
type Server struct {
	static      *internalserver.Server
	port        int
	vault       string
	output      string
	catalog     *model.SourceCatalog
	coordinator *Coordinator
	username    string
	hash        string

	mu       sync.Mutex
	sessions map[string]session

	previewMu sync.Mutex
	previews  map[string]*editorPreview
}

var setupMu sync.Mutex

type session struct {
	username string
	csrf     string
	expires  time.Time
}

// New validates the configured account and generated output before returning a
// server. It does not listen or modify the vault.
func New(vaultPath, outputPath string, port int, catalogs ...*model.SourceCatalog) (*Server, error) {
	resolvedVault, err := internalfsutil.ResolveVaultPath(vaultPath)
	if err != nil {
		return nil, err
	}
	cfg, err := internalconfig.LoadForBuildWithOutput(resolvedVault, outputPath)
	if err != nil {
		return nil, err
	}
	if cfg.Edit == nil {
		return nil, fmt.Errorf("edit.username and edit.passwordHash must be configured before edit can listen")
	}
	boundary, err := internalfsutil.ResolveVaultOutput(resolvedVault, outputPath)
	if err != nil {
		return nil, err
	}
	static, err := internalserver.New(boundary.OutputPath, port)
	if err != nil {
		return nil, err
	}
	var catalog *model.SourceCatalog
	if len(catalogs) > 0 {
		catalog = catalogs[0]
	}
	coordinator, err := NewCoordinator(resolvedVault, outputPath, catalog)
	if err != nil {
		return nil, err
	}
	return &Server{
		static:      static,
		port:        port,
		vault:       resolvedVault,
		output:      boundary.OutputPath,
		catalog:     catalog,
		coordinator: coordinator,
		username:    cfg.Edit.Username,
		hash:        cfg.Edit.PasswordHash,
		sessions:    make(map[string]session),
		previews:    make(map[string]*editorPreview),
	}, nil
}

// Handler exposes the HTTP handler for httptest and embedding.
func (s *Server) Handler() http.Handler { return s }

// ListenAndServe starts the editor server after construction and validation.
func (s *Server) ListenAndServe() error {
	if s == nil || s.static == nil {
		return fmt.Errorf("edit server is nil")
	}
	return http.ListenAndServe(s.static.Addr(), s)
}

// EnableLiveReload enables the existing static-server reload channel.
func (s *Server) EnableLiveReload() {
	if s != nil && s.static != nil {
		s.static.EnableLiveReload()
	}
}

// NotifyReload broadcasts a reload to connected public pages.
func (s *Server) NotifyReload() {
	if s != nil && s.static != nil {
		s.static.NotifyReload()
	}
}

// ServeHTTP keeps all editor control routes under the reserved prefix and
// delegates every other request to the ordinary read-only output server.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil {
		http.Error(w, "edit server is unavailable", http.StatusInternalServerError)
		return
	}
	if strings.HasPrefix(r.URL.Path, controlPrefix) {
		s.serveControl(w, r)
		return
	}
	if r.URL.Path == strings.TrimSuffix(controlPrefix, "/") {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.sessionForRequest(r); ok {
		if entry := s.catalogEntryForRequest(r); entry != nil && entry.Route != "" {
			s.serveDecoratedStatic(w, r, entry)
			return
		}
	}
	s.static.ServeHTTP(w, r)
}

func (s *Server) serveControl(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, controlPrefix)
	switch path {
	case "login":
		s.serveLogin(w, r)
	case "login.css":
		s.serveLoginAsset(w, r)
	case "logo.svg":
		s.serveLogoAsset(w, r)
	case "editor":
		s.serveEditor(w, r)
	case "editor.css":
		s.serveEditorAsset(w, r, "web/editor.css", "text/css; charset=utf-8")
	case "editor.js":
		s.serveEditorAsset(w, r, "web/editor.bundle.js", "text/javascript; charset=utf-8")
	case "sources":
		s.serveSources(w, r)
	case "files":
		s.serveFiles(w, r)
	case "file/folder":
		s.serveFileFolder(w, r)
	case "file/markdown":
		s.serveFileMarkdown(w, r)
	case "file":
		s.serveFile(w, r)
	case "source-meta":
		s.serveSourceMeta(w, r)
	case "frontmatter":
		s.serveFrontmatter(w, r)
	case "media":
		s.serveMedia(w, r)
	case "preview":
		s.servePreview(w, r)
	case "source":
		s.serveSource(w, r)
	case "logout":
		s.serveLogout(w, r)
	case "session":
		s.serveSession(w, r)
	case "csrf":
		s.serveCSRF(w, r)
	default:
		if strings.HasPrefix(path, "preview/") {
			s.servePreview(w, r)
			return
		}
		http.NotFound(w, r)
	}
}

func (s *Server) serveEditor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.sessionForRequest(r); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	editorBody, err := editorWeb.ReadFile("web/editor.html")
	if err != nil {
		http.Error(w, "editor unavailable", http.StatusInternalServerError)
		return
	}
	editorBody = injectEditorBasePath(editorBody, s.static.BasePath())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(editorBody)
}

type sourceCatalogResponse struct {
	RelPath          string `json:"relPath"`
	Route            string `json:"route,omitempty"`
	Title            string `json:"title"`
	Kind             string `json:"kind"`
	Type             string `json:"type,omitempty"`
	Publish          bool   `json:"publish"`
	EffectivePublish bool   `json:"effectivePublish"`
	SectionPath      string `json:"sectionPath"`
	VersionID        string `json:"versionID,omitempty"`
}

func (s *Server) serveSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.sessionForRequest(r); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	catalog := s.sourceCatalog()
	response := make([]sourceCatalogResponse, 0)
	if catalog != nil {
		response = make([]sourceCatalogResponse, 0, len(catalog.Entries))
		for _, entry := range catalog.Entries {
			response = append(response, sourceCatalogResponse{RelPath: entry.RelPath, Route: entry.Route, Title: entry.Title, Kind: entry.Kind, Type: entry.Type, Publish: entry.Publish, EffectivePublish: entry.EffectivePublish, SectionPath: entry.SectionPath, VersionID: entry.VersionID})
		}
	}
	writeJSON(w, map[string]any{"sources": response})
}

func (s *Server) serveSource(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.serveSourceRead(w, r)
	case http.MethodPut:
		s.serveSourceSave(w, r)
	case http.MethodPost:
		s.serveSourceCreate(w, r)
	case http.MethodDelete:
		s.serveSourceDelete(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) serveSourceRead(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.sessionForRequest(r); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	entry := s.catalogEntryByRelPath(r.URL.Query().Get("path"))
	if entry == nil || (entry.Kind != "article" && entry.Kind != "section") {
		http.NotFound(w, r)
		return
	}
	_, data, _, err := internalfsutil.ReadContainedRegularFile(s.vault, entry.RelPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	hash := sha256.Sum256(data)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-OXPIO-Source-Hash", hex.EncodeToString(hash[:]))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) serveSourceSave(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeMutation(w, r) {
		return
	}
	pathValue := r.URL.Query().Get("path")
	entry := s.catalogEntryByRelPath(pathValue)
	if entry == nil {
		http.NotFound(w, r)
		return
	}
	content, err := readSourceBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	result, err := s.coordinator.Save(entry.RelPath, r.Header.Get("X-OXPIO-Source-Hash"), content)
	if err != nil {
		s.writeMutationError(w, err)
		return
	}
	s.NotifyReload()
	s.writeMutationResult(w, result)
}

func (s *Server) serveSourceCreate(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeMutation(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid create request", http.StatusBadRequest)
		return
	}
	pathValue, title, typeValue, dateValue := r.Form.Get("path"), r.Form.Get("title"), r.Form.Get("type"), r.Form.Get("date")
	content, err := NewArticleSource(title, typeValue, dateValue)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.coordinator.Create(pathValue, r.Header.Get("X-OXPIO-Source-Hash"), content)
	if err != nil {
		s.writeMutationError(w, err)
		return
	}
	s.NotifyReload()
	s.writeMutationResult(w, result)
}

func (s *Server) serveSourceDelete(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeMutation(w, r) {
		return
	}
	if r.URL.Query().Get("confirm") != "true" {
		http.Error(w, "delete confirmation is required", http.StatusBadRequest)
		return
	}
	result, err := s.coordinator.Delete(r.URL.Query().Get("path"), r.Header.Get("X-OXPIO-Source-Hash"))
	if err != nil {
		s.writeMutationError(w, err)
		return
	}
	s.NotifyReload()
	s.writeMutationResult(w, result)
}

func (s *Server) writeMutationResult(w http.ResponseWriter, result TransactionResult) {
	response := map[string]any{"ok": true, "sourceHash": result.SourceHash}
	if result.SourceCleanupError != nil {
		// Report the post-commit warning without exposing private filesystem paths.
		response["sourceCleanupWarning"] = true
	}
	if result.RelPath != "" {
		response["path"] = result.RelPath
	}
	if result.Build != nil {
		response["warningCount"] = result.Build.WarningCount
		response["diagnostics"] = s.editorDiagnostics(result.Build.Diagnostics)
		if result.Build.OutputCleanupError != nil {
			// This is a post-commit operational status. Do not expose filesystem
			// paths or turn a committed source/output pair into a failure.
			response["outputCleanupWarning"] = true
		}
	}
	writeJSON(w, response)
}

func (s *Server) authorizeMutation(w http.ResponseWriter, r *http.Request) bool {
	sess, ok := s.sessionForRequest(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return false
	}
	if !sameOrigin(r) || !validCSRF(r, sess.csrf) {
		http.Error(w, "csrf validation failed", http.StatusForbidden)
		return false
	}
	if s.coordinator == nil {
		http.Error(w, "edit coordinator is unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (s *Server) writeMutationError(w http.ResponseWriter, err error) {
	status := http.StatusUnprocessableEntity
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		status = http.StatusConflict
	}
	var buildErr *CandidateBuildError
	if errors.As(err, &buildErr) && buildErr.Result != nil {
		writeJSONStatus(w, status, map[string]any{"ok": false, "path": buildErr.Path, "error": s.editorDiagnosticText(err.Error()), "diagnostics": s.editorDiagnostics(buildErr.Result.Diagnostics)})
		return
	}
	writeJSONStatus(w, status, map[string]any{"ok": false, "error": s.editorDiagnosticText(err.Error())})
}

func (s *Server) editorDiagnostics(diagnostics []diag.Diagnostic) []diag.Diagnostic {
	result := append([]diag.Diagnostic(nil), diagnostics...)
	root, err := filepath.Abs(s.vault)
	if err != nil {
		return result
	}
	for index := range result {
		result[index].Location.Path = editorDiagnosticPath(root, result[index].Location.Path)
		result[index].Target = editorDiagnosticText(root, result[index].Target)
		result[index].Message = editorDiagnosticText(root, result[index].Message)
	}
	return result
}

func (s *Server) editorDiagnosticText(value string) string {
	root, err := filepath.Abs(s.vault)
	if err != nil {
		return value
	}
	return editorDiagnosticText(root, value)
}

func editorDiagnosticPath(root, value string) string {
	if value == "" || !filepath.IsAbs(value) {
		return filepath.ToSlash(value)
	}
	relative, err := filepath.Rel(root, value)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(relative)
	}
	return filepath.ToSlash(filepath.Base(value))
}

func editorDiagnosticText(root, value string) string {
	if value == "" {
		return value
	}
	for _, prefix := range []string{root + string(filepath.Separator), filepath.ToSlash(root) + "/", root} {
		if prefix != "" {
			value = strings.ReplaceAll(value, prefix, "")
		}
	}
	// Build/staging failures can mention paths outside the vault. Keep the
	// diagnostic useful without returning any machine-local absolute path.
	parts := strings.Fields(value)
	for index, part := range parts {
		leading, trailing := "", ""
		for len(part) > 0 && strings.ContainsRune("([{\"'", rune(part[0])) {
			leading += part[:1]
			part = part[1:]
		}
		for len(part) > 0 && strings.ContainsRune(")]},;\"'", rune(part[len(part)-1])) {
			trailing = part[len(part)-1:] + trailing
			part = part[:len(part)-1]
		}
		if filepath.IsAbs(filepath.FromSlash(part)) || (len(part) > 2 && part[1] == ':' && (part[2] == '/' || part[2] == '\\')) {
			part = "<path>"
		}
		parts[index] = leading + part + trailing
	}
	return strings.Join(parts, " ")
}

func readSourceBody(r *http.Request) ([]byte, error) {
	const maxSourceBytes = 8 << 20
	data, err := io.ReadAll(io.LimitReader(r.Body, maxSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("could not read source")
	}
	if len(data) > maxSourceBytes {
		return nil, fmt.Errorf("source exceeds 8 MiB limit")
	}
	return data, nil
}

func (s *Server) sourceCatalog() *model.SourceCatalog {
	if s == nil {
		return nil
	}
	if s.coordinator != nil {
		return s.coordinator.CatalogSnapshot()
	}
	return cloneCatalog(s.catalog)
}

func (s *Server) catalogEntryByRelPath(relPath string) *model.SourceCatalogEntry {
	catalog := s.sourceCatalog()
	if catalog == nil || relPath == "" {
		return nil
	}
	for index := range catalog.Entries {
		if catalog.Entries[index].RelPath == relPath {
			entry := catalog.Entries[index]
			return &entry
		}
	}
	return nil
}

func (s *Server) catalogEntryForRequest(r *http.Request) *model.SourceCatalogEntry {
	catalog := s.sourceCatalog()
	if catalog == nil || r == nil {
		return nil
	}
	route := r.URL.EscapedPath()
	base := catalog.BasePath
	if base == "" {
		base = "/"
	}
	if base != "/" {
		baseRoot := strings.TrimSuffix(base, "/")
		if route == baseRoot {
			route = "/"
		} else if strings.HasPrefix(route, base) {
			route = strings.TrimPrefix(route, baseRoot)
		} else {
			return nil
		}
	}
	if route == "" {
		route = "/"
	}
	for index := range catalog.Entries {
		entry := &catalog.Entries[index]
		if entry.Route == route && entry.EffectivePublish {
			return entry
		}
	}
	return nil
}

func (s *Server) serveDecoratedStatic(w http.ResponseWriter, r *http.Request, entry *model.SourceCatalogEntry) {
	recorder := httptest.NewRecorder()
	s.static.ServeHTTP(recorder, r)
	status := recorder.Code
	body := append([]byte(nil), recorder.Body.Bytes()...)
	contentType := strings.ToLower(recorder.Header().Get("Content-Type"))
	if r.Method != http.MethodHead && status >= 200 && status < 300 && strings.Contains(contentType, "text/html") {
		link := `<a class="edit-page-link" href="/_oxpio/editor?path=` + url.QueryEscape(entry.RelPath) + `">Edit</a>`
		lower := strings.ToLower(string(body))
		if index := strings.LastIndex(lower, "</body>"); index >= 0 {
			body = append(append(append([]byte(nil), body[:index]...), []byte(link)...), body[index:]...)
		} else {
			body = append(body, []byte(link)...)
		}
	}
	for name, values := range recorder.Header() {
		w.Header()[name] = append([]string(nil), values...)
	}
	if r.Method != http.MethodHead {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func injectEditorBasePath(data []byte, basePath string) []byte {
	if strings.TrimSpace(basePath) == "" {
		basePath = "/"
	}
	return bytes.ReplaceAll(data, []byte("__OXPIO_BASE_PATH__"), []byte(stdhtml.EscapeString(basePath)))
}

func (s *Server) serveLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		htmlBody, err := editorWeb.ReadFile("web/login.html")
		if err != nil {
			http.Error(w, "login unavailable", http.StatusInternalServerError)
			return
		}
		htmlBody = injectEditorBasePath(htmlBody, s.static.BasePath())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(htmlBody)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(htmlBody)
		}
	case http.MethodPost:
		if !sameOrigin(r) {
			http.Error(w, "csrf validation failed", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid login request", http.StatusBadRequest)
			return
		}
		username := r.Form.Get("username")
		password := r.Form.Get("password")
		validUsername := subtle.ConstantTimeCompare([]byte(username), []byte(s.username)) == 1
		validPassword, err := internalconfig.VerifyArgon2idPassword(password, s.hash)
		if err != nil {
			validPassword = false
		}
		if !validUsername || !validPassword {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		sessionID, err := newToken()
		if err != nil {
			http.Error(w, "could not create session", http.StatusInternalServerError)
			return
		}
		csrf, err := newToken()
		if err != nil {
			http.Error(w, "could not create session", http.StatusInternalServerError)
			return
		}
		s.mu.Lock()
		s.sessions[sessionID] = session{username: s.username, csrf: csrf, expires: time.Now().Add(sessionLifetime)}
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: sessionID, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(sessionLifetime / time.Second)})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"authenticated":true}`)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) serveLoginAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data, err := editorWeb.ReadFile("web/login.css")
	if err != nil {
		http.Error(w, "login asset unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func (s *Server) serveLogoAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data := branding.LogoSVG()
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func (s *Server) serveSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sess, ok := s.sessionForRequest(r)
	response := map[string]any{"authenticated": ok}
	if ok {
		response["username"] = sess.username
		response["csrf"] = sess.csrf
	}
	writeJSON(w, response)
}

func (s *Server) serveCSRF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sess, ok := s.sessionForRequest(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]string{"csrf": sess.csrf})
}

func (s *Server) serveLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessionID, sess, ok := s.sessionForRequestWithID(r)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if !sameOrigin(r) || !validCSRF(r, sess.csrf) {
		http.Error(w, "csrf validation failed", http.StatusForbidden)
		return
	}
	s.mu.Lock()
	delete(s.sessions, sessionID)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	writeJSON(w, map[string]bool{"authenticated": false})
}

func (s *Server) sessionForRequest(r *http.Request) (session, bool) {
	_, value, ok := s.sessionForRequestWithID(r)
	return value, ok
}

func (s *Server) sessionForRequestWithID(r *http.Request) (string, session, bool) {
	if r == nil {
		return "", session{}, false
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return "", session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.sessions[cookie.Value]
	if !ok || (!value.expires.IsZero() && time.Now().After(value.expires)) {
		if ok {
			delete(s.sessions, cookie.Value)
		}
		return "", session{}, false
	}
	return cookie.Value, value, true
}

func validCSRF(r *http.Request, expected string) bool {
	got := r.Header.Get(csrfHeaderName)
	if got == "" {
		got = r.FormValue("csrf")
	}
	return expected != "" && subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func sameOrigin(r *http.Request) bool {
	if r == nil {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		origin = strings.TrimSpace(r.Header.Get("Referer"))
	}
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return false
	}
	expectedScheme := "http"
	if r.TLS != nil {
		expectedScheme = "https"
	}
	return strings.EqualFold(u.Scheme, expectedScheme) && strings.EqualFold(u.Host, r.Host)
}

func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeJSON(w, value)
}

func writeJSON(w http.ResponseWriter, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "could not encode response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func newToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

// Setup interactively adds the one edit account to an already valid config.
// It refuses non-terminal input so credentials are never silently generated or
// accepted from a redirected command stream.
func Setup(vaultPath string, input io.Reader, output io.Writer) error {
	setupMu.Lock()
	defer setupMu.Unlock()
	cfg, err := internalconfig.LoadForBuild(vaultPath)
	if err != nil {
		return err
	}
	if cfg.Edit != nil {
		return fmt.Errorf("edit account is already configured")
	}
	file, ok := input.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return fmt.Errorf("edit setup requires an interactive terminal")
	}
	if output == nil {
		output = io.Discard
	}
	resolvedVault, err := internalfsutil.ResolveVaultPath(vaultPath)
	if err != nil {
		return err
	}
	configPath := filepath.Join(resolvedVault, internalconfig.Filename)
	_, original, _, err := internalfsutil.ReadContainedRegularFile(resolvedVault, internalconfig.Filename)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	_, _ = io.WriteString(output, "Username: ")
	username, err := readTerminalLine(file)
	if err != nil {
		return fmt.Errorf("read username: %w", err)
	}
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("username must be non-empty")
	}
	_, _ = io.WriteString(output, "Password: ")
	password, err := term.ReadPassword(int(file.Fd()))
	_, _ = io.WriteString(output, "\n")
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	if len(password) == 0 {
		return fmt.Errorf("password must be non-empty")
	}
	hash, err := internalconfig.HashArgon2idPassword(string(password))
	if err != nil {
		return err
	}
	if err := internalconfig.ValidateArgon2idPasswordHash(hash); err != nil {
		return err
	}
	updated := appendEditConfig(original, username, hash)
	if _, err := internalconfig.ValidateConfigBytes(updated); err != nil {
		return fmt.Errorf("validate generated edit config: %w", err)
	}
	if err := atomicReplaceConfig(resolvedVault, configPath, original, updated); err != nil {
		return err
	}
	_, _ = io.WriteString(output, "Edit account configured.\n")
	return nil
}

func readTerminalLine(file *os.File) (string, error) {
	var data []byte
	one := []byte{0}
	for {
		n, err := file.Read(one)
		if n > 0 {
			if one[0] == '\n' {
				return string(data), nil
			}
			if one[0] != '\r' {
				data = append(data, one[0])
			}
		}
		if err != nil {
			return "", err
		}
	}
}

func appendEditConfig(original []byte, username, hash string) []byte {
	updated := append([]byte(nil), original...)
	if len(updated) != 0 && updated[len(updated)-1] != '\n' {
		updated = append(updated, '\n')
	}
	updated = append(updated, []byte("edit:\n  username: "+fmt.Sprintf("%q", username)+"\n  passwordHash: "+hash+"\n")...)
	return updated
}

// atomicReplaceConfig holds the cross-process lock across both CAS checks and
// the final rename, so another cooperating config writer cannot change the
// file between the last check and commit.
func atomicReplaceConfig(vaultRoot, configPath string, expected, updated []byte) error {
	releaseConfigLock, err := acquireConfigLock(vaultRoot)
	if err != nil {
		return fmt.Errorf("acquire config lock before setup: %w", err)
	}
	defer func() { _ = releaseConfigLock() }()

	_, current, _, err := internalfsutil.ReadContainedRegularFile(vaultRoot, internalconfig.Filename)
	if err != nil {
		return fmt.Errorf("recheck config before setup: %w", err)
	}
	if subtle.ConstantTimeCompare(current, expected) != 1 {
		return errors.New("config changed during edit setup; retry without overwriting the external change")
	}
	if _, _, err := internalfsutil.InspectContainedRegularFile(vaultRoot, configPath); err != nil {
		return fmt.Errorf("inspect config before setup: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(configPath), ".oxpio-config-*")
	if err != nil {
		return fmt.Errorf("create atomic config file: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set atomic config permissions: %w", err)
	}
	if _, err := temporary.Write(updated); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write atomic config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync atomic config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close atomic config: %w", err)
	}
	_, current, _, err = internalfsutil.ReadContainedRegularFile(vaultRoot, internalconfig.Filename)
	if err != nil {
		return fmt.Errorf("recheck config before commit: %w", err)
	}
	if subtle.ConstantTimeCompare(current, expected) != 1 {
		return errors.New("config changed during edit setup; retry without overwriting the external change")
	}
	if err := os.Rename(temporaryName, configPath); err != nil {
		return fmt.Errorf("replace config atomically: %w", err)
	}
	return nil
}
