package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	internalanalyze "github.com/simp-lee/oxpio/internal/analyze"
	internalasset "github.com/simp-lee/oxpio/internal/asset"
	internalbuild "github.com/simp-lee/oxpio/internal/build"
	internalconfig "github.com/simp-lee/oxpio/internal/config"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	internalserver "github.com/simp-lee/oxpio/internal/server"
	"github.com/spf13/cobra"
)

const defaultWatchDebounce = 350 * time.Millisecond

const watchRelevantOps = fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename

type fileWatcher interface {
	Add(name string) error
	Remove(name string) error
	Close() error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
}

type fsnotifyWatcher struct {
	watcher *fsnotify.Watcher
}

type serveWatchLoop struct {
	watcher                fileWatcher
	vaultPath              string
	outputPath             string
	configPath             string
	fixedWatchInputs       []string
	relevantWatchFiles     map[string]struct{}
	outputTransactionPaths map[string]struct{}
	refreshRelevantInputs  func() map[string]struct{}
	debounce               time.Duration
	rebuild                func() error
	notifyReload           func()
	onError                func(error)
	watchedDirs            map[string]struct{}
	vaultWatchDirs         map[string]struct{}
	fixedWatchDirs         map[string]struct{}
	done                   chan error
}

func newServeCommand(deps commandDependencies) *cobra.Command {
	var outputPath string
	var vaultPath string
	var port int
	var watch bool

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve a generated site for local preview",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateCommandPort(port, cmd.Flags().Changed("port")); err != nil {
				return err
			}
			boundary, err := resolveVaultOutputPaths(vaultPath, outputPath)
			if err != nil {
				return err
			}
			if watch {
				return runServeWatchMode(cmd, deps, boundary.VaultPath, boundary.OutputPath, port)
			}

			srv, err := deps.newPreviewServer(boundary.OutputPath, port)
			if err != nil {
				return errors.Join(fmt.Errorf("create preview server: %w", err), closePreviewServer(srv))
			}
			listenErr := srv.ListenAndServe()
			closeErr := closePreviewServer(srv)
			if listenErr != nil {
				return errors.Join(fmt.Errorf("listen and serve: %w", listenErr), closeErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close preview server: %w", closeErr)
			}
			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&outputPath, "output", "", "Path to the generated site output (default <vault>/public)")
	flags.StringVar(&vaultPath, "vault", "", "Path to the Obsidian vault (default current directory)")
	flags.IntVar(&port, "port", 0, fmt.Sprintf("Port for the local preview server (default %d)", internalserver.DefaultPort))
	flags.BoolVar(&watch, "watch", false, "Rebuild on vault/config changes and live-reload connected browsers")

	return cmd
}

func runServeWatchMode(cmd *cobra.Command, deps commandDependencies, normalizedVaultPath string, resolvedOutputPath string, port int) (runErr error) {
	resolvedConfigPath := filepath.Join(normalizedVaultPath, defaultConfigFilename)
	outputTransactionPaths := make(map[string]struct{})
	trackOutputTransactionPath := func(path string) {
		cleanPath := filepath.Clean(path)
		if strings.TrimSpace(cleanPath) != "" && cleanPath != "." && pathWithinRoot(normalizedVaultPath, cleanPath) {
			outputTransactionPaths[cleanPath] = struct{}{}
		}
	}

	build := func() error {
		if _, err := deps.buildSiteWithOptions(normalizedVaultPath, resolvedOutputPath, internalbuild.Options{
			DiagnosticsWriter:          cmd.ErrOrStderr(),
			TrackOutputTransactionPath: trackOutputTransactionPath,
		}); err != nil {
			return fmt.Errorf("build site: %w", err)
		}
		return nil
	}

	if err := build(); err != nil {
		return err
	}

	srv, err := deps.newPreviewServer(resolvedOutputPath, port)
	if err != nil {
		return errors.Join(fmt.Errorf("create preview server: %w", err), closePreviewServer(srv))
	}
	srv.EnableLiveReload()

	watcher, err := deps.newFileWatcher()
	if err != nil {
		return errors.Join(fmt.Errorf("create file watcher: %w", err), closePreviewServer(srv), closeFileWatcher(watcher))
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	done := make(chan error, 1)

	refreshInputs := func() map[string]struct{} {
		return plannedWatchFiles(normalizedVaultPath, resolvedOutputPath)
	}
	if err := startServeWatchLoop(ctx, serveWatchLoop{
		watcher:                watcher,
		vaultPath:              normalizedVaultPath,
		outputPath:             resolvedOutputPath,
		configPath:             resolvedConfigPath,
		outputTransactionPaths: outputTransactionPaths,
		relevantWatchFiles:     refreshInputs(),
		refreshRelevantInputs:  refreshInputs,
		debounce:               defaultWatchDebounce,
		done:                   done,
		rebuild:                build,
		notifyReload: func() {
			if refresher, ok := srv.(interface{ RefreshBasePath() }); ok {
				refresher.RefreshBasePath()
			}
			srv.NotifyReload()
		},
		onError: func(err error) {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "watch: %v\n", err)
		},
	}); err != nil {
		cancel()
		return errors.Join(fmt.Errorf("start watch loop: %w", err), closePreviewServer(srv))
	}
	defer func() {
		cancel()
		watchErr := <-done
		runErr = errors.Join(runErr, watchErr, closePreviewServer(srv))
	}()

	if err := srv.ListenAndServe(); err != nil {
		return fmt.Errorf("listen and serve: %w", err)
	}

	return nil
}

