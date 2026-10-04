package edit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	internalbuild "github.com/simp-lee/obsite/internal/build"
	internalconfig "github.com/simp-lee/obsite/internal/config"
	"github.com/simp-lee/obsite/internal/model"
)

func TestMutationResultReportsPostCommitOutputCleanupWarning(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	server.writeMutationResult(recorder, TransactionResult{Build: &internalbuild.BuildResult{OutputCleanupError: errors.New("/private/vault/.obsite-output-backup")}})
	if recorder.Code != http.StatusOK {
		t.Fatalf("response status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if warning, ok := response["outputCleanupWarning"].(bool); !ok || !warning {
		t.Fatalf("response = %s, want outputCleanupWarning=true", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "/private/vault") {
		t.Fatalf("response leaked cleanup path: %s", recorder.Body.String())
	}
}

func TestEditorPagesUseConfiguredPublicBasePath(t *testing.T) {
	vault := t.TempDir()
	output := t.TempDir()
	hash, err := internalconfig.HashArgon2idPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	writeEditFile(t, vault, "obsite.yaml", "title: Site\nbaseURL: https://example.test/docs/\nnavigation: []\nedit:\n  username: admin\n  passwordHash: "+hash+"\n")
	writeEditFile(t, output, "index.html", `<!doctype html><body data-obsite-base-path="/docs/">public</body></html>`)
	server, err := New(vault, output, 0)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	server.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/_obsite/login", nil))
	if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), `href="/docs/"`) {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}
	server.sessions["test-session"] = session{username: "admin", expires: time.Now().Add(time.Hour)}
	request := httptest.NewRequest(http.MethodGet, "/_obsite/editor", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "test-session"})
	editor := httptest.NewRecorder()
	server.ServeHTTP(editor, request)
	if editor.Code != http.StatusOK || !strings.Contains(editor.Body.String(), `href="/docs/"`) {
		t.Fatalf("editor = %d %s", editor.Code, editor.Body.String())
	}
}

