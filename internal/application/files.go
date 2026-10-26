package application

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	internalconfig "github.com/simp-lee/oxpio/internal/config"
	internaldiag "github.com/simp-lee/oxpio/internal/diag"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/workspace"
)

type DirectoryMutationRequest struct {
	Operation    string
	Path         string
	ExpectedHash string
	DryRun       bool
}

var mkdirDirectory = internalfsutil.CreateDirectoryRelativeWithinRoot

func MutateDirectory(vaultPath, outputPath string, request DirectoryMutationRequest) (MutationResult, error) {
	boundary, err := mutationBoundary(vaultPath, outputPath)
	if err != nil {
		return MutationResult{}, err
	}
	if request.Operation != MutationMkdir {
		return MutationResult{}, fmt.Errorf("unsupported directory mutation %q", request.Operation)
	}
	if request.ExpectedHash != AbsentHash {
		return MutationResult{}, errors.New("mkdir requires --if-hash absent")
	}
	if request.DryRun {
		if err := workspace.CheckDryRun(boundary.VaultPath); err != nil {
			return MutationResult{}, err
		}
		return mutateDirectoryLocked(boundary, request)
	}
	if err := recoverPendingApplicationJournal(boundary.VaultPath); err != nil {
		return MutationResult{}, err
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
	if err := validateDirectoryBoundary(boundary, pathValue); err != nil {
		return MutationResult{}, err
	}
	if err := validateReservedDirectoryPath(boundary, pathValue, true); err != nil {
		return MutationResult{}, err
	}
	startFingerprint, err := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return MutationResult{}, err
	}
	sourceAbsolute := filepath.Join(boundary.VaultPath, filepath.FromSlash(pathValue))
	_, err = os.Lstat(sourceAbsolute)
	if !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return MutationResult{}, fmt.Errorf("directory %q already exists", pathValue)
		}
		return MutationResult{}, err
	}
	if err := requireDirectoryParent(boundary.VaultPath, pathValue); err != nil {
		return MutationResult{}, err
	}
	parentAbsolute := filepath.Join(boundary.VaultPath, filepath.FromSlash(path.Dir(pathValue)))
	parentSnapshot, err := captureDirectoryIdentity(parentAbsolute)
	if err != nil {
		return MutationResult{}, err
	}
	targetName := filepath.Base(filepath.FromSlash(pathValue))
	snapshot := directoryMutationSnapshot{parent: parentSnapshot, source: sourceAbsolute}
	result := MutationResult{Operation: request.Operation, Path: pathValue, Next: "directory mutation does not publish", Changes: []MutationChange{{Kind: "create", Domain: "source", Path: pathValue, BeforeHash: nil, AfterHash: emptyHashPtr()}}}
	if err := internalfsutil.CheckDirectoryMutationSupport(boundary.VaultPath, parentAbsolute); err != nil {
		return MutationResult{}, fmt.Errorf("secure directory mutation is unavailable: %w", err)
	}
	if err := internalfsutil.CheckDirectoryRollbackSupport(); err != nil {
		return MutationResult{}, fmt.Errorf("secure directory rollback is unavailable: %w", err)
	}
	if request.DryRun {
		return checkDirectoryDryRun(boundary, startFingerprint, snapshot, result)
	}
	parentDirectory, err := internalfsutil.OpenDirectoryNoFollow(parentAbsolute)
	if err != nil {
		return MutationResult{}, err
	}
	defer func() { _ = parentDirectory.Close() }()
	if err := verifyDirectoryMutationSnapshot(snapshot); err != nil {
		return MutationResult{}, err
	}
	completedPaths := []workspace.CompletedPath{{Path: sourceAbsolute, AfterHash: directoryEmptyHash()}}
	if err := workspace.RecordCompleted(boundary.VaultPath, completedPaths); err != nil {
		cleanupErr := workspace.RemoveCompleted(boundary.VaultPath, completedPaths)
		return MutationResult{}, errors.Join(fmt.Errorf("watch suppression marker could not be recorded: %w", err), cleanupErr)
	}
	createdDirectory, err := mkdirDirectory(boundary.VaultPath, parentDirectory, targetName, 0o755)
	if err != nil {
		directoryCleanupErr := cleanupCreatedDirectory(createdDirectory)
		cleanupErr := workspace.RemoveCompleted(boundary.VaultPath, completedPaths)
		if errors.Is(err, internalfsutil.ErrDirectoryCreated) {
			err = errors.Join(err, fmt.Errorf("%w: secure directory creation failed after creating the target", workspace.ErrRecovery))
		}
		if errors.Is(err, internalfsutil.ErrPathOutsideRoot) {
			err = errors.Join(err, fmt.Errorf("%w: directory parent left the Vault", workspace.ErrRecovery))
		}
		return MutationResult{}, errors.Join(err, directoryCleanupErr, cleanupErr)
	}
	if err := internalfsutil.VerifyDirectoryHandleWithinRoot(boundary.VaultPath, parentDirectory); err != nil {
		directoryCleanupErr := cleanupCreatedDirectory(createdDirectory)
		cleanupErr := workspace.RemoveCompleted(boundary.VaultPath, completedPaths)
		return MutationResult{}, errors.Join(err, fmt.Errorf("%w: created directory parent left the Vault", workspace.ErrRecovery), directoryCleanupErr, cleanupErr)
	}
	if err := verifyCreatedDirectory(createdDirectory.File()); err != nil {
		directoryCleanupErr := cleanupCreatedDirectory(createdDirectory)
		cleanupErr := workspace.RemoveCompleted(boundary.VaultPath, completedPaths)
		return MutationResult{}, errors.Join(err, fmt.Errorf("%w: created directory postcondition failed", workspace.ErrRecovery), directoryCleanupErr, cleanupErr)
	}
	if err := verifyDirectoryParent(snapshot); err != nil {
		directoryCleanupErr := cleanupCreatedDirectory(createdDirectory)
		cleanupErr := workspace.RemoveCompleted(boundary.VaultPath, completedPaths)
		return MutationResult{}, errors.Join(err, fmt.Errorf("%w: created directory parent changed", workspace.ErrRecovery), directoryCleanupErr, cleanupErr)
	}
	if err := createdDirectory.Verify(); err != nil {
		directoryCleanupErr := cleanupCreatedDirectory(createdDirectory)
		cleanupErr := workspace.RemoveCompleted(boundary.VaultPath, completedPaths)
		return MutationResult{}, errors.Join(err, fmt.Errorf("%w: created directory identity changed", workspace.ErrRecovery), directoryCleanupErr, cleanupErr)
	}
	if err := createdDirectory.Close(); err != nil {
		directoryCleanupErr := cleanupCreatedDirectory(createdDirectory)
		cleanupErr := workspace.RemoveCompleted(boundary.VaultPath, completedPaths)
		return MutationResult{}, errors.Join(err, fmt.Errorf("%w: created directory handle could not be closed", workspace.ErrRecovery), directoryCleanupErr, cleanupErr)
	}
	return checkDirectoryCommit(boundary, startFingerprint, result)
}