func newFSNotifyWatcher() (fileWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	return &fsnotifyWatcher{watcher: watcher}, nil
}

func (w *fsnotifyWatcher) Add(name string) error {
	return w.watcher.Add(name)
}

func (w *fsnotifyWatcher) Remove(name string) error {
	return w.watcher.Remove(name)
}

func (w *fsnotifyWatcher) Close() error {
	return w.watcher.Close()
}

func (w *fsnotifyWatcher) Events() <-chan fsnotify.Event {
	return w.watcher.Events
}

func (w *fsnotifyWatcher) Errors() <-chan error {
	return w.watcher.Errors
}

func startServeWatchLoop(ctx context.Context, loop serveWatchLoop) error {
	if loop.watcher == nil {
		return fmt.Errorf("watcher is nil")
	}
	if loop.rebuild == nil {
		return closeServeWatchLoopWatcher(fmt.Errorf("rebuild function is nil"), loop.watcher)
	}
	if loop.debounce <= 0 {
		loop.debounce = defaultWatchDebounce
	}
	loop.fixedWatchInputs = fixedServeWatchInputs(loop.vaultPath)
	loop.watchedDirs = make(map[string]struct{})
	loop.vaultWatchDirs = make(map[string]struct{})
	loop.fixedWatchDirs = make(map[string]struct{})
	loop.pruneMissingOutputTransactions()

	if err := loop.addVaultTree(loop.vaultPath); err != nil {
		return closeServeWatchLoopWatcher(err, loop.watcher)
	}
	if err := loop.addFixedWatchInputs(); err != nil {
		return closeServeWatchLoopWatcher(err, loop.watcher)
	}

	go loop.run(ctx)
	return nil
}

