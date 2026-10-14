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
	if dryRun {
		journal, err := workspace.LoadJournal(vault)
		if errors.Is(err, os.ErrNotExist) {
			if err := workspace.CheckDryRun(vault); err != nil {
				return RecoveryResult{}, err
			}
			return RecoveryResult{Action: "none"}, nil
		}
		if err != nil {
			return RecoveryResult{}, err
		}
		result := RecoveryResult{Found: true, Phase: journal.Phase}
		if err := inspectRecoveryJournal(vault, journal); err != nil {
			return result, err
		}
		if err := validateRecoveryReadOnly(vault, journal); err != nil {
			return result, err
		}
		result.Action = recoveryAction(journal)
		return result, nil
	}

	lease, err := workspace.AcquireForRecovery(vault)
	if err != nil {
		return RecoveryResult{}, err
	}
	defer func() { _ = lease.Release() }()
	journal, err := workspace.LoadJournal(vault)
	if errors.Is(err, os.ErrNotExist) {
		return RecoveryResult{Action: "none"}, nil
	}
	if err != nil {
		return RecoveryResult{}, err
	}
	result := RecoveryResult{Found: true, Phase: journal.Phase}
	if err := inspectRecoveryJournal(vault, journal); err != nil {
		return result, err
	}
	lockedTarget := filepath.Clean(journal.TargetPath)
	outputLease, _, err := acquireOutputLease(vault, lockedTarget)
	if err != nil {
		return result, err
	}
	defer func() {
		if outputLease != nil {
			_ = outputLease()
		}
	}()
	// Re-read after acquiring both coordination locks. If the journal target
	// changed, move the output lock to that exact target before committing.
	journal, err = workspace.LoadJournal(vault)
	if err != nil {
		return result, err
	}
	if filepath.Clean(journal.TargetPath) != lockedTarget {
		if outputLease != nil {
			if releaseErr := outputLease(); releaseErr != nil {
				return result, releaseErr
			}
		}
		outputLease = nil
		lockedTarget = filepath.Clean(journal.TargetPath)
		outputLease, _, err = acquireOutputLease(vault, lockedTarget)
		if err != nil {
			return result, err
		}
		journal, err = workspace.LoadJournal(vault)
		if err != nil {
			return result, err
		}
		if filepath.Clean(journal.TargetPath) != lockedTarget {
			return result, fmt.Errorf("%w: recovery target changed while locking", workspace.ErrRecovery)
		}
	}
	result.Phase = journal.Phase
	if err := inspectRecoveryJournal(vault, journal); err != nil {
		return result, err
	}
	switch journal.Phase {
	case "prepared":
		if journal.OutputAfterHash != "" && directoryHash(journal.TargetPath) == journal.OutputAfterHash {
			if err := validatePreparedOutput(journal); err != nil {
				return result, err
			}
			if err := removeJournalArtifacts(journal); err != nil {
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
		if journal.OutputIdentity != "" && outputIdentity(journal.TargetPath) != journal.OutputIdentity {
			return result, fmt.Errorf("%w: prepared output identity changed before recovery", workspace.ErrRecovery)
		}
		if journal.CommitKind == "source-only" {
			if sourceOnlyAfter(journal) {
				journal.Phase = "source-committed"
				if err := workspace.WriteJournal(vault, journal); err != nil {
					return result, fmt.Errorf("%w: finalize source-only recovery: %v", workspace.ErrRecovery, err)
				}
				if err := workspace.ClearJournal(vault); err != nil {
					return result, err
				}
				result.Action, result.Changed = "confirm-source-only", true
				break
			}
			if !sourceOnlyBefore(journal) {
				return result, fmt.Errorf("%w: prepared source-only mutation has an unknown source state", workspace.ErrRecovery)
			}
			if err := workspace.RemoveCompleted(vault, sourceOnlyCompletedPaths(journal)); err != nil {
				return result, err
			}
		}
		if journal.CommitKind == "source+output" && journal.SourcePath != "" {
			if sourceCommittedAfterPrepared(journal) {
				journal.Phase = "source-committed"
				if err := workspace.WriteJournal(vault, journal); err != nil {
					return result, fmt.Errorf("%w: finalize source commit after prepared phase: %v", workspace.ErrRecovery, err)
				}
				if err := continueOutputCommit(vault, journal); err != nil {
					return result, err
				}
				if err := workspace.ClearJournal(vault); err != nil {
					return result, err
				}
				result.Action, result.Changed = "complete-output", true
				break
			}
			if err := validatePreparedSource(journal); err != nil {
				return result, err
			}
		}
		if err := removeJournalArtifacts(journal); err != nil {
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
			if journal.SourceBeforePath != "" && filepath.Clean(journal.SourceBeforePath) != filepath.Clean(sourcePath) && fileHash(journal.SourceBeforePath) != "absent" {
				return result, fmt.Errorf("%w: source-only destination was recreated", workspace.ErrRecovery)
			}
			if journal.OutputPath != "" {
				if journal.OutputBeforeHash != "" && directoryHash(journal.OutputPath) != journal.OutputBeforeHash {
					return result, fmt.Errorf("%w: source-only output changed", workspace.ErrRecovery)
				}
				if journal.OutputIdentity != "" && outputIdentity(journal.OutputPath) != journal.OutputIdentity {
					return result, fmt.Errorf("%w: source-only output identity changed", workspace.ErrRecovery)
				}
			}
			if err := removeJournalArtifacts(journal); err != nil {
				return result, err
			}
			if err := workspace.ClearJournal(vault); err != nil {
				return result, err
			}
			result.Action, result.Changed = "confirm-source-only", true
			break
		}
		if journal.SourceBeforeHash != "" {
			beforePath := journal.SourceBeforePath
			if beforePath == "" {
				beforePath = journal.SourcePath
			}
			if fileHash(beforePath) == journal.SourceBeforeHash {
				if filepath.Clean(beforePath) != filepath.Clean(journal.SourcePath) && fileHash(journal.SourcePath) != "absent" {
					return result, fmt.Errorf("%w: source-committed destination was recreated", workspace.ErrRecovery)
				}
				if journal.OutputBeforeHash != "" && directoryHash(journal.TargetPath) != journal.OutputBeforeHash {
					return result, fmt.Errorf("%w: source-committed output changed", workspace.ErrRecovery)
				}
				if journal.OutputIdentity != "" && outputIdentity(journal.TargetPath) != journal.OutputIdentity {
					return result, fmt.Errorf("%w: source-committed output identity changed", workspace.ErrRecovery)
				}
				if err := removeJournalArtifacts(journal); err != nil {
					return result, err
				}
				if err := workspace.ClearJournal(vault); err != nil {
					return result, err
				}
				result.Action, result.Changed = "discard-source-commit", true
				break
			}
		}
		if err := validateSourceAfter(journal); err != nil {
			return result, err
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
		if !validOutputProof(want, journal.OutputAfterIdentity) {
			return result, fmt.Errorf("%w: committed output proof is unavailable", workspace.ErrRecovery)
		}
		if directoryHash(journal.TargetPath) != want {
			return result, fmt.Errorf("%w: committed output hash does not match", workspace.ErrRecovery)
		}
		if outputIdentity(journal.TargetPath) != journal.OutputAfterIdentity {
			return result, fmt.Errorf("%w: committed output postcondition identity does not match", workspace.ErrRecovery)
		}
		if journal.OutputIdentity != "" && outputIdentity(journal.TargetPath) != journal.OutputIdentity {
			return result, fmt.Errorf("%w: committed output identity does not match", workspace.ErrRecovery)
		}
		if err := removeJournalArtifacts(journal); err != nil {
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

func sourceOnlyAfter(journal workspace.Journal) bool {
	sourcePath := journal.SourcePath
	if sourcePath == "" {
		sourcePath = journal.TargetPath
	}
	after := journal.SourceAfterHash
	if after == "" {
		after = journal.AfterHash
	}
	if sourcePath == "" || after == "" || fileHash(sourcePath) != after {
		return false
	}
	if journal.SourceBeforePath != "" && filepath.Clean(journal.SourceBeforePath) != filepath.Clean(sourcePath) {
		return fileHash(journal.SourceBeforePath) == "absent"
	}
	return true
}

func sourceOnlyCompletedPaths(journal workspace.Journal) []workspace.CompletedPath {
	paths := make([]workspace.CompletedPath, 0, 2)
	after := journal.SourceAfterHash
	if after == "" {
		after = journal.AfterHash
	}
	if journal.SourcePath != "" && after != "" {
		paths = append(paths, workspace.CompletedPath{Path: journal.SourcePath, AfterHash: after})
	}
	if journal.SourceBeforePath != "" && filepath.Clean(journal.SourceBeforePath) != filepath.Clean(journal.SourcePath) {
		paths = append(paths, workspace.CompletedPath{Path: journal.SourceBeforePath, AfterHash: "absent"})
	}
	return paths
}

func sourceOnlyBefore(journal workspace.Journal) bool {
	beforePath := journal.SourceBeforePath
	if beforePath == "" {
		beforePath = journal.SourcePath
	}
	if beforePath == "" || journal.SourceBeforeHash == "" || fileHash(beforePath) != journal.SourceBeforeHash {
		return false
	}
	if journal.SourcePath != "" && filepath.Clean(journal.SourcePath) != filepath.Clean(beforePath) {
		return fileHash(journal.SourcePath) == "absent"
	}
	return true
}

func recoveryAction(journal workspace.Journal) string {
	switch journal.Phase {
	case "prepared":
		if journal.OutputAfterHash != "" && directoryHash(journal.TargetPath) == journal.OutputAfterHash {
			return "cleanup-committed"
		}
		if journal.CommitKind == "source-only" && sourceOnlyAfter(journal) {
			return "confirm-source-only"
		}
		return "discard-prepared"
	case "source-committed":
		if journal.CommitKind == "source-only" {
			return "confirm-source-only"
		}
		if journal.OutputAfterHash != "" && directoryHash(journal.TargetPath) == journal.OutputAfterHash {
			return "cleanup-committed"
		}
		return "complete-output"
	case "output-committed", "completed":
		return "cleanup-committed"
	default:
		return "reject"
	}
}

func inspectRecoveryJournal(vault string, journal workspace.Journal) error {
	if strings.TrimSpace(journal.InputFingerprint) == "" {
		return fmt.Errorf("%w: transaction input fingerprint is missing", workspace.ErrRecovery)
	}
	if strings.TrimSpace(journal.TargetPath) == "" {
		return fmt.Errorf("%w: transaction target is empty", workspace.ErrRecovery)
	}
	if err := validateTransactionPath(vault, journal.TargetPath, journal.TemporaryPath); err != nil {
		return err
	}
	if err := validateTransactionPath(vault, journal.TargetPath, journal.BackupPath); err != nil {
		return err
	}
	if journal.CommitKind == "source-only" {
		if journal.OutputPath != "" {
			boundary, boundaryErr := internalfsutil.ResolveVaultOutput(vault, journal.OutputPath)
			if boundaryErr != nil || filepath.Clean(boundary.OutputPath) != filepath.Clean(journal.OutputPath) {
				return fmt.Errorf("%w: source-only output boundary is invalid", workspace.ErrRecovery)
			}
		}
	} else {
		boundary, boundaryErr := internalfsutil.ResolveVaultOutput(vault, journal.TargetPath)
		if boundaryErr != nil || filepath.Clean(boundary.OutputPath) != filepath.Clean(journal.TargetPath) {
			return fmt.Errorf("%w: recovery output boundary is invalid", workspace.ErrRecovery)
		}
	}
	if journal.SourcePath != "" && !internalfsutil.PathWithinRoot(vault, journal.SourcePath) {
		return fmt.Errorf("%w: source path escapes vault", workspace.ErrRecovery)
	}
	if journal.SourceBeforePath != "" && !internalfsutil.PathWithinRoot(vault, journal.SourceBeforePath) {
		return fmt.Errorf("%w: source before path escapes vault", workspace.ErrRecovery)
	}
	for _, cleanupPath := range journal.CleanupPaths {
		if err := validateTransactionPath(vault, journal.TargetPath, cleanupPath); err != nil {
			return err
		}
	}
	if journal.Phase == "prepared" && strings.TrimSpace(journal.TemporaryPath) == "" {
		return fmt.Errorf("%w: prepared transaction has no temporary path", workspace.ErrRecovery)
	}
	if journal.Phase == "source-committed" && journal.CommitKind == "source+output" && strings.TrimSpace(journal.TemporaryPath) == "" {
		return fmt.Errorf("%w: source-committed transaction has no staging path", workspace.ErrRecovery)
	}
	if err := validateJournalCompleteness(journal); err != nil {
		return err
	}
	return nil
}

func validateJournalCompleteness(journal workspace.Journal) error {
	require := func(value, field string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: journal field %s is missing", workspace.ErrRecovery, field)
		}
		return nil
	}
	for _, field := range []struct {
		value string
		name  string
	}{
		{journal.InputFingerprint, "inputFingerprint"},
		{journal.TargetPath, "targetPath"},
	} {
		if err := require(field.value, field.name); err != nil {
			return err
		}
	}
	switch journal.Phase {
	case "prepared":
		for _, field := range []struct {
			value string
			name  string
		}{
			{journal.OutputBeforeHash, "outputBeforeHash"},
			{journal.OutputIdentity, "outputIdentity"},
		} {
			if err := require(field.value, field.name); err != nil {
				return err
			}
		}
		if journal.OutputAfterHash != "" {
			if err := require(journal.OutputAfterIdentity, "outputAfterIdentity"); err != nil {
				return err
			}
		}
		if journal.CommitKind == "source+output" && journal.SourcePath != "" {
			for _, field := range []struct {
				value string
				name  string
			}{
				{journal.SourceBeforePath, "sourceBeforePath"},
				{journal.SourceBeforeHash, "sourceBeforeHash"},
				{journal.SourceAfterHash, "sourceAfterHash"},
			} {
				if err := require(field.value, field.name); err != nil {
					return err
				}
			}
		}
	case "source-committed":
		if journal.CommitKind == "source-only" {
			if journal.SourcePath == "" && journal.TargetPath == "" {
				return fmt.Errorf("%w: source-only journal has no source path", workspace.ErrRecovery)
			}
			if journal.SourceAfterHash == "" && journal.AfterHash == "" {
				return fmt.Errorf("%w: source-only journal has no source after hash", workspace.ErrRecovery)
			}
			return nil
		}
		for _, field := range []struct {
			value string
			name  string
		}{
			{journal.SourcePath, "sourcePath"},
			{journal.SourceBeforePath, "sourceBeforePath"},
			{journal.OutputBeforeHash, "outputBeforeHash"},
			{journal.OutputIdentity, "outputIdentity"},
			{journal.SourceBeforeHash, "sourceBeforeHash"},
			{journal.SourceAfterHash, "sourceAfterHash"},
			{journal.OutputAfterHash, "outputAfterHash"},
			{journal.OutputAfterIdentity, "outputAfterIdentity"},
		} {
			if err := require(field.value, field.name); err != nil {
				return err
			}
		}
	case "output-committed", "completed":
		for _, field := range []struct {
			value string
			name  string
		}{
			{journal.OutputIdentity, "outputIdentity"},
			{journal.OutputAfterHash, "outputAfterHash"},
			{journal.OutputAfterIdentity, "outputAfterIdentity"},
		} {
			if err := require(field.value, field.name); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w: unsupported phase", workspace.ErrRecovery)
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
	cleanTarget := filepath.Clean(target)
	if absolute == cleanTarget || filepath.Base(absolute) == "." || filepath.Base(absolute) == ".." {
		return fmt.Errorf("%w: transaction path is unsafe", workspace.ErrRecovery)
	}
	base := filepath.Base(absolute)
	recognized := strings.HasPrefix(base, ".oxpio-") || base == ".stage"
	stageRoot := filepath.Dir(absolute)
	nestedStage := filepath.Dir(stageRoot) == filepath.Dir(cleanTarget) && strings.HasPrefix(filepath.Base(stageRoot), ".oxpio-")
	if !recognized && nestedStage {
		recognized = true
	}
	if !internalfsutil.PathWithinRoot(vault, absolute) && filepath.Dir(absolute) != filepath.Dir(cleanTarget) && !nestedStage {
		return fmt.Errorf("%w: transaction path escapes its coordination boundary", workspace.ErrRecovery)
	}
	if !recognized {
		return fmt.Errorf("%w: transaction path is not a recognized temporary object", workspace.ErrRecovery)
	}
	return nil
}

func continueOutputCommit(vault string, journal workspace.Journal) error {
	expectedHash := journal.OutputAfterHash
	expectedIdentity := journal.OutputAfterIdentity
	if !validOutputProof(expectedHash, expectedIdentity) {
		return fmt.Errorf("%w: recovered output proof is unavailable", workspace.ErrRecovery)
	}
	if err := validateAlreadySwappedOutput(vault, journal); err == nil {
		journal.Phase = "output-committed"
		journal.OutputIdentity = outputIdentity(journal.TargetPath)
		if err := workspace.WriteJournal(vault, journal); err != nil {
			return fmt.Errorf("%w: persist recovered output phase: %v", workspace.ErrRecovery, err)
		}
		return removeJournalArtifacts(journal)
	}
	if err := validateOutputContinuation(vault, journal); err != nil {
		return err
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
	committedHash := directoryHash(journal.TargetPath)
	committedIdentity := outputIdentity(journal.TargetPath)
	journal.Phase = "output-committed"
	journal.OutputAfterHash = expectedHash
	journal.OutputIdentity = committedIdentity
	journal.OutputAfterIdentity = expectedIdentity
	journal.AfterHash = expectedHash
	if committedHash != expectedHash || committedIdentity != expectedIdentity {
		if err := workspace.WriteJournal(vault, journal); err != nil {
			return fmt.Errorf("%w: persist recovered output postcondition: %v", workspace.ErrRecovery, err)
		}
		return fmt.Errorf("%w: recovered output postcondition mismatch: expected hash %s and identity %s, found hash %s and identity %s", workspace.ErrRecovery, expectedHash, expectedIdentity, committedHash, committedIdentity)
	}
	if err := removeTransactionPath(journal.TemporaryPath); err != nil {
		return err
	}
	if err := workspace.WriteJournal(vault, journal); err != nil {
		return fmt.Errorf("%w: persist recovery phase: %v", workspace.ErrRecovery, err)
	}
	if err := removeJournalArtifacts(journal); err != nil {
		return err
	}
	return nil
}

func sourceCommittedAfterPrepared(journal workspace.Journal) bool {
	if journal.SourcePath == "" || journal.SourceAfterHash == "" || fileHash(journal.SourcePath) != journal.SourceAfterHash {
		return false
	}
	beforePath := journal.SourceBeforePath
	if beforePath == "" || filepath.Clean(beforePath) == filepath.Clean(journal.SourcePath) {
		return true
	}
	return fileHash(beforePath) == "absent"
}

func validatePreparedSource(journal workspace.Journal) error {
	beforePath := journal.SourceBeforePath
	if beforePath == "" {
		beforePath = journal.SourcePath
	}
	if journal.SourceBeforeHash == "" || fileHash(beforePath) != journal.SourceBeforeHash {
		return fmt.Errorf("%w: prepared source changed unexpectedly", workspace.ErrRecovery)
	}
	if filepath.Clean(beforePath) != filepath.Clean(journal.SourcePath) && fileHash(journal.SourcePath) != "absent" {
		return fmt.Errorf("%w: prepared destination changed unexpectedly", workspace.ErrRecovery)
	}
	return nil
}

func validatePreparedOutput(journal workspace.Journal) error {
	if journal.OutputAfterIdentity == "" || outputIdentity(journal.TargetPath) != journal.OutputAfterIdentity {
		return fmt.Errorf("%w: prepared output identity does not match", workspace.ErrRecovery)
	}
	if journal.TemporaryPath != "" {
		stagingHash := directoryHash(journal.TemporaryPath)
		if stagingHash != "absent" && stagingHash != journal.OutputBeforeHash {
			return fmt.Errorf("%w: prepared staging does not contain the previous output", workspace.ErrRecovery)
		}
	}
	if journal.SourcePath != "" && journal.SourceAfterHash != "" && fileHash(journal.SourcePath) != journal.SourceAfterHash {
		return fmt.Errorf("%w: prepared source does not match committed output", workspace.ErrRecovery)
	}
	return nil
}

func validateSourceAfter(journal workspace.Journal) error {
	sourcePath := journal.SourcePath
	if sourcePath == "" {
		sourcePath = journal.TargetPath
	}
	sourceAfterHash := journal.SourceAfterHash
	if sourceAfterHash == "" {
		sourceAfterHash = journal.AfterHash
	}
	if sourcePath == "" || sourceAfterHash == "" {
		return fmt.Errorf("%w: source after hash is unavailable", workspace.ErrRecovery)
	}
	if fileHash(sourcePath) != sourceAfterHash {
		return fmt.Errorf("%w: source after hash does not match", workspace.ErrRecovery)
	}
	return nil
}

func validateAlreadySwappedOutput(vault string, journal workspace.Journal) error {
	if !validOutputProof(journal.OutputAfterHash, journal.OutputAfterIdentity) {
		return fmt.Errorf("%w: recovered output proof is unavailable", workspace.ErrRecovery)
	}
	if err := validateSourceAfter(journal); err != nil {
		return err
	}
	fingerprint, err := workspace.Fingerprint(vault, journal.TargetPath)
	if err != nil {
		return fmt.Errorf("%w: input fingerprint: %v", workspace.ErrRecovery, err)
	}
	if fingerprint != journal.InputFingerprint {
		return fmt.Errorf("%w: input fingerprint changed", workspace.ErrRecovery)
	}
	if directoryHash(journal.TargetPath) != journal.OutputAfterHash || outputIdentity(journal.TargetPath) != journal.OutputAfterIdentity {
		return fmt.Errorf("%w: recovered output proof does not match", workspace.ErrRecovery)
	}
	return nil
}

func validOutputProof(hash, identity string) bool {
	return strings.TrimSpace(hash) != "" && hash != "unavailable" && strings.TrimSpace(identity) != "" && identity != "absent"
}

func validateOutputContinuation(vault string, journal workspace.Journal) error {
	if err := validateSourceAfter(journal); err != nil {
		return err
	}
	fingerprint, err := workspace.Fingerprint(vault, journal.TargetPath)
	if err != nil {
		return fmt.Errorf("%w: input fingerprint: %v", workspace.ErrRecovery, err)
	}
	if fingerprint != journal.InputFingerprint {
		return fmt.Errorf("%w: input fingerprint changed", workspace.ErrRecovery)
	}
	if journal.OutputBeforeHash == "" || directoryHash(journal.TargetPath) != journal.OutputBeforeHash {
		return fmt.Errorf("%w: output changed before recovery", workspace.ErrRecovery)
	}
	if journal.OutputIdentity != "" && outputIdentity(journal.TargetPath) != journal.OutputIdentity {
		return fmt.Errorf("%w: output identity changed before recovery", workspace.ErrRecovery)
	}
	if journal.OutputAfterHash == "" {
		return fmt.Errorf("%w: staged output hash is unavailable", workspace.ErrRecovery)
	}
	stagingInfo, err := os.Lstat(journal.TemporaryPath)
	if err != nil || stagingInfo.Mode()&os.ModeSymlink != 0 || !stagingInfo.IsDir() {
		return fmt.Errorf("%w: staging is unavailable", workspace.ErrRecovery)
	}
	if directoryHash(journal.TemporaryPath) != journal.OutputAfterHash {
		return fmt.Errorf("%w: staging hash does not match", workspace.ErrRecovery)
	}
	return nil
}

func validateRecoveryReadOnly(vault string, journal workspace.Journal) error {
	switch journal.Phase {
	case "prepared":
		if journal.OutputAfterHash != "" && directoryHash(journal.TargetPath) == journal.OutputAfterHash {
			return validatePreparedOutput(journal)
		}
		if journal.OutputBeforeHash != "" && directoryHash(journal.TargetPath) != journal.OutputBeforeHash {
			return fmt.Errorf("%w: prepared output changed before recovery", workspace.ErrRecovery)
		}
		if journal.OutputIdentity != "" && outputIdentity(journal.TargetPath) != journal.OutputIdentity {
			return fmt.Errorf("%w: prepared output identity changed before recovery", workspace.ErrRecovery)
		}
		if journal.CommitKind == "source-only" {
			if !sourceOnlyAfter(journal) && !sourceOnlyBefore(journal) {
				return fmt.Errorf("%w: prepared source-only mutation has an unknown source state", workspace.ErrRecovery)
			}
			return nil
		}
		if journal.CommitKind == "source+output" && journal.SourcePath != "" {
			return validatePreparedSource(journal)
		}
		return nil
	case "source-committed":
		if journal.CommitKind == "source-only" {
			if err := validateSourceAfter(journal); err != nil {
				return err
			}
			if journal.OutputPath != "" {
				if journal.OutputBeforeHash != "" && directoryHash(journal.OutputPath) != journal.OutputBeforeHash {
					return fmt.Errorf("%w: source-only output changed", workspace.ErrRecovery)
				}
				if journal.OutputIdentity != "" && outputIdentity(journal.OutputPath) != journal.OutputIdentity {
					return fmt.Errorf("%w: source-only output identity changed", workspace.ErrRecovery)
				}
			}
			return nil
		}
		if journal.SourceBeforeHash != "" {
			beforePath := journal.SourceBeforePath
			if beforePath == "" {
				beforePath = journal.SourcePath
			}
			if fileHash(beforePath) == journal.SourceBeforeHash {
				if filepath.Clean(beforePath) != filepath.Clean(journal.SourcePath) && fileHash(journal.SourcePath) != "absent" {
					return fmt.Errorf("%w: source-committed destination was recreated", workspace.ErrRecovery)
				}
				if journal.OutputBeforeHash != "" && directoryHash(journal.TargetPath) != journal.OutputBeforeHash {
					return fmt.Errorf("%w: source-committed output changed", workspace.ErrRecovery)
				}
				if journal.OutputIdentity != "" && outputIdentity(journal.TargetPath) != journal.OutputIdentity {
					return fmt.Errorf("%w: source-committed output identity changed", workspace.ErrRecovery)
				}
				return nil
			}
		}
		if journal.OutputAfterHash != "" && directoryHash(journal.TargetPath) == journal.OutputAfterHash {
			return validateAlreadySwappedOutput(vault, journal)
		}
		return validateOutputContinuation(vault, journal)
	case "output-committed", "completed":
		want := journal.OutputAfterHash
		if want == "" {
			want = journal.AfterHash
		}
		if !validOutputProof(want, journal.OutputAfterIdentity) {
			return fmt.Errorf("%w: committed output proof is unavailable", workspace.ErrRecovery)
		}
		if directoryHash(journal.TargetPath) != want {
			return fmt.Errorf("%w: committed output hash does not match", workspace.ErrRecovery)
		}
		if outputIdentity(journal.TargetPath) != journal.OutputAfterIdentity {
			return fmt.Errorf("%w: committed output postcondition identity does not match", workspace.ErrRecovery)
		}
		if journal.OutputIdentity != "" && outputIdentity(journal.TargetPath) != journal.OutputIdentity {
			return fmt.Errorf("%w: committed output identity does not match", workspace.ErrRecovery)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported phase", workspace.ErrRecovery)
	}
}

func removeJournalArtifacts(journal workspace.Journal) error {
	paths := append([]string{journal.BackupPath, journal.TemporaryPath}, journal.CleanupPaths...)
	seen := make(map[string]struct{}, len(paths))
	var err error
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		err = errors.Join(err, removeTransactionPath(clean))
	}
	return err
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
