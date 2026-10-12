package publish

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/workspace"
)

// RecoveryResult reports the bounded action selected for an active journal.
// Paths are intentionally omitted so adapters cannot leak internal state.
type RecoveryResult struct {
	Found   bool
	Phase   string
	Action  string
	Changed bool
}

// Recover completes only a journal state whose hashes and filesystem
// identities still prove the documented transaction matrix. dryRun performs
// the same state inspection without taking a lock or changing any path.
func Recover(vault string, dryRun bool) (RecoveryResult, error) {
	journal, err := workspace.LoadJournal(vault)
	if errors.Is(err, os.ErrNotExist) {
		return RecoveryResult{Action: "none"}, nil
	}
	if err != nil {
		return RecoveryResult{}, err
	}
	result := RecoveryResult{Found: true, Phase: journal.Phase}
	if dryRun {
		if err := inspectRecoveryJournal(vault, journal); err != nil {
			return result, err
		}
		result.Action = recoveryAction(journal)
		return result, nil
	}
	lease, err := workspace.AcquireForRecovery(vault)
	if err != nil {
		return result, err
	}
	defer func() { _ = lease.Release() }()
	outputLease, _, err := acquireOutputLease(vault, journal.TargetPath)
	if err != nil {
		return result, err
	}
	defer func() {
		if outputLease != nil {
			_ = outputLease()
		}
	}()
	// Re-read after acquiring the lock. The first read is only a cheap
	// decision point and must never be used as a commit authority.
	journal, err = workspace.LoadJournal(vault)
	if err != nil {
		return result, err
	}
	result.Phase = journal.Phase
	if err := inspectRecoveryJournal(vault, journal); err != nil {
		return result, err
	}
	switch journal.Phase {
	case "prepared":
		if journal.OutputAfterHash != "" && directoryHash(journal.TargetPath) == journal.OutputAfterHash {
			if err := removeTransactionPath(journal.TemporaryPath); err != nil {
				return result, err
			}
			if err := workspace.ClearJournal(vault); err != nil {
				return result, err
			}
			result.Action, result.Changed = "cleanup-committed", true
			break
		}
		if journal.OutputBeforeHash != "" && directoryHash(journal.TargetPath) != journal.OutputBeforeHash {
			return result, fmt.Errorf("%w: prepared output changed before recovery", workspace.ErrRecovery)
		}
		if err := removeTransactionPath(journal.TemporaryPath); err != nil {
			return result, err
		}
		if err := workspace.ClearJournal(vault); err != nil {
			return result, err
		}
		result.Action, result.Changed = "discard-prepared", true
	case "source-committed":
		if journal.CommitKind == "source-only" {
			sourcePath := journal.SourcePath
			if sourcePath == "" {
				sourcePath = journal.TargetPath
			}
			sourceAfterHash := journal.SourceAfterHash
			if sourceAfterHash == "" {
				sourceAfterHash = journal.AfterHash
			}
			if fileHash(sourcePath) != sourceAfterHash {
				return result, fmt.Errorf("%w: source after hash does not match", workspace.ErrRecovery)
			}
			if journal.OutputPath != "" {
				if journal.OutputBeforeHash != "" && directoryHash(journal.OutputPath) != journal.OutputBeforeHash {
					return result, fmt.Errorf("%w: source-only output changed", workspace.ErrRecovery)
				}
				if journal.OutputIdentity != "" && outputIdentity(journal.OutputPath) != journal.OutputIdentity {
					return result, fmt.Errorf("%w: source-only output identity changed", workspace.ErrRecovery)
				}
			}
			if err := removeTransactionPath(journal.BackupPath); err != nil {
				return result, err
			}
			if err := workspace.ClearJournal(vault); err != nil {
				return result, err
			}
			result.Action, result.Changed = "confirm-source-only", true
			break
		}
		if err := continueOutputCommit(vault, journal); err != nil {
			return result, err
		}
		if err := workspace.ClearJournal(vault); err != nil {
			return result, err
		}
		result.Action, result.Changed = "complete-output", true
	case "output-committed", "completed":
		want := journal.OutputAfterHash
		if want == "" {
			want = journal.AfterHash
		}
		if want != "" && directoryHash(journal.TargetPath) != want {
			return result, fmt.Errorf("%w: committed output hash does not match", workspace.ErrRecovery)
		}
		if err := removeTransactionPath(journal.TemporaryPath); err != nil {
			return result, err
		}
		if err := removeTransactionPath(journal.BackupPath); err != nil {
			return result, err
		}
		if err := workspace.ClearJournal(vault); err != nil {
			return result, err
		}
		result.Action, result.Changed = "cleanup-committed", true
	default:
		return result, fmt.Errorf("%w: unsupported phase", workspace.ErrRecovery)
	}
	return result, nil
}

func recoveryAction(journal workspace.Journal) string {
	switch journal.Phase {
	case "prepared":
		return "discard-prepared"
	case "source-committed":
		if journal.CommitKind == "source-only" {
			return "confirm-source-only"
		}
		return "complete-output"
	case "output-committed", "completed":
		return "cleanup-committed"
	default:
		return "reject"
	}
}

