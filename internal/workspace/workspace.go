// Package workspace owns the cross-command Vault coordination boundary.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	internalconfig "github.com/simp-lee/oxpio/internal/config"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
)

const (
	JournalVersion    = 1
	journalName       = "current.json"
	completedName     = "completed.json"
	completedLockName = "completed.lock"
)

var validJournalPhases = map[string]bool{
	"prepared":         true,
	"source-committed": true,
	"output-committed": true,
	"completed":        true,
}

var (
	ErrBusy        = errors.New("WORKSPACE_BUSY")
	ErrRecovery    = errors.New("RECOVERY_ERROR")
	ErrCASConflict = errors.New("CAS_CONFLICT")
)

type codedError struct {
	Code string
	Err  error
}

func (e *codedError) Error() string { return e.Code + ": " + e.Err.Error() }
func (e *codedError) Unwrap() error { return e.Err }
func (e *codedError) Is(target error) bool {
	if target == ErrRecovery && e.Code == "RECOVERY_ERROR" {
		return true
	}
	return errors.Is(e.Err, target)
}

// Journal is the minimal durable state needed to decide whether a transaction
// can be completed after a process interruption. Paths are internal state and
// must never be copied to public agent responses.
type CompletedPath struct {
	Path      string `json:"path"`
	AfterHash string `json:"afterHash"`
}

// SourceChange records one create-only source path in a batch mutation.
type SourceChange struct {
	Path       string `json:"path"`
	BeforeHash string `json:"beforeHash"`
	AfterHash  string `json:"afterHash"`
}

type Journal struct {
	Version             int            `json:"version"`
	CommitKind          string         `json:"commitKind"`
	Phase               string         `json:"phase"`
	InputFingerprint    string         `json:"inputFingerprint"`
	TargetPath          string         `json:"targetPath"`
	SourcePath          string         `json:"sourcePath,omitempty"`
	SourceBeforePath    string         `json:"sourceBeforePath,omitempty"`
	OutputPath          string         `json:"outputPath,omitempty"`
	TemporaryPath       string         `json:"temporaryPath"`
	BackupPath          string         `json:"backupPath,omitempty"`
	CleanupPaths        []string       `json:"cleanupPaths,omitempty"`
	BeforeHash          string         `json:"beforeHash,omitempty"`
	AfterHash           string         `json:"afterHash,omitempty"`
	SourceBeforeHash    string         `json:"sourceBeforeHash,omitempty"`
	SourceAfterHash     string         `json:"sourceAfterHash,omitempty"`
	SourceChanges       []SourceChange `json:"sourceChanges,omitempty"`
	OutputIdentity      string         `json:"outputIdentity,omitempty"`
	OutputAfterIdentity string         `json:"outputAfterIdentity,omitempty"`
	OutputBeforeHash    string         `json:"outputBeforeHash,omitempty"`
	OutputAfterHash     string         `json:"outputAfterHash,omitempty"`
	AfterFingerprint    string         `json:"afterFingerprint,omitempty"`
	Integrity           string         `json:"integrity"`
}

// Lease is an exclusive workspace lock. Release is idempotent.
type Lease struct {
	release func() error
	done    bool
}

func (l *Lease) Release() error {
	if l == nil || l.done {
		return nil
	}
	l.done = true
	return l.release()
}

// WithLock runs fn while holding the Vault workspace lock. It never attempts
// recovery automatically: an unfinished journal is a blocking condition.
func WithLock(vault string, fn func() error) error {
	if fn == nil {
		return errors.New("workspace callback is nil")
	}
	lease, err := Acquire(vault)
	if err != nil {
		return err
	}
	defer func() { _ = lease.Release() }()
	return fn()
}

// Acquire creates the OXPIO control directory and obtains its exclusive lock.
func Acquire(vault string) (*Lease, error) {
	resolved, err := internalfsutil.ResolveVaultPath(vault)
	if err != nil {
		return nil, err
	}
	if err := ensureControlDir(resolved); err != nil {
		return nil, err
	}
	leaseRelease, err := acquireFileLock(filepath.Join(resolved, ".oxpio", "lock"), true)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return nil, &codedError{Code: "BUSY", Err: err}
		}
		return nil, err
	}
	lease := &Lease{release: leaseRelease}
	if err := checkJournalsLocked(resolved); err != nil {
		_ = lease.Release()
		return nil, err
	}
	return lease, nil
}

