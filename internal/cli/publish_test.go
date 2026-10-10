package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPublishCommandPublishesAndDryRunPreservesOutput(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	stdout, stderr, err := executeForTest(t, defaultCommandDependencies(), []string{"publish", "--vault", vault, "--output", output})
	if err != nil || stdout != "" || stderr != "" {
		t.Fatalf("publish = stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	before := snapshotCLIOutput(t, output)
	stdout, stderr, err = executeForTest(t, defaultCommandDependencies(), []string{"publish", "--dry-run", "--vault", vault, "--output", output})
	if err != nil || stdout != "" || stderr != "" {
		t.Fatalf("publish --dry-run = stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	if after := snapshotCLIOutput(t, output); !reflect.DeepEqual(after, before) {
		t.Fatal("dry-run changed formal output")
	}
	if _, err := os.Stat(filepath.Join(output, "index.html")); err != nil {
		t.Fatal(err)
	}
}
