package edit

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	internalbuild "github.com/simp-lee/oxpio/internal/build"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
)

const (
	fileManagerHashAbsent = AbsentSourceHash
	fileMutationFolder    = "folder"
	fileMutationMarkdown  = "markdown"
	fileMutationUpload    = "upload"
	fileMutationRename    = "rename"
	fileMutationDelete    = "delete"
)

type fileManagerMutation struct {
	operation   string
	path        string
	destination string
	expected    string
	content     []byte
}

type fileManagerState struct {
	path   string
	hash   string
	exists bool
	isDir  bool
}

// CreateFolder creates one empty vault directory after a CAS check and a
// successful candidate build.
func (coordinator *Coordinator) CreateFolder(relPath, expectedHash string) (TransactionResult, error) {
	return coordinator.mutateFile(fileManagerMutation{operation: fileMutationFolder, path: relPath, expected: expectedHash})
}

// CreateMarkdown creates an editor-supported Markdown file after a CAS check
// and a successful candidate build. The caller chooses the source template.
func (coordinator *Coordinator) CreateMarkdown(relPath, expectedHash string, content []byte) (TransactionResult, error) {
	return coordinator.mutateFile(fileManagerMutation{operation: fileMutationMarkdown, path: relPath, expected: expectedHash, content: content})
}

// UploadFile creates one new Markdown or supported image file through the
// same candidate build and publication transaction as other file operations.
// Uploads are create-only and therefore require AbsentSourceHash.
func (coordinator *Coordinator) UploadFile(relPath, expectedHash string, content []byte) (TransactionResult, error) {
	return coordinator.mutateFile(fileManagerMutation{operation: fileMutationUpload, path: relPath, expected: expectedHash, content: content})
}

// RenamePath atomically renames one managed file or directory after checking
// both the source hash and destination absence.
func (coordinator *Coordinator) RenamePath(relPath, destination, expectedHash string) (TransactionResult, error) {
	return coordinator.mutateFile(fileManagerMutation{operation: fileMutationRename, path: relPath, destination: destination, expected: expectedHash})
}

// DeletePath removes one managed article or empty directory after a CAS check;
// the mutation remains reversible until candidate output publication succeeds.
func (coordinator *Coordinator) DeletePath(relPath, expectedHash string) (TransactionResult, error) {
	return coordinator.mutateFile(fileManagerMutation{operation: fileMutationDelete, path: relPath, expected: expectedHash})
}

