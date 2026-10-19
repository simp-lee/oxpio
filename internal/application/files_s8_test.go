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

func TestMutateDirectoryAllowsPublicOutsideCustomOutput(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "site")
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	if _, err := MutateDirectory(vault, output, DirectoryMutationRequest{Operation: MutationMkdir, Path: "public", ExpectedHash: AbsentHash}); err != nil {
		t.Fatalf("mkdir public outside custom output: %v", err)
	}
	if info, err := os.Stat(filepath.Join(vault, "public")); err != nil || !info.IsDir() {
		t.Fatalf("created public directory = %v, info=%v", err, info)
	}
}

func TestMutateUploadBatchValidatesMutualReferencesBeforeCommit(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writePublishTestFile(t, vault, "docs/_index.md", "---\ntitle: Docs\npublish: true\n---\nDocs\n")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := MutateUploadBatch(vault, output, UploadBatchMutationRequest{Items: []UploadBatchItem{
		{Path: "docs/one.md", Kind: UploadArticle, Content: []byte("---\ntitle: One\npublish: true\ntype: doc\n---\n[Two](two.md)\n")},
		{Path: "docs/two.md", Kind: UploadArticle, Content: []byte("---\ntitle: Two\npublish: true\ntype: doc\n---\n[One](one.md)\n")},
	}})
	if err != nil {
		t.Fatalf("mutual-reference batch import = %v", err)
	}
	for _, name := range []string{"docs/one.md", "docs/two.md"} {
		if _, statErr := os.Stat(filepath.Join(vault, filepath.FromSlash(name))); statErr != nil {
			t.Fatalf("imported %s: %v", name, statErr)
		}
	}
}

func TestMutateUploadExplainsMissingAttachmentDirectory(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nattachments:\n  directory: attachments\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	_, err := MutateUpload(vault, "", UploadMutationRequest{Directory: "attachments", Name: "file.bin", Kind: UploadAttachment, ExpectedHash: AbsentHash, Content: []byte("data")})
	if err == nil || !strings.Contains(err.Error(), "oxpio mkdir attachments") {
		t.Fatalf("missing attachment directory error = %v, want initialization command", err)
	}
}

func TestMutateUploadExplainsMissingAttachmentConfiguration(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	_, err := MutateUpload(vault, "", UploadMutationRequest{Directory: "attachments", Name: "file.bin", Kind: UploadAttachment, ExpectedHash: AbsentHash, Content: []byte("data")})
	if err == nil || !strings.Contains(err.Error(), "attachments.directory is not configured") {
		t.Fatalf("missing attachment configuration error = %v", err)
	}
}

func TestMutateUploadRejectsCaseVariantAttachmentRoot(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nattachments:\n  directory: attachments\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	if err := os.MkdirAll(filepath.Join(vault, "attachments"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := MutateUpload(vault, "", UploadMutationRequest{Directory: "ATTACHMENTS", Name: "file.bin", Kind: UploadAttachment, ExpectedHash: AbsentHash, Content: []byte("data")})
	if err == nil || !strings.Contains(err.Error(), "inside attachments.directory") {
		t.Fatalf("case-variant attachment root error = %v", err)
	}
}

func TestMutateDirectoryRejectsCaseVariantAttachmentRoot(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nattachments:\n  directory: attachments\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	if err := os.MkdirAll(filepath.Join(vault, "attachments"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationMkdir, Path: "ATTACHMENTS", ExpectedHash: AbsentHash})
	if err == nil || !strings.Contains(err.Error(), "differs in case") {
		t.Fatalf("case-variant attachment root error = %v", err)
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

func TestMutateUploadRejectsPortableIdentityCollisionBeforeCommit(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nattachments:\n  directory: attachments\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writePublishTestFile(t, vault, "attachments/Photo.bin", "existing")
	if err := os.MkdirAll(filepath.Join(vault, "attachments"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := MutateUpload(vault, "", UploadMutationRequest{Directory: "attachments", Name: "photo.bin", Kind: UploadAttachment, ExpectedHash: AbsentHash, Content: []byte("new")})
	if err == nil || !strings.Contains(err.Error(), "collide on portable filesystems") {
		t.Fatalf("MutateUpload() error = %v, want actionable portable collision", err)
	}
	if _, statErr := os.Stat(filepath.Join(vault, "attachments", "photo.bin")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("colliding upload target exists: %v", statErr)
	}
}

func TestMutateDirectoryInitializesNestedAttachmentRoot(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nattachments:\n  directory: media/assets\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	for _, rel := range []string{"media", "media/assets"} {
		if _, err := MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationMkdir, Path: rel, ExpectedHash: AbsentHash}); err != nil {
			t.Fatalf("mkdir %s = %v", rel, err)
		}
	}
}

func TestMutateDirectoryRejectsCaseVariantNestedAttachmentAncestor(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nattachments:\n  directory: media/assets\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	if err := os.MkdirAll(filepath.Join(vault, "media", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationMkdir, Path: "MEDIA", ExpectedHash: AbsentHash})
	if err == nil || !strings.Contains(err.Error(), "differs in case") {
		t.Fatalf("case-variant attachment ancestor error = %v", err)
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
	if _, err := MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationMkdir, Path: "custom.css", ExpectedHash: AbsentHash}); err == nil {
		t.Fatal("reserved mkdir custom.css succeeded")
	}
	if _, err := MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationMkdir, Path: "attachments", ExpectedHash: AbsentHash}); err != nil {
		t.Fatalf("attachment root mkdir = %v", err)
	}
	if _, err := MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationMkdir, Path: "attachments/new", ExpectedHash: AbsentHash}); err != nil {
		t.Fatalf("attachment subdirectory mkdir = %v", err)
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
