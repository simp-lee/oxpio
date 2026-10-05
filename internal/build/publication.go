package build

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
)

type managedOutputDirState struct {
	info      os.FileInfo
	exists    bool
	isDir     bool
	hasMarker bool
	empty     bool
}

type stagedOutputPublisher struct {
	outputPath           string
	stagingPath          string
	backupPath           string
	createdParentDirs    []string
	initialOutput        managedOutputDirState
	publishedMode        os.FileMode
	committed            bool
	publicationAttempted bool
	publicationSource    os.FileInfo
	cleanupErr           error
	trackTransactionPath func(string)
}

var (
	stagedOutputRename    = os.Rename
	stagedOutputRemoveAll = os.RemoveAll
	stagedOutputStat      = os.Stat
)

const (
	managedOutputMarkerFilename = ".oxpio-output"
	managedOutputMarkerContents = "managed by oxpio\n"
	customCSSOutputPath         = "assets/custom.css"
)

type strictOutputRegistry struct {
	claims       map[string]string
	previousRoot string
	previous     map[string]strictCacheOutput
	records      []strictCacheOutput
	dependencies []strictCacheDependency
}

func newStrictOutputRegistry(previousRoot string, manifest *strictCacheManifest) *strictOutputRegistry {
	previous := make(map[string]strictCacheOutput)
	if manifest != nil {
		for _, entry := range manifest.Outputs {
			if entry.Route != "" {
				previous[entry.Route] = entry
			}
		}
	}
	return &strictOutputRegistry{claims: make(map[string]string), previousRoot: previousRoot, previous: previous}
}

func (registry *strictOutputRegistry) dependency(owner, source string, value any) error {
	if registry == nil {
		return fmt.Errorf("output registry is required")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal cache dependency %q: %w", source, err)
	}
	hash := sha256.Sum256(data)
	registry.dependencies = append(registry.dependencies, strictCacheDependency{
		Owner: owner, Source: source, InputSignature: fmt.Sprintf("%x", hash),
	})
	return nil
}

func (registry *strictOutputRegistry) dependencyBytes(owner, source string, data []byte) error {
	if registry == nil {
		return fmt.Errorf("output registry is required")
	}
	hash := sha256.Sum256(data)
	registry.dependencies = append(registry.dependencies, strictCacheDependency{
		Owner: owner, Source: source, InputSignature: fmt.Sprintf("%x", hash),
	})
	return nil
}

func (registry *strictOutputRegistry) write(outputRoot, relPath, owner string, content []byte) error {
	if registry == nil {
		return fmt.Errorf("output registry is required")
	}
	cleaned, _, err := resolveOutputWritePath(outputRoot, relPath)
	if err != nil {
		return err
	}
	if existing, ok := registry.claims[cleaned]; ok {
		return fmt.Errorf("output path %q claimed by %q and written again by %q", cleaned, existing, owner)
	}
	registry.claims[cleaned] = owner
	writeContent := content
	hash := sha256.Sum256(content)
	// The producer may change when identical assets share a destination. Reuse
	// depends on the path and verified bytes; the new record captures ownership.
	if previous, ok := registry.previous[cleaned]; ok && previous.OutputHash == fmt.Sprintf("%x", hash) && registry.previousRoot != "" {
		_, previousPath, pathErr := resolveOutputWritePath(registry.previousRoot, cleaned)
		if pathErr != nil {
			return pathErr
		}
		previousInfo, statErr := os.Lstat(previousPath)
		if statErr == nil && previousInfo.Mode().IsRegular() {
			if previousContent, err := os.ReadFile(previousPath); err == nil {
				previousHash := sha256.Sum256(previousContent)
				if fmt.Sprintf("%x", previousHash) == previous.OutputHash {
					if err := linkCachedOutput(previousPath, outputRoot, cleaned); err == nil {
						registry.records = append(registry.records, strictCacheOutput{Owner: owner, Route: cleaned, OutputHash: fmt.Sprintf("%x", hash)})
						return nil
					}
					writeContent = previousContent
				}
			}
		}
	}
	if err := writeOutputFile(outputRoot, cleaned, writeContent); err != nil {
		return err
	}
	registry.records = append(registry.records, strictCacheOutput{Owner: owner, Route: cleaned, OutputHash: fmt.Sprintf("%x", hash)})
	return nil
}