// AcquireForRecovery obtains the workspace lock without treating the active
// journal as a reason to refuse the lock. Only the recovery owner may use it.
func AcquireForRecovery(vault string) (*Lease, error) {
	resolved, err := internalfsutil.ResolveVaultPath(vault)
	if err != nil {
		return nil, err
	}
	if err := ensureControlDir(resolved); err != nil {
		return nil, err
	}
	if err := inspectTransactionDir(resolved); err != nil {
		return nil, &codedError{Code: "RECOVERY_ERROR", Err: err}
	}
	release, err := acquireFileLock(filepath.Join(resolved, ".oxpio", "lock"), true)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return nil, &codedError{Code: "BUSY", Err: err}
		}
		return nil, err
	}
	return &Lease{release: release}, nil
}

// Check is the read-only recovery preflight used by validate. Missing control
// state is clean. It never creates a directory, lock, journal, or marker.
func Check(vault string) error {
	resolved, err := internalfsutil.ResolveVaultPath(vault)
	if err != nil {
		return err
	}
	if err := CheckLock(resolved); err != nil {
		return err
	}
	return checkJournals(resolved)
}

// CheckDryRun inspects recovery state without acquiring or creating a lock.
// The caller must perform its own start/end fingerprint checks because this is
// intentionally not a linearizable workspace snapshot.
func CheckDryRun(vault string) error {
	resolved, err := internalfsutil.ResolveVaultPath(vault)
	if err != nil {
		return err
	}
	return checkJournals(resolved)
}

// CheckLock probes the existing workspace lock without retaining it. It is
// used by validate and recovery paths that require active-lock detection.
func CheckLock(vault string) error {
	resolved, err := internalfsutil.ResolveVaultPath(vault)
	if err != nil {
		return err
	}
	lockPath := filepath.Join(resolved, ".oxpio", "lock")
	if _, err := os.Lstat(lockPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect workspace lock: %w", err)
	}
	release, lockErr := acquireFileLock(lockPath, false)
	if lockErr != nil {
		if errors.Is(lockErr, ErrBusy) {
			return &codedError{Code: "BUSY", Err: lockErr}
		}
		return &codedError{Code: "RECOVERY_ERROR", Err: lockErr}
	}
	if releaseErr := release(); releaseErr != nil {
		return &codedError{Code: "RECOVERY_ERROR", Err: releaseErr}
	}
	return nil
}

func ensureControlDir(vault string) error {
	control := filepath.Join(vault, ".oxpio")
	info, err := os.Lstat(control)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(control, 0o700); err != nil {
			return fmt.Errorf("create OXPIO control directory: %w", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf(".oxpio must be a regular directory")
	}
	return nil
}

func checkJournals(vault string) error {
	if err := inspectControlDir(vault); err != nil {
		return err
	}
	transactions := filepath.Join(vault, ".oxpio", "transactions")
	info, err := os.Lstat(transactions)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return &codedError{Code: "RECOVERY_ERROR", Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return &codedError{Code: "RECOVERY_ERROR", Err: errors.New("transaction directory is not a regular directory")}
	}
	return checkJournalsLocked(vault)
}

func checkJournalsLocked(vault string) error {
	transactions := filepath.Join(vault, ".oxpio", "transactions")
	info, statErr := os.Lstat(transactions)
	if errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return &codedError{Code: "RECOVERY_ERROR", Err: errors.New("transaction directory is not a regular directory")}
	}
	entries, err := os.ReadDir(transactions)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return &codedError{Code: "RECOVERY_ERROR", Err: err}
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if entry.IsDir() {
			return &codedError{Code: "RECOVERY_ERROR", Err: fmt.Errorf("invalid transaction journal %q", entry.Name())}
		}
		path := filepath.Join(transactions, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return &codedError{Code: "RECOVERY_ERROR", Err: fmt.Errorf("invalid transaction journal %q", entry.Name())}
		}
		journal, readErr := readJournal(path)
		if readErr != nil {
			return readErr
		}
		return &codedError{Code: "RECOVERY_ERROR", Err: fmt.Errorf("unfinished transaction journal %q at phase %s", entry.Name(), journal.Phase)}
	}
	return nil
}

func journalPath(vault string) string {
	return filepath.Join(vault, ".oxpio", "transactions", journalName)
}

