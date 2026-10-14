package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProbeAtomicReplaceLocalFilesystem(t *testing.T) {
	parent := t.TempDir()
	err := ProbeAtomicReplace(parent)
	if runtime.GOOS == "linux" {
		if err != nil {
			t.Fatalf("ProbeAtomicReplace() error = %v, want supported local filesystem", err)
		}
		return
	}
	if !errors.Is(err, ErrAtomicityUnavailable) {
		t.Fatalf("ProbeAtomicReplace() error = %v, want OUTPUT_ATOMICITY_UNAVAILABLE", err)
	}
}

func TestCASFileMutationsPreserveExternalContent(t *testing.T) {
	root := t.TempDir()
	filename := filepath.Join(root, "article.md")
	if err := os.WriteFile(filename, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFileCAS(filename, hashBytes([]byte("original")), []byte("updated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte("external"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFileCAS(filename, hashBytes([]byte("updated")), []byte("lost"), 0o644); !errors.Is(err, ErrCASConflict) {
		t.Fatalf("AtomicWriteFileCAS() error = %v, want CAS conflict", err)
	}
	if data, err := os.ReadFile(filename); err != nil || string(data) != "external" {
		t.Fatalf("external source = %q, err=%v; want preserved bytes", data, err)
	}
	if err := RenameFileCAS(filename, filepath.Join(root, "moved.md"), hashBytes([]byte("external"))); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFileCAS(filepath.Join(root, "moved.md"), hashBytes([]byte("external"))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "moved.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("moved source stat error = %v, want absent", err)
	}
}

func TestJournalIntegrityAndRecoveryPreflight(t *testing.T) {
	vault := t.TempDir()
	journal := Journal{Version: JournalVersion, CommitKind: "source+output", Phase: "prepared", TargetPath: filepath.Join(vault, "public"), TemporaryPath: filepath.Join(vault, ".stage")}
	if err := WriteJournal(vault, journal); err != nil {
		t.Fatal(err)
	}
	if err := Check(vault); err == nil || !strings.Contains(err.Error(), "RECOVERY_ERROR") {
		t.Fatalf("Check() error = %v, want RECOVERY_ERROR", err)
	}
	loaded, err := LoadJournal(vault)
	if err != nil || loaded.Integrity == "" {
		t.Fatalf("LoadJournal() = %#v, %v; want sealed journal", loaded, err)
	}
	journalPath := filepath.Join(vault, ".oxpio", "transactions", journalName)
	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-2] ^= 1
	if err := os.WriteFile(journalPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Check(vault); err == nil || !strings.Contains(err.Error(), "RECOVERY_ERROR") {
		t.Fatalf("Check() after journal tamper = %v, want RECOVERY_ERROR", err)
	}
}

func TestCheckRefusesActiveWorkspaceLockWithoutWriting(t *testing.T) {
	vault := t.TempDir()
	lease, err := Acquire(vault)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release() }()
	if err := Check(vault); err == nil || !errors.Is(err, ErrBusy) && !strings.Contains(err.Error(), "BUSY") {
		t.Fatalf("Check() error = %v, want busy", err)
	}
}

func TestFingerprintTracksInputsButExcludesManagedState(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	writeWorkspaceTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\ntheme: alpha\nnavigation: []\n")
	writeWorkspaceTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeWorkspaceTestFile(t, vault, "custom.css", "body{}\n")
	writeWorkspaceTestFile(t, vault, ".oxpio/themes/alpha/theme.yaml", "apiVersion: 1\nname: alpha\n")
	writeWorkspaceTestFile(t, vault, ".oxpio/themes/beta/theme.yaml", "apiVersion: 1\nname: beta\n")
	writeWorkspaceTestFile(t, vault, ".obsidian/app.json", "ignored")
	writeWorkspaceTestFile(t, vault, "public/index.html", "generated")
	before, err := Fingerprint(vault, output)
	if err != nil {
		t.Fatal(err)
	}
	writeWorkspaceTestFile(t, vault, ".obsidian/app.json", "changed")
	writeWorkspaceTestFile(t, vault, "public/index.html", "changed")
	afterIgnored, err := Fingerprint(vault, output)
	if err != nil {
		t.Fatal(err)
	}
	if before != afterIgnored {
		t.Fatalf("fingerprint changed for ignored state: %s -> %s", before, afterIgnored)
	}
	writeWorkspaceTestFile(t, vault, ".oxpio/themes/beta/theme.yaml", "apiVersion: 1\nname: changed\n")
	afterUnselectedTheme, err := Fingerprint(vault, output)
	if err != nil {
		t.Fatal(err)
	}
	if afterIgnored != afterUnselectedTheme {
		t.Fatalf("fingerprint changed for unselected-theme input: %s -> %s", afterIgnored, afterUnselectedTheme)
	}
	writeWorkspaceTestFile(t, vault, ".oxpio/themes/alpha/theme.yaml", "apiVersion: 1\nname: changed\n")
	afterTheme, err := Fingerprint(vault, output)
	if err != nil {
		t.Fatal(err)
	}
	if before == afterTheme {
		t.Fatalf("fingerprint did not change for selected-theme input: %s -> %s", before, afterTheme)
	}
}

func writeWorkspaceTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