func linkCachedOutput(previousPath, outputRoot, relPath string) error {
	if strings.TrimSpace(previousPath) == "" || strings.TrimSpace(outputRoot) == "" {
		return os.ErrNotExist
	}
	info, err := os.Lstat(previousPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("cached output source %q must be a regular file", previousPath)
	}
	cleanRelPath, destination, err := resolveOutputWritePath(outputRoot, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := os.Link(previousPath, destination); err != nil {
		return fmt.Errorf("link cached output %q: %w", cleanRelPath, err)
	}
	return nil
}

func (registry *strictOutputRegistry) claim(relPath, owner string) error {
	if registry == nil {
		return fmt.Errorf("output registry is required")
	}
	cleaned := strings.Trim(strings.ReplaceAll(relPath, `\\`, "/"), "/")
	if cleaned == "" || cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("invalid output path %q", relPath)
	}
	if existing, ok := registry.claims[cleaned]; ok && existing != owner {
		if !strings.HasPrefix(existing, "asset:") || !strings.HasPrefix(owner, "asset:") {
			return fmt.Errorf("output path %q claimed by %q and %q", cleaned, existing, owner)
		}
		return nil
	}
	registry.claims[cleaned] = owner
	return nil
}

func prepareStagedOutputPublisher(vaultPath string, outputPath string) (*stagedOutputPublisher, error) {
	return prepareStagedOutputPublisherWithTransactionTracking(vaultPath, outputPath, nil)
}

func prepareStagedOutputPublisherWithTransactionTracking(vaultPath string, outputPath string, trackTransactionPath func(string)) (*stagedOutputPublisher, error) {
	boundary, err := internalfsutil.ResolveVaultOutput(vaultPath, outputPath)
	if err != nil {
		return nil, err
	}
	outputPath = boundary.OutputPath

	state, err := validateManagedOutputDir(outputPath)
	if err != nil {
		return nil, err
	}

	publishedMode := os.FileMode(0o755)
	if state.exists && state.info != nil {
		publishedMode = state.info.Mode().Perm()
	}
	publisher := &stagedOutputPublisher{
		outputPath:           outputPath,
		initialOutput:        state,
		publishedMode:        publishedMode,
		trackTransactionPath: trackTransactionPath,
	}
	stagingParent, err := nearestExistingOutputAncestor(outputPath)
	if err != nil {
		return nil, err
	}
	stagingPath, err := os.MkdirTemp(stagingParent, managedOutputTempPattern(outputPath, "stage"))
	if err != nil {
		return nil, fmt.Errorf("create staged output for %q: %w", outputPath, err)
	}
	publisher.stagingPath = stagingPath
	publisher.trackTransaction(stagingPath)
	return publisher, nil
}

func nearestExistingOutputAncestor(outputPath string) (string, error) {
	current := filepath.Dir(outputPath)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return "", fmt.Errorf("output parent path %q must be a non-symlink directory", current)
			}
			return current, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat output parent path %q: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve output parent for %q: reached missing filesystem root", outputPath)
		}
		current = parent
	}
}

func validateManagedOutputDir(outputPath string) (managedOutputDirState, error) {
	state, err := inspectManagedOutputDir(outputPath)
	if err != nil {
		return managedOutputDirState{}, err
	}
	if state.exists {
		if !state.isDir {
			return managedOutputDirState{}, fmt.Errorf("output path %q exists and is not a directory", outputPath)
		}
		if !state.empty && !state.hasMarker {
			return managedOutputDirState{}, fmt.Errorf("output path %q already contains unmanaged content; choose an empty directory or rebuild into a previously managed output directory", outputPath)
		}
	}
	return state, nil
}

