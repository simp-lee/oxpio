package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCLIConfig(t *testing.T, vaultPath string) {
	t.Helper()
	writeValidateFile(t, vaultPath, defaultConfigFilename, "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
}

func writeValidateFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type cliOutputSnapshot map[string][]byte

func snapshotCLIOutput(t *testing.T, root string) cliOutputSnapshot {
	t.Helper()
	result := make(cliOutputSnapshot)
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(rel)] = data
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertCLIOutputUnchanged(t *testing.T, root string, before cliOutputSnapshot, reason string) {
	t.Helper()
	after := snapshotCLIOutput(t, root)
	if len(before) != len(after) {
		t.Fatalf("%s changed output file count: before=%d after=%d", reason, len(before), len(after))
	}
	for path, data := range before {
		if string(after[path]) != string(data) {
			t.Fatalf("%s changed output %q", reason, path)
		}
	}
}

func assertNoCLITransactionResidue(t *testing.T, outputRoot, outputPath string) {
	t.Helper()
	for _, path := range []string{
		filepath.Join(outputRoot, ".oxpio-staging"),
		filepath.Join(filepath.Dir(outputPath), ".oxpio-staging"),
	} {
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("transaction residue at %q", path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat transaction residue %q: %v", path, err)
		}
	}
}
