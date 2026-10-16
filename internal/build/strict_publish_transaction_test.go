package build

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalanalyze "github.com/simp-lee/oxpio/internal/analyze"
	"github.com/simp-lee/oxpio/internal/workspace"
)

func TestStrictPublishRejectsInputChangedBeforeOutputCommit(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "site")
	writeStrictFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	article := "---\ntitle: Article\npublish: true\ntype: doc\n---\nOriginal\n"
	writeStrictFile(t, vault, "article.md", article)
	if _, err := BuildWithOptions(vault, output, Options{Strict: true}); err != nil {
		t.Fatal(err)
	}
	before := strictOutputBytes(t, output)

	candidate := "---\ntitle: Article\npublish: true\ntype: doc\n---\nCandidate\n"
	writeStrictFile(t, vault, "article.md", candidate)
	planned, err := internalanalyze.AnalyzeWithOutput(vault, output)
	if err != nil || planned.Plan == nil || planned.Plan.Index == nil {
		t.Fatalf("candidate analysis = %#v, err=%v", planned, err)
	}
	fingerprint, err := workspace.Fingerprint(vault, output)
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(vault, ".oxpio", "cache", "manifest.json")
	cacheBefore, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	changed := "---\ntitle: Article\npublish: true\ntype: doc\n---\nExternal change\n"
	result, err := buildStrictSiteWithTransactionTracking(planned.Plan, vault, output, nil, nil, false, fingerprint, func() error {
		if writeErr := os.WriteFile(filepath.Join(vault, "article.md"), []byte(changed), 0o644); writeErr != nil {
			return writeErr
		}
		current, fingerprintErr := workspace.Fingerprint(vault, output)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		if current == fingerprint {
			return fmt.Errorf("CONCURRENT_INPUT: source mutation did not change fingerprint")
		}
		return fmt.Errorf("CONCURRENT_INPUT: input fingerprint changed from %s to %s", fingerprint, current)
	})
	if err == nil || !strings.Contains(err.Error(), "CONCURRENT_INPUT") {
		t.Fatalf("publish error = %v, want concurrent input rejection", err)
	}
	if result == nil {
		t.Fatal("publish result is nil")
	}
	after := strictOutputBytes(t, output)
	compareStrictOutputBytes(t, before, after)
	staging, globErr := filepath.Glob(filepath.Join(filepath.Dir(output), ".oxpio-publish-stage-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(staging) != 0 {
		t.Fatalf("staging residue after concurrent input rejection: %v", staging)
	}
	cacheAfter, cacheErr := os.ReadFile(cachePath)
	if cacheErr != nil || !bytes.Equal(cacheBefore, cacheAfter) {
		t.Fatalf("cache changed after pre-commit rejection: read err=%v", cacheErr)
	}
	if _, journalErr := workspace.LoadJournal(vault); !os.IsNotExist(journalErr) {
		t.Fatalf("active journal after pre-commit rejection: %v", journalErr)
	}
}
