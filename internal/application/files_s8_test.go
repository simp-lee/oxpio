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

func TestMutateDirectoryMkdirDryRunChecksSecureSupport(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")

	if _, err := MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationMkdir, Path: "planned", ExpectedHash: AbsentHash, DryRun: true}); err != nil {
		t.Fatalf("mkdir dry-run error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(vault, "planned")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run directory state = %v, want absent", err)
	}
}

func TestMutateDirectoryMkdirCreatesDirectory(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")

	if _, err := MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationMkdir, Path: "created", ExpectedHash: AbsentHash}); err != nil {
		t.Fatalf("mkdir error = %v", err)
	}
	if info, err := os.Stat(filepath.Join(vault, "created")); err != nil || !info.IsDir() {
		t.Fatalf("created directory = %v, info=%v", err, info)
	}
	if _, markerErr := os.Lstat(filepath.Join(vault, ".oxpio", "completed.json")); markerErr != nil {
		t.Fatalf("completed marker = %v, want recorded", markerErr)
	}
}

func TestMutateDirectoryRejectsInvalidCreatedDirectoryPostcondition(t *testing.T) {
	tests := []struct {
		name    string
		replace func(string) error
	}{
		{
			name: "deleted",
			replace: func(name string) error {
				return os.Remove(name)
			},
		},
		{
			name: "regular file",
			replace: func(name string) error {
				if err := os.Remove(name); err != nil {
					return err
				}
				return os.WriteFile(name, []byte("replacement"), 0o644)
			},
		},
		{
			name: "symbolic link",
			replace: func(name string) error {
				if err := os.Remove(name); err != nil {
					return err
				}
				return os.Symlink(filepath.Dir(name), name)
			},
		},
		{
			name: "non-empty directory",
			replace: func(name string) error {
				return os.WriteFile(filepath.Join(name, "external"), []byte("replacement"), 0o644)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			vault := t.TempDir()
			writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
			writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")

			originalMkdir := mkdirDirectory
			mkdirDirectory = func(root string, parent *os.File, name string, mode os.FileMode) (*internalfsutil.CreatedDirectory, error) {
				created, err := internalfsutil.CreateDirectoryRelativeWithinRoot(root, parent, name, mode)
				if err != nil {
					return created, err
				}
				return created, test.replace(filepath.Join(parent.Name(), name))
			}
			defer func() { mkdirDirectory = originalMkdir }()

			_, err := MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationMkdir, Path: "created", ExpectedHash: AbsentHash})
			if !errors.Is(err, workspace.ErrRecovery) {
				t.Fatalf("mkdir error = %v, want recovery error", err)
			}
			if _, markerErr := os.Lstat(filepath.Join(vault, ".oxpio", "completed.json")); !errors.Is(markerErr, os.ErrNotExist) {
				t.Fatalf("completed marker error = %v, want marker removed", markerErr)
			}
		})
	}
}

func TestMutateDirectoryDoesNotRollbackAnEmptyCompetingDirectory(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")

	originalMkdir := mkdirDirectory
	mkdirDirectory = func(root string, parent *os.File, name string, mode os.FileMode) (*internalfsutil.CreatedDirectory, error) {
		created, err := internalfsutil.CreateDirectoryRelativeWithinRoot(root, parent, name, mode)
		if err != nil {
			return created, err
		}
		if err := os.Remove(filepath.Join(parent.Name(), name)); err != nil {
			return created, err
		}
		if err := os.Mkdir(filepath.Join(parent.Name(), name), mode.Perm()); err != nil {
			return created, err
		}
		return created, nil
	}
	defer func() { mkdirDirectory = originalMkdir }()

	_, err := MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationMkdir, Path: "created", ExpectedHash: AbsentHash})
	if !errors.Is(err, workspace.ErrRecovery) {
		t.Fatalf("mkdir error = %v, want recovery error", err)
	}
	if info, statErr := os.Stat(filepath.Join(vault, "created")); statErr != nil || !info.IsDir() {
		t.Fatalf("competing directory = %v, info=%v; want preserved", statErr, info)
	}
}

func TestMutateDirectoryMkdirRollsBackWhenCreateReportsAnError(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")

	createErr := errors.New("mkdir completed but reported an error")
	originalMkdir := mkdirDirectory
	mkdirDirectory = func(root string, parent *os.File, name string, mode os.FileMode) (*internalfsutil.CreatedDirectory, error) {
		created, err := internalfsutil.CreateDirectoryRelativeWithinRoot(root, parent, name, mode)
		if err != nil {
			return created, err
		}
		return created, errors.Join(internalfsutil.ErrDirectoryCreated, createErr)
	}
	defer func() { mkdirDirectory = originalMkdir }()

	_, err := MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationMkdir, Path: "created", ExpectedHash: AbsentHash})
	if !errors.Is(err, createErr) {
		t.Fatalf("mkdir error = %v, want create error", err)
	}
	if _, statErr := os.Lstat(filepath.Join(vault, "created")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("created directory state = %v, want absent", statErr)
	}
	if _, markerErr := os.Lstat(filepath.Join(vault, ".oxpio", "completed.json")); !errors.Is(markerErr, os.ErrNotExist) {
		t.Fatalf("completed marker error = %v, want marker removed", markerErr)
	}
}

func TestMutateDirectoryMkdirRejectsParentMovedOutsideVault(t *testing.T) {
	vault := t.TempDir()
	outside := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	parent := filepath.Join(vault, "parent")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(vault, "symlink-probe")
	if err := os.Symlink(outside, probe); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}

	createdOutside := false
	originalMkdir := mkdirDirectory
	mkdirDirectory = func(root string, parentDirectory *os.File, name string, mode os.FileMode) (*internalfsutil.CreatedDirectory, error) {
		created, err := internalfsutil.CreateDirectoryRelativeWithinRoot(root, parentDirectory, name, mode)
		if err != nil {
			return created, err
		}
		moved := filepath.Join(outside, "parent-original")
		if err := os.Rename(parent, moved); err != nil {
			return created, err
		}
		if err := os.Symlink(outside, parent); err != nil {
			return created, err
		}
		createdOutside = true
		return created, nil
	}
	defer func() { mkdirDirectory = originalMkdir }()

	_, err := MutateDirectory(vault, "", DirectoryMutationRequest{Operation: MutationMkdir, Path: "parent/created", ExpectedHash: AbsentHash})
	if !createdOutside {
		t.Fatal("controlled race did not create the directory through the moved parent")
	}
	if !errors.Is(err, workspace.ErrRecovery) {
		t.Fatalf("mkdir error = %v, want recovery error", err)
	}
	if _, statErr := os.Lstat(filepath.Join(outside, "created")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside directory state = %v, want absent", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(outside, "parent-original", "created")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside parent directory state = %v, want absent", statErr)
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

func TestMutateDirectoryRequiresAbsentMkdirCAS(t *testing.T) {
	vault := t.TempDir()
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nattachments:\n  directory: attachments\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
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
}
