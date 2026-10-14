package edit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	internalapplication "github.com/simp-lee/oxpio/internal/application"
	internalbuild "github.com/simp-lee/oxpio/internal/build"
	internalserver "github.com/simp-lee/oxpio/internal/server"
)

const previewLifetime = 30 * time.Minute

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
	built, buildErr := internalbuild.BuildWithOptions(s.vault, stageOutput, internalbuild.Options{
		Strict:            true,
		Ephemeral:         true,
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
	previewCatalog := internalapplication.NewCatalog(built.Catalog)
	previewEntry, previewEntryErr := previewCatalog.EntryByRelPath(previewPath, true)
	if previewEntryErr != nil || previewEntry.Route == "" {
		cleanupErr := s.discardUnpublishedPreview(nil, root)
		writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]string{"error": previewFailureMessage("source has no preview route; publish its parent section or repair its frontmatter", cleanupErr)})
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
	token, err := newToken()
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
	contentHTML := rewritePreviewHTML(fullPage, s.controlPrefix()+"preview/"+token)
	s.previewMu.Lock()
	if s.closed {
		s.previewMu.Unlock()
		cleanupErr := s.discardUnpublishedPreview(static, root)
		http.Error(w, previewFailureMessage("editor is closed", cleanupErr), http.StatusServiceUnavailable)
		return
	}
	if err := s.cleanupPreviewsLocked(time.Now()); err != nil {
		s.previewMu.Unlock()
		cleanupErr := errors.Join(err, s.discardUnpublishedPreview(static, root))
		http.Error(w, previewFailureMessage("could not clean up expired preview", cleanupErr), http.StatusInternalServerError)
		return
	}
	delete(s.previewRoots, root)
	s.previews[token] = &editorPreview{static: static, root: root, contentHTML: contentHTML, expires: time.Now().Add(previewLifetime)}
	s.previewMu.Unlock()
	response := map[string]any{
		"url":          fullURL,
		"fullURL":      fullURL,
		"contentURL":   s.controlPrefix() + "preview/" + token + "/content",
		"warningCount": built.WarningCount,
		"diagnostics":  s.editorDiagnostics(built.Diagnostics),
	}
	writeJSON(w, response)
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
	now := time.Now()
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