func TestEditServerAuthenticatesBelowReservedControlBoundary(t *testing.T) {
	vault := t.TempDir()
	output := t.TempDir()
	hash, err := internalconfig.HashArgon2idPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	writeEditFile(t, vault, "obsite.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nedit:\n  username: admin\n  passwordHash: "+hash+"\n")
	if err := os.WriteFile(filepath.Join(output, "index.html"), []byte("<!doctype html><body>public</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(output, "article"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "article", "index.html"), []byte("<!doctype html><body>article</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeEditFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeEditFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: doc\n---\nBody\n")
	catalog := &model.SourceCatalog{BasePath: "/", Entries: []model.SourceCatalogEntry{
		{RelPath: "article.md", Route: "/article/", Title: "Article", Kind: "article", Type: "doc", Publish: true, EffectivePublish: true},
		{RelPath: "draft.md", Route: "", Title: "Draft", Kind: "article", Type: "doc", Publish: false},
	}}

	server, err := New(vault, output, 0, catalog)
	if err != nil {
		t.Fatal(err)
	}
	listener := httptest.NewServer(server)
	defer listener.Close()
	client, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	httpClient := listener.Client()
	httpClient.Jar = client

	response, err := httpClient.Get(listener.URL + "/_obsite/login")
	if err != nil {
		t.Fatal(err)
	}
	loginBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(loginBody), "Welcome back") || !strings.Contains(string(loginBody), "login.css") {
		t.Fatalf("login page = %d %q", response.StatusCode, loginBody)
	}
	response, err = httpClient.Get(listener.URL + "/_obsite/login.css")
	if err != nil {
		t.Fatal(err)
	}
	loginCSS, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(loginCSS), ".login-card") {
		t.Fatalf("login CSS = %d %q", response.StatusCode, loginCSS)
	}

	response, err = httpClient.Get(listener.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "public") || strings.Contains(string(body), "edit-page-link") {
		t.Fatalf("anonymous public response = %d %q", response.StatusCode, body)
	}

	response, err = httpClient.Get(listener.URL + "/_obsite/sources")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous sources status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	response, err = httpClient.Get(listener.URL + "/_obsite/session")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("anonymous session status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	loginRequest, err := http.NewRequest(http.MethodPost, listener.URL+"/_obsite/login", strings.NewReader("username=admin&password=secret"))
	if err != nil {
		t.Fatal(err)
	}
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRequest.Header.Set("Origin", "https://evil.example")
	response, err = httpClient.Do(loginRequest)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin login status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
	_ = response.Body.Close()

	loginRequest, err = http.NewRequest(http.MethodPost, listener.URL+"/_obsite/login", strings.NewReader("username=admin&password=secret"))
	if err != nil {
		t.Fatal(err)
	}
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRequest.Header.Set("Origin", listener.URL)
	response, err = httpClient.Do(loginRequest)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("login response = %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie = %#v, want HttpOnly and SameSite=Lax", cookies)
	}
	_ = response.Body.Close()

	response, err = httpClient.Get(listener.URL + "/_obsite/session")
	if err != nil {
		t.Fatal(err)
	}
	sessionBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if !strings.Contains(string(sessionBody), `"authenticated":true`) || !strings.Contains(string(sessionBody), `"csrf":`) {
		t.Fatalf("session body = %s", sessionBody)
	}

	response, err = httpClient.Get(listener.URL + "/article/")
	if err != nil {
		t.Fatal(err)
	}
	articleBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(articleBody), "edit-page-link") {
		t.Fatalf("authenticated article response = %d %q", response.StatusCode, articleBody)
	}

	response, err = httpClient.Get(listener.URL + "/_obsite/sources")
	if err != nil {
		t.Fatal(err)
	}
	sourcesBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if !strings.Contains(string(sourcesBody), "draft.md") || !strings.Contains(string(sourcesBody), "article.md") {
		t.Fatalf("sources body = %s", sourcesBody)
	}

	response, err = httpClient.Get(listener.URL + "/_obsite/source?path=article.md")
	if err != nil {
		t.Fatal(err)
	}
	sourceBody, _ := io.ReadAll(response.Body)
	sourceHash := response.Header.Get("X-Obsite-Source-Hash")
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || string(sourceBody) != "---\ntitle: Article\npublish: true\ntype: doc\n---\nBody\n" || sourceHash == "" {
		t.Fatalf("source response = %d %q", response.StatusCode, sourceBody)
	}

	response, err = httpClient.Get(listener.URL + "/_obsite/session")
	if err != nil {
		t.Fatal(err)
	}
	sessionBody, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	var sessionData struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(sessionBody, &sessionData); err != nil || sessionData.CSRF == "" {
		t.Fatalf("session JSON = %s; err=%v", sessionBody, err)
	}

	request, err := http.NewRequest(http.MethodPut, listener.URL+"/_obsite/source?path=article.md", strings.NewReader("---\ntitle: Article\npublish: true\ntype: doc\n---\nChanged\n"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", listener.URL)
	request.Header.Set(csrfHeaderName, sessionData.CSRF)
	request.Header.Set("X-Obsite-Source-Hash", sourceHash)
	response, err = httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		t.Fatalf("source save response = %d %s", response.StatusCode, body)
	}
	_ = response.Body.Close()
	if got, err := os.ReadFile(filepath.Join(vault, "article.md")); err != nil || !strings.Contains(string(got), "Changed") {
		t.Fatalf("saved source = %q, err=%v", got, err)
	}

	request, err = http.NewRequest(http.MethodPut, listener.URL+"/_obsite/source?path=article.md", strings.NewReader("---\ntitle: Article\npublish: true\ntype: invalid\n---\nRejected\n"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", listener.URL)
	request.Header.Set(csrfHeaderName, sessionData.CSRF)
	request.Header.Set("X-Obsite-Source-Hash", sourceHashForTest([]byte("---\ntitle: Article\npublish: true\ntype: doc\n---\nChanged\n")))
	response, err = httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	diagnosticBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode == http.StatusOK || strings.Contains(string(diagnosticBody), vault) || !strings.Contains(string(diagnosticBody), "article.md") {
		t.Fatalf("diagnostic response = %d %s", response.StatusCode, diagnosticBody)
	}

	request, err = http.NewRequest(http.MethodPost, listener.URL+"/_obsite/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", listener.URL)
	response, err = httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("logout without csrf status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
	_ = response.Body.Close()

	request, err = http.NewRequest(http.MethodPost, listener.URL+"/_obsite/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", listener.URL)
	request.Header.Set(csrfHeaderName, sessionData.CSRF)
	response, err = httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("logout with csrf status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
}

func sourceHashForTest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func writeEditFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
