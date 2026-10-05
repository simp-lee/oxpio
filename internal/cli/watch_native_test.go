package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestFSNotifyWatchLoopRebuildsFixedInputsAndExcludesOutput(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	themeDir := filepath.Join(vault, ".oxpio", "theme")
	for _, dir := range []string{filepath.Join(vault, "notes"), filepath.Join(vault, ".obsidian"), themeDir, filepath.Join(vault, "node_modules"), filepath.Join(vault, ".hidden"), output} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeNativeWatchFile(t, filepath.Join(vault, defaultConfigFilename), "title: Garden\nbaseURL: https://example.com/\n")
	writeNativeWatchFile(t, filepath.Join(vault, "custom.css"), "body{}")
	writeNativeWatchFile(t, filepath.Join(themeDir, "theme.css"), ":root{}")
	writeNativeWatchFile(t, filepath.Join(vault, ".obsidian", "app.json"), "{}")

	watcher, err := newFSNotifyWatcher()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := make(chan error, 32)
	errorsSeen := make(chan error, 8)
	var failNext atomic.Bool
	var reloads atomic.Int32
	if err := startServeWatchLoop(ctx, serveWatchLoop{
		watcher:    watcher,
		vaultPath:  vault,
		outputPath: output,
		configPath: filepath.Join(vault, defaultConfigFilename),
		debounce:   20 * time.Millisecond,
		rebuild: func() error {
			if failNext.Swap(false) {
				err := errors.New("synthetic rebuild failure")
				attempts <- err
				return err
			}
			attempts <- nil
			return nil
		},
		notifyReload: func() { reloads.Add(1) },
		onError:      func(err error) { errorsSeen <- err },
	}); err != nil {
		t.Fatal(err)
	}

	expectRebuild := func(action func()) {
		t.Helper()
		drainNativeAttempts(attempts)
		before := reloads.Load()
		action()
		select {
		case err := <-attempts:
			if err != nil {
				t.Fatalf("rebuild error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for native watch rebuild")
		}
		deadline := time.Now().Add(2 * time.Second)
		for reloads.Load() <= before && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if reloads.Load() <= before {
			t.Fatal("successful rebuild did not notify reload")
		}
		time.Sleep(40 * time.Millisecond)
	}

	note := filepath.Join(vault, "notes", "note.md")
	renamedNote := filepath.Join(vault, "notes", "renamed.md")
	expectRebuild(func() { writeNativeWatchFile(t, note, "# Note") })
	expectRebuild(func() { writeNativeWatchFile(t, note, "# Modified") })
	expectRebuild(func() {
		if err := os.Rename(note, renamedNote); err != nil {
			t.Fatal(err)
		}
	})
	expectRebuild(func() {
		if err := os.Remove(renamedNote); err != nil {
			t.Fatal(err)
		}
	})
	for filePath, content := range map[string]string{
		filepath.Join(vault, "image.png"):             "asset",
		filepath.Join(vault, defaultConfigFilename):   "title: Changed\nbaseURL: https://example.com/\n",
		filepath.Join(vault, "custom.css"):            "body{color:red}",
		filepath.Join(themeDir, "theme.css"):          ":root{color:red}",
		filepath.Join(vault, ".obsidian", "app.json"): `{"attachmentFolderPath":"assets"}`,
	} {
		path, data := filePath, content
		expectRebuild(func() { writeNativeWatchFile(t, path, data) })
	}

	expectRebuild(func() {
		if err := os.RemoveAll(themeDir); err != nil {
			t.Fatal(err)
		}
	})
	incomingThemeDir := filepath.Join(filepath.Dir(vault), filepath.Base(vault)+"-incoming-theme")
	if err := os.MkdirAll(incomingThemeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeNativeWatchFile(t, filepath.Join(incomingThemeDir, "theme.css"), ":root{color:purple}")
	expectRebuild(func() {
		if err := os.Rename(incomingThemeDir, themeDir); err != nil {
			t.Fatal(err)
		}
	})

	obsidianDir := filepath.Join(vault, ".obsidian")
	expectRebuild(func() {
		if err := os.RemoveAll(obsidianDir); err != nil {
			t.Fatal(err)
		}
	})
	incomingObsidianDir := filepath.Join(filepath.Dir(vault), filepath.Base(vault)+"-incoming-obsidian")
	if err := os.MkdirAll(incomingObsidianDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeNativeWatchFile(t, filepath.Join(incomingObsidianDir, "app.json"), `{"attachmentFolderPath":"one-shot"}`)
	expectRebuild(func() {
		if err := os.Rename(incomingObsidianDir, obsidianDir); err != nil {
			t.Fatal(err)
		}
	})

	for filePath := range map[string]struct{}{
		filepath.Join(output, "index.html"):                 {},
		filepath.Join(vault, "node_modules", "ignored.md"):  {},
		filepath.Join(vault, ".oxpio", "ignored.txt"):      {},
		filepath.Join(vault, ".obsidian", "workspace.json"): {},
	} {
		drainNativeAttempts(attempts)
		writeNativeWatchFile(t, filePath, "ignored")
		select {
		case err := <-attempts:
			t.Fatalf("excluded path %q triggered rebuild: %v", filePath, err)
		case <-time.After(120 * time.Millisecond):
		}
	}

	failNext.Store(true)
	drainNativeAttempts(attempts)
	before := reloads.Load()
	writeNativeWatchFile(t, filepath.Join(vault, "custom.css"), "broken")
	select {
	case err := <-attempts:
		if err == nil {
			t.Fatal("failed rebuild returned nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for failed rebuild")
	}
	select {
	case <-errorsSeen:
	case <-time.After(2 * time.Second):
		t.Fatal("failed rebuild was not reported")
	}
	if reloads.Load() != before {
		t.Fatal("failed rebuild notified reload")
	}
	expectRebuild(func() { writeNativeWatchFile(t, filepath.Join(vault, "custom.css"), "fixed") })
}

func writeNativeWatchFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func drainNativeAttempts(attempts <-chan error) {
	for {
		select {
		case <-attempts:
		default:
			return
		}
	}
}