func inspectRecoveryJournal(vault string, journal workspace.Journal) error {
	if strings.TrimSpace(journal.TargetPath) == "" {
		return fmt.Errorf("%w: transaction target is empty", workspace.ErrRecovery)
	}
	if err := validateTransactionPath(vault, journal.TargetPath, journal.TemporaryPath); err != nil {
		return err
	}
	if err := validateTransactionPath(vault, journal.TargetPath, journal.BackupPath); err != nil {
		return err
	}
	if journal.SourcePath != "" && !internalfsutil.PathWithinRoot(vault, journal.SourcePath) {
		return fmt.Errorf("%w: source path escapes vault", workspace.ErrRecovery)
	}
	if journal.Phase == "prepared" && strings.TrimSpace(journal.TemporaryPath) == "" {
		return fmt.Errorf("%w: prepared transaction has no temporary path", workspace.ErrRecovery)
	}
	if journal.Phase == "source-committed" && journal.CommitKind == "source+output" && strings.TrimSpace(journal.TemporaryPath) == "" {
		return fmt.Errorf("%w: source-committed transaction has no staging path", workspace.ErrRecovery)
	}
	return nil
}

func validateTransactionPath(vault, target, path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("%w: transaction path: %v", workspace.ErrRecovery, err)
	}
	absolute = filepath.Clean(absolute)
	if absolute == filepath.Clean(target) || filepath.Base(absolute) == "." || filepath.Base(absolute) == ".." {
		return fmt.Errorf("%w: transaction path is unsafe", workspace.ErrRecovery)
	}
	if !internalfsutil.PathWithinRoot(vault, absolute) && filepath.Dir(absolute) != filepath.Dir(filepath.Clean(target)) {
		return fmt.Errorf("%w: transaction path escapes its coordination boundary", workspace.ErrRecovery)
	}
	base := filepath.Base(absolute)
	if !strings.HasPrefix(base, ".oxpio-") && base != ".stage" {
		return fmt.Errorf("%w: transaction path is not a recognized temporary object", workspace.ErrRecovery)
	}
	return nil
}

func continueOutputCommit(vault string, journal workspace.Journal) error {
	if journal.SourcePath == "" || journal.SourceAfterHash == "" || fileHash(journal.SourcePath) != journal.SourceAfterHash {
		return fmt.Errorf("%w: source after hash does not match", workspace.ErrRecovery)
	}
	if journal.OutputBeforeHash == "" || directoryHash(journal.TargetPath) != journal.OutputBeforeHash {
		return fmt.Errorf("%w: output changed before recovery", workspace.ErrRecovery)
	}
	if journal.OutputIdentity != "" && outputIdentity(journal.TargetPath) != journal.OutputIdentity {
		return fmt.Errorf("%w: output identity changed before recovery", workspace.ErrRecovery)
	}
	stagingInfo, err := os.Lstat(journal.TemporaryPath)
	if err != nil || stagingInfo.Mode()&os.ModeSymlink != 0 || !stagingInfo.IsDir() {
		return fmt.Errorf("%w: staging is unavailable", workspace.ErrRecovery)
	}
	parent := filepath.Dir(journal.TargetPath)
	if _, err := os.Lstat(parent); err != nil {
		return fmt.Errorf("%w: output parent is unavailable", workspace.ErrRecovery)
	}
	if _, err := os.Lstat(journal.TargetPath); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(journal.TemporaryPath, journal.TargetPath); err != nil {
			return fmt.Errorf("%w: continue output commit: %v", workspace.ErrRecovery, err)
		}
	} else if err == nil {
		if err := workspace.AtomicReplaceDirectory(journal.TemporaryPath, journal.TargetPath); err != nil {
			return fmt.Errorf("%w: continue output commit: %v", workspace.ErrRecovery, err)
		}
	} else {
		return fmt.Errorf("%w: inspect output: %v", workspace.ErrRecovery, err)
	}
	if err := removeTransactionPath(journal.TemporaryPath); err != nil {
		return err
	}
	journal.Phase = "output-committed"
	journal.OutputAfterHash = directoryHash(journal.TargetPath)
	journal.AfterHash = journal.OutputAfterHash
	if err := workspace.WriteJournal(vault, journal); err != nil {
		return fmt.Errorf("%w: persist recovery phase: %v", workspace.ErrRecovery, err)
	}
	if err := removeTransactionPath(journal.BackupPath); err != nil {
		return err
	}
	return nil
}

func removeTransactionPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: transaction path is symbolic", workspace.ErrRecovery)
	}
	return os.RemoveAll(path)
}

func fileHash(path string) string {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent"
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "unavailable"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "unavailable"
	}
	return fmt.Sprintf("%x", sha256Sum(data))
}

func sha256Sum(data []byte) [32]byte {
	return sha256.Sum256(data)
}
