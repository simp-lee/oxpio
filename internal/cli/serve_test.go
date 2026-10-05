package cli

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	internalbuild "github.com/simp-lee/oxpio/internal/build"
)

func TestServeCommandDefaultsToCurrentVaultPublicOutput(t *testing.T) {
	vaultPath := t.TempDir()
	t.Chdir(vaultPath)
	deps := testCommandDependencies()
	server := &fakePreviewServer{}
	var gotOutput string
	deps.newPreviewServer = func(output string, port int) (previewServer, error) {
		gotOutput = output
		return server, nil
	}
	_, _, err := executeForTest(t, deps, []string{"serve"})
	if err != nil {
		t.Fatalf("executeForTest() error = %v", err)
	}
	if gotOutput != filepath.Join(vaultPath, "public") {
		t.Fatalf("output = %q", gotOutput)
	}
}

func TestServeCommandUsesServerDefaultPortWhenOmitted(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "site")
	deps := testCommandDependencies()
	server := &fakePreviewServer{}
	var gotOutputPath string
	var gotPort int
	deps.newPreviewServer = func(outputPath string, port int) (previewServer, error) {
		gotOutputPath = outputPath
		gotPort = port
		return server, nil
	}

	_, _, err := executeForTest(t, deps, []string{"serve", "--output", outputPath})
	if err != nil {
		t.Fatalf("executeForTest() error = %v", err)
	}
	if gotOutputPath != outputPath {
		t.Fatalf("newPreviewServer outputPath = %q, want %q", gotOutputPath, outputPath)
	}
	if gotPort != 0 {
		t.Fatalf("newPreviewServer port = %d, want 0 so server.New applies its default", gotPort)
	}
	if server.listenCalls != 1 {
		t.Fatalf("ListenAndServe calls = %d, want 1", server.listenCalls)
	}
}

func TestServeCommandWatchRoutesBuildDiagnosticsAndWatchErrorsToInjectedStderr(t *testing.T) {
	vaultPath := t.TempDir()
	configPath := filepath.Join(vaultPath, defaultConfigFilename)
	outputPath := filepath.Join(t.TempDir(), "site")
	if err := os.WriteFile(configPath, []byte("title: Garden\nbaseURL: https://example.com\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", configPath, err)
	}

	deps := testCommandDependencies()
	watcher := newFakeFileWatcher()
	listenStarted := make(chan struct{}, 1)
	listenBlock := make(chan struct{})
	server := &fakePreviewServer{listenStarted: listenStarted, listenBlock: listenBlock}
	deps.buildSiteWithOptions = func(vaultPath string, outputPath string, options internalbuild.Options) (*internalbuild.BuildResult, error) {
		if options.DiagnosticsWriter == nil {
			t.Fatal("build options DiagnosticsWriter = nil, want injected stderr writer")
		}
		if _, err := options.DiagnosticsWriter.Write([]byte("Warnings (1):\n- build [structured_data] synthetic build warning\n")); err != nil {
			t.Fatalf("DiagnosticsWriter.Write() error = %v", err)
		}
		return &internalbuild.BuildResult{}, nil
	}
	deps.newPreviewServer = func(outputPath string, port int) (previewServer, error) {
		return server, nil
	}
	deps.newFileWatcher = func() (fileWatcher, error) {
		return watcher, nil
	}

	var stdoutBuf lockedBuffer
	var stderrBuf lockedBuffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- executeWithDeps([]string{"serve", "--output", outputPath, "--watch", "--vault", vaultPath}, deps, &stdoutBuf, &stderrBuf)
	}()

	select {
	case <-listenStarted:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out waiting for preview server to start listening")
	}
	waitForServeWatchAddCount(t, watcher, vaultPath, 1)

	watcher.errors <- errors.New("boom")
	waitForLockedBufferContains(t, &stderrBuf, "watch: watcher error: boom")
	close(listenBlock)

	err := <-errCh
	if err != nil {
		t.Fatalf("executeWithDeps() error = %v", err)
	}
	if got := stdoutBuf.String(); got != "" {
		t.Fatalf("stdout = %q, want empty stdout", got)
	}
	stderr := stderrBuf.String()
	if !strings.Contains(stderr, "synthetic build warning") {
		t.Fatalf("stderr = %q, want build diagnostics routed through injected stderr", stderr)
	}
	if !strings.Contains(stderr, "watch: watcher error: boom") {
		t.Fatalf("stderr = %q, want watch error prefix routed through injected stderr", stderr)
	}
	if strings.Count(stderr, "watch: watcher error: boom") != 1 {
		t.Fatalf("stderr = %q, want exactly one watch error entry", stderr)
	}
	if server.enableCalls != 1 {
		t.Fatalf("EnableLiveReload calls = %d, want 1 in watch mode", server.enableCalls)
	}
	if server.listenCalls != 1 {
		t.Fatalf("ListenAndServe calls = %d, want 1", server.listenCalls)
	}
}

func TestServeCommandPassesExplicitPortToPreviewServer(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "site")
	deps := testCommandDependencies()
	server := &fakePreviewServer{}
	var gotOutputPath string
	var gotPort int
	deps.newPreviewServer = func(outputPath string, port int) (previewServer, error) {
		gotOutputPath = outputPath
		gotPort = port
		return server, nil
	}

	stdout, stderr, err := executeForTest(t, deps, []string{"serve", "--output", outputPath, "--port", "9090"})
	if err != nil {
		t.Fatalf("executeForTest() error = %v", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty stdout", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty stderr", stderr)
	}
	if gotOutputPath != outputPath {
		t.Fatalf("newPreviewServer outputPath = %q, want %q", gotOutputPath, outputPath)
	}
	if gotPort != 9090 {
		t.Fatalf("newPreviewServer port = %d, want %d", gotPort, 9090)
	}
	if server.listenCalls != 1 {
		t.Fatalf("ListenAndServe calls = %d, want 1", server.listenCalls)
	}
}

func TestServeCommandPropagatesListenFailure(t *testing.T) {
	deps := testCommandDependencies()
	server := &fakePreviewServer{listenErr: errors.New("bind failed")}
	deps.newPreviewServer = func(outputPath string, port int) (previewServer, error) {
		return server, nil
	}

	_, _, err := executeForTest(t, deps, []string{"serve", "--output", filepath.Join(t.TempDir(), "site"), "--port", "9090"})
	if err == nil {
		t.Fatal("executeForTest() error = nil, want listen failure")
	}
	if !strings.Contains(err.Error(), "listen and serve: bind failed") {
		t.Fatalf("error = %q, want wrapped listen failure", err.Error())
	}
	if server.listenCalls != 1 {
		t.Fatalf("ListenAndServe calls = %d, want 1", server.listenCalls)
	}
}