func (loop *serveWatchLoop) run(ctx context.Context) {
	defer func() {
		closeErr := loop.closeWatcher()
		if loop.done != nil {
			loop.done <- closeErr
			return
		}
		if closeErr != nil {
			loop.reportError(fmt.Errorf("close watcher: %w", closeErr))
		}
	}()

	var timer *time.Timer
	var timerC <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				stopWatchTimer(timer)
			}
			return
		case err, ok := <-loop.watcher.Errors():
			if !ok {
				return
			}
			if err != nil {
				loop.reportError(fmt.Errorf("watcher error: %w", err))
			}
		case event, ok := <-loop.watcher.Events():
			if !ok {
				return
			}

			cleanPath := filepath.Clean(event.Name)
			effectiveOp := relevantWatchOp(cleanPath, event.Op)
			if effectiveOp == 0 {
				continue
			}

			wasWatchedDir := loop.isWatchedDir(cleanPath)
			outputTransaction := loop.pathIsOutputTransaction(cleanPath)
			if outputTransaction {
				loop.forgetOutputTransaction(cleanPath, effectiveOp)
			}
			if hasDirectoryLifecycleOp(effectiveOp) {
				loop.removeWatchedDirSubtree(cleanPath)
			}
			if err := loop.addNewDirectoryWatch(cleanPath); err != nil {
				loop.reportError(err)
			}
			recoveredFixedInput, err := loop.recoverFixedInputWatches(cleanPath, effectiveOp)
			if err != nil {
				loop.reportError(err)
			}
			if outputTransaction {
				continue
			}
			if !recoveredFixedInput && !loop.shouldTrigger(cleanPath, effectiveOp, wasWatchedDir) {
				continue
			}

			if timer == nil {
				timer = time.NewTimer(loop.debounce)
				timerC = timer.C
				continue
			}

			stopWatchTimer(timer)
			timer.Reset(loop.debounce)
			timerC = timer.C
		case <-timerC:
			timerC = nil
			rebuildErr := loop.rebuild()
			if err := loop.syncFixedWatchInputs(loop.fixedWatchInputs); err != nil {
				loop.reportError(err)
			}
			if loop.refreshRelevantInputs != nil {
				loop.relevantWatchFiles = loop.refreshRelevantInputs()
			}
			if rebuildErr != nil {
				loop.reportError(rebuildErr)
				continue
			}
			if loop.notifyReload != nil {
				loop.notifyReload()
			}
		}
	}
}

func closeServeWatchLoopWatcher(cause error, watcher fileWatcher) error {
	if watcher == nil {
		return cause
	}

	if err := watcher.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("close watcher: %w", err))
	}

	return cause
}

func (loop *serveWatchLoop) closeWatcher() error {
	if loop == nil || loop.watcher == nil {
		return nil
	}

	return loop.watcher.Close()
}

func (loop *serveWatchLoop) addVaultTree(root string) error {
	return filepath.WalkDir(root, func(current string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}

		cleanPath := filepath.Clean(current)
		if loop.pathIsOutput(cleanPath) || loop.pathIsOutputTransaction(cleanPath) {
			return filepath.SkipDir
		}
		if !loop.shouldWatchVaultDirectory(cleanPath) {
			if cleanPath == loop.vaultPath {
				return nil
			}
			return filepath.SkipDir
		}

		return loop.addVaultWatchDir(cleanPath)
	})
}

func (loop *serveWatchLoop) addFixedWatchInputs() error {
	return loop.syncFixedWatchInputs(loop.fixedWatchInputs)
}

func (loop *serveWatchLoop) syncFixedWatchInputs(inputs []string) error {
	normalizedInputs := normalizeServeWatchInputs(inputs)
	nextWatchDirs, err := loop.watchDirsForInputs(normalizedInputs)
	if err != nil {
		return err
	}

	for watchDir := range nextWatchDirs {
		if err := loop.addWatchDir(watchDir); err != nil {
			return err
		}
	}

	for watchDir := range loop.fixedWatchDirs {
		if _, ok := nextWatchDirs[watchDir]; ok {
			continue
		}
		if loop.shouldRetainBaseWatchDir(watchDir) {
			continue
		}
		if err := loop.removeWatchDir(watchDir); err != nil {
			return err
		}
	}

	loop.fixedWatchInputs = normalizedInputs
	loop.fixedWatchDirs = nextWatchDirs
	return nil
}

func (loop *serveWatchLoop) watchDirsForInputs(inputs []string) (map[string]struct{}, error) {
	watchDirs := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		targetWatchDir := filepath.Clean(input)
		if !loop.pathWithinVault(targetWatchDir) {
			return nil, fmt.Errorf("watch input %q must stay inside the vault", targetWatchDir)
		}
		if strings.TrimSpace(targetWatchDir) == "" || targetWatchDir == "." || loop.pathIsOutput(targetWatchDir) {
			continue
		}
		recursiveWatchDirs, err := loop.recursiveWatchDirs(targetWatchDir)
		if err != nil {
			return nil, err
		}
		for watchDir := range recursiveWatchDirs {
			watchDirs[watchDir] = struct{}{}
		}

		watchDir := nearestExistingWatchDir(targetWatchDir)
		if strings.TrimSpace(watchDir) == "" || watchDir == "." || loop.pathIsOutput(watchDir) {
			continue
		}
		if isFilesystemRoot(watchDir) && filepath.Clean(watchDir) != filepath.Clean(targetWatchDir) {
			continue
		}

		watchDirs[filepath.Clean(watchDir)] = struct{}{}
		if filepath.Clean(watchDir) != filepath.Clean(targetWatchDir) {
			continue
		}

		recoveryDir := nearestExistingWatchDir(filepath.Dir(targetWatchDir))
		if strings.TrimSpace(recoveryDir) == "" || recoveryDir == "." || filepath.Clean(recoveryDir) == filepath.Clean(watchDir) || isFilesystemRoot(recoveryDir) || loop.pathIsOutput(recoveryDir) {
			continue
		}

		watchDirs[filepath.Clean(recoveryDir)] = struct{}{}
	}

	return watchDirs, nil
}

