package application

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	internaldiag "github.com/simp-lee/oxpio/internal/diag"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/workspace"
)

type DirectoryMutationRequest struct {
	Operation    string
	Path         string
	Destination  string
	ExpectedHash string
	DryRun       bool
}

func MutateDirectory(vaultPath, outputPath string, request DirectoryMutationRequest) (MutationResult, error) {
	boundary, err := mutationBoundary(vaultPath, outputPath)
	if err != nil {
		return MutationResult{}, err
	}
	if request.Operation != MutationMkdir && request.Operation != MutationRenameDir {
		return MutationResult{}, fmt.Errorf("unsupported directory mutation %q", request.Operation)
	}
	if request.DryRun {
		if err := workspace.CheckDryRun(boundary.VaultPath); err != nil {
			return MutationResult{}, err
		}
		return mutateDirectoryLocked(boundary, request)
	}
	lease, err := workspace.Acquire(boundary.VaultPath)
	if err != nil {
		return MutationResult{}, err
	}
	defer func() { _ = lease.Release() }()
	return mutateDirectoryLocked(boundary, request)
}

func mutateDirectoryLocked(boundary internalfsutil.VaultOutputBoundary, request DirectoryMutationRequest) (MutationResult, error) {
	pathValue, err := normalizeMutationDirectory(request.Path)
	if err != nil {
		return MutationResult{}, err
	}
	if request.Operation == MutationRenameDir {
		request.Destination, err = normalizeMutationDirectory(request.Destination)
		if err != nil {
			return MutationResult{}, err
		}
		if path.Dir(pathValue) != path.Dir(request.Destination) {
			return MutationResult{}, errors.New("rename-dir requires source and destination in the same parent")
		}
		if pathValue == request.Destination {
			return MutationResult{}, errors.New("source and destination paths are identical")
		}
	}
	if err := validateDirectoryBoundary(boundary, pathValue); err != nil {
		return MutationResult{}, err
	}
	if request.Operation == MutationRenameDir {
		if err := validateDirectoryBoundary(boundary, request.Destination); err != nil {
			return MutationResult{}, err
		}
	}
	startFingerprint, err := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return MutationResult{}, err
	}
	sourceAbsolute := filepath.Join(boundary.VaultPath, filepath.FromSlash(pathValue))
	sourceInfo, err := os.Lstat(sourceAbsolute)
	if request.Operation == MutationMkdir {
		if !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return MutationResult{}, fmt.Errorf("directory %q already exists", pathValue)
			}
			return MutationResult{}, err
		}
		if err := requireDirectoryParent(boundary.VaultPath, pathValue); err != nil {
			return MutationResult{}, err
		}
		result := MutationResult{Operation: request.Operation, Path: pathValue, Next: "directory mutation does not publish", Changes: []MutationChange{{Kind: "create", Domain: "source", Path: pathValue, BeforeHash: nil, AfterHash: emptyHashPtr()}}}
		if request.DryRun {
			return checkDirectoryDryRun(boundary, startFingerprint, result)
		}
		if err := os.Mkdir(sourceAbsolute, 0o755); err != nil {
			return MutationResult{}, err
		}
		if err := workspace.RecordCompleted(boundary.VaultPath, []workspace.CompletedPath{{Path: sourceAbsolute, AfterHash: directoryEmptyHash()}}); err != nil {
			result.Diagnostics = append(result.Diagnostics, directoryWarning("directory committed; watch suppression marker could not be recorded"))
		}
		return checkDirectoryCommit(boundary, startFingerprint, result)
	}
	if err != nil {
		return MutationResult{}, err
	}
	if request.ExpectedHash == "" {
		return MutationResult{}, errors.New("--if-hash is required")
	}
	if err := validateExpectedHash(request.ExpectedHash, false); err != nil {
		return MutationResult{}, err
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.IsDir() {
		return MutationResult{}, fmt.Errorf("source %q must be a regular directory", pathValue)
	}
	entries, err := os.ReadDir(sourceAbsolute)
	if err != nil {
		return MutationResult{}, err
	}
	if len(entries) != 0 {
		return MutationResult{}, fmt.Errorf("directory %q must be empty", pathValue)
	}
	beforeHash, err := directoryHash(sourceAbsolute)
	if err != nil {
		return MutationResult{}, err
	}
	if request.ExpectedHash != beforeHash {
		return MutationResult{}, &ConflictError{Path: pathValue, Expected: request.ExpectedHash, Actual: beforeHash}
	}
	destinationAbsolute := filepath.Join(boundary.VaultPath, filepath.FromSlash(request.Destination))
	if _, err := os.Lstat(destinationAbsolute); err == nil {
		return MutationResult{}, &ConflictError{Path: request.Destination, Expected: AbsentHash, Actual: "present"}
	} else if !errors.Is(err, os.ErrNotExist) {
		return MutationResult{}, err
	}
	if err := requireDirectoryParent(boundary.VaultPath, request.Destination); err != nil {
		return MutationResult{}, err
	}
	result := MutationResult{Operation: request.Operation, Path: pathValue, Destination: request.Destination, Next: "directory mutation does not publish", Changes: []MutationChange{{Kind: "delete", Domain: "source", Path: pathValue, BeforeHash: stringPtrHash(beforeHash), AfterHash: nil}, {Kind: "create", Domain: "source", Path: request.Destination, BeforeHash: nil, AfterHash: stringPtrHash(beforeHash)}}}
	if request.DryRun {
		return checkDirectoryDryRun(boundary, startFingerprint, result)
	}
	latestHash, err := directoryHash(sourceAbsolute)
	if err != nil {
		return MutationResult{}, err
	}
	if latestHash != beforeHash {
		return MutationResult{}, fmt.Errorf("CONCURRENT_INPUT: directory %q changed during mutation", pathValue)
	}
	if err := internalfsutil.RenameNoReplace(sourceAbsolute, destinationAbsolute); err != nil {
		if errors.Is(err, os.ErrExist) {
			return MutationResult{}, &ConflictError{Path: request.Destination, Expected: AbsentHash, Actual: "present"}
		}
		return MutationResult{}, err
	}
	if err := workspace.RecordCompleted(boundary.VaultPath, []workspace.CompletedPath{{Path: sourceAbsolute, AfterHash: AbsentHash}, {Path: destinationAbsolute, AfterHash: beforeHash}}); err != nil {
		result.Diagnostics = append(result.Diagnostics, directoryWarning("directory committed; watch suppression marker could not be recorded"))
	}
	return checkDirectoryCommit(boundary, startFingerprint, result)
}

