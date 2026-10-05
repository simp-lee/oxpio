package edit

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	configLockHelperEnv        = "OXPIO_CONFIG_LOCK_HELPER"
	configLockHelperVaultEnv   = "OXPIO_CONFIG_HELPER_VAULT"
	configLockHelperReadyEnv   = "OXPIO_CONFIG_HELPER_READY"
	configLockHelperReleaseEnv = "OXPIO_CONFIG_HELPER_RELEASE"
)

func TestConfigLockRejectsSymlink(t *testing.T) {
	if !configLockSupportedOnTestPlatform() {
		t.Skipf("config lock is unavailable on %s", runtime.GOOS)
	}

	vault := t.TempDir()
	target := filepath.Join(vault, "lock-target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(vault, configLockFilename)); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := acquireConfigLock(vault); err == nil {
		t.Fatal("acquireConfigLock succeeded through a symbolic link")
	}
}

func TestAtomicReplaceConfigSerializesConcurrentModification(t *testing.T) {
	if !configLockSupportedOnTestPlatform() {
		t.Skipf("config lock is unavailable on %s", runtime.GOOS)
	}

	vault := t.TempDir()
	configPath := filepath.Join(vault, "oxpio.yaml")
	original := []byte("title: Original\n")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	readyPath := filepath.Join(vault, "helper-ready")
	releasePath := filepath.Join(vault, "helper-release")
	var helperOutput bytes.Buffer
	helper := exec.Command(os.Args[0], "-test.run=^TestConfigLockHelper$", "-test.count=1")
	helper.Env = append(os.Environ(),
		configLockHelperEnv+"=1",
		configLockHelperVaultEnv+"="+vault,
		configLockHelperReadyEnv+"="+readyPath,
		configLockHelperReleaseEnv+"="+releasePath,
	)
	helper.Stdout = &helperOutput
	helper.Stderr = &helperOutput
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	released := false
	waited := false
	defer func() {
		if !released {
			_ = os.WriteFile(releasePath, nil, 0o600)
		}
		if !waited {
			_ = helper.Wait()
		}
	}()

	waitForConfigLockMarker(t, readyPath)

	replaceDone := make(chan error, 1)
	go func() {
		replaceDone <- atomicReplaceConfig(vault, configPath, original, []byte("title: Updated\n"))
	}()
	select {
	case err := <-replaceDone:
		t.Fatalf("atomicReplaceConfig returned while another process held the config lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	released = true
	if err := <-replaceDone; err == nil || !strings.Contains(err.Error(), "config changed during edit setup") {
		t.Fatalf("atomicReplaceConfig error = %v, want config-changed conflict", err)
	}
	helperErr := helper.Wait()
	waited = true
	if helperErr != nil {
		t.Fatalf("lock helper failed: %v\n%s", helperErr, helperOutput.String())
	}
	if got, err := os.ReadFile(configPath); err != nil {
		t.Fatal(err)
	} else if string(got) != "title: External\n" {
		t.Fatalf("config after concurrent modification = %q, want external content", got)
	}
}

func TestConfigLockHelper(t *testing.T) {
	if os.Getenv(configLockHelperEnv) != "1" {
		return
	}

	vault := os.Getenv(configLockHelperVaultEnv)
	readyPath := os.Getenv(configLockHelperReadyEnv)
	releasePath := os.Getenv(configLockHelperReleaseEnv)
	if vault == "" || readyPath == "" || releasePath == "" {
		t.Fatal("config lock helper environment is incomplete")
	}
	unlock, err := acquireConfigLock(vault)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unlock() }()
	if err := os.WriteFile(filepath.Join(vault, "oxpio.yaml"), []byte("title: External\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := os.Stat(releasePath)
		if err == nil {
			return
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for config lock release")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func configLockSupportedOnTestPlatform() bool {
	switch runtime.GOOS {
	case "aix", "android", "darwin", "dragonfly", "freebsd", "illumos", "ios", "linux", "netbsd", "openbsd", "solaris", "windows":
		return true
	default:
		return false
	}
}

func waitForConfigLockMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat config lock marker: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for config lock marker %q", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