func (loop *serveWatchLoop) shouldRetainBaseWatchDir(path string) bool {
	_, ok := loop.vaultWatchDirs[filepath.Clean(path)]
	return ok
}

func (loop *serveWatchLoop) addNewDirectoryWatch(path string) error {
	cleanPath := filepath.Clean(path)
	if cleanPath == "" || loop.pathIsOutput(cleanPath) || loop.pathIsOutputTransaction(cleanPath) {
		return nil
	}

	info, err := os.Lstat(cleanPath)
	if err != nil {
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("watch directory %q must not be a symbolic link", cleanPath)
	}
	if !info.IsDir() {
		return nil
	}
	if loop.isFixedDirectoryInput(cleanPath) {
		return loop.addRecursiveWatchTree(cleanPath)
	}
	if loop.shouldDirectlyWatchDirectory(cleanPath) {
		return loop.addWatchDir(cleanPath)
	}
	if !loop.pathWithinVault(cleanPath) || !loop.shouldWatchVaultDirectory(cleanPath) {
		return nil
	}

	return loop.addVaultTree(cleanPath)
}

func (loop *serveWatchLoop) recoverFixedInputWatches(path string, op fsnotify.Op) (bool, error) {
	if op&fsnotify.Create == 0 || len(loop.fixedWatchInputs) == 0 {
		return false, nil
	}

	cleanPath := filepath.Clean(path)
	info, err := os.Lstat(cleanPath)
	if err != nil {
		return false, nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("watch directory %q must not be a symbolic link", cleanPath)
	}
	if !info.IsDir() {
		return false, nil
	}

	needsRescan := false
	for _, input := range loop.fixedWatchInputs {
		if loop.shouldRescanFixedInputPath(cleanPath, input) {
			needsRescan = true
			break
		}
	}
	if !needsRescan {
		return false, nil
	}

	if err := loop.syncFixedWatchInputs(loop.fixedWatchInputs); err != nil {
		return false, err
	}

	for _, input := range loop.fixedWatchInputs {
		if !loop.shouldRescanFixedInputPath(cleanPath, input) {
			continue
		}
		if fixedWatchInputExists(input) {
			return true, nil
		}
	}

	return false, nil
}

func (loop *serveWatchLoop) shouldRescanFixedInputPath(path string, input string) bool {
	targetWatchDir := filepath.Clean(input)
	if strings.TrimSpace(targetWatchDir) == "" || targetWatchDir == "." {
		return false
	}

	return pathWithinRoot(filepath.Clean(path), targetWatchDir)
}

func fixedWatchInputExists(input string) bool {
	cleanPath := filepath.Clean(input)
	if strings.TrimSpace(cleanPath) == "" || cleanPath == "." {
		return false
	}

	info, err := os.Stat(cleanPath)
	return err == nil && info.IsDir()
}

func (loop *serveWatchLoop) isFixedDirectoryInput(path string) bool {
	cleanPath := filepath.Clean(path)
	for _, input := range loop.fixedWatchInputs {
		if internalfsutil.SamePath(input, cleanPath) {
			return true
		}
	}
	return false
}

func (loop *serveWatchLoop) shouldDirectlyWatchDirectory(path string) bool {
	cleanPath := filepath.Clean(path)
	for _, input := range loop.fixedWatchInputs {
		targetWatchDir := filepath.Clean(input)
		if internalfsutil.SamePath(targetWatchDir, cleanPath) || pathWithinRoot(cleanPath, targetWatchDir) {
			return true
		}
	}
	return false
}