func checkDirectoryDryRun(boundary internalfsutil.VaultOutputBoundary, start string, result MutationResult) (MutationResult, error) {
	end, err := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return MutationResult{}, err
	}
	if end != start {
		return MutationResult{}, fmt.Errorf("CONCURRENT_INPUT: Vault inputs changed during dry-run")
	}
	return result, nil
}

func checkDirectoryCommit(boundary internalfsutil.VaultOutputBoundary, start string, result MutationResult) (MutationResult, error) {
	end, err := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, directoryWarning("directory committed; final input fingerprint could not be checked"))
		return result, nil
	}
	if end != start {
		result.Diagnostics = append(result.Diagnostics, directoryWarning("directory committed while another input changed"))
	}
	return result, nil
}

func directoryWarning(message string) internaldiag.Diagnostic {
	return internaldiag.Diagnostic{Severity: internaldiag.SeverityWarning, Kind: internaldiag.KindOutputCleanup, Message: message}
}

func normalizeMutationDirectory(value string) (string, error) {
	cleaned, err := NormalizeDirectoryPath(value)
	if err != nil {
		return "", err
	}
	if cleaned == "." {
		return "", errors.New("directory path must not be the Vault root")
	}
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.HasPrefix(segment, ".") || strings.EqualFold(segment, "node_modules") || strings.EqualFold(segment, "public") || strings.EqualFold(segment, "oxpio.yaml") {
			return "", errors.New("directory path contains a reserved segment")
		}
	}
	return cleaned, nil
}

func validateDirectoryBoundary(boundary internalfsutil.VaultOutputBoundary, rel string) error {
	absolute := filepath.Join(boundary.VaultPath, filepath.FromSlash(rel))
	if internalfsutil.PathWithinRoot(boundary.OutputPath, absolute) || internalfsutil.PathWithinRoot(absolute, boundary.OutputPath) {
		return fmt.Errorf("directory path %q overlaps the formal output", rel)
	}
	return nil
}

func requireDirectoryParent(vault, rel string) error {
	parent := path.Dir(rel)
	if parent == "." {
		return nil
	}
	_, info, err := internalfsutil.InspectContainedDirectory(vault, filepath.FromSlash(parent))
	if err != nil {
		return fmt.Errorf("parent directory %q is not available: %w", parent, err)
	}
	if info == nil || !info.IsDir() {
		return fmt.Errorf("parent directory %q is not a directory", parent)
	}
	return nil
}

func emptyHashPtr() *string { value := directoryEmptyHash(); return &value }

func directoryHash(root string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("directory is not regular")
	}
	type item struct{ kind, path, data string }
	items := make([]item, 0)
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("directory contains a symbolic link")
		}
		if entry.IsDir() {
			items = append(items, item{kind: "d", path: rel})
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("directory contains an unsupported entry")
		}
		data, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		items = append(items, item{kind: "f", path: rel, data: string(data)})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].path == items[j].path {
			return items[i].kind < items[j].kind
		}
		return items[i].path < items[j].path
	})
	hash := sha256.New()
	for _, item := range items {
		_, _ = hash.Write([]byte(item.kind + ":" + item.path + "\x00" + item.data + "\x00"))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func directoryEmptyHash() string { sum := sha256.Sum256(nil); return hex.EncodeToString(sum[:]) }