func writeManagedOutputMarker(outputPath string) error {
	if err := os.MkdirAll(outputPath, 0o755); err != nil {
		return fmt.Errorf("create output directory %q: %w", outputPath, err)
	}
	if err := os.WriteFile(filepath.Join(outputPath, managedOutputMarkerFilename), []byte(managedOutputMarkerContents), 0o644); err != nil {
		return fmt.Errorf("write output marker for %q: %w", outputPath, err)
	}
	return nil
}

func managedOutputTempPattern(outputPath string, purpose string) string {
	base := strings.ReplaceAll(filepath.Base(outputPath), "*", "_")
	if strings.TrimSpace(base) == "" || base == "." {
		base = "site"
	}
	return fmt.Sprintf(".%s-oxpio-%s-*", base, purpose)
}

func reserveManagedOutputPath(outputPath string, purpose string) (string, error) {
	reservedPath, err := os.MkdirTemp(filepath.Dir(outputPath), managedOutputTempPattern(outputPath, purpose))
	if err != nil {
		return "", fmt.Errorf("reserve %s path for %q: %w", purpose, outputPath, err)
	}
	if err := os.Remove(reservedPath); err != nil {
		return "", fmt.Errorf("reserve %s path for %q: %w", purpose, outputPath, err)
	}
	return reservedPath, nil
}

func (publisher *stagedOutputPublisher) trackTransaction(path string) {
	if publisher == nil || publisher.trackTransactionPath == nil || strings.TrimSpace(path) == "" {
		return
	}
	publisher.trackTransactionPath(filepath.Clean(path))
}

func (publisher *stagedOutputPublisher) OutputPath() string {
	if publisher == nil {
		return ""
	}
	return publisher.stagingPath
}

func (publisher *stagedOutputPublisher) Finalize(success bool) error {
	if publisher == nil {
		return nil
	}
	if success {
		if err := publisher.publish(); err != nil {
			if publisher.committed {
				return err
			}
			rollbackErr := publisher.rollback()
			if rollbackErr != nil {
				return errors.Join(err, rollbackErr)
			}
			return err
		}
		return nil
	}
	return publisher.rollback()
}

func (publisher *stagedOutputPublisher) publish() error {
	if publisher == nil || publisher.stagingPath == "" {
		return nil
	}
	if err := publisher.revalidateOutput(); err != nil {
		return err
	}
	if err := publisher.createMissingOutputParents(); err != nil {
		return err
	}
	if publisher.backupPath == "" && publisher.initialOutput.exists {
		backupPath, err := reserveManagedOutputPath(publisher.outputPath, "backup")
		if err != nil {
			return err
		}
		publisher.backupPath = backupPath
		publisher.trackTransaction(backupPath)
		if err := stagedOutputRename(publisher.outputPath, backupPath); err != nil {
			return fmt.Errorf("backup managed output %q: %w", publisher.outputPath, err)
		}
	}
	publicationSource, err := stagedOutputStat(publisher.stagingPath)
	if err != nil {
		return fmt.Errorf("stat staged output %q before publication: %w", publisher.stagingPath, err)
	}
	publisher.publicationSource = publicationSource
	publisher.publicationAttempted = true
	if err := stagedOutputRename(publisher.stagingPath, publisher.outputPath); err != nil {
		return fmt.Errorf("publish staged output %q -> %q: %w", publisher.stagingPath, publisher.outputPath, err)
	}
	if err := os.Chmod(publisher.outputPath, publisher.publishedMode); err != nil {
		return fmt.Errorf("set published output permissions for %q: %w", publisher.outputPath, err)
	}
	publisher.stagingPath = ""
	publisher.createdParentDirs = nil
	publisher.committed = true
	if publisher.backupPath != "" {
		if err := removeOutputBackup(publisher.backupPath); err != nil {
			publisher.cleanupErr = fmt.Errorf("remove previous output backup %q: %w", publisher.backupPath, err)
			return nil
		}
		publisher.backupPath = ""
	}
	return nil
}