func (loop *serveWatchLoop) addWatchDir(path string) error {
	cleanPath := filepath.Clean(path)
	if _, ok := loop.watchedDirs[cleanPath]; ok {
		return nil
	}
	if err := loop.watcher.Add(cleanPath); err != nil {
		return fmt.Errorf("watch %q: %w", cleanPath, err)
	}
	loop.watchedDirs[cleanPath] = struct{}{}
	return nil
}

func (loop *serveWatchLoop) addVaultWatchDir(path string) error {
	cleanPath := filepath.Clean(path)
	if err := loop.addWatchDir(cleanPath); err != nil {
		return err
	}
	loop.vaultWatchDirs[cleanPath] = struct{}{}
	return nil
}

func (loop *serveWatchLoop) isWatchedDir(path string) bool {
	_, ok := loop.watchedDirs[filepath.Clean(path)]
	return ok
}

func (loop *serveWatchLoop) removeWatchedDirSubtree(path string) {
	cleanPath := filepath.Clean(path)
	for watchedPath := range loop.watchedDirs {
		if watchedPath == cleanPath || pathWithinRoot(cleanPath, watchedPath) {
			if err := loop.removeWatchDir(watchedPath); err != nil {
				loop.reportError(err)
			}
		}
	}
}

func (loop *serveWatchLoop) removeWatchDir(path string) error {
	cleanPath := filepath.Clean(path)
	delete(loop.watchedDirs, cleanPath)
	delete(loop.vaultWatchDirs, cleanPath)
	if err := loop.watcher.Remove(cleanPath); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
		return fmt.Errorf("unwatch %q: %w", cleanPath, err)
	}

	return nil
}

func (loop *serveWatchLoop) shouldTrigger(path string, op fsnotify.Op, wasWatchedDir bool) bool {
	if !hasRelevantWatchOp(op) {
		return false
	}
	if path == "" || loop.pathIsOutput(path) || loop.pathIsOutputTransaction(path) {
		return false
	}
	cleanPath := filepath.Clean(path)
	if cleanPath == filepath.Clean(loop.configPath) {
		return true
	}
	if loop.matchesFixedWatchInput(cleanPath, op) {
		return true
	}
	if !loop.pathWithinVault(cleanPath) {
		return false
	}

	relPath, err := filepath.Rel(loop.vaultPath, cleanPath)
	if err != nil {
		return false
	}
	relPath = filepath.Clean(relPath)
	if relPath == "." {
		return false
	}
	if relPath == filepath.Join(".obsidian", "app.json") {
		return true
	}
	if relPath == ".obsidian" && obsidianAppInputMayHaveChanged(loop.vaultPath, op, wasWatchedDir) {
		return true
	}
	if !isWatchableVaultPath(relPath) {
		return false
	}
	if isWatchableDirectoryEvent(cleanPath, op, wasWatchedDir) {
		return true
	}

	if filepath.Clean(relPath) == filepath.Clean(filepath.Base(loop.configPath)) {
		return true
	}
	if loop.relevantWatchFiles == nil {
		return isWatchableVaultFile(relPath)
	}
	if op&fsnotify.Create != 0 {
		return true
	}
	if strings.HasSuffix(strings.ToLower(relPath), ".md") {
		return true
	}
	_, relevant := loop.relevantWatchFiles[filepath.Clean(cleanPath)]
	return relevant
}

func (loop *serveWatchLoop) matchesFixedWatchInput(path string, op fsnotify.Op) bool {
	cleanPath := filepath.Clean(path)
	for _, input := range loop.fixedWatchInputs {
		trimmedInput := strings.TrimSpace(input)
		if trimmedInput == "" {
			continue
		}

		cleanInput := filepath.Clean(trimmedInput)
		if pathWithinRoot(cleanInput, cleanPath) {
			return true
		}
		if hasDirectoryLifecycleOp(op) && pathWithinRoot(cleanPath, cleanInput) {
			return true
		}
	}

	return false
}

func hasRelevantWatchOp(op fsnotify.Op) bool {
	return op&watchRelevantOps != 0
}