type directoryMutationSnapshot struct {
	parent directoryIdentity
	source string
}

type directoryIdentity struct {
	name string
	info os.FileInfo
}

func captureDirectoryIdentity(name string) (directoryIdentity, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return directoryIdentity{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return directoryIdentity{}, errors.New("directory parent must be a regular directory")
	}
	return directoryIdentity{name: name, info: info}, nil
}

func (snapshot directoryMutationSnapshot) unchanged() error {
	current, err := captureDirectoryIdentity(snapshot.parent.name)
	if err != nil {
		return fmt.Errorf("CONCURRENT_INPUT: directory parent changed: %w", err)
	}
	if !sameDirectoryIdentity(snapshot.parent, current) {
		return errors.New("CONCURRENT_INPUT: directory parent changed")
	}
	if _, err := os.Lstat(snapshot.source); err == nil {
		return errors.New("CONCURRENT_INPUT: directory target appeared")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func sameDirectoryIdentity(left, right directoryIdentity) bool {
	if left.info == nil || right.info == nil || !os.SameFile(left.info, right.info) {
		return false
	}
	return left.info.Mode() == right.info.Mode() && left.info.Size() == right.info.Size() && left.info.ModTime().Equal(right.info.ModTime())
}

func verifyDirectoryMutationSnapshot(snapshot directoryMutationSnapshot) error {
	return snapshot.unchanged()
}

func verifyDirectoryParent(snapshot directoryMutationSnapshot) error {
	current, err := captureDirectoryIdentity(snapshot.parent.name)
	if err != nil {
		return fmt.Errorf("CONCURRENT_INPUT: directory parent changed: %w", err)
	}
	if snapshot.parent.info == nil || current.info == nil || !os.SameFile(snapshot.parent.info, current.info) {
		return errors.New("CONCURRENT_INPUT: directory parent changed")
	}
	return nil
}

func checkDirectoryDryRun(boundary internalfsutil.VaultOutputBoundary, start string, snapshot directoryMutationSnapshot, result MutationResult) (MutationResult, error) {
	if err := verifyDirectoryMutationSnapshot(snapshot); err != nil {
		return MutationResult{}, err
	}
	end, err := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return MutationResult{}, err
	}
	if end != start {
		return MutationResult{}, fmt.Errorf("CONCURRENT_INPUT: Vault inputs changed during dry-run")
	}
	return result, nil
}

func cleanupCreatedDirectory(directory *internalfsutil.CreatedDirectory) error {
	if directory == nil {
		return nil
	}
	return directory.Rollback()
}

func verifyCreatedDirectory(directory *os.File) error {
	if directory == nil {
		return os.ErrInvalid
	}
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("created target is not a regular directory")
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("created target directory is not empty")
	}
	return nil
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
		if segment == "" || segment == "." || segment == ".." || strings.HasPrefix(segment, ".") || strings.EqualFold(segment, "node_modules") || strings.EqualFold(segment, "oxpio.yaml") {
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

func validateReservedDirectoryPath(boundary internalfsutil.VaultOutputBoundary, rel string, allowAttachmentDirectory bool) error {
	if strings.EqualFold(rel, internalconfig.Filename) || strings.EqualFold(rel, internalconfig.CustomCSSFilename) {
		return fmt.Errorf("directory path %q is a reserved file", rel)
	}
	config, err := internalconfig.LoadForThemePreview(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		if allowAttachmentDirectory {
			attachment, attachmentErr := internalconfig.ConfiguredAttachmentDirectory(boundary.VaultPath)
			if attachmentErr == nil && attachment != "" {
				if caseVariantPathOverlaps(rel, attachment) {
					return fmt.Errorf("directory path %q differs in case from attachments.directory %q", rel, attachment)
				}
				if portablePathWithin(rel, attachment) || portablePathWithin(attachment, rel) {
					return nil
				}
			}
		}
		return err
	}
	attachment := filepath.ToSlash(config.AttachmentDirectory)
	if attachment != "" {
		if caseVariantPathOverlaps(rel, attachment) {
			return fmt.Errorf("directory path %q differs in case from attachments.directory %q", rel, attachment)
		}
		if portablePathWithin(rel, attachment) {
			if allowAttachmentDirectory {
				return nil
			}
			return fmt.Errorf("directory path %q is inside attachments.directory", rel)
		}
	}
	return nil
}

func portablePathWithin(candidate, root string) bool {
	candidate = path.Clean(filepath.ToSlash(candidate))
	root = path.Clean(filepath.ToSlash(root))
	return candidate == root || strings.HasPrefix(candidate, root+"/")
}

func caseVariantPathOverlaps(left, right string) bool {
	return caseVariantPathWithin(left, right) || caseVariantPathWithin(right, left)
}

func caseVariantPathWithin(candidate, root string) bool {
	candidateParts := strings.Split(path.Clean(filepath.ToSlash(candidate)), "/")
	rootParts := strings.Split(path.Clean(filepath.ToSlash(root)), "/")
	if len(candidateParts) < len(rootParts) {
		return false
	}
	variant := false
	for index, rootPart := range rootParts {
		if !strings.EqualFold(candidateParts[index], rootPart) {
			return false
		}
		if candidateParts[index] != rootPart {
			variant = true
		}
	}
	return variant
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

func directoryEmptyHash() string { sum := sha256.Sum256(nil); return hex.EncodeToString(sum[:]) }
