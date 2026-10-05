package edit

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type fileManagerEntryResponse struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	SourceKind string `json:"sourceKind,omitempty"`
	Editable   bool   `json:"editable"`
	Hash       string `json:"hash"`
	Size       int64  `json:"size,omitempty"`
}

type fileManagerMutationRequest struct {
	Path        string `json:"path"`
	Destination string `json:"destination"`
	Title       string `json:"title"`
	Type        string `json:"type"`
	Date        string `json:"date"`
	Kind        string `json:"kind"`
}

func (s *Server) serveFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.sessionForRequest(r); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	entries, err := s.fileManagerEntries()
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not list files"})
		return
	}
	writeJSON(w, map[string]any{"entries": entries})
}

func (s *Server) serveFileFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeMutation(w, r) {
		return
	}
	var request fileManagerMutationRequest
	if err := decodeFileManagerRequest(r, &request); err != nil {
		http.Error(w, "invalid folder request", http.StatusBadRequest)
		return
	}
	result, err := s.coordinator.CreateFolder(request.Path, fileHashHeader(r))
	if err != nil {
		s.writeMutationError(w, err)
		return
	}
	s.NotifyReload()
	s.writeMutationResult(w, result)
}

func (s *Server) serveFileMarkdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeMutation(w, r) {
		return
	}
	var request fileManagerMutationRequest
	if err := decodeFileManagerRequest(r, &request); err != nil {
		http.Error(w, "invalid Markdown request", http.StatusBadRequest)
		return
	}
	if err := validateManagedMarkdownPath(request.Path); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var (
		content []byte
		err     error
	)
	kind := strings.ToLower(strings.TrimSpace(request.Kind))
	if kind == "section" {
		if path.Base(request.Path) != "_index.md" {
			http.Error(w, "section Markdown files must be named _index.md", http.StatusBadRequest)
			return
		}
		content, err = NewSectionSource(request.Title)
	} else {
		if path.Base(request.Path) == "_index.md" {
			http.Error(w, "_index.md is a section source", http.StatusBadRequest)
			return
		}
		content, err = NewArticleSource(request.Title, request.Type, request.Date)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := s.coordinator.CreateMarkdown(request.Path, fileHashHeader(r), content)
	if err != nil {
		s.writeMutationError(w, err)
		return
	}
	s.NotifyReload()
	s.writeMutationResult(w, result)
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeMutation(w, r) {
		return
	}
	switch r.Method {
	case http.MethodPut:
		var request fileManagerMutationRequest
		if err := decodeFileManagerRequest(r, &request); err != nil || request.Destination == "" {
			http.Error(w, "a destination path is required", http.StatusBadRequest)
			return
		}
		result, err := s.coordinator.RenamePath(r.URL.Query().Get("path"), request.Destination, fileHashHeader(r))
		if err != nil {
			s.writeMutationError(w, err)
			return
		}
		s.NotifyReload()
		s.writeMutationResult(w, result)
	case http.MethodDelete:
		if r.URL.Query().Get("confirm") != "true" {
			http.Error(w, "delete confirmation is required", http.StatusBadRequest)
			return
		}
		result, err := s.coordinator.DeletePath(r.URL.Query().Get("path"), fileHashHeader(r))
		if err != nil {
			s.writeMutationError(w, err)
			return
		}
		s.NotifyReload()
		s.writeMutationResult(w, result)
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func fileHashHeader(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("X-OXPIO-File-Hash"))
	if value == "" {
		value = strings.TrimSpace(r.Header.Get("X-OXPIO-Source-Hash"))
	}
	return value
}

func decodeFileManagerRequest(r *http.Request, request *fileManagerMutationRequest) error {
	data, err := readSourceBody(r)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, request)
}

func (s *Server) fileManagerEntries() ([]fileManagerEntryResponse, error) {
	catalog := s.sourceCatalog()
	sourceKinds := make(map[string]string)
	if catalog != nil {
		for _, entry := range catalog.Entries {
			sourceKinds[entry.RelPath] = entry.Kind
		}
	}
	output, err := filepath.Abs(s.output)
	if err != nil {
		return nil, err
	}
	entries := make([]fileManagerEntryResponse, 0)
	err = filepath.WalkDir(s.vault, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == s.vault {
			return nil
		}
		rel, err := filepath.Rel(s.vault, current)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if output != "" && isSameOrChildPath(current, output) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if fileManagerPathHidden(rel) {
				return filepath.SkipDir
			}
			hash, hashErr := hashManagedDirectory(current, nil)
			if hashErr != nil {
				return hashErr
			}
			editable := s.editableFileManagerTarget(rel, true)
			entries = append(entries, fileManagerEntryResponse{Path: rel, Name: path.Base(rel), Kind: "folder", Editable: editable, Hash: hash})
			return nil
		}
		if entry.Type()&os.ModeType != 0 || !entry.Type().IsRegular() || fileManagerPathHidden(rel) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		hash, err := hashManagedFile(current)
		if err != nil {
			return err
		}
		entries = append(entries, fileManagerEntryResponse{
			Path: rel, Name: path.Base(rel), Kind: "file", SourceKind: sourceKinds[rel],
			Editable: sourceKinds[rel] == "article", Hash: hash, Size: info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		depthI, depthJ := strings.Count(entries[i].Path, "/"), strings.Count(entries[j].Path, "/")
		if depthI != depthJ {
			return depthI < depthJ
		}
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind == "folder"
		}
		return entries[i].Path < entries[j].Path
	})
	return entries, nil
}

func (s *Server) editableFileManagerTarget(relPath string, isDir bool) bool {
	if isDir {
		return s.coordinator == nil || s.coordinator.validateEditableFileManagerTarget(relPath, true) == nil
	}
	entry := s.catalogEntryByRelPath(relPath)
	return entry != nil && entry.Kind == "article"
}

func fileManagerPathHidden(relPath string) bool {
	if strings.EqualFold(relPath, "oxpio.yaml") {
		return true
	}
	for _, segment := range strings.Split(relPath, "/") {
		if segment == "" || strings.HasPrefix(segment, ".") || isManagedReservedSegment(segment) {
			return true
		}
	}
	return false
}