func removeOutputBackup(backupPath string) error {
	if err := filepath.WalkDir(backupPath, func(currentPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.Chmod(currentPath, info.Mode().Perm()|0o700)
	}); err != nil {
		return err
	}
	return stagedOutputRemoveAll(backupPath)
}

func (publisher *stagedOutputPublisher) revalidateOutput() error {
	current, err := inspectManagedOutputDir(publisher.outputPath)
	if err != nil {
		return err
	}
	initial := publisher.initialOutput
	if initial.exists != current.exists {
		return fmt.Errorf("output path %q changed while the site was being staged", publisher.outputPath)
	}
	if !initial.exists {
		return nil
	}
	if initial.info == nil || current.info == nil || !os.SameFile(initial.info, current.info) {
		return fmt.Errorf("output path %q was replaced while the site was being staged", publisher.outputPath)
	}
	if initial.empty && !current.empty {
		return fmt.Errorf("empty output path %q gained content while the site was being staged", publisher.outputPath)
	}
	if initial.hasMarker && !current.hasMarker {
		return fmt.Errorf("managed output path %q lost its marker while the site was being staged", publisher.outputPath)
	}
	return nil
}

func (publisher *stagedOutputPublisher) createMissingOutputParents() error {
	parent := filepath.Dir(publisher.outputPath)
	missing := make([]string, 0, 2)
	for current := parent; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("output parent path %q must be a non-symlink directory", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat output parent path %q: %w", current, err)
		}
		missing = append(missing, current)
		if next := filepath.Dir(current); next == current {
			return fmt.Errorf("create output parent for %q: reached missing filesystem root", publisher.outputPath)
		}
	}

	for index := len(missing) - 1; index >= 0; index-- {
		if err := os.Mkdir(missing[index], 0o755); err != nil {
			cleanupErr := publisher.removeCreatedOutputParents()
			return errors.Join(fmt.Errorf("create output parent %q: %w", missing[index], err), cleanupErr)
		}
		publisher.createdParentDirs = append(publisher.createdParentDirs, missing[index])
	}
	return nil
}

func (publisher *stagedOutputPublisher) removeCreatedOutputParents() error {
	var cleanupErr error
	for index := len(publisher.createdParentDirs) - 1; index >= 0; index-- {
		if err := os.Remove(publisher.createdParentDirs[index]); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove created output parent %q: %w", publisher.createdParentDirs[index], err))
		}
	}
	publisher.createdParentDirs = nil
	return cleanupErr
}

func (publisher *stagedOutputPublisher) rollback() (cleanupErr error) {
	if publisher == nil {
		return nil
	}
	defer func() {
		cleanupErr = errors.Join(cleanupErr, publisher.removeCreatedOutputParents())
	}()

	publishedStageAtOutput, identityErr := publisher.publicationAtOutput()
	cleanupErr = errors.Join(cleanupErr, identityErr)
	if publisher.stagingPath != "" {
		if err := stagedOutputRemoveAll(publisher.stagingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove staged output %q: %w", publisher.stagingPath, err))
		}
		publisher.stagingPath = ""
	}

	if publisher.backupPath == "" {
		if publishedStageAtOutput && !publisher.initialOutput.exists {
			failedPath, moved, quarantineErr := publisher.quarantinePublication()
			cleanupErr = errors.Join(cleanupErr, quarantineErr)
			if moved {
				if removeErr := stagedOutputRemoveAll(failedPath); removeErr != nil {
					cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove unconfirmed output quarantine %q: %w", failedPath, removeErr))
				}
			}
		}
		return cleanupErr
	}

	if _, err := stagedOutputStat(publisher.backupPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("stat previous output backup %q: %w", publisher.backupPath, err))
		}
		publisher.backupPath = ""
		return cleanupErr
	}
	if identityErr != nil {
		return cleanupErr
	}

	failedOutputPath := ""
	if publishedStageAtOutput {
		var moved bool
		var quarantineErr error
		failedOutputPath, moved, quarantineErr = publisher.quarantinePublication()
		cleanupErr = errors.Join(cleanupErr, quarantineErr)
		if !moved {
			return cleanupErr
		}
	}

	if restoreErr := stagedOutputRename(publisher.backupPath, publisher.outputPath); restoreErr != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("restore previous output %q: %w", publisher.outputPath, restoreErr))
	} else {
		publisher.backupPath = ""
	}
	if failedOutputPath != "" {
		if removeErr := stagedOutputRemoveAll(failedOutputPath); removeErr != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove quarantined failed output %q: %w", failedOutputPath, removeErr))
		}
	}
	return cleanupErr
}

