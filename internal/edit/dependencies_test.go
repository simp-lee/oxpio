package edit

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testClock struct {
	now time.Time
}

func (clock *testClock) Now() time.Time { return clock.now }

func TestServerUsesInjectedClockForSessionExpiry(t *testing.T) {
	clock := &testClock{now: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)}
	server := &Server{
		clock: clock,
		sessions: map[string]session{
			"session": {username: "admin", expires: clock.now.Add(time.Hour)},
		},
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session"})

	if _, ok := server.sessionForRequest(request); !ok {
		t.Fatal("session is unexpectedly expired")
	}
	clock.now = clock.now.Add(2 * time.Hour)
	if _, ok := server.sessionForRequest(request); ok {
		t.Fatal("expired session was accepted")
	}
}

func TestPreviewExpiryUsesInjectedClock(t *testing.T) {
	clock := &testClock{now: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)}
	server := &Server{
		clock: clock,
		previews: map[string]*editorPreview{
			"preview": {expires: clock.now.Add(time.Hour)},
		},
	}
	if preview, err := server.lookupPreview("preview"); err != nil || preview == nil {
		t.Fatalf("preview lookup before expiry = (%v, %v), want a preview", preview, err)
	}
	clock.now = clock.now.Add(2 * time.Hour)
	if preview, err := server.lookupPreview("preview"); err != nil || preview != nil {
		t.Fatalf("preview lookup after expiry = (%v, %v), want nil", preview, err)
	}
}

func TestNewTokenUsesInjectedRandomSource(t *testing.T) {
	data := bytes.Repeat([]byte{0xA5}, 32)
	got, err := newToken(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	want := base64.RawURLEncoding.EncodeToString(data)
	if got != want {
		t.Fatalf("token = %q, want %q", got, want)
	}
}

func TestWithWorkspaceMutationUsesInjectedClock(t *testing.T) {
	vault := t.TempDir()
	clock := &testClock{now: time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)}
	var createdAt string
	err := withWorkspaceMutation(vault, "theme-write", []string{"b.css", "a.css"}, clock, func() error {
		entries, err := os.ReadDir(filepath.Join(vault, ".oxpio", "transactions"))
		if err != nil {
			return err
		}
		if len(entries) != 1 {
			t.Fatalf("transaction entries = %d, want 1", len(entries))
		}
		data, err := os.ReadFile(filepath.Join(vault, ".oxpio", "transactions", entries[0].Name()))
		if err != nil {
			return err
		}
		var intent struct {
			CreatedAt string `json:"createdAt"`
		}
		if err := json.Unmarshal(data, &intent); err != nil {
			return err
		}
		createdAt = intent.CreatedAt
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := clock.now.UTC().Format(time.RFC3339Nano)
	if createdAt != want {
		t.Fatalf("createdAt = %q, want %q", createdAt, want)
	}
}
