package edit

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	internalbuild "github.com/simp-lee/obsite/internal/build"
	internalserver "github.com/simp-lee/obsite/internal/server"
)

const previewLifetime = 30 * time.Minute

type editorPreview struct {
	static  *internalserver.Server
	root    string
	expires time.Time
}

func (s *Server) servePreview(w http.ResponseWriter, r *http.Request) {
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
		body = rewritePreviewHTML(body, "/_obsite/preview/"+token)
	}
	for name, values := range recorder.Header() {
		w.Header()[name] = append([]string(nil), values...)
	}
	if r.Method != http.MethodHead {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	}
	w.WriteHeader(recorder.Code)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
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
	root, err := os.MkdirTemp("", ".obsite-preview-*")
	if err != nil {
		http.Error(w, "could not create preview workspace", http.StatusInternalServerError)
		return
	}
	stageOutput := filepath.Join(root, "public")
	built, buildErr := internalbuild.BuildWithOptions(s.vault, stageOutput, internalbuild.Options{SourceOverlay: overlay})
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
	s.previewMu.Lock()
	s.cleanupPreviewsLocked(time.Now())
	s.previews[token] = &editorPreview{static: static, root: root, expires: time.Now().Add(previewLifetime)}
	s.previewMu.Unlock()
	basePath := ""
	if built.Catalog != nil {
		basePath = strings.TrimSuffix(built.Catalog.BasePath, "/")
	}
	response := map[string]any{
		"url":          "/_obsite/preview/" + token + basePath + previewEntry.Route,
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