func (publisher *stagedOutputPublisher) publicationAtOutput() (bool, error) {
	if publisher == nil || !publisher.publicationAttempted || publisher.publicationSource == nil {
		return false, nil
	}
	current, err := stagedOutputStat(publisher.outputPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat output path %q after failed publication: %w", publisher.outputPath, err)
	}
	if os.SameFile(publisher.publicationSource, current) {
		return true, nil
	}
	return false, fmt.Errorf("output path %q is occupied by a different filesystem object after failed publication", publisher.outputPath)
}

func (publisher *stagedOutputPublisher) quarantinePublication() (string, bool, error) {
	failedPath, err := reserveManagedOutputPath(publisher.outputPath, "failed")
	if err != nil {
		return "", false, err
	}
	publisher.trackTransaction(failedPath)
	var quarantineErr error
	if err := stagedOutputRename(publisher.outputPath, failedPath); err != nil {
		quarantineErr = fmt.Errorf("quarantine failed published output %q: %w", publisher.outputPath, err)
	}
	current, statErr := stagedOutputStat(failedPath)
	if statErr != nil {
		if !errors.Is(statErr, os.ErrNotExist) {
			quarantineErr = errors.Join(quarantineErr, fmt.Errorf("stat quarantined failed output %q: %w", failedPath, statErr))
		}
		return "", false, quarantineErr
	}
	if publisher.publicationSource == nil || !os.SameFile(publisher.publicationSource, current) {
		conflictErr := fmt.Errorf("quarantine path %q contains a different filesystem object", failedPath)
		if restoreErr := stagedOutputRename(failedPath, publisher.outputPath); restoreErr != nil {
			conflictErr = errors.Join(conflictErr, fmt.Errorf("restore concurrent output %q: %w", publisher.outputPath, restoreErr))
		}
		return "", false, errors.Join(quarantineErr, conflictErr)
	}
	return failedPath, true, quarantineErr
}

func inspectManagedOutputDir(outputPath string) (managedOutputDirState, error) {
	info, err := os.Lstat(outputPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return managedOutputDirState{}, nil
		}
		return managedOutputDirState{}, fmt.Errorf("stat output path %q: %w", outputPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return managedOutputDirState{}, fmt.Errorf("output path %q must not be a symbolic link", outputPath)
	}

	state := managedOutputDirState{info: info, exists: true, isDir: info.IsDir()}
	if !state.isDir {
		return state, nil
	}

	entries, err := os.ReadDir(outputPath)
	if err != nil {
		return managedOutputDirState{}, fmt.Errorf("read output path %q: %w", outputPath, err)
	}
	state.empty = len(entries) == 0
	for _, entry := range entries {
		if entry.Name() != managedOutputMarkerFilename {
			continue
		}
		_, markerData, _, markerErr := internalfsutil.ReadContainedRegularFile(outputPath, managedOutputMarkerFilename)
		state.hasMarker = markerErr == nil && string(markerData) == managedOutputMarkerContents
		break
	}

	return state, nil
}

func writeOutputFile(outputRoot string, relPath string, content []byte) error {
	cleanRelPath, absPath, err := resolveOutputWritePath(outputRoot, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("create output directory for %q: %w", cleanRelPath, err)
	}
	if err := os.WriteFile(absPath, content, 0o644); err != nil {
		return fmt.Errorf("write output file %q: %w", cleanRelPath, err)
	}
	return nil
}

func resolveOutputWritePath(outputRoot string, relPath string) (string, string, error) {
	return internalfsutil.ResolveOutputURLPath(outputRoot, relPath)
}
