package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
