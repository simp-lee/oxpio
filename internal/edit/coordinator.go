package edit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	internalbuild "github.com/simp-lee/obsite/internal/build"
	internalfsutil "github.com/simp-lee/obsite/internal/fsutil"
	"github.com/simp-lee/obsite/internal/model"
)

// AbsentSourceHash is the compare-and-swap sentinel for a source that does not
// exist when a create operation starts.
const AbsentSourceHash = "absent"

// ConflictError is returned when the source changed since the client read it.
type ConflictError struct {
	Path     string
	Expected string
	Actual   string
}

func (err *ConflictError) Error() string {
	return fmt.Sprintf("source conflict for %q: expected %s, found %s", err.Path, err.Expected, err.Actual)
}

// CandidateBuildError retains the ordinary analyzer diagnostics for the
// editor while identifying the source operation that failed validation/build.
type CandidateBuildError struct {
	Path   string
	Result *internalbuild.BuildResult
	Err    error
}

func (err *CandidateBuildError) Error() string {
	return fmt.Sprintf("candidate build for %q failed: %v", err.Path, err.Err)
}

func (err *CandidateBuildError) Unwrap() error { return err.Err }

// TransactionResult describes a successfully published source mutation.
type TransactionResult struct {
	SourceHash string
	Build      *internalbuild.BuildResult
}

// Coordinator serializes source mutations and candidate builds for one edit
// server. It is deliberately independent from HTTP and can be tested directly.
type Coordinator struct {
	vault  string
	output string

	mu      sync.Mutex
	catalog *model.SourceCatalog
}

// NewCoordinator creates a serialized source/output transaction coordinator.
func NewCoordinator(vaultPath, outputPath string, catalog *model.SourceCatalog) (*Coordinator, error) {
	vault, err := internalfsutil.ResolveVaultPath(vaultPath)
	if err != nil {
		return nil, err
	}
	boundary, err := internalfsutil.ResolveVaultOutput(vault, outputPath)
	if err != nil {
		return nil, err
	}
	return &Coordinator{vault: boundary.VaultPath, output: boundary.OutputPath, catalog: cloneCatalog(catalog)}, nil
}