// WriteJournal atomically records a transaction phase. The caller must hold
// the workspace lock for durable writes.
func WriteJournal(vault string, journal Journal) error {
	if journal.Version == 0 {
		journal.Version = JournalVersion
	}
	if err := validateJournal(journal, false); err != nil {
		return err
	}
	journal.Integrity = journalDigest(journal)
	if err := ensureControlDir(vault); err != nil {
		return err
	}
	if err := ensureTransactionDir(vault); err != nil {
		return fmt.Errorf("create transaction directory: %w", err)
	}
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return AtomicWriteFile(journalPath(vault), append(data, '\n'), 0o600)
}

// ClearJournal removes the active journal without touching any other product
// file. It is used only after a transaction has reached a known state.
func ClearJournal(vault string) error {
	if err := inspectTransactionDir(vault); err != nil {
		return err
	}
	path := journalPath(vault)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("transaction journal is not regular")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return SyncDirectory(filepath.Dir(path))
}

// RecordCompleted records source-only paths whose resulting filesystem event
// may be consumed by an active watch without triggering a formal publish.
func RecordCompleted(vault string, paths []CompletedPath) error {
	return updateCompleted(vault, paths, false)
}

// ReplaceCompleted replaces pending records for the supplied paths. It is used
// when a source/output transaction rolls its source back so a watch suppresses
// the rollback event rather than publishing a second, stale output.
func ReplaceCompleted(vault string, paths []CompletedPath) error {
	return updateCompleted(vault, paths, true)
}

