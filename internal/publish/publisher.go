// Package publish owns formal output transactions. It consumes a prepared
// staging directory and never interprets Markdown or configuration.
package publish

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/workspace"
)

const (
	ManagedMarkerFilename = ".oxpio-output"
	ManagedMarkerContents = "managed by oxpio\n"
)

type Options struct {
	Durable              bool
	InputFingerprint     string
	ValidateInput        func() error
	TrackTransactionPath func(string)
	RemoveAll            func(string) error
}

type Transaction struct {
	vault          string
	output         string
	staging        string
	outputState    outputState
	options        Options
	outputLease    func() error
	outputLockPath string
	journal        workspace.Journal
	validateInput  func() error
	closed         bool
}

type CommitResult struct{ CleanupError error }

type outputState struct {
	exists  bool
	info    os.FileInfo
	empty   bool
	managed bool
}

// CheckOutputBoundary validates output state without creating staging, locks,
// journals, or probing replacement capability.
func CheckOutputBoundary(vault, output string) error {
	boundary, err := internalfsutil.ResolveVaultOutput(vault, output)
	if err != nil {
		return err
	}
	_, err = inspectOutputBoundary(boundary)
	return err
}

// Prepare validates the output boundary, creates same-filesystem staging, and
// records a prepared journal for formal transactions.
func Prepare(vault, output string, options Options) (*Transaction, error) {
	boundary, err := internalfsutil.ResolveVaultOutput(vault, output)
	if err != nil {
		return nil, err
	}
	state, err := inspectOutputBoundary(boundary)
	if err != nil {
		return nil, err
	}
	parent, err := existingParent(filepath.Dir(boundary.OutputPath))
	if err != nil {
		return nil, err
	}
	transaction := &Transaction{vault: boundary.VaultPath, output: boundary.OutputPath, outputState: state, options: options, validateInput: options.ValidateInput}
	if options.Durable {
		lease, lockPath, lockErr := acquireOutputLease(boundary.VaultPath, boundary.OutputPath)
		if lockErr != nil {
			return nil, lockErr
		}
		transaction.outputLease = lease
		transaction.outputLockPath = lockPath
	}
	if options.Durable {
		if probeErr := workspace.ProbeAtomicReplace(parent); probeErr != nil {
			_ = transaction.releaseOutputLock()
			return nil, probeErr
		}
	}
	staging, err := os.MkdirTemp(parent, ".oxpio-publish-stage-*")
	if err != nil {
		_ = transaction.releaseOutputLock()
		return nil, fmt.Errorf("create publish staging: %w", err)
	}
	transaction.staging = staging
	if options.TrackTransactionPath != nil {
		options.TrackTransactionPath(filepath.Clean(staging))
	}
	if options.Durable {
		beforeHash := directoryHash(boundary.OutputPath)
		transaction.journal = workspace.Journal{
			Version: workspace.JournalVersion, CommitKind: "source+output", Phase: "prepared",
			InputFingerprint: options.InputFingerprint, TargetPath: boundary.OutputPath, TemporaryPath: staging,
			BeforeHash: beforeHash, OutputBeforeHash: beforeHash, OutputIdentity: outputIdentity(boundary.OutputPath),
		}
		if err := workspace.WriteJournal(boundary.VaultPath, transaction.journal); err != nil {
			_ = transaction.releaseOutputLock()
			_ = os.RemoveAll(staging)
			return nil, fmt.Errorf("write publish journal: %w", err)
		}
	}
	return transaction, nil
}

func (t *Transaction) StagingPath() string {
	if t == nil {
		return ""
	}
	return t.staging
}

func (t *Transaction) OutputPath() string {
	if t == nil {
		return ""
	}
	return t.output
}

// AdoptStaging moves an already-generated same-filesystem staging tree into a
// transaction owned by this package. The caller must not use the source path
// after this function returns.
func AdoptStaging(vault, output, staging string, options Options) (*Transaction, error) {
	transaction, err := Prepare(vault, output, options)
	if err != nil {
		return nil, err
	}
	if err := os.RemoveAll(transaction.staging); err != nil {
		_ = transaction.Abort()
		return nil, err
	}
	if err := os.Rename(staging, transaction.staging); err != nil {
		_ = transaction.Abort()
		return nil, fmt.Errorf("adopt publish staging: %w", err)
	}
	return transaction, nil
}