func (coordinator *Coordinator) mutateFile(mutation fileManagerMutation) (TransactionResult, error) {
	if coordinator == nil {
		return TransactionResult{}, errors.New("source coordinator is nil")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()

	if err := validateManagedRelPath(mutation.path); err != nil {
		return TransactionResult{}, err
	}
	if mutation.operation == fileMutationRename {
		if err := validateManagedRelPath(mutation.destination); err != nil {
			return TransactionResult{}, err
		}
		if mutation.path == mutation.destination {
			return TransactionResult{}, fmt.Errorf("source and destination paths are identical")
		}
		if strings.EqualFold(path.Ext(mutation.path), ".md") {
			if err := validateManagedMarkdownPath(mutation.destination); err != nil {
				return TransactionResult{}, err
			}
			if strings.EqualFold(path.Base(mutation.destination), "_index.md") {
				return TransactionResult{}, fmt.Errorf("cannot rename an article to a section source")
			}
		}
	}
	if mutation.operation != fileMutationFolder && mutation.operation != fileMutationMarkdown && mutation.operation != fileMutationUpload && mutation.operation != fileMutationRename && mutation.operation != fileMutationDelete {
		return TransactionResult{}, fmt.Errorf("unsupported file operation %q", mutation.operation)
	}
	if err := ensureNoReplaceRename(); err != nil {
		return TransactionResult{}, err
	}
	if mutation.operation == fileMutationMarkdown || mutation.operation == fileMutationUpload {
		if mutation.operation == fileMutationMarkdown || strings.EqualFold(path.Ext(mutation.path), ".md") {
			if !utf8.Valid(mutation.content) {
				return TransactionResult{}, fmt.Errorf("markdown upload %q must contain valid UTF-8", mutation.path)
			}
			if err := validateManagedMarkdownPath(mutation.path); err != nil {
				return TransactionResult{}, err
			}
		} else if err := validateUploadImage(mutation.path, mutation.content); err != nil {
			return TransactionResult{}, err
		}
		if mutation.expected != fileManagerHashAbsent {
			return TransactionResult{}, &ConflictError{Path: mutation.path, Expected: fileManagerHashAbsent, Actual: mutation.expected}
		}
	}
	if err := validateManagedBoundary(coordinator.vault, coordinator.output, mutation.path); err != nil {
		return TransactionResult{}, err
	}
	if mutation.operation == fileMutationRename {
		if err := validateManagedBoundary(coordinator.vault, coordinator.output, mutation.destination); err != nil {
			return TransactionResult{}, err
		}
	}

	current, err := inspectFileManagerState(coordinator.vault, mutation.path)
	if err != nil {
		return TransactionResult{}, err
	}
	if current.hash != mutation.expected {
		return TransactionResult{}, &ConflictError{Path: mutation.path, Expected: mutation.expected, Actual: current.hash}
	}

	var destinationState fileManagerState
	if mutation.operation == fileMutationRename {
		if current.isDir && (mutation.destination == mutation.path || strings.HasPrefix(mutation.destination, mutation.path+"/")) {
			return TransactionResult{}, fmt.Errorf("cannot move a directory inside itself")
		}
		destinationState, err = inspectFileManagerState(coordinator.vault, mutation.destination)
		if err != nil {
			return TransactionResult{}, err
		}
		if destinationState.exists {
			return TransactionResult{}, &ConflictError{Path: mutation.destination, Expected: fileManagerHashAbsent, Actual: destinationState.hash}
		}
	}
	if mutation.operation == fileMutationFolder || mutation.operation == fileMutationMarkdown || mutation.operation == fileMutationUpload || mutation.operation == fileMutationRename {
		parent := mutation.path
		if mutation.operation == fileMutationRename {
			parent = mutation.destination
		}
		if err := inspectManagedParent(coordinator.vault, parent); err != nil {
			return TransactionResult{}, err
		}
	}
	if mutation.operation == fileMutationFolder || mutation.operation == fileMutationMarkdown || mutation.operation == fileMutationUpload {
		if current.exists {
			return TransactionResult{}, &ConflictError{Path: mutation.path, Expected: fileManagerHashAbsent, Actual: current.hash}
		}
	}
	if mutation.operation == fileMutationRename || mutation.operation == fileMutationDelete {
		if !current.exists {
			return TransactionResult{}, &ConflictError{Path: mutation.path, Expected: mutation.expected, Actual: fileManagerHashAbsent}
		}
	}
	if current.exists {
		if mutation.operation == fileMutationRename || mutation.operation == fileMutationDelete {
			if err := coordinator.validateEditableFileManagerTarget(mutation.path, current.isDir); err != nil {
				return TransactionResult{}, err
			}
		}
		if err := validateManagedSubtree(coordinator.vault, coordinator.output, mutation.path, current.isDir); err != nil {
			return TransactionResult{}, err
		}
	}
	if latest, latestErr := inspectFileManagerState(coordinator.vault, mutation.path); latestErr != nil {
		return TransactionResult{}, latestErr
	} else if latest.hash != mutation.expected || latest.isDir != current.isDir {
		return TransactionResult{}, &ConflictError{Path: mutation.path, Expected: mutation.expected, Actual: latest.hash}
	}

	candidateRoot, err := os.MkdirTemp(filepath.Dir(coordinator.vault), ".oxpio-file-candidate-*")
	if err != nil {
		return TransactionResult{}, fmt.Errorf("create file-operation candidate vault: %w", err)
	}
	defer func() { _ = os.RemoveAll(candidateRoot) }()
	if err := copyFileManagerTree(coordinator.vault, candidateRoot, coordinator.output); err != nil {
		return TransactionResult{}, fmt.Errorf("copy file-operation candidate vault: %w", err)
	}
	_, _, candidateExpected, err := applyFileManagerMutation(candidateRoot, mutation, current)
	if err != nil {
		return TransactionResult{}, err
	}
	if err := verifyFileManagerMutation(candidateRoot, mutation, candidateExpected); err != nil {
		return TransactionResult{}, err
	}

	stageRoot, err := os.MkdirTemp(filepath.Dir(coordinator.output), ".oxpio-file-stage-*")
	if err != nil {
		return TransactionResult{}, fmt.Errorf("create file-operation staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stageRoot) }()
	stageOutput := filepath.Join(stageRoot, "public")
	built, buildErr := buildCandidate(candidateRoot, stageOutput)
	if buildErr != nil {
		return TransactionResult{}, &CandidateBuildError{Path: mutation.path, Result: built, Err: buildErr}
	}
	if err := verifyFileManagerPrecondition(coordinator.vault, mutation, current); err != nil {
		return TransactionResult{}, err
	}

	rollback, finalize, expectedAfter, err := applyFileManagerMutation(coordinator.vault, mutation, current)
	if err != nil {
		return TransactionResult{}, err
	}
	rollbackMutation := func(operationErr error) (TransactionResult, error) {
		rollbackErr := rollback()
		return TransactionResult{}, errors.Join(operationErr, rollbackErr)
	}
	if expectedAfter.hash != candidateExpected.hash || expectedAfter.exists != candidateExpected.exists || expectedAfter.isDir != candidateExpected.isDir {
		return rollbackMutation(&ConflictError{Path: mutation.path, Expected: candidateExpected.hash, Actual: expectedAfter.hash})
	}
	if err := verifyFileManagerMutation(coordinator.vault, mutation, expectedAfter); err != nil {
		return rollbackMutation(err)
	}

	rollbackOutput, finalizeOutput, cleanupOutput, err := publishOutput(stageOutput, coordinator.output)
	if err != nil {
		return rollbackMutation(err)
	}
	if err := verifyFileManagerMutation(coordinator.vault, mutation, expectedAfter); err != nil {
		return TransactionResult{}, errors.Join(rollbackOutput(), rollback(), err)
	}
	if err := finalizeOutput(); err != nil {
		return TransactionResult{}, errors.Join(rollbackOutput(), rollback(), err)
	}
	if err := verifyFileManagerMutation(coordinator.vault, mutation, expectedAfter); err != nil {
		return TransactionResult{}, errors.Join(rollbackOutput(), rollback(), err)
	}
	if err := cleanupOutput(); err != nil {
		// Publication has already committed the new source/output pair. A
		// backup cleanup failure must not roll back through a partially
		// removed backup and destroy the committed output.
		built.OutputCleanupError = err
	}
	if err := finalize(); err != nil {
		return TransactionResult{}, errors.Join(rollbackOutput(), rollback(), err)
	}
	coordinator.catalog = cloneCatalog(built.Catalog)
	return TransactionResult{SourceHash: expectedAfter.hash, RelPath: mutation.path, Build: built}, nil
}

func (coordinator *Coordinator) validateEditableFileManagerTarget(relPath string, isDir bool) error {
	if !isDir {
		entry := catalogEntry(coordinator.catalog, relPath)
		if entry == nil || entry.Kind != "article" {
			return fmt.Errorf("only ordinary article sources can be renamed or deleted: %q", relPath)
		}
		return nil
	}
	root := filepath.Join(coordinator.vault, filepath.FromSlash(relPath))
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("nonempty folders and section sources cannot be renamed or deleted: %q", relPath)
	}
	return nil
}

func buildCandidate(vault, output string) (*internalbuild.BuildResult, error) {
	return internalbuild.BuildWithOptions(vault, output, internalbuild.Options{})
}

func applyFileManagerMutation(vault string, mutation fileManagerMutation, current fileManagerState) (rollback func() error, finalize func() error, expected fileManagerState, err error) {
	source := filepath.Join(vault, filepath.FromSlash(mutation.path))

	switch mutation.operation {
	case fileMutationFolder:
		if err := os.Mkdir(source, 0o755); err != nil {
			return nil, nil, fileManagerState{}, err
		}
		expected, err := inspectFileManagerState(vault, mutation.path)
		if err != nil {
			_ = os.Remove(source)
			return nil, nil, fileManagerState{}, err
		}
		rollback = func() error { return removeCreatedPath(vault, mutation.path, expected.hash) }
		return rollback, func() error { return nil }, expected, nil

	case fileMutationMarkdown, fileMutationUpload:
		if err := createFileNoReplace(source, mutation.content); err != nil {
			return nil, nil, fileManagerState{}, err
		}
		expected, err := inspectFileManagerState(vault, mutation.path)
		if err != nil {
			_ = os.Remove(source)
			return nil, nil, fileManagerState{}, err
		}
		rollback = func() error { return removeCreatedPath(vault, mutation.path, expected.hash) }
		return rollback, func() error { return nil }, expected, nil

	case fileMutationDelete:
		// Retain exact rollback bytes even if displaced-file cleanup succeeds
		// before a later output-cleanup failure. Directories here are empty.
		var original []byte
		if !current.isDir {
			_, original, _, err = internalfsutil.ReadContainedRegularFile(vault, mutation.path)
			if err != nil {
				return nil, nil, fileManagerState{}, err
			}
		}
		displaced, err := movePathForCAS(source)
		if err != nil {
			return nil, nil, fileManagerState{}, fmt.Errorf("delete %q: %w", mutation.path, err)
		}
		if actual, hashErr := hashMovedPath(displaced, current.isDir); hashErr != nil || actual != current.hash {
			restoreErr := restoreDisplacedPath(displaced, source)
			if hashErr != nil {
				return nil, nil, fileManagerState{}, errors.Join(hashErr, restoreErr)
			}
			return nil, nil, fileManagerState{}, errors.Join(&ConflictError{Path: mutation.path, Expected: current.hash, Actual: actual}, restoreErr)
		}
		expected = fileManagerState{path: mutation.path, hash: fileManagerHashAbsent, exists: false}
		rollback = func() error {
			state, stateErr := inspectFileManagerState(vault, mutation.path)
			if stateErr != nil {
				return errors.Join(stateErr, os.RemoveAll(displaced))
			}
			if state.exists {
				return errors.Join(&ConflictError{Path: mutation.path, Expected: fileManagerHashAbsent, Actual: state.hash}, os.RemoveAll(displaced))
			}
			if _, err := os.Lstat(displaced); err == nil {
				return renamePathNoReplace(displaced, source)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if current.isDir {
				return os.Mkdir(source, 0o755)
			}
			return createFileNoReplace(source, original)
		}
		finalize = func() error {
			_ = os.RemoveAll(displaced)
			return nil
		}
		return rollback, finalize, expected, nil

	case fileMutationRename:
		destination := filepath.Join(vault, filepath.FromSlash(mutation.destination))
		backup, err := createFileManagerBackup(source, current.isDir)
		if err != nil {
			return nil, nil, fileManagerState{}, err
		}
		cleanupBackup := func() error { return os.RemoveAll(backup) }
		displaced, err := movePathForCAS(source)
		if err != nil {
			return nil, nil, fileManagerState{}, errors.Join(fmt.Errorf("rename %q: %w", mutation.path, err), cleanupBackup())
		}
		if actual, hashErr := hashMovedPath(displaced, current.isDir); hashErr != nil || actual != current.hash {
			restoreErr := restoreDisplacedPath(displaced, source)
			if hashErr != nil {
				return nil, nil, fileManagerState{}, errors.Join(hashErr, restoreErr, cleanupBackup())
			}
			return nil, nil, fileManagerState{}, errors.Join(&ConflictError{Path: mutation.path, Expected: current.hash, Actual: actual}, restoreErr, cleanupBackup())
		}
		if err := renamePathNoReplace(displaced, destination); err != nil {
			restoreErr := restoreDisplacedPath(displaced, source)
			return nil, nil, fileManagerState{}, errors.Join(fmt.Errorf("rename %q to %q: %w", mutation.path, mutation.destination, err), restoreErr, cleanupBackup())
		}
		expected = fileManagerState{path: mutation.destination, hash: current.hash, exists: true, isDir: current.isDir}
		rollback = func() error {
			restoreErr := restoreFileManagerBackup(backup, source)
			removeErr := removeCreatedPath(vault, mutation.destination, expected.hash)
			return errors.Join(restoreErr, removeErr)
		}
		return rollback, func() error {
			_ = cleanupBackup()
			return nil
		}, expected, nil
	}
	return nil, nil, fileManagerState{}, fmt.Errorf("unsupported file operation %q", mutation.operation)
}

func validateManagedSubtree(vault, output, relPath string, isDir bool) error {
	candidate := filepath.Join(vault, filepath.FromSlash(relPath))
	if !isDir {
		if output != "" && isSameOrChildPath(candidate, output) {
			return fmt.Errorf("path %q is inside the generated output", relPath)
		}
		return nil
	}
	return filepath.WalkDir(candidate, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if output != "" && isSameOrChildPath(current, output) {
			return fmt.Errorf("path %q contains the generated output", relPath)
		}
		relative, err := filepath.Rel(vault, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q contains a symbolic link", relative)
		}
		if current != candidate && fileManagerPathHidden(relative) {
			return fmt.Errorf("path %q contains a reserved or hidden entry", relative)
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("path %q contains an unsupported file", relative)
			}
		}
		return nil
	})
}

func verifyFileManagerPrecondition(vault string, mutation fileManagerMutation, expected fileManagerState) error {
	actual, err := inspectFileManagerState(vault, mutation.path)
	if err != nil {
		return err
	}
	if actual.hash != expected.hash || actual.exists != expected.exists || actual.isDir != expected.isDir {
		return &ConflictError{Path: mutation.path, Expected: expected.hash, Actual: actual.hash}
	}
	if mutation.operation == fileMutationRename {
		destination, err := inspectFileManagerState(vault, mutation.destination)
		if err != nil {
			return err
		}
		if destination.exists {
			return &ConflictError{Path: mutation.destination, Expected: fileManagerHashAbsent, Actual: destination.hash}
		}
		if err := inspectManagedParent(vault, mutation.destination); err != nil {
			return err
		}
	}
	if mutation.operation == fileMutationFolder || mutation.operation == fileMutationMarkdown || mutation.operation == fileMutationUpload || mutation.operation == fileMutationRename {
		parent := mutation.path
		if mutation.operation == fileMutationRename {
			parent = mutation.destination
		}
		if err := inspectManagedParent(vault, parent); err != nil {
			return err
		}
	}
	return nil
}

func verifyFileManagerMutation(vault string, mutation fileManagerMutation, expected fileManagerState) error {
	pathValue := mutation.path
	if mutation.operation == fileMutationRename {
		pathValue = mutation.destination
	}
	actual, err := inspectFileManagerState(vault, pathValue)
	if err != nil {
		return err
	}
	if actual.hash != expected.hash || actual.exists != expected.exists || actual.isDir != expected.isDir {
		return &ConflictError{Path: pathValue, Expected: expected.hash, Actual: actual.hash}
	}
	if mutation.operation == fileMutationRename {
		original, err := inspectFileManagerState(vault, mutation.path)
		if err != nil {
			return err
		}
		if original.exists {
			return &ConflictError{Path: mutation.path, Expected: fileManagerHashAbsent, Actual: original.hash}
		}
	}
	return nil
}

func restoreDisplacedPath(displaced, original string) error {
	if err := renamePathNoReplace(displaced, original); err == nil {
		return nil
	} else if _, statErr := os.Lstat(original); statErr == nil {
		return errors.Join(err, os.RemoveAll(displaced))
	} else {
		return err
	}
}

func copyFileManagerTree(source, destination, excludedPath string) error {
	if excludedPath != "" {
		excludedPath = filepath.Clean(excludedPath)
	}
	return filepath.WalkDir(source, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if excludedPath != "" && isSameOrChildPath(current, excludedPath) {
			if filepath.Clean(current) == excludedPath && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(current)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			return os.Mkdir(target, info.Mode().Perm())
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported vault entry %q", filepath.ToSlash(rel))
		}
		return copyFileManagerFile(current, target, info.Mode().Perm())
	})
}

func copyFileManagerFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	return errors.Join(copyErr, closeErr)
}

func removeCreatedPath(vault, relPath, expectedHash string) error {
	state, err := inspectFileManagerState(vault, relPath)
	if err != nil {
		return err
	}
	if !state.exists {
		return nil
	}
	if state.hash != expectedHash {
		return &ConflictError{Path: relPath, Expected: expectedHash, Actual: state.hash}
	}
	filename := filepath.Join(vault, filepath.FromSlash(relPath))
	if handled, atomicErr := removeCreatedPathAtomic(filename, relPath, expectedHash, state.isDir); handled {
		return atomicErr
	}
	return os.RemoveAll(filename)
}

func createFileManagerBackup(filename string, isDir bool) (string, error) {
	if isDir {
		return os.MkdirTemp(filepath.Dir(filename), ".oxpio-rename-backup-*")
	}
	input, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer func() { _ = input.Close() }()
	backup, err := os.CreateTemp(filepath.Dir(filename), ".oxpio-rename-backup-*")
	if err != nil {
		return "", err
	}
	name := backup.Name()
	cleanup := func() { _ = backup.Close(); _ = os.Remove(name) }
	if _, err := io.Copy(backup, input); err != nil {
		cleanup()
		return "", err
	}
	if err := backup.Sync(); err != nil {
		cleanup()
		return "", err
	}
	if err := backup.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func restoreFileManagerBackup(backup, filename string) error {
	if _, err := os.Lstat(filename); err == nil {
		return fmt.Errorf("source changed before rename rollback")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return renamePathNoReplace(backup, filename)
}

func createFileNoReplace(filename string, content []byte) error {
	parent := filepath.Dir(filename)
	temporary, err := os.CreateTemp(parent, ".oxpio-file-*")
	if err != nil {
		return fmt.Errorf("create file transaction: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryName, filename); err != nil {
		if errors.Is(err, os.ErrExist) {
			return &ConflictError{Path: filepath.ToSlash(filename), Expected: fileManagerHashAbsent, Actual: "present"}
		}
		return err
	}
	return nil
}

func movePathForCAS(filename string) (string, error) {
	temporary, err := os.CreateTemp(filepath.Dir(filename), ".oxpio-displaced-*")
	if err != nil {
		return "", err
	}
	temporaryName := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryName)
		return "", err
	}
	if err := os.Remove(temporaryName); err != nil {
		return "", err
	}
	if err := os.Rename(filename, temporaryName); err != nil {
		return "", err
	}
	return temporaryName, nil
}

func inspectManagedParent(vault, relPath string) error {
	parent := path.Dir(relPath)
	if parent == "." {
		return nil
	}
	_, _, err := internalfsutil.InspectContainedDirectory(vault, filepath.FromSlash(parent))
	if err != nil {
		return fmt.Errorf("parent directory %q is not available: %w", parent, err)
	}
	return nil
}

func inspectFileManagerState(vault, relPath string) (fileManagerState, error) {
	state := fileManagerState{path: relPath, hash: fileManagerHashAbsent}
	resolved, _, err := internalfsutil.InspectContainedRegularFile(vault, filepath.FromSlash(relPath))
	if err == nil {
		state.hash, err = hashManagedFile(resolved)
		if err != nil {
			return state, err
		}
		state.exists = true
		return state, nil
	}
	if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, internalfsutil.ErrUnsupportedRegularFileSource) {
		return state, err
	}
	resolved, info, err := internalfsutil.InspectContainedDirectory(vault, filepath.FromSlash(relPath))
	if err == nil {
		hash, hashErr := hashManagedDirectory(resolved, info)
		if hashErr != nil {
			return state, hashErr
		}
		state.hash = hash
		state.exists = true
		state.isDir = true
		return state, nil
	}
	if errors.Is(err, os.ErrNotExist) && (info == nil || !info.IsDir()) {
		return state, nil
	}
	return state, err
}

