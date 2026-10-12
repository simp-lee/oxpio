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
	JournalVersion = 1
	journalName    = "current.json"
)

var validJournalPhases = map[string]bool{
	"prepared":         true,
	"source-committed": true,
	"output-committed": true,
	"completed":        true,
}

var (
	ErrBusy     = errors.New("WORKSPACE_BUSY")
	ErrRecovery = errors.New("RECOVERY_ERROR")
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
type Journal struct {
	Version          int    `json:"version"`
	CommitKind       string `json:"commitKind"`
	Phase            string `json:"phase"`
	InputFingerprint string `json:"inputFingerprint"`
	TargetPath       string `json:"targetPath"`
	SourcePath       string `json:"sourcePath,omitempty"`
	OutputPath       string `json:"outputPath,omitempty"`
	TemporaryPath    string `json:"temporaryPath"`
	BackupPath       string `json:"backupPath,omitempty"`
	BeforeHash       string `json:"beforeHash,omitempty"`
	AfterHash        string `json:"afterHash,omitempty"`
	SourceBeforeHash string `json:"sourceBeforeHash,omitempty"`
	SourceAfterHash  string `json:"sourceAfterHash,omitempty"`
	OutputIdentity   string `json:"outputIdentity,omitempty"`
	OutputBeforeHash string `json:"outputBeforeHash,omitempty"`
	OutputAfterHash  string `json:"outputAfterHash,omitempty"`
	Integrity        string `json:"integrity"`
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
	release, err := acquireFileLock(filepath.Join(resolved, ".oxpio", "lock"), true)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return nil, &codedError{Code: "BUSY", Err: err}
		}
		return nil, err
	}
	return &Lease{release: release}, nil
}

// Check is the read-only recovery preflight used by validate and dry-run.
// Missing control state is clean. It never creates a directory, lock, journal,
// or marker.
func Check(vault string) error {
	resolved, err := internalfsutil.ResolveVaultPath(vault)
	if err != nil {
		return err
	}
	if err := checkJournals(resolved); err != nil {
		return err
	}
	lockPath := filepath.Join(resolved, ".oxpio", "lock")
	if _, err := os.Lstat(lockPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect workspace lock: %w", err)
	}
	release, err := acquireFileLock(lockPath, false)
	if err != nil {
		if errors.Is(err, ErrBusy) {
			return &codedError{Code: "BUSY", Err: err}
		}
		return &codedError{Code: "RECOVERY_ERROR", Err: err}
	}
	return release()
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
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
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
	transactions := filepath.Dir(journalPath(vault))
	if err := os.MkdirAll(transactions, 0o700); err != nil {
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

// LoadJournal reads the sole active journal while refusing symlinked state.
func LoadJournal(vault string) (Journal, error) {
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
	if journal.CommitKind != "source-only" && journal.CommitKind != "source+output" {
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
	if config, configErr := internalconfig.LoadForThemePreview(root, outputResolved); configErr == nil {
		selectedTheme = config.Theme
		if selectedTheme == "" {
			selectedTheme = internalconfig.ThemeBaseID
		}
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
		if path != root && entry.IsDir() {
			switch {
			case internalfsutil.PathWithinRoot(outputResolved, path):
				return fs.SkipDir
			case strings.HasPrefix(filepath.Base(path), ".oxpio-publish-stage-") || strings.HasPrefix(filepath.Base(path), ".oxpio-edit-stage-") || strings.HasPrefix(filepath.Base(path), ".oxpio-file-stage-"):
				return fs.SkipDir
			case rel == ".obsidian" || rel == ".oxpio/cache" || rel == ".oxpio/staging" || rel == ".oxpio/transactions":
				return fs.SkipDir
			case rel == ".oxpio/themes" && selectedTheme == internalconfig.ThemeBaseID:
				return fs.SkipDir
			case strings.HasPrefix(rel, ".oxpio/themes/") && selectedTheme != "" && selectedTheme != internalconfig.ThemeBaseID && rel != ".oxpio/themes/"+selectedTheme && !strings.HasPrefix(rel, ".oxpio/themes/"+selectedTheme+"/"):
				return fs.SkipDir
			case strings.HasPrefix(rel, ".oxpio/") && rel != ".oxpio/themes" && !strings.HasPrefix(rel, ".oxpio/themes/"):
				return fs.SkipDir
			}
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if strings.HasPrefix(rel, ".oxpio/themes/") && selectedTheme != "" && (selectedTheme == internalconfig.ThemeBaseID || !strings.HasPrefix(rel, ".oxpio/themes/"+selectedTheme+"/")) {
			return nil
		}
		if strings.HasPrefix(rel, ".oxpio/") && !strings.HasPrefix(rel, ".oxpio/themes/") {
			return nil
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

// SyncDirectory flushes directory entry changes on supported filesystems.
func SyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