// Commit makes the staged tree the complete formal output. Existing output is
// exchanged, never renamed away first; a cleanup failure is returned only as a
// post-commit warning in CommitResult.
func (t *Transaction) Commit() (result CommitResult, err error) {
	if t == nil || t.closed {
		return result, errors.New("publish transaction is closed")
	}
	if t.validateInput != nil {
		if err := t.validateInput(); err != nil {
			return result, err
		}
	}
	if err := t.revalidate(); err != nil {
		return result, err
	}
	if err := os.Chmod(t.staging, t.mode()); err != nil {
		return result, fmt.Errorf("set staged output permissions: %w", err)
	}
	if t.options.Durable {
		t.journal.OutputAfterHash = directoryHash(t.staging)
		t.journal.AfterHash = t.journal.OutputAfterHash
		if err := workspace.WriteJournal(t.vault, t.journal); err != nil {
			return result, fmt.Errorf("persist staged output hash: %w", err)
		}
	}
	if !t.outputState.exists {
		if err := os.Rename(t.staging, t.output); err != nil {
			return result, fmt.Errorf("publish output: %w", err)
		}
	} else {
		if err := workspace.AtomicReplaceDirectory(t.staging, t.output); err != nil {
			return result, err
		}
	}
	// After a successful replacement, staging names the old root. It is safe to
	// remove only that identity and never the public output name.
	oldRoot := t.staging
	t.staging = ""
	t.closed = true
	if t.options.Durable {
		t.journal.Phase = "output-committed"
		t.journal.OutputAfterHash = directoryHash(t.output)
		t.journal.AfterHash = t.journal.OutputAfterHash
		t.journal.OutputIdentity = outputIdentity(t.output)
		if journalErr := workspace.WriteJournal(t.vault, t.journal); journalErr != nil {
			result.CleanupError = journalErr
		}
	}
	if removeErr := t.removeAll(oldRoot); removeErr != nil {
		result.CleanupError = errors.Join(result.CleanupError, fmt.Errorf("cleanup previous output: %w", removeErr))
	}
	if t.options.Durable {
		if clearErr := workspace.ClearJournal(t.vault); clearErr != nil {
			result.CleanupError = errors.Join(result.CleanupError, clearErr)
		}
	}
	result.CleanupError = errors.Join(result.CleanupError, t.releaseOutputLock())
	return result, nil
}

// Abort removes only uncommitted staging and clears its prepared journal.
func (t *Transaction) Abort() error {
	if t == nil || t.closed {
		return nil
	}
	t.closed = true
	var err error
	if strings.TrimSpace(t.staging) != "" {
		err = t.removeAll(t.staging)
	}
	if t.options.Durable {
		err = errors.Join(err, workspace.ClearJournal(t.vault))
	}
	err = errors.Join(err, t.releaseOutputLock())
	return err
}

func (t *Transaction) removeAll(path string) error {
	if t != nil && t.options.RemoveAll != nil {
		return t.options.RemoveAll(path)
	}
	return os.RemoveAll(path)
}

