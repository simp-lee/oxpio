package edit

import (
	"fmt"
	"net/http"
	"path"
	"strings"

	internalrender "github.com/simp-lee/oxpio/internal/render"
	internaltheme "github.com/simp-lee/oxpio/internal/theme"
)

func (s *Server) serveEditorRuntimeAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.sessionForRequest(r); !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	requestPath := r.URL.EscapedPath()
	if requestPath == "" {
		requestPath = r.URL.Path
	}
	relative := strings.TrimPrefix(requestPath, s.controlPrefix()+"editor-runtime/")
	if relative == "" || strings.Contains(relative, "..") {
		http.NotFound(w, r)
		return
	}
	if relative == "task-status.css" {
		files, err := internaltheme.BaseFiles()
		if err != nil {
			http.Error(w, "editor runtime unavailable", http.StatusInternalServerError)
			return
		}
		data, ok := files["assets/style.css"]
		if !ok {
			http.Error(w, "editor runtime unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
		return
	}
	if relative == "wasm_exec.js" || relative == "live-preview.wasm" {
		data, err := editorWeb.ReadFile("web/" + relative)
		if err != nil {
			http.Error(w, "editor runtime unavailable", http.StatusInternalServerError)
			return
		}
		contentType := "text/javascript; charset=utf-8"
		if relative == "live-preview.wasm" {
			contentType = "application/wasm"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
		return
	}
	wanted := path.Join("assets/oxpio-runtime", relative)
	assets, err := internalrender.RuntimeAssetData(true, true)
	if err != nil {
		http.Error(w, fmt.Sprintf("editor runtime unavailable: %v", err), http.StatusInternalServerError)
		return
	}
	for _, asset := range assets {
		if asset.OutputPath != wanted {
			continue
		}
		contentType := "application/octet-stream"
		switch {
		case strings.HasSuffix(asset.OutputPath, ".js"):
			contentType = "text/javascript; charset=utf-8"
		case strings.HasSuffix(asset.OutputPath, ".css"):
			contentType = "text/css; charset=utf-8"
		case strings.HasSuffix(asset.OutputPath, ".woff2"):
			contentType = "font/woff2"
		case strings.HasSuffix(asset.OutputPath, ".woff"):
			contentType = "font/woff"
		case strings.HasSuffix(asset.OutputPath, ".ttf"):
			contentType = "font/ttf"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(asset.Data)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(asset.Data)
		}
		return
	}
	http.NotFound(w, r)
}
