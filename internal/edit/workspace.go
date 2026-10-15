package edit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const workspaceLockFilename = ".oxpio/lock"

// WithWorkspaceMutation serializes a persistent Agent mutation and records a
// short-lived transaction intent before the callback changes any product file.
// Dry-run callers must not invoke it.
func WithWorkspaceMutation(vault, operation string, paths []string, fn func() error) error {
	if fn == nil {
		return errors.New("workspace mutation callback is nil")
	}
	if err := ensureWorkspaceControlDir(vault); err != nil {
		return err
	}
	unlock, err := acquireWorkspaceLock(vault)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()

	transactions := filepath.Join(vault, ".oxpio", "transactions")
	if info, statErr := os.Lstat(transactions); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("workspace transaction directory must be a regular directory")
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		if err := os.Mkdir(transactions, 0o700); err != nil {
			return fmt.Errorf("create workspace transaction directory: %w", err)
		}
	} else {
		return statErr
	}
	sort.Strings(paths)
	intent := struct {
		Operation string   `json:"operation"`
		Paths     []string `json:"paths"`
		CreatedAt string   `json:"createdAt"`
	}{operation, append([]string(nil), paths...), time.Now().UTC().Format(time.RFC3339Nano)}
	data, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	journal := filepath.Join(transactions, "theme-"+hex.EncodeToString(digest[:8])+".json")
	if err := AtomicWriteFile(journal, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write workspace transaction: %w", err)
	}
	mutationErr := fn()
	removeErr := os.Remove(journal)
	if mutationErr != nil {
		return mutationErr
	}
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return fmt.Errorf("remove workspace transaction: %w", removeErr)
	}
	return nil
}

// AtomicWriteFile writes one regular file by same-directory temp-file replace.
func AtomicWriteFile(name string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(name)
	temporary, err := os.CreateTemp(directory, ".oxpio-atomic-*")
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
	return atomicReplace(temporaryName, name)
}

func ensureWorkspaceControlDir(vault string) error {
	info, err := os.Lstat(vault)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("vault must be a regular directory")
	}
	control := filepath.Join(vault, ".oxpio")
	info, err = os.Lstat(control)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(control, 0o700); err != nil {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf(".oxpio must be a regular directory")
	}
	return nil
}
