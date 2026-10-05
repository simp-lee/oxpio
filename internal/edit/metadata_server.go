package edit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/model"
)

type sourceMetaResponse struct {
	Path        string            `json:"path"`
	Kind        string            `json:"kind"`
	Source      string            `json:"source"`
	Body        string            `json:"body"`
	Frontmatter editorFrontmatter `json:"frontmatter"`
	SourceHash  string            `json:"sourceHash"`
	ParseError  string            `json:"parseError,omitempty"`
}

func (s *Server) serveEditorAsset(w http.ResponseWriter, r *http.Request, name, contentType string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.sessionForRequest(r); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	data, err := editorWeb.ReadFile(name)
	if err != nil {
		http.Error(w, "editor asset unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) serveSourceMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.sessionForRequest(r); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	entry := s.catalogEntryByRelPath(r.URL.Query().Get("path"))
	if entry == nil || (entry.Kind != "article" && entry.Kind != "section") {
		http.NotFound(w, r)
		return
	}
	_, content, _, err := internalfsutil.ReadContainedRegularFile(s.vault, entry.RelPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	response := makeEditorMetaResponse(entry, content)
	writeJSON(w, response)
}

func (s *Server) serveFrontmatter(w http.ResponseWriter, r *http.Request) {
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
	var request editorFrontmatterRequest
	if err := json.Unmarshal(data, &request); err != nil {
		http.Error(w, "invalid frontmatter request", http.StatusBadRequest)
		return
	}
	entry := s.catalogEntryByRelPath(request.Path)
	if entry == nil || (entry.Kind != "article" && entry.Kind != "section") {
		http.NotFound(w, r)
		return
	}
	var source []byte
	switch {
	case request.Source != nil:
		source = []byte(*request.Source)
	default:
		_, source, _, err = internalfsutil.ReadContainedRegularFile(s.vault, entry.RelPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	var updated []byte
	if request.Fields != nil {
		updated, err = applyEditorFrontmatter(source, optionalEditorBody(request.Body), *request.Fields, entry.Kind, request.Changed)
	} else if request.Body != nil {
		updated, err = replaceEditorBody(source, []byte(*request.Body))
	} else {
		updated = source
	}
	if err != nil {
		writeJSONStatus(w, http.StatusUnprocessableEntity, map[string]any{"error": fmt.Sprintf("frontmatter: %v", err)})
		return
	}
	response := makeEditorMetaResponse(entry, updated)
	responseMap := map[string]any{
		"source":      string(updated),
		"body":        response.Body,
		"frontmatter": response.Frontmatter,
	}
	if response.ParseError != "" {
		responseMap["parseError"] = response.ParseError
	}
	writeJSON(w, responseMap)
}

func optionalEditorBody(body *string) []byte {
	if body == nil {
		return nil
	}
	return []byte(*body)
}

func replaceEditorBody(source, body []byte) ([]byte, error) {
	document, err := parseEditorDocument(source)
	if err != nil {
		return nil, err
	}
	if !document.HasBlock {
		return normalizeEditorBody(body), nil
	}
	return append(append([]byte(nil), source[:document.CloseEnd]...), normalizeEditorBody(body)...), nil
}

func makeEditorMetaResponse(entry *model.SourceCatalogEntry, content []byte) sourceMetaResponse {
	document, err := parseEditorDocument(content)
	response := sourceMetaResponse{Path: entry.RelPath, Kind: entry.Kind, Source: string(content), Body: string(document.Body)}
	hash := sha256.Sum256(content)
	response.SourceHash = hex.EncodeToString(hash[:])
	if err != nil {
		response.ParseError = err.Error()
		return response
	}
	response.Frontmatter = document.Frontmatter
	return response
}