// CatalogSnapshot returns a copy of the current normalized source mapping.
func (coordinator *Coordinator) CatalogSnapshot() *model.SourceCatalog {
	if coordinator == nil {
		return nil
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return cloneCatalog(coordinator.catalog)
}

// Save replaces an existing article or section source after full-byte CAS.
func (coordinator *Coordinator) Save(relPath, expectedHash string, content []byte) (TransactionResult, error) {
	return coordinator.mutate(relPath, expectedHash, content, false, false)
}

// Create creates a new article source. expectedHash must be AbsentSourceHash.
func (coordinator *Coordinator) Create(relPath, expectedHash string, content []byte) (TransactionResult, error) {
	return coordinator.mutate(relPath, expectedHash, content, false, true)
}

// Delete removes an existing article source after full-byte CAS.
func (coordinator *Coordinator) Delete(relPath, expectedHash string) (TransactionResult, error) {
	return coordinator.mutate(relPath, expectedHash, nil, true, false)
}

func (coordinator *Coordinator) mutate(relPath, expectedHash string, content []byte, deleting, creating bool) (TransactionResult, error) {
	if coordinator == nil {
		return TransactionResult{}, errors.New("source coordinator is nil")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()

	if err := validateSourceRelPath(relPath); err != nil {
		return TransactionResult{}, err
	}
	entry := catalogEntry(coordinator.catalog, relPath)
	if creating {
		if err := validateManagedRelPath(relPath); err != nil {
			return TransactionResult{}, fmt.Errorf("source path %q must be a contained normalized Markdown path: %w", relPath, err)
		}
		if !strings.EqualFold(path.Ext(relPath), ".md") {
			return TransactionResult{}, fmt.Errorf("source path %q must be a contained normalized Markdown path", relPath)
		}
		if entry != nil || strings.EqualFold(path.Base(relPath), "_index.md") {
			return TransactionResult{}, fmt.Errorf("cannot create existing or section source %q", relPath)
		}
		if err := coordinator.validateCreateParent(relPath); err != nil {
			return TransactionResult{}, err
		}
	} else {
		if entry == nil || (entry.Kind != "article" && entry.Kind != "section") {
			return TransactionResult{}, fmt.Errorf("source %q is not an editable Markdown source", relPath)
		}
		// The catalog is the source of truth for the exact vault-relative path.
		// In particular, do not reconstruct or normalize its extension: the
		// scanner accepts Markdown extensions case-insensitively.
		relPath = entry.RelPath
		if deleting && entry.Kind != "article" {
			return TransactionResult{}, fmt.Errorf("cannot delete section source %q", relPath)
		}
	}

	current, exists, err := readSource(coordinator.vault, relPath)
	if err != nil {
		return TransactionResult{}, err
	}
	actual := hashForAbsentOrBytes(current)
	if !exists {
		actual = AbsentSourceHash
	}
	if actual != expectedHash {
		return TransactionResult{}, &ConflictError{Path: relPath, Expected: expectedHash, Actual: actual}
	}
	if creating && exists {
		return TransactionResult{}, &ConflictError{Path: relPath, Expected: AbsentSourceHash, Actual: actual}
	}
	if !creating && !exists {
		return TransactionResult{}, &ConflictError{Path: relPath, Expected: expectedHash, Actual: AbsentSourceHash}
	}

	overlay := map[string][]byte{}
	deleted := map[string]bool{}
	if deleting {
		deleted[relPath] = true
	} else {
		overlay[relPath] = append([]byte(nil), content...)
	}
	stageRoot, err := os.MkdirTemp(filepath.Dir(coordinator.output), ".obsite-edit-stage-*")
	if err != nil {
		return TransactionResult{}, fmt.Errorf("create edit staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stageRoot) }()
	stageOutput := filepath.Join(stageRoot, "public")
	built, err := internalbuild.BuildWithOptions(coordinator.vault, stageOutput, internalbuild.Options{SourceOverlay: overlay, SourceDeleted: deleted})
	if err != nil {
		return TransactionResult{}, &CandidateBuildError{Path: relPath, Result: built, Err: err}
	}

	current, exists, err = readSource(coordinator.vault, relPath)
	if err != nil {
		return TransactionResult{}, err
	}
	actual = hashForAbsentOrBytes(current)
	if !exists {
		actual = AbsentSourceHash
	}
	if actual != expectedHash {
		return TransactionResult{}, &ConflictError{Path: relPath, Expected: expectedHash, Actual: actual}
	}

	if err := commitSource(coordinator.vault, relPath, expectedHash, content, deleting, creating); err != nil {
		return TransactionResult{}, err
	}
	committedHash := AbsentSourceHash
	if !deleting {
		committedHash = hashForAbsentOrBytes(content)
	}

	currentAfterSource, existsAfterSource, readErr := readSource(coordinator.vault, relPath)
	actualAfterSource := AbsentSourceHash
	if existsAfterSource {
		actualAfterSource = hashForAbsentOrBytes(currentAfterSource)
	}
	if readErr != nil {
		return TransactionResult{}, errors.Join(restoreSource(coordinator.vault, relPath, committedHash, current, exists), readErr)
	}
	if actualAfterSource != committedHash {
		return TransactionResult{}, errors.Join(restoreSource(coordinator.vault, relPath, committedHash, current, exists), &ConflictError{Path: relPath, Expected: committedHash, Actual: actualAfterSource})
	}

	rollbackSource := func() error {
		return restoreSource(coordinator.vault, relPath, committedHash, current, exists)
	}
	rollbackOutput, finalizeOutput, err := publishOutput(stageOutput, coordinator.output)
	if err != nil {
		return TransactionResult{}, errors.Join(rollbackSource(), err)
	}
	currentAfterCommit, nowExists, readErr := readSource(coordinator.vault, relPath)
	actualAfterCommit := AbsentSourceHash
	if nowExists {
		actualAfterCommit = hashForAbsentOrBytes(currentAfterCommit)
	}
	if readErr != nil {
		return TransactionResult{}, errors.Join(rollbackOutput(), rollbackSource(), readErr)
	}
	if actualAfterCommit != committedHash {
		return TransactionResult{}, errors.Join(rollbackOutput(), rollbackSource(), &ConflictError{Path: relPath, Expected: committedHash, Actual: actualAfterCommit})
	}
	if err := verifyCommittedSource(coordinator.vault, relPath, committedHash); err != nil {
		return TransactionResult{}, errors.Join(rollbackOutput(), rollbackSource(), err)
	}
	if err := finalizeOutput(); err != nil {
		return TransactionResult{}, errors.Join(rollbackOutput(), rollbackSource(), err)
	}
	coordinator.catalog = cloneCatalog(built.Catalog)
	return TransactionResult{SourceHash: committedHash, Build: built}, nil
}

func (coordinator *Coordinator) validateCreateParent(relPath string) error {
	parent := path.Dir(relPath)
	if parent == "." {
		parent = "."
	}
	if coordinator.catalog != nil {
		for _, entry := range coordinator.catalog.Entries {
			if entry.Kind == "section" && entry.SectionPath == parent {
				return nil
			}
		}
	}
	return fmt.Errorf("source parent section %q is not published or does not exist", parent)
}

func validateSourceRelPath(relPath string) error {
	if strings.TrimSpace(relPath) == "" || strings.Contains(relPath, `\`) || strings.HasPrefix(relPath, "/") || path.Clean(relPath) != relPath || strings.HasPrefix(relPath, "../") || strings.Contains(relPath, "?") || strings.Contains(relPath, "#") {
		return fmt.Errorf("source path %q must be a contained normalized Markdown path", relPath)
	}
	for _, segment := range strings.Split(relPath, "/") {
		if segment == "" || segment == "." || segment == ".." || !internalfsutil.IsPortableSitePath(segment) {
			return fmt.Errorf("source path %q must be a contained normalized Markdown path", relPath)
		}
	}
	return nil
}

func readSource(vault, relPath string) ([]byte, bool, error) {
	_, data, _, err := internalfsutil.ReadContainedRegularFile(vault, relPath)
	if err == nil {
		return data, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		if parentErr := inspectSourceParent(vault, relPath); parentErr != nil && !errors.Is(parentErr, os.ErrNotExist) {
			return nil, false, fmt.Errorf("inspect source parent: %w", parentErr)
		}
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("read source %q: %w", relPath, err)
}

func verifyCommittedSource(vault, relPath, expected string) error {
	data, exists, err := readSource(vault, relPath)
	if err != nil {
		return err
	}
	actual := AbsentSourceHash
	if exists {
		actual = hashForAbsentOrBytes(data)
	}
	if actual != expected {
		return &ConflictError{Path: relPath, Expected: expected, Actual: actual}
	}
	return nil
}

func inspectSourceParent(vault, relPath string) error {
	parent := path.Dir(relPath)
	if parent == "." {
		return nil
	}
	_, _, err := internalfsutil.InspectContainedDirectory(vault, parent)
	return err
}

func commitSource(vault, relPath, expected string, content []byte, deleting, creating bool) error {
	current, exists, err := readSource(vault, relPath)
	if err != nil {
		return err
	}
	actual := AbsentSourceHash
	if exists {
		actual = hashForAbsentOrBytes(current)
	}
	if actual != expected {
		return &ConflictError{Path: relPath, Expected: expected, Actual: actual}
	}
	filename := filepath.Join(vault, filepath.FromSlash(relPath))
	if err := inspectSourceParent(vault, relPath); err != nil {
		return fmt.Errorf("inspect source parent: %w", err)
	}
	if creating {
		if exists {
			return &ConflictError{Path: relPath, Expected: AbsentSourceHash, Actual: actual}
		}
		return createSourceNoReplace(filename, relPath, content)
	}

	casName, err := makeCASSnapshot(filename)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(casName) }()
	if err := verifyCASSnapshot(filename, casName, relPath, expected); err != nil {
		return err
	}
	if deleting {
		displaced, err := moveSourceForCAS(filename)
		if err != nil {
			return fmt.Errorf("delete source %q: %w", relPath, err)
		}
		if err := verifyCASSnapshot(displaced, casName, relPath, expected); err != nil {
			preserveMovedSource(displaced, filename)
			return err
		}
		if _, err := os.Lstat(filename); err == nil {
			preserveMovedSource(displaced, filename)
			return &ConflictError{Path: relPath, Expected: expected, Actual: currentSourceHashFromPath(filename)}
		} else if !errors.Is(err, os.ErrNotExist) {
			preserveMovedSource(displaced, filename)
			return err
		}
		if err := verifySnapshotBytes(casName, expected, relPath); err != nil {
			preserveMovedSource(displaced, filename)
			return err
		}
		if err := os.Remove(displaced); err != nil {
			return errors.Join(fmt.Errorf("delete source %q: %w", relPath, err), restoreMovedSource(displaced, filename))
		}
		return nil
	}

	temporary, err := writeSourceTransactionFile(filepath.Dir(filename), content)
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := verifyCASSnapshot(filename, casName, relPath, expected); err != nil {
		return err
	}
	displaced, err := moveSourceForCAS(filename)
	if err != nil {
		return fmt.Errorf("replace source %q: %w", relPath, err)
	}
	if err := verifyCASSnapshot(displaced, casName, relPath, expected); err != nil {
		preserveMovedSource(displaced, filename)
		return err
	}
	if _, err := os.Lstat(filename); err == nil {
		preserveMovedSource(displaced, filename)
		return &ConflictError{Path: relPath, Expected: expected, Actual: currentSourceHashFromPath(filename)}
	} else if !errors.Is(err, os.ErrNotExist) {
		preserveMovedSource(displaced, filename)
		return err
	}
	if err := os.Link(temporaryName, filename); err != nil {
		preserveMovedSource(displaced, filename)
		if errors.Is(err, os.ErrExist) {
			return &ConflictError{Path: relPath, Expected: expected, Actual: currentSourceHashFromPath(filename)}
		}
		return fmt.Errorf("replace source %q: %w", relPath, err)
	}
	if err := verifySnapshotBytes(casName, expected, relPath); err != nil {
		return errors.Join(err, rollbackReplacedSource(filename, displaced, hashForAbsentOrBytes(content), relPath))
	}
	if err := os.Remove(displaced); err != nil {
		return errors.Join(fmt.Errorf("finalize source %q: %w", relPath, err), rollbackReplacedSource(filename, displaced, hashForAbsentOrBytes(content), relPath))
	}
	return nil
}

func moveSourceForCAS(filename string) (string, error) {
	temporary, err := os.CreateTemp(filepath.Dir(filename), ".obsite-displaced-*")
	if err != nil {
		return "", err
	}
	name := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := os.Remove(name); err != nil {
		return "", err
	}
	if err := os.Rename(filename, name); err != nil {
		return "", err
	}
	return name, nil
}

func preserveMovedSource(displaced, filename string) {
	if _, err := os.Lstat(filename); errors.Is(err, os.ErrNotExist) {
		_ = os.Rename(displaced, filename)
		return
	}
	_ = os.Remove(displaced)
}

func restoreMovedSource(displaced, filename string) error {
	if _, err := os.Lstat(filename); err == nil {
		return fmt.Errorf("source changed before rollback")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(displaced, filename)
}

func rollbackReplacedSource(filename, displaced, expected, relPath string) error {
	if actual := currentSourceHashFromPath(filename); actual != expected {
		return &ConflictError{Path: relPath, Expected: expected, Actual: actual}
	}
	candidate, err := moveSourceForCAS(filename)
	if err != nil {
		return err
	}
	if err := restoreMovedSource(displaced, filename); err != nil {
		return errors.Join(err, restoreMovedSource(candidate, filename))
	}
	return os.Remove(candidate)
}

func createSourceNoReplace(filename, relPath string, content []byte) error {
	temporary, err := writeSourceTransactionFile(filepath.Dir(filename), content)
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := os.Link(temporaryName, filename); err != nil {
		if errors.Is(err, os.ErrExist) {
			return &ConflictError{Path: relPath, Expected: AbsentSourceHash, Actual: "present"}
		}
		return fmt.Errorf("create source %q: %w", relPath, err)
	}
	return nil
}

func writeSourceTransactionFile(parent string, content []byte) (*os.File, error) {
	temporary, err := os.CreateTemp(parent, ".obsite-source-*")
	if err != nil {
		return nil, fmt.Errorf("create source transaction file: %w", err)
	}
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return nil, err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return nil, fmt.Errorf("write source transaction file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return nil, fmt.Errorf("sync source transaction file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporary.Name())
		return nil, err
	}
	return temporary, nil
}

func makeCASSnapshot(filename string) (string, error) {
	temporary, err := os.CreateTemp(filepath.Dir(filename), ".obsite-cas-*")
	if err != nil {
		return "", fmt.Errorf("create source CAS marker: %w", err)
	}
	name := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	_ = os.Remove(name)
	if err := os.Link(filename, name); err != nil {
		return "", fmt.Errorf("create source CAS marker: %w", err)
	}
	return name, nil
}

func verifyCASSnapshot(filename, snapshot, relPath, expected string) error {
	currentInfo, err := os.Stat(filename)
	if err != nil {
		return &ConflictError{Path: relPath, Expected: expected, Actual: AbsentSourceHash}
	}
	snapshotInfo, err := os.Stat(snapshot)
	if err != nil || !os.SameFile(currentInfo, snapshotInfo) {
		return &ConflictError{Path: relPath, Expected: expected, Actual: currentSourceHashFromPath(filename)}
	}
	return verifySnapshotBytes(snapshot, expected, relPath)
}

func verifySnapshotBytes(snapshot, expected, relPath string) error {
	data, err := os.ReadFile(snapshot)
	if err != nil {
		return fmt.Errorf("read source CAS marker: %w", err)
	}
	actual := hashForAbsentOrBytes(data)
	if actual != expected {
		return &ConflictError{Path: relPath, Expected: expected, Actual: actual}
	}
	return nil
}

func currentSourceHashFromPath(filename string) string {
	data, err := os.ReadFile(filename)
	if err != nil {
		return AbsentSourceHash
	}
	return hashForAbsentOrBytes(data)
}

func restoreSource(vault, relPath, expected string, original []byte, existed bool) error {
	current, nowExists, err := readSource(vault, relPath)
	if err != nil {
		return err
	}
	actual := AbsentSourceHash
	if nowExists {
		actual = hashForAbsentOrBytes(current)
	}
	if actual != expected {
		return &ConflictError{Path: relPath, Expected: expected, Actual: actual}
	}
	if !existed {
		if nowExists {
			return os.Remove(filepath.Join(vault, filepath.FromSlash(relPath)))
		}
		return nil
	}
	return commitSource(vault, relPath, expected, original, false, expected == AbsentSourceHash)
}

func publishOutput(stage, output string) (func() error, func() error, error) {
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return nil, nil, err
	}
	backupRoot, err := os.MkdirTemp(filepath.Dir(output), ".obsite-output-backup-*")
	if err != nil {
		return nil, nil, err
	}
	backup := filepath.Join(backupRoot, "previous")
	outputInfo, outputErr := os.Lstat(output)
	hadOutput := outputErr == nil
	if outputErr == nil && (outputInfo.Mode()&os.ModeSymlink != 0 || !outputInfo.IsDir()) {
		_ = os.RemoveAll(backupRoot)
		return nil, nil, fmt.Errorf("formal output %q must be a directory and not a symbolic link", output)
	}
	if outputErr != nil && !errors.Is(outputErr, os.ErrNotExist) {
		_ = os.RemoveAll(backupRoot)
		return nil, nil, outputErr
	}
	if hadOutput {
		currentInfo, currentErr := os.Lstat(output)
		if currentErr != nil || !os.SameFile(outputInfo, currentInfo) {
			_ = os.RemoveAll(backupRoot)
			return nil, nil, fmt.Errorf("formal output changed before publication")
		}
		if err := os.Rename(output, backup); err != nil {
			_ = os.RemoveAll(backupRoot)
			return nil, nil, fmt.Errorf("backup formal output: %w", err)
		}
	}
	if err := os.Rename(stage, output); err != nil {
		publishErr := fmt.Errorf("publish formal output: %w", err)
		var restoreErr error
		restored := !hadOutput
		if hadOutput {
			if _, statErr := os.Lstat(output); statErr == nil {
				restoreErr = fmt.Errorf("formal output changed while restoring after failed publication")
			} else if !errors.Is(statErr, os.ErrNotExist) {
				restoreErr = fmt.Errorf("inspect formal output before restore: %w", statErr)
			} else if renameErr := os.Rename(backup, output); renameErr != nil {
				restoreErr = fmt.Errorf("restore formal output: %w", renameErr)
			} else if restoredInfo, inspectErr := os.Lstat(output); inspectErr != nil {
				restoreErr = fmt.Errorf("confirm restored formal output: %w", inspectErr)
			} else if !os.SameFile(outputInfo, restoredInfo) {
				restoreErr = fmt.Errorf("formal output restore identity changed")
			} else {
				restored = true
			}
		}
		var cleanupErr error
		if restored {
			if err := os.RemoveAll(backupRoot); err != nil {
				cleanupErr = fmt.Errorf("cleanup formal output backup: %w", err)
			}
		}
		return nil, nil, errors.Join(publishErr, restoreErr, cleanupErr)
	}
	restorePublishedOutput := func() error {
		var rollbackErr error
		if _, err := os.Lstat(output); err == nil {
			if err := os.RemoveAll(output); err != nil {
				rollbackErr = err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			rollbackErr = err
		}
		if hadOutput {
			if _, err := os.Lstat(output); errors.Is(err, os.ErrNotExist) {
				if err := os.Rename(backup, output); err != nil {
					rollbackErr = errors.Join(rollbackErr, err)
				}
			}
		}
		if err := os.RemoveAll(backupRoot); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
		}
		return rollbackErr
	}
	publishedInfo, err := os.Lstat(output)
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("inspect published formal output: %w", err), restorePublishedOutput())
	}
	rolledBack := false
	rollback := func() error {
		if rolledBack {
			return nil
		}
		rolledBack = true
		var rollbackErr error
		currentInfo, currentErr := os.Lstat(output)
		if currentErr == nil {
			if os.SameFile(publishedInfo, currentInfo) {
				if err := os.RemoveAll(output); err != nil {
					rollbackErr = err
				}
			} else {
				rollbackErr = fmt.Errorf("formal output changed during rollback")
			}
		} else if !errors.Is(currentErr, os.ErrNotExist) {
			rollbackErr = currentErr
		}
		if hadOutput {
			if _, statErr := os.Lstat(output); errors.Is(statErr, os.ErrNotExist) {
				if err := os.Rename(backup, output); err != nil {
					rollbackErr = errors.Join(rollbackErr, err)
				}
			} else if statErr == nil {
				// Preserve an external replacement rather than overwriting it.
			} else {
				rollbackErr = errors.Join(rollbackErr, statErr)
			}
		}
		if err := os.RemoveAll(backupRoot); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
		}
		return rollbackErr
	}
	finalize := func() error {
		return os.RemoveAll(backupRoot)
	}
	return rollback, finalize, nil
}

func hashForAbsentOrBytes(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func catalogEntry(catalog *model.SourceCatalog, relPath string) *model.SourceCatalogEntry {
	if catalog == nil {
		return nil
	}
	for index := range catalog.Entries {
		if catalog.Entries[index].RelPath == relPath {
			return &catalog.Entries[index]
		}
	}
	return nil
}

func cloneCatalog(catalog *model.SourceCatalog) *model.SourceCatalog {
	if catalog == nil {
		return nil
	}
	clone := *catalog
	clone.Entries = append([]model.SourceCatalogEntry(nil), catalog.Entries...)
	return &clone
}
