package application

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/workspace"
)

func TestNormalizeAttachmentUploadNameLowercasesImageExtensions(t *testing.T) {
	for input, want := range map[string]string{"photo.PNG": "photo.png", "cover.JpEg": "cover.jpeg", "manual.PDF": "manual.PDF"} {
		if got := normalizeAttachmentUploadName(input); got != want {
			t.Fatalf("normalizeAttachmentUploadName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestMutateUploadRejectsNestedReservedManagementPaths(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nattachments:\n  directory: attachments\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	if err := os.MkdirAll(filepath.Join(vault, "attachments"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{"attachments/.oxpio", "attachments/.obsidian"} {
		if _, err := MutateUpload(vault, "", UploadMutationRequest{
			Directory: directory, Name: "file.bin", Kind: UploadAttachment, ExpectedHash: AbsentHash, Content: []byte("data"),
		}); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("MutateUpload(%q) error = %v, want reserved path error", directory, err)
		}
	}
	if _, err := MutateUpload(vault, "", UploadMutationRequest{
		Directory: "attachments", Name: ".oxpio", Kind: UploadAttachment, ExpectedHash: AbsentHash, Content: []byte("data"),
	}); err == nil || !strings.Contains(err.Error(), "safe path segment") {
		t.Fatalf("reserved upload basename error = %v", err)
	}
}

func TestMutateDirectoryRenameRejectsChangedDestinationPostcondition(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	if err := os.MkdirAll(filepath.Join(vault, "left", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := directoryHash(filepath.Join(vault, "left", "empty"))
	if err != nil {
		t.Fatal(err)
	}
	originalRename := renameDirectoryNoReplace
	renameDirectoryNoReplace = func(source, destination string) error {
		if err := internalfsutil.RenameNoReplace(source, destination); err != nil {
			return err
		}
		return os.Mkdir(filepath.Join(destination, "external"), 0o755)
	}
	defer func() { renameDirectoryNoReplace = originalRename }()

	_, err = MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationRenameDir, Path: "left/empty", Destination: "left/moved", ExpectedHash: before})
	if !errors.Is(err, workspace.ErrRecovery) {
		t.Fatalf("rename error = %v, want recovery error", err)
	}
	if _, statErr := os.Stat(filepath.Join(vault, "left", "empty")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("source directory state = %v, want absent", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(vault, "left", "moved", "external")); statErr != nil {
		t.Fatalf("destination postcondition fixture missing: %v", statErr)
	}
	if suppressed, consumeErr := workspace.ConsumeCompleted(vault, filepath.Join(vault, "left", "moved")); consumeErr != nil || suppressed {
		t.Fatalf("stale directory suppression = %v, %v; want false, nil", suppressed, consumeErr)
	}
}

func TestMutateUploadRejectsChangedTargetPostcondition(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nattachments:\n  directory: attachments\n")
	if err := os.MkdirAll(filepath.Join(vault, "attachments"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("uploaded")
	originalCreate := atomicCreateUploadFile
	atomicCreateUploadFile = func(name string, data []byte, mode os.FileMode) error {
		if err := workspace.AtomicCreateFile(name, data, mode); err != nil {
			return err
		}
		return os.WriteFile(name, []byte("external replacement"), mode)
	}
	defer func() { atomicCreateUploadFile = originalCreate }()

	_, err := MutateUpload(vault, "", UploadMutationRequest{Directory: "attachments", Name: "file.bin", Kind: UploadAttachment, ExpectedHash: AbsentHash, Content: content})
	if !errors.Is(err, workspace.ErrRecovery) {
		t.Fatalf("upload error = %v, want recovery error", err)
	}
	actual, readErr := os.ReadFile(filepath.Join(vault, "attachments", "file.bin"))
	if readErr != nil || string(actual) != "external replacement" {
		t.Fatalf("upload target = %q, %v; want external replacement", actual, readErr)
	}
	journal, journalErr := workspace.LoadJournal(vault)
	if journalErr != nil || journal.Phase != "prepared" || journal.AfterHash != hashBytes(content) {
		t.Fatalf("upload journal = %#v, %v; want prepared journal with expected after hash", journal, journalErr)
	}
}

func TestMutateDirectoryRequiresSameParentAndAbsentMkdirCAS(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nattachments:\n  directory: attachments\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	for _, rel := range []string{"left", "right", "left/empty"} {
		if err := os.MkdirAll(filepath.Join(vault, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationMkdir, Path: "new"}); err == nil {
		t.Fatal("mkdir without absent CAS succeeded")
	}
	for _, rel := range []string{"custom.css", "attachments/new"} {
		if _, err := MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationMkdir, Path: rel, ExpectedHash: AbsentHash}); err == nil {
			t.Fatalf("reserved mkdir %q succeeded", rel)
		}
	}
	before, err := directoryHash(filepath.Join(vault, "left", "empty"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationRenameDir, Path: "left/empty", Destination: "right/moved", ExpectedHash: before}); err == nil || !strings.Contains(err.Error(), "same parent") {
		t.Fatalf("cross-parent rename error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "left", "empty")); err != nil {
		t.Fatalf("source directory changed after rejected rename: %v", err)
	}
}
