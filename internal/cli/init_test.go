package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalconfig "github.com/simp-lee/oxpio/internal/config"
)

func TestInitCommandDefaultsToCurrentVaultAndSeedsStrictRoot(t *testing.T) {
	vaultPath := t.TempDir()
	t.Chdir(vaultPath)
	stdout, _, err := executeForTest(t, testCommandDependencies(), []string{"init"})
	if err != nil {
		t.Fatalf("executeForTest() error = %v", err)
	}
	if !strings.Contains(stdout, "Replace baseURL") {
		t.Fatalf("stdout = %q", stdout)
	}
	for _, name := range []string{defaultConfigFilename, "_index.md"} {
		if _, err := os.Stat(filepath.Join(vaultPath, name)); err != nil {
			t.Fatalf("missing init file %q: %v", name, err)
		}
	}
	if _, err := internalconfig.LoadForBuild(vaultPath); err != nil {
		t.Fatalf("strict config load error = %v", err)
	}
	if _, _, err := executeForTest(t, defaultCommandDependencies(), []string{"build"}); err != nil {
		t.Fatalf("default build after init error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(vaultPath, "public", "index.html")); err != nil {
		t.Fatalf("missing generated root page: %v", err)
	}
}

func TestInitCommandTrimsVaultPathWhitespace(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	_, _, err := executeForTest(t, testCommandDependencies(), []string{"init", "--vault", " \tvault \n"})
	if err != nil {
		t.Fatalf("executeForTest() error = %v", err)
	}
	for _, name := range []string{defaultConfigFilename, "_index.md"} {
		if _, err := os.Stat(filepath.Join(root, "vault", name)); err != nil {
			t.Fatalf("missing init file %q: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, " \tvault \n")); !os.IsNotExist(err) {
		t.Fatalf("init used untrimmed vault path: %v", err)
	}
}

func TestInitCommandCreatesMissingDirectoryAndStrictConfig(t *testing.T) {
	vaultPath := filepath.Join(t.TempDir(), "new-vault")
	_, _, err := executeForTest(t, testCommandDependencies(), []string{"init", "--vault", vaultPath})
	if err != nil {
		t.Fatalf("executeForTest() error = %v", err)
	}
	if _, err := internalconfig.LoadForBuild(vaultPath); err != nil {
		t.Fatalf("LoadForBuild() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(vaultPath, "_index.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "---\ntitle: Home\npublish: true\n---\n" {
		t.Fatalf("root seed = %q", data)
	}
}

func TestInitCommandRejectsNonemptyVaultWithoutWrites(t *testing.T) {
	vaultPath := t.TempDir()
	before := []string{"existing.txt"}
	if err := os.WriteFile(filepath.Join(vaultPath, before[0]), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := executeForTest(t, testCommandDependencies(), []string{"init", "--vault", vaultPath})
	if err == nil || !strings.Contains(err.Error(), "completely empty") {
		t.Fatalf("executeForTest() error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(vaultPath, defaultConfigFilename)); !os.IsNotExist(statErr) {
		t.Fatalf("init wrote config into nonempty vault: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(vaultPath, "_index.md")); !os.IsNotExist(statErr) {
		t.Fatalf("init wrote root into nonempty vault: %v", statErr)
	}
}

func TestInitCommandRejectsFilePath(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "not-a-vault")
	if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := executeForTest(t, testCommandDependencies(), []string{"init", "--vault", filePath})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("executeForTest() error = %v", err)
	}
}
