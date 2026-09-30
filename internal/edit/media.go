package edit

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	internalasset "github.com/simp-lee/obsite/internal/asset"
	internalfsutil "github.com/simp-lee/obsite/internal/fsutil"
)

const (
	mediaDirectory = "uploads"
	maxUploadBytes = 16 << 20
)

type mediaItem struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.sessionForRequest(r); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if mediaPath := strings.TrimSpace(r.URL.Query().Get("path")); mediaPath != "" {
			s.serveMediaFile(w, r, mediaPath)
			return
		}
		s.serveMediaList(w)
	case http.MethodPost:
		if !s.authorizeMutation(w, r) {
			return
		}
		s.serveMediaUpload(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) serveMediaList(w http.ResponseWriter) {
	items := make([]mediaItem, 0)
	output, _ := filepath.Abs(s.output)
	err := filepath.WalkDir(s.vault, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if current != s.vault && shouldSkipMediaDirectory(current, entry.Name(), output) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() || !internalasset.HasImageExtension(entry.Name()) {
			return nil
		}
		absolute, absoluteErr := filepath.Abs(current)
		if absoluteErr != nil || isSameOrChildPath(absolute, output) {
			return nil
		}
		rel, relErr := filepath.Rel(s.vault, current)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if fileManagerPathHidden(rel) || validateManagedBoundary(s.vault, s.output, rel) != nil {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		items = append(items, mediaItem{Path: rel, Name: filepath.Base(rel), Size: info.Size(), URL: "/_obsite/media?path=" + url.QueryEscape(rel)})
		return nil
	})
	if err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "could not list media"})
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	writeJSON(w, map[string]any{"items": items})
}

func shouldSkipMediaDirectory(current, name, output string) bool {
	if isManagedReservedSegment(name) || strings.HasPrefix(name, ".") {
		return true
	}
	absolute, err := filepath.Abs(current)
	return err == nil && output != "" && isSameOrChildPath(absolute, output)
}

func (s *Server) serveMediaFile(w http.ResponseWriter, r *http.Request, relPath string) {
	if err := validateMediaPath(relPath); err != nil {
		http.NotFound(w, r)
		return
	}
	_, file, info, err := internalfsutil.OpenContainedRegularFile(s.vault, relPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = file.Close() }()
	w.Header().Set("Cache-Control", "private, max-age=60")
	http.ServeContent(w, r, filepath.Base(relPath), info.ModTime(), file)
}

func (s *Server) serveMediaUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		http.Error(w, "invalid or oversized upload", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	defer func() { _ = file.Close() }()
	name := path.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	if name == "." || name == "" || !internalfsutil.IsPortableSitePath(name) || !internalasset.HasImageExtension(name) {
		http.Error(w, "only portable image filenames are accepted", http.StatusBadRequest)
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
	if err != nil || len(data) > maxUploadBytes {
		http.Error(w, "upload exceeds 16 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	if !isUploadedImage(name, data) {
		http.Error(w, "uploaded content is not a supported image", http.StatusUnsupportedMediaType)
		return
	}
	if err := s.ensureMediaDirectory(); err != nil {
		http.Error(w, "could not create media directory", http.StatusInternalServerError)
		return
	}
	relPath := path.Join(mediaDirectory, name)
	if err := validateMediaPath(relPath); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resolved, _, err := internalfsutil.InspectContainedRegularFile(s.vault, relPath)
	if err == nil {
		_ = resolved
		http.Error(w, "a media file with that name already exists", http.StatusConflict)
		return
	}
	if !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "could not inspect media destination", http.StatusInternalServerError)
		return
	}
	parent, _, parentErr := internalfsutil.InspectContainedDirectory(s.vault, mediaDirectory)
	if parentErr != nil {
		http.Error(w, "could not inspect media directory", http.StatusInternalServerError)
		return
	}
	filePath := filepath.Join(parent, name)
	output, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			http.Error(w, "a media file with that name already exists", http.StatusConflict)
			return
		}
		http.Error(w, "could not create media file", http.StatusInternalServerError)
		return
	}
	_, writeErr := output.Write(data)
	closeErr := output.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(filePath)
		http.Error(w, "could not save media file", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"path": relPath, "name": name, "size": len(data), "url": "/_obsite/media?path=" + url.QueryEscape(relPath)})
}

func (s *Server) ensureMediaDirectory() error {
	if _, _, err := internalfsutil.InspectContainedDirectory(s.vault, mediaDirectory); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(filepath.Join(s.vault, mediaDirectory), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	_, _, err := internalfsutil.InspectContainedDirectory(s.vault, mediaDirectory)
	return err
}

func validateMediaPath(relPath string) error {
	relPath = strings.TrimSpace(relPath)
	if err := validateManagedRelPath(relPath); err != nil {
		return fmt.Errorf("media path %q is not a contained portable path", relPath)
	}
	if !internalasset.HasImageExtension(relPath) {
		return fmt.Errorf("media path %q is not a supported image", relPath)
	}
	return nil
}

func isUploadedImage(name string, data []byte) bool {
	if strings.EqualFold(filepath.Ext(name), ".svg") {
		text := strings.ToLower(string(data))
		return strings.Contains(text, "<svg") || strings.Contains(text, "<?xml")
	}
	contentType := http.DetectContentType(data)
	return strings.HasPrefix(contentType, "image/")
}

func isSameOrChildPath(candidate, root string) bool {
	if candidate == "" || root == "" {
		return false
	}
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}