func updateCompleted(vault string, paths []CompletedPath, replace bool) error {
	resolved, err := internalfsutil.ResolveVaultPath(vault)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	if err := ensureControlDir(resolved); err != nil {
		return err
	}
	return withCompletedLock(resolved, func() error {
		marker := filepath.Join(resolved, ".oxpio", completedName)
		var existing []CompletedPath
		data, readErr := readCompletedMarker(marker)
		if readErr == nil {
			if err := json.Unmarshal(data, &existing); err != nil {
				return fmt.Errorf("read completed transaction marker: %w", err)
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		validated := make([]CompletedPath, 0, len(paths))
		replaced := make(map[string]struct{}, len(paths))
		for _, item := range paths {
			cleanPath := filepath.Clean(item.Path)
			if !internalfsutil.PathWithinRoot(resolved, cleanPath) || strings.TrimSpace(item.AfterHash) == "" {
				return errors.New("completed transaction path is outside the Vault or has no after hash")
			}
			if _, seen := replaced[cleanPath]; seen {
				continue
			}
			replaced[cleanPath] = struct{}{}
			validated = append(validated, CompletedPath{Path: cleanPath, AfterHash: item.AfterHash})
		}
		if replace {
			kept := existing[:0]
			for _, prior := range existing {
				if _, replacedPath := replaced[filepath.Clean(prior.Path)]; !replacedPath {
					kept = append(kept, prior)
				}
			}
			existing = kept
		}
		for _, item := range validated {
			duplicate := false
			for _, prior := range existing {
				if filepath.Clean(prior.Path) == item.Path && prior.AfterHash == item.AfterHash {
					duplicate = true
					break
				}
			}
			if !duplicate {
				existing = append(existing, item)
			}
		}
		return writeCompletedRecords(marker, existing)
	})
}

// RemoveCompleted removes pending suppression records for paths that were
// prepared but never committed.
func RemoveCompleted(vault string, paths []CompletedPath) error {
	resolved, err := internalfsutil.ResolveVaultPath(vault)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	marker := filepath.Join(resolved, ".oxpio", completedName)
	if _, err := os.Lstat(marker); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return withCompletedLock(resolved, func() error {
		data, err := readCompletedMarker(marker)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var existing []CompletedPath
		if err := json.Unmarshal(data, &existing); err != nil {
			return fmt.Errorf("read completed transaction marker: %w", err)
		}
		remove := make(map[string]struct{}, len(paths))
		for _, item := range paths {
			cleanPath := filepath.Clean(item.Path)
			if !internalfsutil.PathWithinRoot(resolved, cleanPath) {
				return errors.New("completed transaction path is outside the Vault")
			}
			remove[cleanPath] = struct{}{}
		}
		remaining := existing[:0]
		for _, item := range existing {
			if _, ok := remove[filepath.Clean(item.Path)]; !ok {
				remaining = append(remaining, item)
			}
		}
		return writeCompletedRecords(marker, remaining)
	})
}

// ConsumeCompleted reports a matching source-only watch event and retains the
// record until the watch debounce window is cleared.
func ConsumeCompleted(vault, eventPath string) (bool, error) {
	lease, err := AcquireForRecovery(vault)
	if err != nil {
		return false, err
	}
	defer func() { _ = lease.Release() }()
	ready, err := completedSuppressionReady(vault)
	if err != nil || !ready {
		return false, err
	}
	resolved, err := internalfsutil.ResolveVaultPath(vault)
	if err != nil {
		return false, err
	}
	marker := filepath.Join(resolved, ".oxpio", completedName)
	if _, err := os.Lstat(marker); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var suppressed bool
	err = withCompletedLock(resolved, func() error {
		data, err := readCompletedMarker(marker)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		var records []CompletedPath
		if err := json.Unmarshal(data, &records); err != nil {
			return fmt.Errorf("read completed transaction marker: %w", err)
		}
		cleanEvent := filepath.Clean(eventPath)
		currentHash := pathHash(cleanEvent)
		remaining := records[:0]
		for _, record := range records {
			if filepath.Clean(record.Path) != cleanEvent {
				remaining = append(remaining, record)
				continue
			}
			if currentHash == record.AfterHash {
				// Keep matching records until the watch debounce window drains;
				// atomic writes can produce more than one fsnotify event.
				suppressed = true
				remaining = append(remaining, record)
			}
			// A different hash is an external event, so discard only the stale
			// record and allow this event through.
		}
		return writeCompletedRecords(marker, remaining)
	})
	return suppressed, err
}

// ClearCompleted removes stale source-only watch suppression records when a
// watch process starts or its debounce window drains. It never touches the
// product or transaction journal.
func readCompletedMarker(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("completed transaction marker is not regular")
	}
	return os.ReadFile(path)
}

func ClearCompleted(vault string) error {
	lease, err := AcquireForRecovery(vault)
	if err != nil {
		return err
	}
	defer func() { _ = lease.Release() }()
	if blocked, err := completedClearBlocked(vault); err != nil {
		return err
	} else if blocked {
		return nil
	}
	resolved, err := internalfsutil.ResolveVaultPath(vault)
	if err != nil {
		return err
	}
	marker := filepath.Join(resolved, ".oxpio", completedName)
	if _, err := os.Lstat(marker); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return withCompletedLock(resolved, func() error {
		if blocked, journalErr := completedClearBlocked(vault); journalErr != nil {
			return journalErr
		} else if blocked {
			return nil
		}
		if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

func completedSuppressionReady(vault string) (bool, error) {
	journal, err := LoadJournal(vault)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if journal.CommitKind == "source-only" || journal.CommitKind == "source-batch" {
		// Source-only commits never publish output. A matching after-hash marker
		// is therefore safe to consume even while its journal is being finalized.
		return true, nil
	}
	switch journal.Phase {
	case "output-committed", "completed":
		return true, nil
	default:
		// A source/output transaction is still before its formal output commit;
		// do not suppress its source event while output may be stale.
		return false, nil
	}
}

func completedClearBlocked(vault string) (bool, error) {
	journal, err := LoadJournal(vault)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if journal.CommitKind == "source-only" || journal.CommitKind == "source-batch" {
		return journal.Phase == "prepared", nil
	}
	return journal.Phase != "output-committed" && journal.Phase != "completed", nil
}

func withCompletedLock(vault string, fn func() error) error {
	lockPath := filepath.Join(vault, ".oxpio", completedLockName)
	release, err := acquireFileLock(lockPath, true)
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	return fn()
}

func writeCompletedRecords(marker string, records []CompletedPath) error {
	if len(records) == 0 {
		if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return AtomicWriteFile(marker, append(encoded, '\n'), 0o600)
}

func pathHash(path string) string {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent"
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return "unavailable"
	}
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			return "unavailable"
		}
		return hashBytes(nil)
	}
	if !info.Mode().IsRegular() {
		return "unavailable"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "unavailable"
	}
	return hashBytes(data)
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// LoadJournal reads the sole active journal while refusing symlinked state.
func LoadJournal(vault string) (Journal, error) {
	if err := inspectTransactionDir(vault); err != nil {
		return Journal{}, &codedError{Code: "RECOVERY_ERROR", Err: err}
	}
	path := journalPath(vault)
	if _, err := os.Lstat(path); err != nil {
		return Journal{}, err
	}
	journal, err := readJournal(path)
	if err != nil {
		return Journal{}, err
	}
	return journal, nil
}

func readJournal(path string) (Journal, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Journal{}, &codedError{Code: "RECOVERY_ERROR", Err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Journal{}, &codedError{Code: "RECOVERY_ERROR", Err: errors.New("transaction journal is not regular")}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Journal{}, &codedError{Code: "RECOVERY_ERROR", Err: err}
	}
	var journal Journal
	if err := json.Unmarshal(data, &journal); err != nil {
		return Journal{}, &codedError{Code: "RECOVERY_ERROR", Err: err}
	}
	if err := validateJournal(journal, true); err != nil {
		return Journal{}, &codedError{Code: "RECOVERY_ERROR", Err: err}
	}
	return journal, nil
}

func validateJournal(journal Journal, requireIntegrity bool) error {
	if journal.Version != JournalVersion || journal.CommitKind == "" || !validJournalPhases[journal.Phase] {
		return errors.New("unsupported transaction journal")
	}
	if journal.CommitKind != "source-only" && journal.CommitKind != "source-batch" && journal.CommitKind != "source+output" {
		return errors.New("unsupported transaction commit kind")
	}
	if requireIntegrity && journal.Integrity != journalDigest(journal) {
		return errors.New("transaction journal integrity mismatch")
	}
	return nil
}

func journalDigest(journal Journal) string {
	journal.Integrity = ""
	data, _ := json.Marshal(journal)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func inspectTransactionDir(vault string) error {
	transactions := filepath.Join(vault, ".oxpio", "transactions")
	if err := internalfsutil.EnsureNoSymlinkComponents(transactions); err != nil {
		return err
	}
	info, err := os.Lstat(transactions)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("transaction directory is not a regular directory")
	}
	return nil
}

func ensureTransactionDir(vault string) error {
	transactions := filepath.Join(vault, ".oxpio", "transactions")
	info, err := os.Lstat(transactions)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("transaction directory is not a regular directory")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(transactions, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return inspectTransactionDir(vault)
}

func inspectControlDir(vault string) error {
	info, err := os.Lstat(filepath.Join(vault, ".oxpio"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		if err == nil {
			err = errors.New("OXPIO control directory is not regular")
		}
		return &codedError{Code: "RECOVERY_ERROR", Err: err}
	}
	return nil
}

// AcquireFileLock is used for the short-lived lock belonging to an external
// output identity. create=false is read-only and never creates the lock file.
func AcquireFileLock(path string, create bool) (func() error, error) {
	return acquireFileLock(path, create)
}

// Fingerprint returns a deterministic digest of source/config and the selected
// theme inputs. Generated output and unknown .oxpio files are excluded.
func Fingerprint(vault, output string) (string, error) {
	return FingerprintWithOverlay(vault, output, nil, nil)
}

// FingerprintWithOverlay fingerprints the same inputs as Fingerprint while
// substituting candidate source bytes for a source/output transaction.
func FingerprintWithOverlay(vault, output string, overlay map[string][]byte, deleted map[string]bool) (string, error) {
	root, err := internalfsutil.ResolveVaultPath(vault)
	if err != nil {
		return "", err
	}
	boundary, err := internalfsutil.ResolveVaultOutput(root, output)
	if err != nil {
		return "", err
	}
	outputResolved := boundary.OutputPath
	selectedTheme := ""
	if configBytes, ok := overlay[internalconfig.Filename]; ok {
		if config, configErr := internalconfig.ValidateConfigBytes(configBytes); configErr == nil {
			selectedTheme = config.Theme
		}
	}
	if selectedTheme == "" {
		if config, configErr := internalconfig.LoadForThemePreview(root, outputResolved); configErr == nil {
			selectedTheme = config.Theme
		}
	}
	if selectedTheme == "" {
		selectedTheme = internalconfig.ThemeBaseID
	}
	type item struct {
		Path string `json:"path"`
		Hash string `json:"hash"`
	}
	itemsByPath := make(map[string]item, 32)
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel := ""
		if path != root {
			var relErr error
			rel, relErr = filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
		}
		if path == root {
			return nil
		}
		baseLower := strings.ToLower(filepath.Base(path))
		relLower := strings.ToLower(rel)
		if strings.HasPrefix(baseLower, ".oxpio-") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path != root && entry.IsDir() {
			switch {
			case internalfsutil.PathWithinRoot(outputResolved, path):
				return fs.SkipDir
			case strings.HasPrefix(baseLower, ".oxpio-publish-stage-") || strings.HasPrefix(baseLower, ".oxpio-edit-stage-") || strings.HasPrefix(baseLower, ".oxpio-file-stage-") || strings.HasPrefix(baseLower, ".oxpio-source-txn-"):
				return fs.SkipDir
			case relLower == ".obsidian" || relLower == ".oxpio/cache" || relLower == ".oxpio/staging" || relLower == ".oxpio/transactions":
				return fs.SkipDir
			case relLower == ".oxpio/themes" && strings.EqualFold(selectedTheme, internalconfig.ThemeBaseID):
				return fs.SkipDir
			case strings.HasPrefix(relLower, ".oxpio/themes/") && selectedTheme != "" && !strings.EqualFold(selectedTheme, internalconfig.ThemeBaseID) && relLower != strings.ToLower(".oxpio/themes/"+selectedTheme) && !strings.HasPrefix(relLower, strings.ToLower(".oxpio/themes/"+selectedTheme+"/")):
				return fs.SkipDir
			case strings.HasPrefix(relLower, ".oxpio/") && relLower != ".oxpio/themes" && !strings.HasPrefix(relLower, ".oxpio/themes/"):
				return fs.SkipDir
			}
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasPrefix(relLower, ".oxpio/themes/") && selectedTheme != "" && (strings.EqualFold(selectedTheme, internalconfig.ThemeBaseID) || !strings.HasPrefix(relLower, strings.ToLower(".oxpio/themes/"+selectedTheme+"/"))) {
			return nil
		}
		if strings.HasPrefix(relLower, ".oxpio/") && !strings.HasPrefix(relLower, ".oxpio/themes/") {
			return nil
		}
		if !entry.Type().IsRegular() {
			if entry.Type()&fs.ModeSymlink != 0 {
				return fmt.Errorf("fingerprint path %q must not be a symbolic link", rel)
			}
			return fmt.Errorf("fingerprint path %q must be a regular file", rel)
		}
		if deleted[rel] {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if candidate, ok := overlay[rel]; ok {
			data = candidate
		}
		hash := sha256.Sum256(data)
		itemsByPath[rel] = item{Path: rel, Hash: hex.EncodeToString(hash[:])}
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}
	for rel, data := range overlay {
		if deleted[rel] {
			continue
		}
		if _, exists := itemsByPath[rel]; exists {
			continue
		}
		hash := sha256.Sum256(data)
		itemsByPath[filepath.ToSlash(rel)] = item{Path: filepath.ToSlash(rel), Hash: hex.EncodeToString(hash[:])}
	}
	items := make([]item, 0, len(itemsByPath))
	for _, value := range itemsByPath {
		items = append(items, value)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	encoded, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// AtomicWriteFile writes, flushes, and replaces a regular file in one
// directory. It is shared by journals and source/config mutation adapters.
func AtomicWriteFile(name string, data []byte, mode os.FileMode) error {
	parent := filepath.Dir(name)
	if err := internalfsutil.EnsureNoSymlinkComponents(parent); err != nil {
		return fmt.Errorf("atomic file parent contains a symbolic link: %w", err)
	}
	info, err := os.Lstat(parent)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("atomic file parent is not a regular directory: %w", err)
	}
	temporary, err := os.CreateTemp(parent, ".oxpio-atomic-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
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
	if err := replaceFile(temporaryName, name); err != nil {
		return err
	}
	return SyncDirectory(parent)
}

// AtomicCreateFile writes a new regular file without replacing an existing
// destination. The temporary file and destination share a directory so the
// final no-replace rename is atomic on supported local filesystems.
func AtomicCreateFile(name string, data []byte, mode os.FileMode) error {
	parent := filepath.Dir(name)
	if err := internalfsutil.EnsureNoSymlinkComponents(parent); err != nil {
		return fmt.Errorf("atomic file parent contains a symbolic link: %w", err)
	}
	info, err := os.Lstat(parent)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("atomic file parent is not a regular directory: %w", err)
	}
	temporary, err := os.CreateTemp(parent, ".oxpio-create-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
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
	if err := internalfsutil.RenameNoReplace(temporaryName, name); err != nil {
		return err
	}
	return SyncDirectory(parent)
}

// AtomicWriteFileCAS replaces a regular file only while its inode and bytes
// still match expectedHash. It preserves an external replacement instead of
// overwriting it between the final check and the atomic swap.
func AtomicWriteFileCAS(name, expectedHash string, data []byte, mode os.FileMode) error {
	if expectedHash == "absent" {
		return AtomicCreateFile(name, data, mode)
	}
	parent := filepath.Dir(name)
	if err := ensureAtomicFileParent(parent); err != nil {
		return err
	}
	snapshot, err := createCASSnapshot(name, parent, expectedHash)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(snapshot) }()
	candidate, err := createCASContent(parent, data, mode)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(candidate) }()
	if err := verifyCASSnapshot(snapshot, name, expectedHash); err != nil {
		return err
	}
	displaced, err := moveCASSource(name, parent)
	if err != nil {
		return err
	}
	defer func() {
		if displaced != "" {
			_ = os.Remove(displaced)
		}
	}()
	if err := verifyCASSnapshot(snapshot, displaced, expectedHash); err != nil {
		if restoreErr := restoreCASSource(displaced, name); restoreErr == nil {
			displaced = ""
			return errors.Join(ErrCASConflict, err)
		} else {
			return errors.Join(ErrCASConflict, err, restoreErr)
		}
	}
	if _, err := os.Lstat(name); err == nil {
		if restoreErr := restoreCASSource(displaced, name); restoreErr == nil {
			displaced = ""
			return ErrCASConflict
		} else {
			return errors.Join(ErrCASConflict, restoreErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Link(candidate, name); err != nil {
		if errors.Is(err, os.ErrExist) {
			if removeErr := os.Remove(displaced); removeErr == nil {
				displaced = ""
				return ErrCASConflict
			}
		}
		if restoreErr := restoreCASSource(displaced, name); restoreErr == nil {
			displaced = ""
		}
		return err
	}
	candidateHash := hashBytes(data)
	if err := verifyCASFile(candidate, candidateHash); err != nil {
		return errors.Join(ErrCASConflict, err)
	}
	candidateInfo, candidateErr := os.Stat(candidate)
	nameInfo, nameErr := os.Stat(name)
	if candidateErr != nil || nameErr != nil || !os.SameFile(candidateInfo, nameInfo) {
		return ErrCASConflict
	}
	if err := os.Remove(displaced); err != nil {
		return err
	}
	displaced = ""
	return SyncDirectory(parent)
}

// RenameFileCAS moves a regular file only while its source inode and bytes
// still match expectedHash, and never replaces an existing destination.
func RenameFileCAS(source, destination, expectedHash string) error {
	if err := ensureAtomicFileParent(filepath.Dir(source)); err != nil {
		return err
	}
	if err := ensureAtomicFileParent(filepath.Dir(destination)); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); err == nil {
		return ErrCASConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	snapshot, err := createCASSnapshot(source, filepath.Dir(source), expectedHash)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(snapshot) }()
	displaced, err := moveCASSource(source, filepath.Dir(source))
	if err != nil {
		return err
	}
	defer func() {
		if displaced != "" {
			_ = os.Remove(displaced)
		}
	}()
	if err := verifyCASSnapshot(snapshot, displaced, expectedHash); err != nil {
		if restoreErr := restoreCASSource(displaced, source); restoreErr == nil {
			displaced = ""
			return errors.Join(ErrCASConflict, err)
		} else {
			return errors.Join(ErrCASConflict, err, restoreErr)
		}
	}
	if _, err := os.Lstat(source); err == nil {
		if restoreErr := restoreCASSource(displaced, source); restoreErr == nil {
			displaced = ""
			return ErrCASConflict
		} else {
			return errors.Join(ErrCASConflict, restoreErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := internalfsutil.RenameNoReplace(displaced, destination); err != nil {
		if restoreErr := restoreCASSource(displaced, source); restoreErr == nil {
			displaced = ""
			return errors.Join(ErrCASConflict, err)
		} else {
			return errors.Join(ErrCASConflict, err, restoreErr)
		}
	}
	if err := verifyCASSnapshot(snapshot, destination, expectedHash); err != nil {
		return errors.Join(ErrCASConflict, err)
	}
	return errors.Join(SyncDirectory(filepath.Dir(source)), SyncDirectory(filepath.Dir(destination)))
}

// RemoveFileCAS deletes a regular file only while its inode and bytes still
// match expectedHash. An external replacement is left untouched.
func RemoveFileCAS(name, expectedHash string) error {
	if err := ensureAtomicFileParent(filepath.Dir(name)); err != nil {
		return err
	}
	snapshot, err := createCASSnapshot(name, filepath.Dir(name), expectedHash)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(snapshot) }()
	displaced, err := moveCASSource(name, filepath.Dir(name))
	if err != nil {
		return err
	}
	defer func() {
		if displaced != "" {
			_ = os.Remove(displaced)
		}
	}()
	if err := verifyCASSnapshot(snapshot, displaced, expectedHash); err != nil {
		if restoreErr := restoreCASSource(displaced, name); restoreErr == nil {
			displaced = ""
			return errors.Join(ErrCASConflict, err)
		} else {
			return errors.Join(ErrCASConflict, err, restoreErr)
		}
	}
	if _, err := os.Lstat(name); err == nil {
		if restoreErr := restoreCASSource(displaced, name); restoreErr == nil {
			displaced = ""
			return ErrCASConflict
		} else {
			return errors.Join(ErrCASConflict, restoreErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(displaced); err != nil {
		if restoreErr := restoreCASSource(displaced, name); restoreErr == nil {
			displaced = ""
			return err
		} else {
			return errors.Join(err, restoreErr)
		}
	}
	return SyncDirectory(filepath.Dir(name))
}

func ensureAtomicFileParent(parent string) error {
	if err := internalfsutil.EnsureNoSymlinkComponents(parent); err != nil {
		return fmt.Errorf("atomic file parent contains a symbolic link: %w", err)
	}
	info, err := os.Lstat(parent)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("atomic file parent is not a regular directory: %w", err)
	}
	return nil
}

func createCASSnapshot(name, parent, expectedHash string) (string, error) {
	if err := verifyCASFile(name, expectedHash); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(parent, ".oxpio-cas-*")
	if err != nil {
		return "", err
	}
	snapshot := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(snapshot)
		return "", err
	}
	if err := os.Remove(snapshot); err != nil {
		return "", err
	}
	if err := os.Link(name, snapshot); err != nil {
		return "", err
	}
	if err := verifyCASSnapshot(snapshot, name, expectedHash); err != nil {
		_ = os.Remove(snapshot)
		return "", err
	}
	return snapshot, nil
}

func createCASContent(parent string, data []byte, mode os.FileMode) (string, error) {
	temporary, err := os.CreateTemp(parent, ".oxpio-candidate-*")
	if err != nil {
		return "", err
	}
	name := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(name)
	}
	if err := temporary.Chmod(mode); err != nil {
		cleanup()
		return "", err
	}
	if _, err := temporary.Write(data); err != nil {
		cleanup()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func moveCASSource(name, parent string) (string, error) {
	temporary, err := os.CreateTemp(parent, ".oxpio-displaced-*")
	if err != nil {
		return "", err
	}
	displaced := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(displaced)
		return "", err
	}
	if err := os.Remove(displaced); err != nil {
		return "", err
	}
	if err := os.Rename(name, displaced); err != nil {
		return "", err
	}
	return displaced, nil
}

func restoreCASSource(displaced, name string) error {
	if _, err := os.Lstat(name); err == nil {
		return ErrCASConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return internalfsutil.RenameNoReplace(displaced, name)
}

func verifyCASSnapshot(snapshot, name, expectedHash string) error {
	if err := verifyCASFile(name, expectedHash); err != nil {
		return err
	}
	snapshotInfo, snapshotErr := os.Stat(snapshot)
	nameInfo, nameErr := os.Stat(name)
	if snapshotErr != nil || nameErr != nil || !os.SameFile(snapshotInfo, nameInfo) {
		return ErrCASConflict
	}
	return nil
}

func verifyCASFile(name, expectedHash string) error {
	info, err := os.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrCASConflict
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrCASConflict
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	if hashBytes(data) != expectedHash {
		return ErrCASConflict
	}
	return nil
}

// SyncDirectory flushes directory entry changes on supported filesystems.
func SyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
