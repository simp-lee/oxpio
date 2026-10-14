package publish

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/workspace"
)

func TestRecoverPreparedJournalOnlyCleansTemporaryState(t *testing.T) {
	vault := t.TempDir()
	temporary := filepath.Join(vault, ".stage")
	if err := os.MkdirAll(temporary, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(vault, "public")
	journal := workspace.Journal{Version: workspace.JournalVersion, CommitKind: "source+output", Phase: "prepared", InputFingerprint: "input", TargetPath: target, TemporaryPath: temporary, OutputBeforeHash: directoryHash(target), OutputIdentity: outputIdentity(target)}
	if err := workspace.WriteJournal(vault, journal); err != nil {
		t.Fatal(err)
	}
	result, err := Recover(vault, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Found || result.Action != "discard-prepared" || !result.Changed {
		t.Fatalf("recovery result = %#v, want prepared cleanup", result)
	}
	if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary state stat error = %v, want absent", err)
	}
	if err := workspace.Check(vault); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverSourceOnlyCommitDoesNotTouchOutput(t *testing.T) {
	vault := t.TempDir()
	source := filepath.Join(vault, "article.md")
	output := filepath.Join(vault, "public")
	if err := os.WriteFile(source, []byte("new source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "index.html"), []byte("old output"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("new source"))
	journal := workspace.Journal{Version: workspace.JournalVersion, CommitKind: "source-only", Phase: "source-committed", InputFingerprint: "input", TargetPath: source, AfterHash: fmt.Sprintf("%x", hash)}
	if err := workspace.WriteJournal(vault, journal); err != nil {
		t.Fatal(err)
	}
	result, err := Recover(vault, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "confirm-source-only" {
		t.Fatalf("recovery result = %#v, want source-only confirmation", result)
	}
	data, err := os.ReadFile(filepath.Join(output, "index.html"))
	if err != nil || string(data) != "old output" {
		t.Fatalf("output after source-only recovery = %q, %v", data, err)
	}
}

func TestRecoverSourceAndOutputCommitCompletesOnlyWithMatchingHashes(t *testing.T) {
	if err := workspace.ProbeAtomicReplace(t.TempDir()); errors.Is(err, workspace.ErrAtomicityUnavailable) {
		t.Skip("directory exchange is unavailable")
	} else if err != nil {
		t.Fatal(err)
	}
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "index.html"), []byte("old output"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(vault, "article.md")
	if err := os.WriteFile(source, []byte("new source"), 0o644); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(vault, ".oxpio-edit-stage-test", "public")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, ManagedMarkerFilename), []byte(ManagedMarkerContents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "index.html"), []byte("new output"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceHash := sha256.Sum256([]byte("new source"))
	inputFingerprint, err := workspace.Fingerprint(vault, output)
	if err != nil {
		t.Fatal(err)
	}
	journal := workspace.Journal{
		Version: workspace.JournalVersion, CommitKind: "source+output", Phase: "source-committed", InputFingerprint: inputFingerprint,
		TargetPath: output, SourcePath: source, SourceBeforePath: source, SourceBeforeHash: "absent", TemporaryPath: staging,
		SourceAfterHash: fmt.Sprintf("%x", sourceHash), OutputIdentity: outputIdentity(output), OutputBeforeHash: directoryHash(output), OutputAfterHash: directoryHash(staging), OutputAfterIdentity: outputIdentity(staging),
	}
	if err := workspace.WriteJournal(vault, journal); err != nil {
		t.Fatal(err)
	}
	result, err := Recover(vault, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "complete-output" || !result.Changed {
		t.Fatalf("recovery result = %#v, want completed output commit", result)
	}
	data, err := os.ReadFile(filepath.Join(output, "index.html"))
	if err != nil || string(data) != "new output" {
		t.Fatalf("output after recovery = %q, %v", data, err)
	}
	if _, err := workspace.LoadJournal(vault); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal after recovery = %v, want absent", err)
	}
}

func TestRecoverRejectsCorruptedEditorStaging(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "index.html"), []byte("old output"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(vault, "article.md")
	if err := os.WriteFile(source, []byte("new source"), 0o644); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(vault, ".oxpio-edit-stage-test", "public")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, ManagedMarkerFilename), []byte(ManagedMarkerContents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "index.html"), []byte("new output"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceHash := sha256.Sum256([]byte("new source"))
	inputFingerprint, err := workspace.Fingerprint(vault, output)
	if err != nil {
		t.Fatal(err)
	}
	journal := workspace.Journal{
		Version: workspace.JournalVersion, CommitKind: "source+output", Phase: "source-committed", InputFingerprint: inputFingerprint,
		TargetPath: output, SourcePath: source, SourceBeforePath: source, SourceBeforeHash: "absent", TemporaryPath: staging,
		SourceAfterHash: fmt.Sprintf("%x", sourceHash), OutputIdentity: outputIdentity(output), OutputBeforeHash: directoryHash(output), OutputAfterHash: directoryHash(staging), OutputAfterIdentity: outputIdentity(staging),
	}
	if err := workspace.WriteJournal(vault, journal); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "index.html"), []byte("tampered output"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(vault, false); !errors.Is(err, workspace.ErrRecovery) {
		t.Fatalf("Recover() error = %v, want recovery error", err)
	}
	data, err := os.ReadFile(filepath.Join(output, "index.html"))
	if err != nil || string(data) != "old output" {
		t.Fatalf("output after rejected recovery = %q, %v", data, err)
	}
	if _, err := workspace.LoadJournal(vault); err != nil {
		t.Fatalf("journal after rejected recovery = %v, want retained journal", err)
	}
}

func TestCommitRejectsOutputAndStagingChanges(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, ManagedMarkerFilename), []byte(ManagedMarkerContents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "index.html"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(vault, ".oxpio-edit-stage-test", "public")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, ManagedMarkerFilename), []byte(ManagedMarkerContents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "index.html"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	transaction, err := AdoptStaging(vault, output, staging, Options{Durable: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "index.html"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Commit(); err == nil {
		t.Fatal("Commit() accepted tampered staging")
	}
	if err := transaction.Abort(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "index.html"))
	if err != nil || string(data) != "old" {
		t.Fatalf("output after staging rejection = %q, %v", data, err)
	}

	staging = filepath.Join(vault, ".oxpio-edit-stage-output", "public")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, ManagedMarkerFilename), []byte(ManagedMarkerContents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "index.html"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	transaction, err = AdoptStaging(vault, output, staging, Options{Durable: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "index.html"), []byte("external"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Commit(); err == nil {
		t.Fatal("Commit() accepted changed output")
	}
	if err := transaction.Abort(); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(output, "index.html"))
	if err != nil || string(data) != "external" {
		t.Fatalf("output after output rejection = %q, %v", data, err)
	}
}

func TestRecoverAcceptsExternalNestedEditorStaging(t *testing.T) {
	vault := t.TempDir()
	outputRoot := t.TempDir()
	output := filepath.Join(outputRoot, "public")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "index.html"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(vault, "article.md")
	if err := os.WriteFile(source, []byte("new source"), 0o644); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(outputRoot, ".oxpio-edit-stage-external", "public")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, ManagedMarkerFilename), []byte(ManagedMarkerContents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "index.html"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceHash := sha256.Sum256([]byte("new source"))
	inputFingerprint, err := workspace.Fingerprint(vault, output)
	if err != nil {
		t.Fatal(err)
	}
	journal := workspace.Journal{
		Version: workspace.JournalVersion, CommitKind: "source+output", Phase: "source-committed", InputFingerprint: inputFingerprint,
		TargetPath: output, SourcePath: source, SourceBeforePath: source, SourceBeforeHash: "absent", TemporaryPath: staging,
		SourceAfterHash: fmt.Sprintf("%x", sourceHash), OutputIdentity: outputIdentity(output), OutputBeforeHash: directoryHash(output), OutputAfterHash: directoryHash(staging), OutputAfterIdentity: outputIdentity(staging),
	}
	if err := workspace.WriteJournal(vault, journal); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(vault, true); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(vault, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "index.html"))
	if err != nil || string(data) != "new" {
		t.Fatalf("external output after recovery = %q, %v", data, err)
	}
}

func TestCommitAtomicallyReplacesManagedOutputAndClearsJournal(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, ManagedMarkerFilename), []byte(ManagedMarkerContents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "index.html"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	transaction, err := Prepare(vault, output, Options{Durable: true, InputFingerprint: "input"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transaction.StagingPath(), "index.html"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("published output = %q, want new", data)
	}
	if err := workspace.Check(vault); err != nil {
		t.Fatalf("workspace after commit = %v, want clean", err)
	}
	if _, err := os.Stat(filepath.Join(vault, ".oxpio", "transactions", "current.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal stat error = %v, want absent", err)
	}
}

func TestCommitRetainsExpectedPostconditionWhenOutputChangesAfterSwap(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, ManagedMarkerFilename), []byte(ManagedMarkerContents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "index.html"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	transaction, err := Prepare(vault, output, Options{Durable: true, InputFingerprint: "input"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transaction.StagingPath(), "index.html"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	originalReplace := atomicReplaceDirectory
	atomicReplaceDirectory = func(source, target string) error {
		if err := workspace.AtomicReplaceDirectory(source, target); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(target, "post-swap-change"), []byte("external"), 0o644)
	}
	defer func() { atomicReplaceDirectory = originalReplace }()

	result, err := transaction.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.CleanupError == nil || !strings.Contains(result.CleanupError.Error(), "postcondition mismatch") {
		t.Fatalf("Commit() cleanup warning = %v, want postcondition mismatch", result.CleanupError)
	}
	journal, err := workspace.LoadJournal(vault)
	if err != nil {
		t.Fatal(err)
	}
	if journal.OutputAfterHash == directoryHash(output) {
		t.Fatalf("journal accepted observed output hash %q", journal.OutputAfterHash)
	}
	if _, err := Recover(vault, false); err == nil || !errors.Is(err, workspace.ErrRecovery) {
		t.Fatalf("Recover() error = %v, want RECOVERY_ERROR", err)
	}
}

func TestCommitInputRecheckLeavesOutputUnchanged(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, ManagedMarkerFilename), []byte(ManagedMarkerContents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "index.html"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	transaction, err := Prepare(vault, output, Options{Durable: true, ValidateInput: func() error {
		return errors.New("CONCURRENT_INPUT: changed")
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transaction.StagingPath(), "index.html"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Commit(); err == nil || !strings.Contains(err.Error(), "CONCURRENT_INPUT") {
		t.Fatalf("Commit() error = %v, want concurrent input", err)
	}
	if err := transaction.Abort(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("output after rejected commit = %q, want old", data)
	}
}

func TestDryRunPreparationDoesNotCreateDurableWorkspaceState(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	transaction, err := Prepare(vault, output, Options{Durable: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vault, ".oxpio")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("control directory stat error = %v, want absent", err)
	}
}