func TestServeCommandDoesNotEnableLiveReloadWithoutWatch(t *testing.T) {
	deps := testCommandDependencies()
	server := &fakePreviewServer{}
	deps.newPreviewServer = func(outputPath string, port int) (previewServer, error) {
		return server, nil
	}

	_, _, err := executeForTest(t, deps, []string{"serve", "--output", filepath.Join(t.TempDir(), "site")})
	if err != nil {
		t.Fatalf("executeForTest() error = %v", err)
	}
	if server.enableCalls != 0 {
		t.Fatalf("EnableLiveReload calls = %d, want 0 without --watch", server.enableCalls)
	}
}

func TestServeCommandRejectsRemovedFlags(t *testing.T) {
	for _, flag := range []string{"--config=other.yaml", "--theme=feature"} {
		_, _, err := executeForTest(t, testCommandDependencies(), []string{"serve", flag})
		if err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("serve %s error = %v", flag, err)
		}
	}
}

func TestServeCommandReportsMissingDefaultOutput(t *testing.T) {
	vaultPath := t.TempDir()
	t.Chdir(vaultPath)
	_, _, err := executeForTest(t, defaultCommandDependencies(), []string{"serve"})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("executeForTest() error = %v", err)
	}
}

func TestServeCommandWatchDefaultsToCurrentVaultAndPublicOutput(t *testing.T) {
	vaultPath := t.TempDir()
	t.Chdir(vaultPath)
	writeCLIConfig(t, vaultPath)
	deps := testCommandDependencies()
	var builtVault, builtOutput, servedOutput string
	deps.buildSiteWithOptions = func(vault string, output string, _ internalbuild.Options) (*internalbuild.BuildResult, error) {
		builtVault, builtOutput = vault, output
		if err := os.MkdirAll(output, 0o755); err != nil {
			return nil, err
		}
		return &internalbuild.BuildResult{}, nil
	}
	server := &fakePreviewServer{}
	deps.newPreviewServer = func(output string, _ int) (previewServer, error) {
		servedOutput = output
		return server, nil
	}
	deps.newFileWatcher = func() (fileWatcher, error) { return newFakeFileWatcher(), nil }
	if _, _, err := executeForTest(t, deps, []string{"serve", "--watch"}); err != nil {
		t.Fatalf("executeForTest() error = %v", err)
	}
	wantOutput := filepath.Join(vaultPath, "public")
	if builtVault != vaultPath || builtOutput != wantOutput || servedOutput != wantOutput {
		t.Fatalf("paths = vault %q, build %q, serve %q", builtVault, builtOutput, servedOutput)
	}
}

func TestServeCommandWatchBuildsBeforeServing(t *testing.T) {
	vaultPath := t.TempDir()
	configPath := filepath.Join(vaultPath, defaultConfigFilename)
	if err := os.WriteFile(configPath, []byte("title: ignored\nbaseURL: https://example.com\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", configPath, err)
	}
	outputPath := filepath.Join(t.TempDir(), "site")

	deps := testCommandDependencies()
	server := &fakePreviewServer{}
	watcher := newFakeFileWatcher()
	var gotVaultPath string
	var gotOutputPath string
	deps.buildSiteWithOptions = func(vaultPath string, outputPath string, options internalbuild.Options) (*internalbuild.BuildResult, error) {
		gotVaultPath = vaultPath
		gotOutputPath = outputPath
		if options.DiagnosticsWriter == nil {
			t.Fatal("build options DiagnosticsWriter = nil, want injected stderr writer")
		}
		if options.TrackOutputTransactionPath == nil {
			t.Fatal("build options TrackOutputTransactionPath = nil in watch mode")
		}
		if err := os.MkdirAll(outputPath, 0o755); err != nil {
			return nil, err
		}
		return &internalbuild.BuildResult{}, nil
	}
	deps.newPreviewServer = func(outputPath string, port int) (previewServer, error) {
		return server, nil
	}
	deps.newFileWatcher = func() (fileWatcher, error) {
		return watcher, nil
	}

	stdout, stderr, err := executeForTest(t, deps, []string{"serve", "--output", outputPath, "--watch", "--vault", vaultPath})
	if err != nil {
		t.Fatalf("executeForTest() error = %v", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty stdout", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty stderr", stderr)
	}
	if gotVaultPath != vaultPath {
		t.Fatalf("build vaultPath = %q, want %q", gotVaultPath, vaultPath)
	}
	if gotOutputPath != outputPath {
		t.Fatalf("build outputPath = %q, want %q", gotOutputPath, outputPath)
	}
	if server.enableCalls != 1 {
		t.Fatalf("EnableLiveReload calls = %d, want 1 in watch mode", server.enableCalls)
	}
	if server.listenCalls != 1 {
		t.Fatalf("ListenAndServe calls = %d, want 1", server.listenCalls)
	}
}

func TestServeCommandWatchReloadsFixedVaultConfig(t *testing.T) {
	vaultPath := t.TempDir()
	configPath := filepath.Join(vaultPath, defaultConfigFilename)
	outputPath := filepath.Join(t.TempDir(), "site")
	if err := os.WriteFile(configPath, []byte("title: ignored\nbaseURL: https://example.com\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", configPath, err)
	}

	deps := testCommandDependencies()
	watcher := newFakeFileWatcher()
	listenStarted := make(chan struct{}, 1)
	listenBlock := make(chan struct{})
	server := &fakePreviewServer{listenStarted: listenStarted, listenBlock: listenBlock}
	buildSignal := make(chan struct{}, 4)
	deps.buildSiteWithOptions = func(vaultPath string, outputPath string, options internalbuild.Options) (*internalbuild.BuildResult, error) {
		if err := os.MkdirAll(outputPath, 0o755); err != nil {
			return nil, err
		}
		buildSignal <- struct{}{}
		return &internalbuild.BuildResult{}, nil
	}
	deps.newPreviewServer = func(outputPath string, port int) (previewServer, error) {
		return server, nil
	}
	deps.newFileWatcher = func() (fileWatcher, error) {
		return watcher, nil
	}

	var stdoutBuf lockedBuffer
	var stderrBuf lockedBuffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- executeWithDeps([]string{"serve", "--output", outputPath, "--watch", "--vault", vaultPath}, deps, &stdoutBuf, &stderrBuf)
	}()

	select {
	case <-listenStarted:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out waiting for preview server to start listening")
	}
	waitForServeWatchSignal(t, buildSignal, "initial build")
	waitForServeWatchAddCount(t, watcher, vaultPath, 1)

	watcher.send(fsnotify.Event{Name: configPath, Op: fsnotify.Write})
	waitForServeWatchSignalWithin(t, buildSignal, "config rebuild", 900*time.Millisecond)

	close(listenBlock)
	if err := <-errCh; err != nil {
		t.Fatalf("executeWithDeps() error = %v", err)
	}
	if got := stdoutBuf.String(); got != "" {
		t.Fatalf("stdout = %q, want empty stdout", got)
	}
	if got := stderrBuf.String(); got != "" {
		t.Fatalf("stderr = %q, want empty stderr", got)
	}
}