// CheckExternalOutputLock performs a read-only probe of the short-lived
// external-output lock. It never creates a lock object.
func CheckExternalOutputLock(vault, output string) error {
	boundary, err := internalfsutil.ResolveVaultOutput(vault, output)
	if err != nil {
		return err
	}
	if !isExternalOutput(boundary.VaultPath, boundary.OutputPath) {
		return nil
	}
	lease, err := workspace.AcquireFileLock(externalLockPath(boundary.OutputPath), false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if errors.Is(err, workspace.ErrBusy) {
		return fmt.Errorf("EXTERNAL_OUTPUT_SHARED: %w", err)
	}
	if err != nil {
		return fmt.Errorf("inspect external output lock: %w", err)
	}
	return lease()
}

func acquireOutputLease(vault, output string) (func() error, string, error) {
	if !isExternalOutput(vault, output) {
		return nil, "", nil
	}
	lockPath := externalLockPath(output)
	lease, err := workspace.AcquireFileLock(lockPath, true)
	if err != nil {
		if errors.Is(err, workspace.ErrBusy) {
			return nil, "", fmt.Errorf("EXTERNAL_OUTPUT_SHARED: %w", err)
		}
		return nil, "", fmt.Errorf("acquire external output lock: %w", err)
	}
	return lease, lockPath, nil
}

func (t *Transaction) releaseOutputLock() error {
	if t.outputLease == nil {
		return nil
	}
	lease := t.outputLease
	t.outputLease = nil
	err := lease()
	t.outputLockPath = ""
	return err
}

func (t *Transaction) mode() os.FileMode {
	if t.outputState.info != nil {
		return t.outputState.info.Mode().Perm()
	}
	return 0o755
}

func (t *Transaction) revalidate() error {
	current, err := inspectOutput(t.output)
	if err != nil {
		return err
	}
	if current.exists != t.outputState.exists {
		return fmt.Errorf("output path changed while staging")
	}
	if current.exists && (current.info == nil || t.outputState.info == nil || !os.SameFile(current.info, t.outputState.info)) {
		return fmt.Errorf("output path was replaced while staging")
	}
	if current.exists && !current.empty && !current.managed {
		return fmt.Errorf("output path gained unmanaged content while staging")
	}
	return nil
}

func inspectOutputBoundary(boundary internalfsutil.VaultOutputBoundary) (outputState, error) {
	state, err := inspectOutput(boundary.OutputPath)
	if err != nil {
		return outputState{}, err
	}
	if state.exists && !state.empty && !state.managed {
		return outputState{}, fmt.Errorf("output path %q already contains unmanaged content", boundary.OutputPath)
	}
	if _, err := existingParent(filepath.Dir(boundary.OutputPath)); err != nil {
		return outputState{}, err
	}
	return state, nil
}

func inspectOutput(output string) (outputState, error) {
	info, err := os.Lstat(output)
	if errors.Is(err, os.ErrNotExist) {
		return outputState{}, nil
	}
	if err != nil {
		return outputState{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return outputState{}, fmt.Errorf("output path %q must be a regular directory", output)
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		return outputState{}, err
	}
	state := outputState{exists: true, info: info, empty: len(entries) == 0}
	for _, entry := range entries {
		if entry.Name() != ManagedMarkerFilename {
			continue
		}
		_, marker, _, readErr := internalfsutil.ReadContainedRegularFile(output, entry.Name())
		state.managed = readErr == nil && string(marker) == ManagedMarkerContents
		break
	}
	return state, nil
}

func existingParent(path string) (string, error) {
	current := filepath.Clean(path)
	info, err := os.Lstat(current)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("output parent %q does not exist", current)
		}
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("output parent %q must be a regular directory", current)
	}
	return current, nil
}

func isExternalOutput(vault, output string) bool {
	return !internalfsutil.PathWithinRoot(vault, output)
}

func externalLockPath(output string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(output)))
	base := strings.Trim(filepath.Base(output), ".")
	if base == "" {
		base = "output"
	}
	return filepath.Join(filepath.Dir(output), fmt.Sprintf(".%s-oxpio-%s.lock", base, hex.EncodeToString(digest[:8])))
}

func directoryHash(path string) string {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent"
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "unavailable"
	}
	hash := sha256.New()
	walkErr := filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("output contains symbolic link %q", current)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(path, current)
		if err != nil {
			return err
		}
		fileHash := sha256.Sum256(data)
		_, _ = fmt.Fprintf(hash, "%s:%x\n", filepath.ToSlash(rel), fileHash)
		return nil
	})
	if walkErr != nil {
		return "unavailable"
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func outputIdentity(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		return "absent"
	}
	return fmt.Sprintf("%T:%v:%o", info.Sys(), info.Sys(), info.Mode())
}
