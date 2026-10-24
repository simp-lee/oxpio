package edit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	internalapplication "github.com/simp-lee/oxpio/internal/application"
	internalbuild "github.com/simp-lee/oxpio/internal/build"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/model"
	internalpublish "github.com/simp-lee/oxpio/internal/publish"
	"github.com/simp-lee/oxpio/internal/workspace"
)

// AbsentSourceHash is the compare-and-swap sentinel for a source that does not
// exist when a create operation starts.
const AbsentSourceHash = "absent"

var editOutputRemoveAll = os.RemoveAll

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
	RelPath    string
	Build      *internalbuild.BuildResult
	// SourceCleanupError reports temporary source/backup cleanup failure after
	// a successful commit. The mutation is still published and must not be retried.
	SourceCleanupError error
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
	return coordinator.SaveContext(context.Background(), relPath, expectedHash, content)
}

// SaveContext replaces an existing article or section source and stops before
// source commit when the caller's request context is cancelled.
func (coordinator *Coordinator) SaveContext(ctx context.Context, relPath, expectedHash string, content []byte) (TransactionResult, error) {
	return coordinator.mutate(ctx, relPath, expectedHash, content, false, false)
}

// Create creates a new article source. expectedHash must be AbsentSourceHash.
func (coordinator *Coordinator) Create(relPath, expectedHash string, content []byte) (TransactionResult, error) {
	return coordinator.CreateContext(context.Background(), relPath, expectedHash, content)
}

// CreateContext creates a new article source and stops before source commit
// when the caller's request context is cancelled.
func (coordinator *Coordinator) CreateContext(ctx context.Context, relPath, expectedHash string, content []byte) (TransactionResult, error) {
	return coordinator.mutate(ctx, relPath, expectedHash, content, false, true)
}

// Delete removes an existing article source after full-byte CAS.
func (coordinator *Coordinator) Delete(relPath, expectedHash string) (TransactionResult, error) {
	return coordinator.DeleteContext(context.Background(), relPath, expectedHash)
}

// DeleteContext removes an existing article source and stops before source
// commit when the caller's request context is cancelled.
func (coordinator *Coordinator) DeleteContext(ctx context.Context, relPath, expectedHash string) (TransactionResult, error) {
	return coordinator.mutate(ctx, relPath, expectedHash, nil, true, false)
}

