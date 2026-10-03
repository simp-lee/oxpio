package edit

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
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
	_ "golang.org/x/image/webp"
)

const maxUploadBytes = 16 << 20

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
	if len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["file"]) != 1 {
		http.Error(w, "upload exactly one file", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	defer func() { _ = file.Close() }()
	name := path.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	if name == "." || name == "" || !internalfsutil.IsPortableSitePath(name) {
		http.Error(w, "only portable filenames are accepted", http.StatusBadRequest)
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
	if err != nil || len(data) > maxUploadBytes {
		http.Error(w, "upload exceeds 16 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	folder := strings.TrimSpace(r.FormValue("folder"))
	if folder != "" {
		if err := validateManagedRelPath(folder); err != nil {
			http.Error(w, "target folder is invalid", http.StatusBadRequest)
			return
		}
		if _, _, err := internalfsutil.InspectContainedDirectory(s.vault, folder); err != nil {
			http.Error(w, "target folder must already exist", http.StatusBadRequest)
			return
		}
	}
	relPath := name
	if folder != "" {
		relPath = path.Join(folder, name)
	}
	if strings.EqualFold(path.Ext(name), ".md") {
		kind := strings.ToLower(strings.TrimSpace(r.FormValue("kind")))
		if path.Base(name) == "_index.md" && kind != "section" {
			http.Error(w, "_index.md uploads require section kind", http.StatusBadRequest)
			return
		}
		if path.Base(name) != "_index.md" && kind == "section" {
			http.Error(w, "section uploads must be named _index.md", http.StatusBadRequest)
			return
		}
	} else if err := validateUploadImage(name, data); err != nil {
		http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
		return
	}
	if err := validateManagedRelPath(relPath); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	expected := fileHashHeader(r)
	if expected == "" {
		expected = AbsentSourceHash
	}
	result, err := s.coordinator.UploadFile(relPath, expected, data)
	if err != nil {
		s.writeMutationError(w, err)
		return
	}
	s.NotifyReload()
	s.writeMutationResult(w, result)
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
	return validateUploadImage(name, data) == nil
}

func validateUploadImage(name string, data []byte) error {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".svg":
		if err := internalasset.ValidateLocalSVG(data); err != nil {
			return fmt.Errorf("invalid SVG image: %w", err)
		}
	case ".png", ".jpg", ".jpeg", ".webp":
		_, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("invalid image data: %w", err)
		}
		if (ext == ".png" && format != "png") || ((ext == ".jpg" || ext == ".jpeg") && format != "jpeg") || (ext == ".webp" && format != "webp") {
			return fmt.Errorf("image content does not match extension")
		}
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			return fmt.Errorf("invalid image data: %w", err)
		}
	default:
		return fmt.Errorf("only PNG, JPEG, WebP, or SVG uploads are supported")
	}
	return nil
}

func isSameOrChildPath(candidate, root string) bool {
	if candidate == "" || root == "" {
		return false
	}
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}