func relevantWatchOp(path string, op fsnotify.Op) fsnotify.Op {
	relevantOp := op & watchRelevantOps
	if relevantOp != 0 {
		return relevantOp
	}
	if op&fsnotify.Chmod != 0 && watchPathMissing(path) {
		return fsnotify.Remove
	}

	return 0
}

func hasDirectoryLifecycleOp(op fsnotify.Op) bool {
	return op&(fsnotify.Remove|fsnotify.Rename) != 0
}

func watchPathMissing(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}

	_, err := os.Lstat(path)
	return os.IsNotExist(err)
}

func isWatchableDirectoryEvent(path string, op fsnotify.Op, wasWatchedDir bool) bool {
	if op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
		return false
	}
	if wasWatchedDir {
		return true
	}

	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func obsidianAppInputMayHaveChanged(vaultPath string, op fsnotify.Op, wasWatchedDir bool) bool {
	if op&(fsnotify.Create|fsnotify.Rename) != 0 {
		if _, err := os.Lstat(filepath.Join(vaultPath, ".obsidian", "app.json")); err == nil {
			return true
		}
	}
	return wasWatchedDir && op&(fsnotify.Remove|fsnotify.Rename) != 0
}

func isWatchableVaultPath(relPath string) bool {
	parts := splitWatchPath(relPath)
	for _, part := range parts {
		if part == "node_modules" || part == ".obsidian" || part == ".oxpio" {
			return false
		}
	}

	return true
}

func isWatchableVaultFile(relPath string) bool {
	return internalasset.IsPublishableAssetPath(filepath.Clean(relPath))
}

func (loop *serveWatchLoop) shouldWatchVaultDirectory(path string) bool {
	relPath, err := filepath.Rel(loop.vaultPath, path)
	if err != nil {
		return false
	}
	relPath = filepath.Clean(relPath)
	if relPath == "." || relPath == ".obsidian" {
		return true
	}
	if loop.pathIsOutputTransaction(path) {
		return false
	}

	parts := splitWatchPath(relPath)
	for _, part := range parts {
		if part == "node_modules" || part == ".oxpio" {
			return false
		}
	}

	return true
}

func (loop *serveWatchLoop) pathIsOutput(path string) bool {
	if strings.TrimSpace(loop.outputPath) == "" {
		return false
	}
	return pathWithinRoot(loop.outputPath, path)
}

func (loop *serveWatchLoop) pathIsOutputTransaction(candidate string) bool {
	if loop == nil || len(loop.outputTransactionPaths) == 0 {
		return false
	}
	candidate = filepath.Clean(candidate)
	for current := candidate; current != "" && pathWithinRoot(loop.vaultPath, current); current = filepath.Dir(current) {
		if _, ok := loop.outputTransactionPaths[current]; ok {
			return true
		}
		if filepath.Clean(current) == filepath.Clean(loop.vaultPath) {
			break
		}
	}
	return false
}

func (loop *serveWatchLoop) pruneMissingOutputTransactions() {
	if loop == nil {
		return
	}
	for path := range loop.outputTransactionPaths {
		if watchPathMissing(path) {
			delete(loop.outputTransactionPaths, path)
		}
	}
}

func (loop *serveWatchLoop) forgetOutputTransaction(path string, op fsnotify.Op) {
	if loop == nil || op&(fsnotify.Remove|fsnotify.Rename) == 0 {
		return
	}
	cleanPath := filepath.Clean(path)
	if _, ok := loop.outputTransactionPaths[cleanPath]; ok && watchPathMissing(cleanPath) {
		delete(loop.outputTransactionPaths, cleanPath)
	}
}

func (loop *serveWatchLoop) pathWithinVault(path string) bool {
	return pathWithinRoot(loop.vaultPath, path)
}

func (loop *serveWatchLoop) reportError(err error) {
	if err == nil || loop.onError == nil {
		return
	}
	loop.onError(err)
}

func splitWatchPath(relPath string) []string {
	if relPath == "." || relPath == "" {
		return nil
	}

	return strings.Split(relPath, string(filepath.Separator))
}

func pathWithinRoot(root string, candidate string) bool {
	return internalfsutil.PathWithinRoot(root, candidate)
}

