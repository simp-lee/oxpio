package edit

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	internalbuild "github.com/simp-lee/obsite/internal/build"
	internalconfig "github.com/simp-lee/obsite/internal/config"
)

func TestMediaUploadUsesSelectedExistingFolderAndOneFileCAS(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	hash, err := internalconfig.HashArgon2idPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	writeEditFile(t, vault, "obsite.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nedit:\n  username: admin\n  passwordHash: "+hash+"\n")
	writeEditFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeEditFile(t, vault, "docs/_index.md", "---\ntitle: Docs\npublish: true\n---\nDocs\n")
	built, err := internalbuild.BuildWithOptions(vault, output, internalbuild.Options{})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(vault, output, 0, built.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	server.sessions["session"] = session{username: "admin", csrf: "csrf", expires: time.Now().Add(time.Hour)}
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	request := func(files int, folder string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		for index := 0; index < files; index++ {
			part, partErr := writer.CreateFormFile("file", "pixel.png")
			if partErr != nil {
				t.Fatal(partErr)
			}
			if _, partErr = part.Write(png); partErr != nil {
				t.Fatal(partErr)
			}
		}
		if folder != "" {
			if err := writer.WriteField("folder", folder); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "http://example.test/_obsite/media", &body)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		r.Header.Set("Origin", "http://example.test")
		r.Header.Set(csrfHeaderName, "csrf")
		r.Header.Set("X-Obsite-File-Hash", AbsentSourceHash)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session"})
		response := httptest.NewRecorder()
		server.ServeHTTP(response, r)
		return response
	}
	if response := request(2, "docs"); response.Code != http.StatusBadRequest {
		t.Fatalf("multi-file upload status = %d, body=%s", response.Code, response.Body.String())
	}
	if response := request(1, "missing"); response.Code != http.StatusBadRequest {
		t.Fatalf("missing-folder upload status = %d, body=%s", response.Code, response.Body.String())
	}
	response := request(1, "docs")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "docs/pixel.png") {
		t.Fatalf("selected-folder upload = %d, body=%s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(vault, "docs", "pixel.png")); err != nil {
		t.Fatal(err)
	}
}

func TestValidateMediaPathRejectsProtectedEntries(t *testing.T) {
	for _, pathValue := range []string{
		"../image.png",
		".hidden/image.png",
		".git/image.png",
		"node_modules/image.png",
		"uploads\\image.png",
		"uploads/image.txt",
	} {
		if err := validateMediaPath(pathValue); err == nil {
			t.Fatalf("validateMediaPath(%q) succeeded", pathValue)
		}
	}
}

func TestValidateManagedBoundaryRejectsOutputDescendant(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	if err := validateManagedBoundary(vault, output, "public/image.png"); err == nil {
		t.Fatal("output descendant was accepted")
	}
}