func (coordinator *Coordinator) mutate(ctx context.Context, relPath, expectedHash string, content []byte, deleting, creating bool) (TransactionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if coordinator == nil {
		return TransactionResult{}, errors.New("source coordinator is nil")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if err := recoverPendingWorkspaceJournal(coordinator.vault); err != nil {
		return TransactionResult{}, err
	}
	lease, err := workspace.Acquire(coordinator.vault)
	if err != nil {
		return TransactionResult{}, err
	}
	defer func() { _ = lease.Release() }()
	if err := ctx.Err(); err != nil {
		return TransactionResult{}, err
	}

	if err := validateSourceRelPath(relPath); err != nil {
		return TransactionResult{}, err
	}
	if !deleting && !utf8.Valid(content) {
		return TransactionResult{}, fmt.Errorf("source %q must contain valid UTF-8", relPath)
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
	inputFingerprint, err := workspace.Fingerprint(coordinator.vault, coordinator.output)
	if err != nil {
		return TransactionResult{}, err
	}
	keepArtifacts := false
	stageRoot, err := os.MkdirTemp(filepath.Dir(coordinator.output), ".oxpio-edit-stage-*")
	if err != nil {
		return TransactionResult{}, fmt.Errorf("create edit staging directory: %w", err)
	}
	defer func() {
		if !keepArtifacts {
			_ = os.RemoveAll(stageRoot)
		}
	}()
	stageOutput := filepath.Join(stageRoot, "public")
	built, err := internalapplication.BuildCandidate(coordinator.vault, stageOutput, internalapplication.CandidateBuildOptions{
		SourceOverlay:    overlay,
		SourceDeleted:    deleted,
		SourceOutputPath: coordinator.output,
	})
	if err != nil {
		return TransactionResult{}, &CandidateBuildError{Path: relPath, Result: built, Err: err}
	}
	if err := ctx.Err(); err != nil {
		return TransactionResult{}, err
	}
	if currentFingerprint, fingerprintErr := workspace.Fingerprint(coordinator.vault, coordinator.output); fingerprintErr != nil {
		return TransactionResult{}, fingerprintErr
	} else if currentFingerprint != inputFingerprint {
		return TransactionResult{}, fmt.Errorf("CONCURRENT_INPUT: Vault inputs changed during candidate build")
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

	committedHash := AbsentSourceHash
	if !deleting {
		committedHash = hashForAbsentOrBytes(content)
	}
	sourceBeforeHash := AbsentSourceHash
	if exists {
		sourceBeforeHash = hashForAbsentOrBytes(current)
	}
	sourceFilename := filepath.Join(coordinator.vault, filepath.FromSlash(relPath))
	artifactRoot, err := os.MkdirTemp(filepath.Dir(sourceFilename), ".oxpio-source-txn-*")
	if err != nil {
		return TransactionResult{}, err
	}
	defer func() {
		if !keepArtifacts {
			_ = os.RemoveAll(artifactRoot)
		}
	}()
	if exists {
		if _, err := createSourceJournalBackupAt(artifactRoot, current); err != nil {
			return TransactionResult{}, err
		}
	}
	expectedAfterFingerprint, err := workspace.FingerprintWithOverlay(coordinator.vault, coordinator.output, overlay, deleted)
	if err != nil {
		return TransactionResult{}, err
	}
	transaction, err := internalapplication.AdoptCandidateStaging(coordinator.vault, coordinator.output, stageOutput, internalapplication.CandidatePublishOptions{
		Durable:          true,
		InputFingerprint: inputFingerprint,
		RemoveAll:        editOutputRemoveAll,
		JournalSeed:      &workspace.Journal{SourcePath: sourceFilename, SourceBeforePath: sourceFilename, SourceBeforeHash: sourceBeforeHash, SourceAfterHash: committedHash, BackupPath: artifactRoot, CleanupPaths: []string{stageRoot}},
	})
	if err != nil {
		return TransactionResult{}, err
	}
	rollbackSource := func() error {
		return restoreSource(coordinator.vault, relPath, committedHash, current, exists)
	}
	sourceCommitted := false
	abortBeforeOutput := func(operationErr error) (TransactionResult, error) {
		if !sourceCommitted {
			currentSource, currentExists, readErr := readSource(coordinator.vault, relPath)
			actualSource := AbsentSourceHash
			if currentExists {
				actualSource = hashForAbsentOrBytes(currentSource)
			}
			if readErr == nil && actualSource == expectedHash {
				return TransactionResult{}, errors.Join(operationErr, transaction.Abort())
			}
			sourceCommitted = true
		}
		rollbackErr := rollbackSource()
		if rollbackErr != nil {
			keepArtifacts = true
			preserveErr := transaction.PreserveForRecovery()
			return TransactionResult{}, errors.Join(operationErr, rollbackErr, preserveErr, fmt.Errorf("%w: source rollback failed", workspace.ErrRecovery))
		}
		markerErr := workspace.ReplaceCompleted(coordinator.vault, []workspace.CompletedPath{{Path: sourceFilename, AfterHash: sourceBeforeHash}})
		return TransactionResult{}, errors.Join(operationErr, markerErr, transaction.Abort())
	}
	validateInput := func() error {
		currentFingerprint, fingerprintErr := workspace.Fingerprint(coordinator.vault, coordinator.output)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		if currentFingerprint != expectedAfterFingerprint {
			return fmt.Errorf("CONCURRENT_INPUT: Vault inputs changed during publish")
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return abortBeforeOutput(err)
	}
	if err := transaction.PrepareSourceCommit(expectedAfterFingerprint, validateInput); err != nil {
		return abortBeforeOutput(err)
	}
	if err := ctx.Err(); err != nil {
		return abortBeforeOutput(err)
	}
	if err := commitSource(coordinator.vault, relPath, expectedHash, content, deleting, creating, artifactRoot); err != nil {
		return abortBeforeOutput(err)
	}
	sourceCommitted = true
	currentAfterSource, existsAfterSource, readErr := readSource(coordinator.vault, relPath)
	actualAfterSource := AbsentSourceHash
	if existsAfterSource {
		actualAfterSource = hashForAbsentOrBytes(currentAfterSource)
	}
	if readErr != nil {
		return abortBeforeOutput(readErr)
	}
	if actualAfterSource != committedHash {
		return abortBeforeOutput(&ConflictError{Path: relPath, Expected: committedHash, Actual: actualAfterSource})
	}
	if err := transaction.MarkSourceCommitted(); err != nil {
		return abortBeforeOutput(err)
	}

	if err := workspace.RecordCompleted(coordinator.vault, []workspace.CompletedPath{{Path: sourceFilename, AfterHash: committedHash}}); err != nil {
		return abortBeforeOutput(fmt.Errorf("watch suppression marker could not be recorded: %w", err))
	}
	commit, err := transaction.Commit()
	if err != nil {
		return abortBeforeOutput(err)
	}
	var postCommitWarning error
	currentAfterCommit, nowExists, readErr := readSource(coordinator.vault, relPath)
	actualAfterCommit := AbsentSourceHash
	if nowExists {
		actualAfterCommit = hashForAbsentOrBytes(currentAfterCommit)
	}
	if readErr != nil {
		postCommitWarning = readErr
	} else if actualAfterCommit != committedHash {
		postCommitWarning = &ConflictError{Path: relPath, Expected: committedHash, Actual: actualAfterCommit}
	} else if verifyErr := verifyCommittedSource(coordinator.vault, relPath, committedHash); verifyErr != nil {
		postCommitWarning = verifyErr
	}
	postCommitWarning = errors.Join(postCommitWarning, commit.CleanupError)
	if verifyErr := verifyCommittedSource(coordinator.vault, relPath, committedHash); verifyErr != nil {
		postCommitWarning = errors.Join(postCommitWarning, verifyErr)
	}
	if postCommitWarning != nil {
		built.OutputCleanupError = postCommitWarning
	}
	coordinator.catalog = cloneCatalog(built.Catalog)
	return TransactionResult{SourceHash: committedHash, RelPath: relPath, Build: built}, nil
}

func recoverPendingWorkspaceJournal(vault string) error {
	_, err := internalpublish.Recover(vault, false)
	return err
}

func (coordinator *Coordinator) validateCreateParent(relPath string) error {
	if relPath == "_index.md" {
		return nil
	}
	parent := path.Dir(relPath)
	if path.Base(relPath) == "_index.md" {
		parent = path.Dir(parent)
	}
	sectionIndex := "_index.md"
	if parent != "." {
		sectionIndex = path.Join(parent, sectionIndex)
	}
	if _, _, err := internalfsutil.InspectContainedRegularFile(coordinator.vault, filepath.FromSlash(sectionIndex)); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect parent section %q: %w", parent, err)
	}
	return fmt.Errorf("source parent folder %q is not a section; create %q first", parent, sectionIndex)
}

func validateSourceRelPath(relPath string) error {
	if strings.TrimSpace(relPath) == "" || strings.Contains(relPath, `\`) || strings.HasPrefix(relPath, "/") || path.Clean(relPath) != relPath || strings.HasPrefix(relPath, "../") || strings.Contains(relPath, "?") {
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

func commitSource(vault, relPath, expected string, content []byte, deleting, creating bool, _ ...string) error {
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
	if !creating {
		if err := ensureNoReplaceRename(); err != nil {
			return err
		}
	}
	if creating {
		err = workspace.AtomicCreateFile(filename, content, 0o644)
	} else if deleting {
		err = workspace.RemoveFileCAS(filename, expected)
	} else {
		err = workspace.AtomicWriteFileCAS(filename, expected, content, 0o644)
	}
	if errors.Is(err, workspace.ErrCASConflict) || errors.Is(err, os.ErrExist) {
		latest, latestExists, latestErr := readSource(vault, relPath)
		if latestErr != nil {
			return errors.Join(&ConflictError{Path: relPath, Expected: expected, Actual: AbsentSourceHash}, latestErr)
		}
		latestHash := AbsentSourceHash
		if latestExists {
			latestHash = hashForAbsentOrBytes(latest)
		}
		return &ConflictError{Path: relPath, Expected: expected, Actual: latestHash}
	}
	return err
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
			return removeCreatedPath(vault, relPath, expected)
		}
		return nil
	}
	return commitSource(vault, relPath, expected, original, false, expected == AbsentSourceHash)
}

func hashForAbsentOrBytes(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func createSourceJournalBackupAt(directory string, content []byte) (string, error) {
	backup, err := os.CreateTemp(directory, ".oxpio-source-backup-*")
	if err != nil {
		return "", fmt.Errorf("create source backup: %w", err)
	}
	name := backup.Name()
	cleanup := func() {
		_ = backup.Close()
		_ = os.Remove(name)
	}
	if _, err := backup.Write(content); err != nil {
		cleanup()
		return "", fmt.Errorf("write source backup: %w", err)
	}
	if err := backup.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("sync source backup: %w", err)
	}
	if err := backup.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("close source backup: %w", err)
	}
	return name, nil
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
	clone.Entries = make([]model.SourceCatalogEntry, 0, len(catalog.Entries))
	for _, entry := range catalog.Entries {
		entry.RawSource = append([]byte(nil), entry.RawSource...)
		clone.Entries = append(clone.Entries, entry)
	}
	return &clone
}