func TestServeWatchCommandUsesRealBuildFailureTransaction(t *testing.T) {
	vaultPath := t.TempDir()
	outputRoot := t.TempDir()
	outputPath := filepath.Join(outputRoot, "site")
	articlePath := filepath.Join(vaultPath, "article.md")
	writeValidateFile(t, vaultPath, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeValidateFile(t, vaultPath, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeArticle := func(status, body string) {
		t.Helper()
		writeValidateFile(t, vaultPath, "article.md", "---\ntitle: Article\npublish: true\ntype: page\n"+status+"---\n"+body+"\n")
	}
	writeArticle("", "Published baseline")

	deps := defaultCommandDependencies()
	watcher := newFakeFileWatcher()
	listenStarted := make(chan struct{}, 1)
	listenBlock := make(chan struct{})
	reloads := make(chan struct{}, 1)
	server := &fakePreviewServer{listenStarted: listenStarted, listenBlock: listenBlock, reloadCalled: reloads}
	deps.newPreviewServer = func(string, int) (previewServer, error) { return server, nil }
	deps.newFileWatcher = func() (fileWatcher, error) { return watcher, nil }

	var stdout lockedBuffer
	var stderr lockedBuffer
	errCh := make(chan error, 1)
	go func() {
		errCh <- executeWithDeps([]string{"serve", "--watch", "--vault", vaultPath, "--output", outputPath}, deps, &stdout, &stderr)
	}()
	select {
	case <-listenStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for real initial build and preview server")
	}
	waitForServeWatchAddCount(t, watcher, vaultPath, 1)
	before := snapshotCLIOutput(t, outputPath)

	writeArticle("status: draft\n", "Invalid update")
	watcher.send(fsnotify.Event{Name: articlePath, Op: fsnotify.Write})
	waitForLockedBufferContainsWithin(t, &stderr, "watch: build site:", 3*time.Second)
	watchDiagnostics := stderr.String()
	for _, want := range []string{"error schema", "article.md:5", "[field=status]", "must be stable, experimental, or deprecated"} {
		if !strings.Contains(watchDiagnostics, want) {
			t.Fatalf("watch diagnostics = %q, want %q", watchDiagnostics, want)
		}
	}
	assertCLIOutputUnchanged(t, outputPath, before, "serve --watch rebuild failure")
	assertNoCLITransactionResidue(t, outputRoot, outputPath)
	select {
	case <-reloads:
		t.Fatal("failed real rebuild notified live reload")
	case <-time.After(100 * time.Millisecond):
	}

	writeArticle("status: stable\n", "Recovered update")
	watcher.send(fsnotify.Event{Name: articlePath, Op: fsnotify.Write})
	waitForServeWatchSignalWithin(t, reloads, "reload after real build recovery", 3*time.Second)
	published, err := os.ReadFile(filepath.Join(outputPath, "article", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(published, []byte("Recovered update")) || bytes.Contains(published, []byte("Published baseline")) {
		t.Fatalf("recovered watch output = %s", published)
	}

	close(listenBlock)
	if err := <-errCh; err != nil {
		t.Fatalf("serve --watch error = %v", err)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("serve --watch stdout = %q, want empty", got)
	}
}

func TestStartServeWatchLoopDebouncesRebuildsAndNotifiesReload(t *testing.T) {
	vaultPath := t.TempDir()
	notePath := filepath.Join(vaultPath, "notes", "alpha.md")
	configPath := filepath.Join(vaultPath, defaultConfigFilename)
	outputPath := filepath.Join(vaultPath, "public")
	for _, filePath := range []string{notePath, configPath} {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(filePath), err)
		}
		if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v", filePath, err)
		}
	}
	if err := os.MkdirAll(outputPath, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", outputPath, err)
	}

	watcher := newFakeFileWatcher()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rebuildSignal := make(chan struct{}, 4)
	reloadSignal := make(chan struct{}, 4)
	errorSignal := make(chan error, 2)
	if err := startServeWatchLoop(ctx, serveWatchLoop{
		watcher:    watcher,
		vaultPath:  vaultPath,
		outputPath: outputPath,
		configPath: configPath,
		debounce:   15 * time.Millisecond,
		rebuild: func() error {
			rebuildSignal <- struct{}{}
			return nil
		},
		notifyReload: func() {
			reloadSignal <- struct{}{}
		},
		onError: func(err error) {
			errorSignal <- err
		},
	}); err != nil {
		t.Fatalf("startServeWatchLoop() error = %v", err)
	}

	watcher.send(fsnotify.Event{Name: notePath, Op: fsnotify.Write})
	watcher.send(fsnotify.Event{Name: notePath, Op: fsnotify.Write})
	waitForServeWatchSignal(t, rebuildSignal, "vault rebuild")
	waitForServeWatchSignal(t, reloadSignal, "vault reload")
	select {
	case <-rebuildSignal:
		t.Fatal("received unexpected second rebuild for debounced vault writes")
	case err := <-errorSignal:
		t.Fatalf("watch loop reported error: %v", err)
	case <-time.After(60 * time.Millisecond):
	}
	watcher.send(fsnotify.Event{Name: filepath.Join(outputPath, "index.html"), Op: fsnotify.Write})
	select {
	case <-rebuildSignal:
		t.Fatal("output-path change triggered rebuild, want ignored output updates")
	case err := <-errorSignal:
		t.Fatalf("watch loop reported error: %v", err)
	case <-time.After(40 * time.Millisecond):
	}

	watcher.send(fsnotify.Event{Name: configPath, Op: fsnotify.Write})
	waitForServeWatchSignal(t, rebuildSignal, "config rebuild")
	waitForServeWatchSignal(t, reloadSignal, "config reload")
}

func TestStartServeWatchLoopTracksOnlyActualOutputTransactionPaths(t *testing.T) {
	vaultPath := t.TempDir()
	configPath := filepath.Join(vaultPath, defaultConfigFilename)
	outputPath := filepath.Join(vaultPath, "public")
	userDirs := []string{
		filepath.Join(vaultPath, ".public-oxpio-stage-user-notes"),
		filepath.Join(vaultPath, ".public-oxpio-backup-user-notes"),
		filepath.Join(vaultPath, ".public-oxpio-failed-user-notes"),
	}
	transactionDir := filepath.Join(vaultPath, ".public-oxpio-stage-actual")
	userNotePath := filepath.Join(userDirs[0], "note.md")
	files := []string{configPath, userNotePath, filepath.Join(transactionDir, "index.html")}
	for _, userDir := range userDirs[1:] {
		files = append(files, filepath.Join(userDir, "note.md"))
	}
	for _, filePath := range files {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	watcher := newFakeFileWatcher()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rebuildSignal := make(chan struct{}, 1)
	if err := startServeWatchLoop(ctx, serveWatchLoop{
		watcher:                watcher,
		vaultPath:              vaultPath,
		outputPath:             outputPath,
		configPath:             configPath,
		outputTransactionPaths: map[string]struct{}{transactionDir: {}},
		debounce:               15 * time.Millisecond,
		rebuild: func() error {
			rebuildSignal <- struct{}{}
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	for _, userDir := range userDirs {
		waitForServeWatchAddCount(t, watcher, userDir, 1)
	}
	if got := watcher.countAddCalls(transactionDir); got != 0 {
		t.Fatalf("watcher.Add(%q) calls = %d, want 0 for tracked transaction", transactionDir, got)
	}
	watcher.send(fsnotify.Event{Name: userNotePath, Op: fsnotify.Write})
	waitForServeWatchSignal(t, rebuildSignal, "user directory rebuild")
}

func TestStartServeWatchLoopRefreshesPartialPlanInputsAfterFailedRebuild(t *testing.T) {
	vaultPath := t.TempDir()
	configPath := filepath.Join(vaultPath, defaultConfigFilename)
	outputPath := filepath.Join(vaultPath, "public")
	articlePath := filepath.Join(vaultPath, "article.md")
	bannerPath := filepath.Join(vaultPath, "images", "banner.png")
	writeCLIConfig(t, vaultPath)
	if err := os.WriteFile(filepath.Join(vaultPath, "_index.md"), []byte("---\ntitle: Home\npublish: true\n---\nHome\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	initialWatchFiles := plannedWatchFiles(vaultPath, outputPath)
	if _, ok := initialWatchFiles[bannerPath]; ok {
		t.Fatalf("initial watch files unexpectedly contain %q", bannerPath)
	}
	if err := os.WriteFile(articlePath, []byte("---\ntitle: Article\npublish: true\ntype: page\nbanner: images/banner.png\nbannerAlt: Banner\n---\nArticle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(bannerPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bannerPath, []byte("not a png"), 0o644); err != nil {
		t.Fatal(err)
	}

	watcher := newFakeFileWatcher()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rebuilds := make(chan int, 2)
	refreshes := make(chan struct{}, 2)
	reloads := make(chan struct{}, 1)
	errorsSeen := make(chan error, 1)
	attempt := 0
	if err := startServeWatchLoop(ctx, serveWatchLoop{
		watcher:            watcher,
		vaultPath:          vaultPath,
		outputPath:         outputPath,
		configPath:         configPath,
		relevantWatchFiles: initialWatchFiles,
		refreshRelevantInputs: func() map[string]struct{} {
			files := plannedWatchFiles(vaultPath, outputPath)
			refreshes <- struct{}{}
			return files
		},
		debounce: 15 * time.Millisecond,
		rebuild: func() error {
			attempt++
			rebuilds <- attempt
			if attempt == 1 {
				return errors.New("synthetic rebuild failure")
			}
			return nil
		},
		notifyReload: func() {
			reloads <- struct{}{}
		},
		onError: func(err error) {
			errorsSeen <- err
		},
	}); err != nil {
		t.Fatal(err)
	}

	watcher.send(fsnotify.Event{Name: articlePath, Op: fsnotify.Write})
	select {
	case got := <-rebuilds:
		if got != 1 {
			t.Fatalf("first rebuild attempt = %d, want 1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for failed rebuild")
	}
	waitForServeWatchSignalWithin(t, refreshes, "failed rebuild input refresh", 2*time.Second)
	waitForServeWatchErrorContains(t, errorsSeen, "synthetic rebuild failure")
	select {
	case <-reloads:
		t.Fatal("failed rebuild notified reload")
	default:
	}

	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bannerPath, imageData.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	watcher.send(fsnotify.Event{Name: bannerPath, Op: fsnotify.Write})
	select {
	case got := <-rebuilds:
		if got != 2 {
			t.Fatalf("rebuild after banner repair = %d, want 2", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("repaired banner did not trigger rebuild")
	}
	waitForServeWatchSignalWithin(t, reloads, "reload after banner repair", 2*time.Second)
}

func TestSyncFixedWatchInputsReaddsInvalidatedDirectory(t *testing.T) {
	vaultPath := t.TempDir()
	themeDir := filepath.Join(vaultPath, ".oxpio", "theme")
	iconsDir := filepath.Join(themeDir, "assets", "icons")
	if err := os.MkdirAll(iconsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	watcher := newFakeFileWatcher()
	loop := serveWatchLoop{
		watcher:        watcher,
		vaultPath:      vaultPath,
		outputPath:     filepath.Join(vaultPath, "public"),
		watchedDirs:    make(map[string]struct{}),
		vaultWatchDirs: make(map[string]struct{}),
		fixedWatchDirs: make(map[string]struct{}),
	}
	inputs := fixedServeWatchInputs(vaultPath)
	if err := loop.syncFixedWatchInputs(inputs); err != nil {
		t.Fatal(err)
	}
	if got := watcher.countAddCalls(iconsDir); got != 1 {
		t.Fatalf("initial watcher.Add(%q) calls = %d, want 1", iconsDir, got)
	}

	if err := os.RemoveAll(iconsDir); err != nil {
		t.Fatal(err)
	}
	loop.removeWatchedDirSubtree(iconsDir)
	if err := os.MkdirAll(iconsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := loop.syncFixedWatchInputs(inputs); err != nil {
		t.Fatal(err)
	}
	if got := watcher.countAddCalls(iconsDir); got != 2 {
		t.Fatalf("watcher.Add(%q) calls after replacement = %d, want 2", iconsDir, got)
	}
}

func TestStartServeWatchLoopReaddsRemovedOrRenamedDirectories(t *testing.T) {
	tests := []struct {
		name          string
		op            fsnotify.Op
		removeWatched func(t *testing.T, watchedDir string)
	}{
		{
			name: "remove",
			op:   fsnotify.Remove,
			removeWatched: func(t *testing.T, watchedDir string) {
				t.Helper()
				if err := os.RemoveAll(watchedDir); err != nil {
					t.Fatalf("os.RemoveAll(%q) error = %v", watchedDir, err)
				}
			},
		},
		{
			name: "rename",
			op:   fsnotify.Rename,
			removeWatched: func(t *testing.T, watchedDir string) {
				t.Helper()
				renamedPath := watchedDir + "-renamed"
				if err := os.Rename(watchedDir, renamedPath); err != nil {
					t.Fatalf("os.Rename(%q, %q) error = %v", watchedDir, renamedPath, err)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vaultPath := t.TempDir()
			watchedDir := filepath.Join(vaultPath, "notes")
			nestedDir := filepath.Join(watchedDir, "alpha")
			notePath := filepath.Join(nestedDir, "guide.md")
			configPath := filepath.Join(vaultPath, defaultConfigFilename)
			outputPath := filepath.Join(vaultPath, "public")
			for _, filePath := range []string{notePath, configPath} {
				if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
					t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(filePath), err)
				}
				if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
					t.Fatalf("os.WriteFile(%q) error = %v", filePath, err)
				}
			}
			if err := os.MkdirAll(outputPath, 0o755); err != nil {
				t.Fatalf("os.MkdirAll(%q) error = %v", outputPath, err)
			}

			watcher := newFakeFileWatcher()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			rebuildSignal := make(chan struct{}, 6)
			errorSignal := make(chan error, 4)
			if err := startServeWatchLoop(ctx, serveWatchLoop{
				watcher:    watcher,
				vaultPath:  vaultPath,
				outputPath: outputPath,
				configPath: configPath,
				debounce:   15 * time.Millisecond,
				rebuild: func() error {
					rebuildSignal <- struct{}{}
					return nil
				},
				onError: func(err error) {
					errorSignal <- err
				},
			}); err != nil {
				t.Fatalf("startServeWatchLoop() error = %v", err)
			}

			waitForServeWatchAddCount(t, watcher, watchedDir, 1)
			waitForServeWatchAddCount(t, watcher, nestedDir, 1)
			watcher.setRemoveErr(watchedDir, fsnotify.ErrNonExistentWatch)
			watcher.setRemoveErr(nestedDir, fsnotify.ErrNonExistentWatch)

			tt.removeWatched(t, watchedDir)
			watcher.send(fsnotify.Event{Name: watchedDir, Op: tt.op})
			waitForServeWatchRemoveCount(t, watcher, watchedDir, 1)
			waitForServeWatchRemoveCount(t, watcher, nestedDir, 1)

			if err := os.MkdirAll(nestedDir, 0o755); err != nil {
				t.Fatalf("os.MkdirAll(%q) error = %v", nestedDir, err)
			}
			if err := os.WriteFile(notePath, []byte("updated"), 0o644); err != nil {
				t.Fatalf("os.WriteFile(%q) error = %v", notePath, err)
			}

			watcher.send(fsnotify.Event{Name: watchedDir, Op: fsnotify.Create})
			waitForServeWatchAddCount(t, watcher, watchedDir, 2)
			waitForServeWatchAddCount(t, watcher, nestedDir, 2)

			drainServeWatchSignals(rebuildSignal)
			watcher.send(fsnotify.Event{Name: notePath, Op: fsnotify.Write})
			waitForServeWatchSignal(t, rebuildSignal, "recreated directory rebuild")
			assertNoServeWatchError(t, errorSignal)
		})
	}
}

func TestStartServeWatchLoopTreatsMissingPathChmodAsRemove(t *testing.T) {
	vaultPath := t.TempDir()
	notePath := filepath.Join(vaultPath, "notes", "guide.md")
	configPath := filepath.Join(vaultPath, defaultConfigFilename)
	outputPath := filepath.Join(vaultPath, "public")
	for _, filePath := range []string{notePath, configPath} {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(filePath), err)
		}
		if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v", filePath, err)
		}
	}
	if err := os.MkdirAll(outputPath, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", outputPath, err)
	}

	watcher := newFakeFileWatcher()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rebuildSignal := make(chan struct{}, 4)
	errorSignal := make(chan error, 4)
	if err := startServeWatchLoop(ctx, serveWatchLoop{
		watcher:    watcher,
		vaultPath:  vaultPath,
		outputPath: outputPath,
		configPath: configPath,
		debounce:   15 * time.Millisecond,
		rebuild: func() error {
			rebuildSignal <- struct{}{}
			return nil
		},
		onError: func(err error) {
			errorSignal <- err
		},
	}); err != nil {
		t.Fatalf("startServeWatchLoop() error = %v", err)
	}

	watcher.send(fsnotify.Event{Name: notePath, Op: fsnotify.Chmod})
	assertNoServeWatchSignal(t, rebuildSignal, errorSignal, "chmod on existing markdown input")

	if err := os.Remove(notePath); err != nil {
		t.Fatalf("os.Remove(%q) error = %v", notePath, err)
	}
	watcher.send(fsnotify.Event{Name: notePath, Op: fsnotify.Chmod})
	waitForServeWatchSignal(t, rebuildSignal, "missing-path chmod rebuild")
	assertNoServeWatchError(t, errorSignal)
}

func TestStartServeWatchLoopFiltersNonBuildOpsAndHiddenFiles(t *testing.T) {
	vaultPath := t.TempDir()
	notePath := filepath.Join(vaultPath, "notes", "guide.md")
	yamlPath := filepath.Join(vaultPath, "notes", "frontmatter.yaml")
	imagePath := filepath.Join(vaultPath, "attachments", "hero.png")
	hiddenPath := filepath.Join(vaultPath, ".oxpio", "scratch.txt")
	configPath := filepath.Join(vaultPath, defaultConfigFilename)
	outputPath := filepath.Join(vaultPath, "public")
	for _, filePath := range []string{notePath, yamlPath, imagePath, hiddenPath, configPath} {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(filePath), err)
		}
		if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v", filePath, err)
		}
	}
	if err := os.MkdirAll(outputPath, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", outputPath, err)
	}

	watcher := newFakeFileWatcher()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rebuildSignal := make(chan struct{}, 6)
	errorSignal := make(chan error, 4)
	if err := startServeWatchLoop(ctx, serveWatchLoop{
		watcher:    watcher,
		vaultPath:  vaultPath,
		outputPath: outputPath,
		configPath: configPath,
		debounce:   15 * time.Millisecond,
		rebuild: func() error {
			rebuildSignal <- struct{}{}
			return nil
		},
		onError: func(err error) {
			errorSignal <- err
		},
	}); err != nil {
		t.Fatalf("startServeWatchLoop() error = %v", err)
	}

	watcher.send(fsnotify.Event{Name: notePath, Op: fsnotify.Chmod})
	assertNoServeWatchSignal(t, rebuildSignal, errorSignal, "chmod on existing markdown input")

	watcher.send(fsnotify.Event{Name: hiddenPath, Op: fsnotify.Write})
	assertNoServeWatchSignal(t, rebuildSignal, errorSignal, "hidden file")

	watcher.send(fsnotify.Event{Name: yamlPath, Op: fsnotify.Write})
	waitForServeWatchSignal(t, rebuildSignal, "yaml rebuild")
	assertNoServeWatchError(t, errorSignal)

	watcher.send(fsnotify.Event{Name: imagePath, Op: fsnotify.Write})
	waitForServeWatchSignal(t, rebuildSignal, "image rebuild")
	assertNoServeWatchError(t, errorSignal)
}

func TestPlannedWatchFilesIncludesRawHTMLAndCustomCSSDependencies(t *testing.T) {
	vaultPath := t.TempDir()
	writeCLIConfig(t, vaultPath)
	for relPath, content := range map[string]string{
		"_index.md":         "---\ntitle: Home\npublish: true\n---\n<img src=\"images/raw.bin\">\n",
		"custom.css":        "@import \"styles/nested.css\";\n",
		"styles/nested.css": "body { background: url(../images/css.bin); }\n",
		"images/raw.bin":    "raw",
		"images/css.bin":    "css",
		"images/unused.bin": "unused",
	} {
		filePath := filepath.Join(vaultPath, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := plannedWatchFiles(vaultPath, filepath.Join(vaultPath, "public"))
	for _, relPath := range []string{"images/raw.bin", "images/css.bin", "styles/nested.css"} {
		filePath := filepath.Join(vaultPath, filepath.FromSlash(relPath))
		if _, ok := files[filePath]; !ok {
			t.Fatalf("planned watch files omit dependency %q: %#v", relPath, files)
		}
	}
	unused := filepath.Join(vaultPath, "images", "unused.bin")
	if _, ok := files[unused]; ok {
		t.Fatalf("planned watch files include unrelated resource %q", unused)
	}

	nested := filepath.Join(vaultPath, "styles", "nested.css")
	if err := os.WriteFile(nested, []byte("body { background: url(../images/missing.bin); }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failedFiles := plannedWatchFiles(vaultPath, filepath.Join(vaultPath, "public"))
	if _, ok := failedFiles[nested]; !ok {
		t.Fatalf("failed CSS analysis dropped discovered dependency %q: %#v", nested, failedFiles)
	}
}

func TestPlannedWatchFilesRetainsAmbiguousMarkdownResourceCandidates(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		reference string
	}{
		{name: "attachment link", reference: `[Logo](logo.svg)`},
		{name: "Markdown image", reference: `![Logo](logo.svg)`},
		{name: "image embed", reference: `![[logo.svg]]`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			vaultPath := t.TempDir()
			writeCLIConfig(t, vaultPath)
			if err := os.WriteFile(filepath.Join(vaultPath, "_index.md"), []byte("---\ntitle: Home\npublish: true\n---\n"+testCase.reference+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"Logo.svg", "LOGO.svg"} {
				if err := os.WriteFile(filepath.Join(vaultPath, name), []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			files := plannedWatchFiles(vaultPath, filepath.Join(vaultPath, "public"))
			for _, name := range []string{"Logo.svg", "LOGO.svg"} {
				candidate := filepath.Join(vaultPath, name)
				if _, ok := files[candidate]; !ok {
					t.Fatalf("failed analysis watch files omit ambiguous candidate %q: %#v", name, files)
				}
				loop := serveWatchLoop{vaultPath: vaultPath, outputPath: filepath.Join(vaultPath, "public"), configPath: filepath.Join(vaultPath, defaultConfigFilename), relevantWatchFiles: files}
				if !loop.shouldTrigger(candidate, fsnotify.Remove, false) {
					t.Fatalf("removing ambiguous candidate %q would not trigger a recovery build", name)
				}
			}
		})
	}
}

func TestServeWatchRecoversWhenAmbiguousResourceCandidateIsDeleted(t *testing.T) {
	vaultPath := t.TempDir()
	outputPath := filepath.Join(vaultPath, "public")
	configPath := filepath.Join(vaultPath, defaultConfigFilename)
	indexPath := filepath.Join(vaultPath, "_index.md")
	logoPath := filepath.Join(vaultPath, "Logo.svg")
	collisionPath := filepath.Join(vaultPath, "LOGO.svg")
	writeCLIConfig(t, vaultPath)
	writeIndex := func(body string) {
		t.Helper()
		content := "---\ntitle: Home\npublish: true\n---\n" + body + "\n\n![Logo](logo.svg)\n"
		if err := os.WriteFile(indexPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeIndex("Initial body")
	if err := os.WriteFile(logoPath, []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := internalbuild.BuildWithOptions(vaultPath, outputPath, internalbuild.Options{}); err != nil {
		t.Fatalf("initial build error = %v", err)
	}

	watcher := newFakeFileWatcher()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := make(chan error, 2)
	reloads := make(chan struct{}, 1)
	watchErrors := make(chan error, 1)
	refreshInputs := func() map[string]struct{} { return plannedWatchFiles(vaultPath, outputPath) }
	if err := startServeWatchLoop(ctx, serveWatchLoop{
		watcher:               watcher,
		vaultPath:             vaultPath,
		outputPath:            outputPath,
		configPath:            configPath,
		relevantWatchFiles:    refreshInputs(),
		refreshRelevantInputs: refreshInputs,
		debounce:              15 * time.Millisecond,
		rebuild: func() error {
			_, err := internalbuild.BuildWithOptions(vaultPath, outputPath, internalbuild.Options{})
			attempts <- err
			return err
		},
		notifyReload: func() { reloads <- struct{}{} },
		onError:      func(err error) { watchErrors <- err },
	}); err != nil {
		t.Fatal(err)
	}

	writeIndex("Updated body")
	if err := os.WriteFile(collisionPath, []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	watcher.send(fsnotify.Event{Name: collisionPath, Op: fsnotify.Create})
	select {
	case err := <-attempts:
		if err == nil {
			t.Fatal("ambiguous resource rebuild unexpectedly succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ambiguous resource rebuild")
	}
	waitForServeWatchErrorContains(t, watchErrors, "site plan has")
	published, err := os.ReadFile(filepath.Join(outputPath, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(published, []byte("Initial body")) || bytes.Contains(published, []byte("Updated body")) {
		t.Fatalf("failed rebuild changed published body: %s", published)
	}

	if err := os.Remove(collisionPath); err != nil {
		t.Fatal(err)
	}
	watcher.send(fsnotify.Event{Name: collisionPath, Op: fsnotify.Remove})
	select {
	case err := <-attempts:
		if err != nil {
			t.Fatalf("resource deletion recovery build error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for resource deletion recovery build")
	}
	waitForServeWatchSignalWithin(t, reloads, "reload after ambiguity repair", 2*time.Second)
	published, err = os.ReadFile(filepath.Join(outputPath, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(published, []byte("Updated body")) {
		t.Fatalf("resource deletion did not publish the pending body: %s", published)
	}
}

func TestPlannedWatchFilesKeepsExactResourceLookupNarrow(t *testing.T) {
	vaultPath := t.TempDir()
	writeCLIConfig(t, vaultPath)
	if err := os.WriteFile(filepath.Join(vaultPath, "_index.md"), []byte("---\ntitle: Home\npublish: true\n---\n![Logo](Logo.svg)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Logo.svg", "LOGO.svg"} {
		if err := os.WriteFile(filepath.Join(vaultPath, name), []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files := plannedWatchFiles(vaultPath, filepath.Join(vaultPath, "public"))
	if _, ok := files[filepath.Join(vaultPath, "Logo.svg")]; !ok {
		t.Fatal("planned watch files omit the exact resource dependency")
	}
	if _, ok := files[filepath.Join(vaultPath, "LOGO.svg")]; ok {
		t.Fatal("planned watch files include an unreferenced canonical collision despite exact lookup")
	}
}

func TestStartServeWatchLoopRebuildsForAttachmentsAndVaultCustomCSS(t *testing.T) {
	vaultPath := t.TempDir()
	attachmentPath := filepath.Join(vaultPath, "files", "manual.pdf")
	customCSSPath := filepath.Join(vaultPath, "custom.css")
	configPath := filepath.Join(vaultPath, defaultConfigFilename)
	outputPath := filepath.Join(vaultPath, "public")
	for _, filePath := range []string{attachmentPath, customCSSPath, configPath} {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(filePath), err)
		}
		if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v", filePath, err)
		}
	}
	if err := os.MkdirAll(outputPath, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", outputPath, err)
	}

	watcher := newFakeFileWatcher()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rebuildSignal := make(chan struct{}, 6)
	reloadSignal := make(chan struct{}, 6)
	errorSignal := make(chan error, 4)
	if err := startServeWatchLoop(ctx, serveWatchLoop{
		watcher:    watcher,
		vaultPath:  vaultPath,
		outputPath: outputPath,
		configPath: configPath,
		debounce:   15 * time.Millisecond,
		rebuild: func() error {
			rebuildSignal <- struct{}{}
			return nil
		},
		notifyReload: func() {
			reloadSignal <- struct{}{}
		},
		onError: func(err error) {
			errorSignal <- err
		},
	}); err != nil {
		t.Fatalf("startServeWatchLoop() error = %v", err)
	}
	tests := []struct {
		name   string
		path   string
		op     fsnotify.Op
		before func(t *testing.T)
	}{
		{
			name: "non-image attachment write",
			path: attachmentPath,
			op:   fsnotify.Write,
			before: func(t *testing.T) {
				t.Helper()
				if err := os.WriteFile(attachmentPath, []byte("updated manual"), 0o644); err != nil {
					t.Fatalf("os.WriteFile(%q) error = %v", attachmentPath, err)
				}
			},
		},
		{
			name: "non-image attachment remove",
			path: attachmentPath,
			op:   fsnotify.Remove,
			before: func(t *testing.T) {
				t.Helper()
				if err := os.Remove(attachmentPath); err != nil {
					t.Fatalf("os.Remove(%q) error = %v", attachmentPath, err)
				}
			},
		},
		{
			name: "vault custom.css write",
			path: customCSSPath,
			op:   fsnotify.Write,
			before: func(t *testing.T) {
				t.Helper()
				if err := os.WriteFile(customCSSPath, []byte("body { color: tomato; }\n"), 0o644); err != nil {
					t.Fatalf("os.WriteFile(%q) error = %v", customCSSPath, err)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			tt.before(t)
			watcher.send(fsnotify.Event{Name: tt.path, Op: tt.op})
			waitForServeWatchSignal(t, rebuildSignal, tt.name+" rebuild")
			waitForServeWatchSignal(t, reloadSignal, tt.name+" reload")
			assertNoServeWatchError(t, errorSignal)
		})
	}
}

func TestStartServeWatchLoopReportsWatcherCloseErrors(t *testing.T) {
	vaultPath := t.TempDir()
	configPath := filepath.Join(vaultPath, defaultConfigFilename)
	rootNotePath := filepath.Join(vaultPath, "root-note.md")
	outputPath := filepath.Join(vaultPath, "public")
	for _, filePath := range []string{configPath, rootNotePath} {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(filePath), err)
		}
		if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
			t.Fatalf("os.WriteFile(%q) error = %v", filePath, err)
		}
	}
	if err := os.MkdirAll(outputPath, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", outputPath, err)
	}

	watcher := newFakeFileWatcher()
	watcher.setCloseErr(errors.New("close failed"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errorSignal := make(chan error, 4)
	if err := startServeWatchLoop(ctx, serveWatchLoop{
		watcher:    watcher,
		vaultPath:  vaultPath,
		outputPath: outputPath,
		configPath: configPath,
		debounce:   15 * time.Millisecond,
		rebuild: func() error {
			return nil
		},
		onError: func(err error) {
			errorSignal <- err
		},
	}); err != nil {
		t.Fatalf("startServeWatchLoop() error = %v", err)
	}

	waitForServeWatchAddCount(t, watcher, vaultPath, 1)
	cancel()
	waitForServeWatchErrorContains(t, errorSignal, "close watcher: close failed")
	if got := watcher.countCloseCalls(); got != 1 {
		t.Fatalf("watcher.Close() calls = %d, want %d", got, 1)
	}
}

type fakePreviewServer struct {
	listenErr     error
	enableCalls   int
	listenCalls   int
	reloadCalls   int
	reloadCalled  chan struct{}
	listenStarted chan struct{}
	listenBlock   <-chan struct{}
}

func (s *fakePreviewServer) EnableLiveReload() {
	s.enableCalls++
}

func (s *fakePreviewServer) ListenAndServe() error {
	s.listenCalls++
	if s.listenStarted != nil {
		select {
		case s.listenStarted <- struct{}{}:
		default:
		}
	}
	if s.listenBlock != nil {
		<-s.listenBlock
	}
	return s.listenErr
}

func (s *fakePreviewServer) NotifyReload() {
	s.reloadCalls++
	if s.reloadCalled != nil {
		s.reloadCalled <- struct{}{}
	}
}

type fakeFileWatcher struct {
	mu          sync.Mutex
	addCalls    []string
	active      map[string]struct{}
	removeCalls []string
	removeErrs  map[string]error
	closeErr    error
	closeCalls  int
	events      chan fsnotify.Event
	errors      chan error
}

func newFakeFileWatcher() *fakeFileWatcher {
	return &fakeFileWatcher{
		active:     make(map[string]struct{}),
		events:     make(chan fsnotify.Event, 16),
		errors:     make(chan error, 4),
		removeErrs: make(map[string]error),
	}
}

func (w *fakeFileWatcher) Add(name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	cleanPath := filepath.Clean(name)
	w.addCalls = append(w.addCalls, cleanPath)
	w.active[cleanPath] = struct{}{}
	return nil
}

func (w *fakeFileWatcher) Remove(name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	cleanPath := filepath.Clean(name)
	w.removeCalls = append(w.removeCalls, cleanPath)
	if err := w.removeErrs[cleanPath]; err != nil {
		return err
	}
	delete(w.active, cleanPath)
	return nil
}

func (w *fakeFileWatcher) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closeCalls++
	return w.closeErr
}

func (w *fakeFileWatcher) Events() <-chan fsnotify.Event {
	return w.events
}

func (w *fakeFileWatcher) Errors() <-chan error {
	return w.errors
}

func (w *fakeFileWatcher) send(event fsnotify.Event) {
	w.events <- event
}

func (w *fakeFileWatcher) setRemoveErr(path string, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.removeErrs[filepath.Clean(path)] = err
}

func (w *fakeFileWatcher) setCloseErr(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.closeErr = err
}

func (w *fakeFileWatcher) countAddCalls(path string) int {
	w.mu.Lock()
	defer w.mu.Unlock()

	count := 0
	cleanPath := filepath.Clean(path)
	for _, addedPath := range w.addCalls {
		if addedPath == cleanPath {
			count++
		}
	}

	return count
}

func (w *fakeFileWatcher) countRemoveCalls(path string) int {
	w.mu.Lock()
	defer w.mu.Unlock()

	count := 0
	cleanPath := filepath.Clean(path)
	for _, removedPath := range w.removeCalls {
		if removedPath == cleanPath {
			count++
		}
	}

	return count
}

func (w *fakeFileWatcher) countCloseCalls() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.closeCalls
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitForLockedBufferContains(t *testing.T, buffer *lockedBuffer, want string) {
	t.Helper()
	waitForLockedBufferContainsWithin(t, buffer, want, 250*time.Millisecond)
}

func waitForLockedBufferContainsWithin(t *testing.T, buffer *lockedBuffer, want string, timeout time.Duration) {
	t.Helper()

	deadline := time.After(timeout)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		if strings.Contains(buffer.String(), want) {
			return
		}

		select {
		case <-deadline:
			t.Fatalf("timed out waiting for output containing %q; got %q", want, buffer.String())
		case <-ticker.C:
		}
	}
}

func waitForServeWatchSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	waitForServeWatchSignalWithin(t, signal, label, 250*time.Millisecond)
}

func waitForServeWatchSignalWithin(t *testing.T, signal <-chan struct{}, label string, timeout time.Duration) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitForServeWatchAddCount(t *testing.T, watcher *fakeFileWatcher, path string, want int) {
	t.Helper()

	deadline := time.After(250 * time.Millisecond)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		if got := watcher.countAddCalls(path); got >= want {
			return
		}

		select {
		case <-deadline:
			t.Fatalf("watcher.Add(%q) count did not reach %d", path, want)
		case <-ticker.C:
		}
	}
}

func waitForServeWatchRemoveCount(t *testing.T, watcher *fakeFileWatcher, path string, want int) {
	t.Helper()

	deadline := time.After(250 * time.Millisecond)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		if got := watcher.countRemoveCalls(path); got >= want {
			return
		}

		select {
		case <-deadline:
			t.Fatalf("watcher.Remove(%q) count did not reach %d", path, want)
		case <-ticker.C:
		}
	}
}

func assertNoServeWatchSignal(t *testing.T, signal <-chan struct{}, errorSignal <-chan error, label string) {
	t.Helper()

	select {
	case <-signal:
		t.Fatalf("received unexpected rebuild for %s", label)
	case err := <-errorSignal:
		t.Fatalf("watch loop reported error during %s: %v", label, err)
	case <-time.After(40 * time.Millisecond):
	}
}

func assertNoServeWatchError(t *testing.T, errorSignal <-chan error) {
	t.Helper()

	select {
	case err := <-errorSignal:
		t.Fatalf("watch loop reported error: %v", err)
	default:
	}
}

func waitForServeWatchErrorContains(t *testing.T, errorSignal <-chan error, want string) {
	t.Helper()

	select {
	case err := <-errorSignal:
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("watch loop error = %q, want substring %q", err.Error(), want)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatalf("timed out waiting for watch loop error containing %q", want)
	}
}

func drainServeWatchSignals(signal <-chan struct{}) {
	for {
		select {
		case <-signal:
		default:
			return
		}
	}
}
