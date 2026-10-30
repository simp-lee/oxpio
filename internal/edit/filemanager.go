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
	Hash       string `json:"hash"`
	Size       int64  `json:"size,omitempty"`
	UsageCount int    `json:"usageCount,omitempty"`
}

type fileManagerMutationRequest struct {
	Path        string `json:"path"`
	Destination string `json:"destination"`
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
	entries, err := s.fileManagerEntries(r.URL.Query().Get("usage") == "unused")
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not list files"})
		return
	}
	writeJSON(w, map[string]any{"entries": entries})
}

func (s *Server) serveFileUsages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.sessionForRequest(r); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	relPath := r.URL.Query().Get("path")
	if err := validateManagedRelPath(relPath); err != nil {
		http.Error(w, "invalid resource path", http.StatusBadRequest)
		return
	}
	usages, err := ResourceUsages(s.vault, relPath)
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not scan resource usages"})
		return
	}
	documents := make(map[string]struct{}, len(usages))
	for _, usage := range usages {
		documents[usage.Document] = struct{}{}
	}
	writeJSON(w, map[string]any{"path": relPath, "count": len(usages), "documentCount": len(documents), "usages": usages})
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
	result, err := s.coordinator.CreateFolderContext(r.Context(), request.Path, fileHashHeader(r))
	if err != nil {
		s.writeMutationError(w, err)
		return
	}
	if result.Build != nil {
		s.NotifyReload()
	}
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
		result, err := s.coordinator.RenamePathContext(r.Context(), r.URL.Query().Get("path"), request.Destination, fileHashHeader(r))
		if err != nil {
			s.writeMutationError(w, err)
			return
		}
		if result.Build != nil {
			s.NotifyReload()
		}
		s.writeMutationResult(w, result)
	case http.MethodDelete:
		if r.URL.Query().Get("confirm") != "true" {
			http.Error(w, "delete confirmation is required", http.StatusBadRequest)
			return
		}
		result, err := s.coordinator.DeletePathContext(r.Context(), r.URL.Query().Get("path"), fileHashHeader(r))
		if err != nil {
			s.writeMutationError(w, err)
			return
		}
		if result.Build != nil {
			s.NotifyReload()
		}
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

func (s *Server) fileManagerEntries(includeUsage bool) ([]fileManagerEntryResponse, error) {
	var usageCounts map[string]int
	if includeUsage {
		var err error
		usageCounts, err = ResourceUsageCounts(s.vault)
		if err != nil {
			return nil, err
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
			entries = append(entries, fileManagerEntryResponse{Path: rel, Name: path.Base(rel), Kind: "folder", Hash: hash})
			return nil
		}
		if entry.Type()&os.ModeType != 0 || !entry.Type().IsRegular() || fileManagerPathHidden(rel) {
			return nil
		}
		if strings.EqualFold(path.Ext(rel), ".md") {
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
			Path: rel, Name: path.Base(rel), Kind: "file",
			Hash: hash, Size: info.Size(), UsageCount: usageCounts[rel],
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