func normalizeServeWatchInputs(inputs []string) []string {
	if len(inputs) == 0 {
		return nil
	}

	normalized := make([]string, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		cleanInput := filepath.Clean(strings.TrimSpace(input))
		if cleanInput == "" || cleanInput == "." {
			continue
		}
		if _, ok := seen[cleanInput]; ok {
			continue
		}

		seen[cleanInput] = struct{}{}
		normalized = append(normalized, cleanInput)
	}
	return normalized
}

func fixedServeWatchInputs(vaultPath string) []string {
	themeDir := filepath.Join(vaultPath, filepath.FromSlash(internalconfig.ThemeDirRelPath))
	return []string{themeDir}
}

// plannedWatchFiles is the watch-side input signature. Markdown/config files
// are always relevant, while ordinary resources are watched only when the
// current planner/index says they participate in the publication.
func plannedWatchFiles(vaultPath, outputPath string) map[string]struct{} {
	files := make(map[string]struct{})
	add := func(value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		if filepath.IsAbs(value) {
			files[filepath.Clean(value)] = struct{}{}
			return
		}
		files[filepath.Clean(filepath.Join(vaultPath, filepath.FromSlash(value)))] = struct{}{}
	}
	add(defaultConfigFilename)
	add(filepath.Join(".obsidian", "app.json"))
	// Failed analyses can still carry a partial plan whose asset references
	// must remain watchable so repairing those assets can trigger a retry.
	result, _ := internalanalyze.AnalyzeWithOutput(vaultPath, outputPath)
	if result.Plan == nil {
		return files
	}
	for _, relPath := range result.Plan.Scan.MarkdownFiles {
		add(relPath)
	}
	if result.Plan.Plan == nil {
		return files
	}
	plan := result.Plan.Plan
	add(plan.Config.CustomCSS)
	add(plan.Config.DefaultImg)
	for _, section := range plan.Sections {
		if section != nil {
			add(section.Banner)
		}
	}
	for _, article := range plan.Articles {
		if article != nil {
			add(article.Frontmatter.Banner)
			add(article.Frontmatter.Cover)
		}
	}
	if result.Plan.Index != nil {
		for source := range result.Plan.Index.Assets {
			add(source)
		}
	}
	for source := range plan.CSSInputFiles {
		add(source)
	}
	return files
}

func (loop *serveWatchLoop) recursiveWatchDirs(root string) (map[string]struct{}, error) {
	cleanRoot := filepath.Clean(root)
	if strings.TrimSpace(cleanRoot) == "" || cleanRoot == "." {
		return nil, nil
	}

	if pathWithinRoot(loop.vaultPath, cleanRoot) {
		if _, _, err := internalfsutil.InspectContainedDirectory(loop.vaultPath, cleanRoot); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, nil
			}
			return nil, fmt.Errorf("inspect watch tree %q: %w", cleanRoot, err)
		}
	}
	info, err := os.Lstat(cleanRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat watch tree %q: %w", cleanRoot, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, nil
	}

	watchDirs := make(map[string]struct{})
	err = filepath.WalkDir(cleanRoot, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry == nil || !entry.IsDir() {
			return nil
		}

		cleanCurrent := filepath.Clean(current)
		if loop.pathIsOutput(cleanCurrent) {
			return filepath.SkipDir
		}
		watchDirs[cleanCurrent] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk watch tree %q: %w", cleanRoot, err)
	}

	return watchDirs, nil
}

func (loop *serveWatchLoop) addRecursiveWatchTree(root string) error {
	watchDirs, err := loop.recursiveWatchDirs(root)
	if err != nil {
		return err
	}
	for watchDir := range watchDirs {
		if err := loop.addWatchDir(watchDir); err != nil {
			return err
		}
	}

	return nil
}

func nearestExistingWatchDir(path string) string {
	cleanPath := filepath.Clean(path)
	if strings.TrimSpace(cleanPath) == "" || cleanPath == "." {
		return ""
	}

	for {
		info, err := os.Stat(cleanPath)
		if err == nil && info.IsDir() {
			return cleanPath
		}

		parent := filepath.Dir(cleanPath)
		if parent == cleanPath {
			return ""
		}
		cleanPath = parent
	}
}

func isFilesystemRoot(path string) bool {
	cleanPath := filepath.Clean(path)
	return filepath.Dir(cleanPath) == cleanPath
}

func stopWatchTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
