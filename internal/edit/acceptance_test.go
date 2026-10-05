package edit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	internalbuild "github.com/simp-lee/oxpio/internal/build"
	internalconfig "github.com/simp-lee/oxpio/internal/config"
)

func TestEditHTTPAcceptanceMatrix(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	hash, err := internalconfig.HashArgon2idPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	writeEditFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nedit:\n  username: admin\n  passwordHash: "+hash+"\n")
	writeEditFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeEditFile(t, vault, "guide/_index.md", "---\ntitle: Guide\npublish: true\n---\nGuide\n")
	original := "---\n# keep this comment\ntitle: \"Article\"\npublish: true\ntype: doc\n---\n\nOriginal\n"
	writeEditFile(t, vault, "article.md", original)
	writeEditFile(t, vault, "draft.md", "---\ntitle: Draft\npublish: false\ntype: doc\n---\nPrivate\n")

	built, err := internalbuild.BuildWithOptions(vault, output, internalbuild.Options{})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(vault, output, 0, built.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server)
	defer ts.Close()

	client, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	httpClient := ts.Client()
	httpClient.Jar = client

	assertStatusBody := func(method, path string, wantStatus int, want, notWant string) {
		t.Helper()
		request, err := http.NewRequest(method, ts.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := httpClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != wantStatus {
			t.Fatalf("%s %s status = %d, want %d; body=%s", method, path, response.StatusCode, wantStatus, body)
		}
		if want != "" && !strings.Contains(string(body), want) {
			t.Fatalf("%s %s body = %q, want %q", method, path, body, want)
		}
		if notWant != "" && strings.Contains(string(body), notWant) {
			t.Fatalf("%s %s body = %q, do not want %q", method, path, body, notWant)
		}
	}

	assertStatusBody(http.MethodGet, "/article/", http.StatusOK, "Original", "edit-page-link")
	assertStatusBody(http.MethodGet, "/_oxpio/source?path=article.md", http.StatusUnauthorized, "", "Original")
	assertStatusBody(http.MethodGet, "/_oxpio/editor?path=article.md", http.StatusUnauthorized, "", "OXPIO editor")
	assertStatusBody(http.MethodGet, "/draft/", http.StatusNotFound, "", "Private")

	loginRequest, err := http.NewRequest(http.MethodPost, ts.URL+"/_oxpio/login", strings.NewReader("username=admin&password=secret"))
	if err != nil {
		t.Fatal(err)
	}
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRequest.Header.Set("Origin", ts.URL)
	response, err := httpClient.Do(loginRequest)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	session := acceptanceSession(t, httpClient, ts.URL)
	if session.CSRF == "" {
		t.Fatal("login returned no CSRF token")
	}
	assertStatusBody(http.MethodGet, "/article/", http.StatusOK, "edit-page-link", "")
	assertStatusBody(http.MethodGet, "/guide/", http.StatusOK, "edit-page-link", "")

	response, err = httpClient.Get(ts.URL + "/_oxpio/sources")
	if err != nil {
		t.Fatal(err)
	}
	sourcesBody, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(sourcesBody), "guide/_index.md") || !strings.Contains(string(sourcesBody), "draft.md") {
		t.Fatalf("source catalog = %d %s", response.StatusCode, sourcesBody)
	}

	readResponse, err := httpClient.Get(ts.URL + "/_oxpio/source?path=article.md")
	if err != nil {
		t.Fatal(err)
	}
	readBody, _ := io.ReadAll(readResponse.Body)
	oldHash := readResponse.Header.Get("X-OXPIO-Source-Hash")
	_ = readResponse.Body.Close()
	if string(readBody) != original || oldHash == "" {
		t.Fatalf("source read = %q, hash=%q", readBody, oldHash)
	}

	updated := "---\n# keep this comment\ntitle: \"Article\"\npublish: true\ntype: doc\n---\n\nChanged\n"
	mutation := func(method, path string, body io.Reader, expectedHash string, origin string) (*http.Response, []byte) {
		t.Helper()
		request, err := http.NewRequest(method, ts.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", origin)
		request.Header.Set(csrfHeaderName, session.CSRF)
		request.Header.Set("X-OXPIO-Source-Hash", expectedHash)
		if method == http.MethodPost {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		response, err := httpClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return response, data
	}

	response, body := mutation(http.MethodPut, "/_oxpio/source?path=article.md", strings.NewReader(updated), oldHash, "https://evil.example")
	if response.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "csrf validation failed") {
		t.Fatalf("cross-origin save = %d %s", response.StatusCode, body)
	}
	response, body = mutation(http.MethodPut, "/_oxpio/source?path=article.md", strings.NewReader(updated), oldHash, ts.URL)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("source save = %d %s", response.StatusCode, body)
	}
	if got, err := os.ReadFile(filepath.Join(vault, "article.md")); err != nil || string(got) != updated {
		t.Fatalf("source-preserving save = %q, err=%v", got, err)
	}

	response, body = mutation(http.MethodPut, "/_oxpio/source?path=article.md", strings.NewReader("stale"), oldHash, ts.URL)
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(body), "source conflict") {
		t.Fatalf("stale save = %d %s, want conflict", response.StatusCode, body)
	}

	beforeFailed, err := os.ReadFile(filepath.Join(vault, "article.md"))
	if err != nil {
		t.Fatal(err)
	}
	response, body = mutation(http.MethodPut, "/_oxpio/source?path=article.md", strings.NewReader("---\ntitle: broken\npublish: true\ntype: invalid\n---\nfailed\n"), sourceHashForTest([]byte(updated)), ts.URL)
	if response.StatusCode == http.StatusOK || !strings.Contains(string(body), "article.md") || strings.Contains(string(body), vault) {
		t.Fatalf("failed save = %d %s", response.StatusCode, body)
	}
	if afterFailed, err := os.ReadFile(filepath.Join(vault, "article.md")); err != nil || string(afterFailed) != string(beforeFailed) {
		t.Fatalf("failed save changed source = %q, err=%v", afterFailed, err)
	}

	form := url.Values{"path": {"new.md"}, "title": {"New draft"}, "type": {"doc"}}
	response, body = mutation(http.MethodPost, "/_oxpio/source", strings.NewReader(form.Encode()), AbsentSourceHash, ts.URL)
	responseBody := string(body)
	if response.StatusCode != http.StatusOK || !strings.Contains(responseBody, `"ok":true`) {
		t.Fatalf("create draft = %d %s", response.StatusCode, body)
	}
	newDraft, err := os.ReadFile(filepath.Join(vault, "new.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(newDraft) != "---\ntitle: \"New draft\"\npublish: false\ntype: doc\n---\n\n" {
		t.Fatalf("default draft = %q", newDraft)
	}
	assertStatusBody(http.MethodGet, "/new/", http.StatusNotFound, "", "New draft")

	response, body = mutation(http.MethodPost, "/_oxpio/source", strings.NewReader(form.Encode()), AbsentSourceHash, ts.URL)
	if response.StatusCode == http.StatusOK {
		t.Fatalf("duplicate create unexpectedly succeeded: %s", body)
	}
	postForm := url.Values{"path": {"dated.md"}, "title": {"A post"}, "type": {"post"}, "date": {"2025-01-02"}}
	response, body = mutation(http.MethodPost, "/_oxpio/source", strings.NewReader(postForm.Encode()), AbsentSourceHash, ts.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("create post = %d %s", response.StatusCode, body)
	}
	postSource, err := os.ReadFile(filepath.Join(vault, "dated.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(postSource), "date: \"2025-01-02\"") || !strings.Contains(string(postSource), "publish: false") {
		t.Fatalf("post source = %q", postSource)
	}
	missingDate := url.Values{"path": {"missing-date.md"}, "title": {"Bad post"}, "type": {"post"}}
	response, body = mutation(http.MethodPost, "/_oxpio/source", strings.NewReader(missingDate.Encode()), AbsentSourceHash, ts.URL)
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "date is required") {
		t.Fatalf("missing post date = %d %s", response.StatusCode, body)
	}

	response, body = mutation(http.MethodDelete, "/_oxpio/source?path=guide/_index.md&confirm=true", nil, sourceHashForTest([]byte("---\ntitle: Guide\npublish: true\n---\nGuide\n")), ts.URL)
	if response.StatusCode == http.StatusOK || !strings.Contains(string(body), "cannot delete section") {
		t.Fatalf("section delete = %d %s", response.StatusCode, body)
	}
	response, body = mutation(http.MethodDelete, "/_oxpio/source?path=new.md", nil, sourceHashForTest(newDraft), ts.URL)
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "confirmation") {
		t.Fatalf("unconfirmed delete = %d %s", response.StatusCode, body)
	}
	response, body = mutation(http.MethodDelete, "/_oxpio/source?path=new.md&confirm=true", nil, sourceHashForTest(newDraft), ts.URL)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("delete = %d %s", response.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(vault, "new.md")); !os.IsNotExist(err) {
		t.Fatalf("deleted source stat = %v", err)
	}

	response, body = mutation(http.MethodPost, "/_oxpio/source", strings.NewReader(url.Values{"path": {"../escape.md"}, "title": {"Escape"}, "type": {"doc"}}.Encode()), AbsentSourceHash, ts.URL)
	if response.StatusCode == http.StatusOK || !strings.Contains(string(body), "contained normalized Markdown path") {
		t.Fatalf("traversal create = %d %s", response.StatusCode, body)
	}

	logout, err := http.NewRequest(http.MethodPost, ts.URL+"/_oxpio/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	logout.Header.Set("Origin", ts.URL)
	logout.Header.Set(csrfHeaderName, session.CSRF)
	response, err = httpClient.Do(logout)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("logout = %d", response.StatusCode)
	}
	assertStatusBody(http.MethodGet, "/_oxpio/source?path=article.md", http.StatusUnauthorized, "", "Changed")
}