func hashMovedPath(filename string, isDir bool) (string, error) {
	if isDir {
		return hashManagedDirectory(filename, nil)
	}
	return hashManagedFile(filename)
}

func hashManagedFile(filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

func hashManagedDirectory(directory string, _ os.FileInfo) (string, error) {
	hasher := sha256.New()
	writePart := func(value []byte) error {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		if _, err := hasher.Write(size[:]); err != nil {
			return err
		}
		_, err := hasher.Write(value)
		return err
	}
	entries := make([]string, 0)
	err := filepath.WalkDir(directory, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == directory {
			return nil
		}
		rel, err := filepath.Rel(directory, current)
		if err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(entries)
	for _, rel := range entries {
		full := filepath.Join(directory, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			if err := writePart([]byte("d:" + rel)); err != nil {
				return "", err
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return "", err
			}
			if err := writePart([]byte("l:" + rel)); err != nil {
				return "", err
			}
			if err := writePart([]byte(target)); err != nil {
				return "", err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("unsupported vault entry %q", filepath.ToSlash(rel))
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return "", err
		}
		if err := writePart([]byte("f:" + rel)); err != nil {
			return "", err
		}
		if err := writePart(data); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

func validateManagedBoundary(vault, output, relPath string) error {
	if output == "" {
		return nil
	}
	candidate := filepath.Join(vault, filepath.FromSlash(relPath))
	if isSameOrChildPath(candidate, output) {
		return fmt.Errorf("path %q is inside the generated output", relPath)
	}
	return nil
}

func isManagedReservedSegment(segment string) bool {
	switch strings.ToLower(segment) {
	case "node_modules", ".git", ".obsidian", ".oxpio":
		return true
	default:
		return false
	}
}

func validateManagedRelPath(relPath string) error {
	if strings.TrimSpace(relPath) == "" || strings.Contains(relPath, `\`) || strings.HasPrefix(relPath, "/") || path.Clean(relPath) != relPath || relPath == "." || relPath == ".." || strings.HasPrefix(relPath, "../") || strings.ContainsAny(relPath, "?#") {
		return fmt.Errorf("path %q must be a contained normalized path", relPath)
	}
	for _, segment := range strings.Split(relPath, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.HasPrefix(segment, ".") || !internalfsutil.IsPortableSitePath(segment) {
			return fmt.Errorf("path %q must be a contained normalized path", relPath)
		}
		if isManagedReservedSegment(segment) {
			return fmt.Errorf("path %q is reserved", relPath)
		}
	}
	if strings.EqualFold(relPath, "oxpio.yaml") {
		return fmt.Errorf("path %q is reserved", relPath)
	}
	return nil
}

func validateManagedMarkdownPath(relPath string) error {
	if err := validateManagedRelPath(relPath); err != nil {
		return err
	}
	if !strings.EqualFold(path.Ext(relPath), ".md") {
		return fmt.Errorf("markdown path %q must use the .md extension", relPath)
	}
	if strings.EqualFold(path.Base(relPath), "_index.md") && path.Base(relPath) != "_index.md" {
		return fmt.Errorf("section source %q must use the exact filename _index.md", relPath)
	}
	return nil
}