func TestCoordinatorSerializesConcurrentCASMutations(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeEditFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeEditFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	original := "---\ntitle: Article\npublish: true\ntype: doc\n---\nOriginal\n"
	writeEditFile(t, vault, "article.md", original)
	built, err := internalbuild.BuildWithOptions(vault, output, internalbuild.Options{})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(vault, output, built.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	oldHash := sourceHashForTest([]byte(original))
	contents := [][]byte{
		[]byte("---\ntitle: Article\npublish: true\ntype: doc\n---\nOne\n"),
		[]byte("---\ntitle: Article\npublish: true\ntype: doc\n---\nTwo\n"),
	}
	results := make(chan error, len(contents))
	var group sync.WaitGroup
	for _, content := range contents {
		content := content
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := coordinator.Save("article.md", oldHash, content)
			results <- err
		}()
	}
	group.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		var conflict *ConflictError
		if errors.As(err, &conflict) {
			conflicts++
			continue
		}
		t.Fatalf("concurrent save error = %v, want ConflictError", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent save outcomes = successes %d, conflicts %d", successes, conflicts)
	}
	finalSource, err := os.ReadFile(filepath.Join(vault, "article.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(finalSource), "One") && !strings.Contains(string(finalSource), "Two") {
		t.Fatalf("final source = %q", finalSource)
	}
	page, err := os.ReadFile(filepath.Join(output, "article", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "One") && !strings.Contains(string(page), "Two") {
		t.Fatalf("final output = %q", page)
	}
}

func TestEditSetupFailureDoesNotWriteConfig(t *testing.T) {
	vault := t.TempDir()
	original := []byte("title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeEditFile(t, vault, "oxpio.yaml", string(original))
	writeEditFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	var output bytes.Buffer
	if err := Setup(vault, strings.NewReader("admin\nsecret\n"), &output); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("non-interactive setup error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(vault, "oxpio.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) || output.Len() != 0 {
		t.Fatalf("failed setup changed config/output: config=%q output=%q", got, output.String())
	}
}

func TestEditNewArticleTemplatesAndMutationGuards(t *testing.T) {
	doc, err := NewArticleSource("A \"safe\" title", "doc", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(doc) != "---\ntitle: \"A \\\"safe\\\" title\"\npublish: false\ntype: doc\n---\n\n" {
		t.Fatalf("doc template = %q", doc)
	}
	post, err := NewArticleSource("Post", "post", "2025-01-02")
	if err != nil || !strings.Contains(string(post), "date: \"2025-01-02\"") {
		t.Fatalf("post template = %q, err=%v", post, err)
	}
	page, err := NewArticleSource("Page", "page", "")
	if err != nil || !strings.Contains(string(page), "type: page") {
		t.Fatalf("page template = %q, err=%v", page, err)
	}
	for _, test := range []struct {
		typeName string
		date     string
	}{
		{typeName: "post"},
		{typeName: "post", date: "not-a-date"},
	} {
		if _, err := NewArticleSource("Post", test.typeName, test.date); err == nil {
			t.Fatalf("NewArticleSource(%q, %q) error = nil", test.typeName, test.date)
		}
	}

	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeEditFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeEditFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeEditFile(t, vault, "guide/_index.md", "---\ntitle: Guide\npublish: true\n---\nGuide\n")
	writeEditFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: doc\n---\nArticle\n")
	built, err := internalbuild.BuildWithOptions(vault, output, internalbuild.Options{})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(vault, output, built.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, relPath := range []string{"../escape.md", "/absolute.md", ".draft.md", "node_modules/draft.md", "not-markdown.txt", "guide/_index.md"} {
		var err error
		if relPath == "guide/_index.md" {
			_, err = coordinator.Delete(relPath, sourceHashForTest([]byte("wrong")))
		} else {
			_, err = coordinator.Create(relPath, AbsentSourceHash, doc)
		}
		if err == nil {
			t.Fatalf("mutation %q succeeded", relPath)
		}
	}
	if err := os.Symlink(filepath.Join(vault, "article.md"), filepath.Join(vault, "linked.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Create("linked.md", AbsentSourceHash, doc); err == nil {
		t.Fatal("create through symlink succeeded")
	}
}

// acceptanceSession deliberately uses the wire representation so the HTTP
// matrix also verifies the browser-facing session contract.
func acceptanceSession(t *testing.T, client *http.Client, origin string) struct {
	CSRF string `json:"csrf"`
} {
	t.Helper()
	response, err := client.Get(origin + "/_oxpio/session")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var session struct {
		CSRF string `json:"csrf"`
	}
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	return session
}
