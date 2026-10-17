package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalagent "github.com/simp-lee/oxpio/internal/agent"
	internalapplication "github.com/simp-lee/oxpio/internal/application"
	"github.com/simp-lee/oxpio/internal/model"
)

func TestValidateAgentInputPathUsesCaseInsensitiveReservedDirectories(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	for _, value := range []string{".OXPIO/input.md", ".ObSiDiAn/input.md"} {
		if err := validateAgentInputPath(vault, output, value); err == nil {
			t.Fatalf("validateAgentInputPath(%q) error = nil, want reserved-directory rejection", value)
		}
	}
	if err := validateAgentInputPath(vault, output, ".draft.md"); err != nil {
		t.Fatalf("validateAgentInputPath(.draft.md) error = %v, want allowed dot file", err)
	}
}

func TestRedactHiddenCatalogDiagnosticsDoesNotHideVisibleSibling(t *testing.T) {
	hiddenPath := "draft.md"
	visiblePath := "article.md"
	diagnostics := []internalagent.Diagnostic{
		{Path: &hiddenPath, Category: "schema", Message: "hidden"},
		{Path: &visiblePath, Category: "schema", Message: "visible"},
	}
	catalog := &model.SourceCatalog{Entries: []model.SourceCatalogEntry{
		{RelPath: "_index.md", SectionPath: ".", Kind: "section", EffectivePublish: true},
		{RelPath: hiddenPath, SectionPath: ".", Kind: "article", EffectivePublish: false},
		{RelPath: visiblePath, SectionPath: ".", Kind: "article", EffectivePublish: true},
	}}
	redacted := redactHiddenCatalogDiagnostics(diagnostics, catalog, "")
	if redacted[0].Path != nil || redacted[0].Message != "target is not available in the selected read scope" {
		t.Fatalf("hidden diagnostic = %#v, want redacted", redacted[0])
	}
	if redacted[1].Path == nil || *redacted[1].Path != visiblePath || redacted[1].Message != "visible" {
		t.Fatalf("visible diagnostic = %#v, want unchanged", redacted[1])
	}
}

func TestRedactUnavailableDiagnosticsProtectsHiddenRootContentOnly(t *testing.T) {
	vault := t.TempDir()
	writeRootTestFile(t, vault, "oxpio.yaml", "title: Site\n")
	writeRootTestFile(t, vault, "_index.md", "---\ntitle: Hidden\npublish: false\n---\n")
	writeRootTestFile(t, vault, "secret.md", "---\ntitle: Secret\npublish: false\ntype: doc\n---\n")
	configPath := "oxpio.yaml"
	secretPath := "secret.md"
	diagnostics := []internalagent.Diagnostic{{Path: &configPath, Message: "config"}, {Path: &secretPath, Message: "secret"}}
	redacted := redactUnavailableDiagnostics(diagnostics, vault)
	if redacted[0].Path == nil || *redacted[0].Path != configPath || redacted[0].Message != "config" {
		t.Fatalf("config diagnostic = %#v, want unchanged", redacted[0])
	}
	if redacted[1].Path != nil || redacted[1].Message != "target is not available in the selected read scope" {
		t.Fatalf("hidden diagnostic = %#v, want redacted", redacted[1])
	}
}

func writeRootTestFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRedactUnavailableDiagnosticsHidesMalformedHiddenSource(t *testing.T) {
	vault := t.TempDir()
	writeRootTestFile(t, vault, "oxpio.yaml", "title: Site\n")
	writeRootTestFile(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\nunknown: true\n---\n")
	writeRootTestFile(t, vault, "#secret.md", "---\ntitle: Secret\npublish: false\nunknown: true\n---\n")
	pathValue := "private/_index.md"
	diagnostics := []internalagent.Diagnostic{{Path: &pathValue, Message: "parse article private/_index.md"}}
	redacted := redactUnavailableDiagnostics(diagnostics, vault)
	if redacted[0].Path != nil || redacted[0].Message != "target is not available in the selected read scope" {
		t.Fatalf("diagnostic = %#v, want generic hidden-source redaction", redacted[0])
	}
	hashPath := "#secret.md"
	hashDiagnostics := []internalagent.Diagnostic{{Path: &hashPath, Message: "parse #secret.md"}}
	hashRedacted := redactUnavailableDiagnostics(hashDiagnostics, vault)
	if hashRedacted[0].Path != nil || hashRedacted[0].Message != "target is not available in the selected read scope" {
		t.Fatalf("hash diagnostic = %#v, want generic hidden-source redaction", hashRedacted[0])
	}
}

func TestExecuteShowsRootHelp(t *testing.T) {
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), nil)
	if err != nil {
		t.Fatalf("executeForTest() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty stderr", stderr)
	}

	for _, want := range []string{"publish", "serve", "init", "version", "find"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q\n%s", want, stdout)
		}
	}
}

func TestExecuteVersionFormsMatch(t *testing.T) {
	setVersionTestState(t)
	var outputs []string
	for _, args := range [][]string{{"version"}, {"--version"}} {
		stdout, _, err := executeForTest(t, testCommandDependencies(), args)
		if err != nil {
			t.Fatalf("executeForTest(%v) error = %v", args, err)
		}
		outputs = append(outputs, stdout)
	}
	if outputs[0] != outputs[1] || outputs[0] != "oxpio version=dev commit=unknown date=unknown type=dev\n" {
		t.Fatalf("version outputs = %#v", outputs)
	}
	for _, args := range [][]string{{"version", "extra"}, {"--version", "extra"}, {"--version", "--unknown"}, {"--version", "--format", "bad"}, {"context", "--version", "--unknown"}, {"context", "--version", "--include-unpublished"}, {"serve", "--version", "--port", "0"}, {"serve", "--version", "--port", "nope"}, {"editor", "--version", "--setup", "--reset-password"}, {"grep", "q", "--version", "--context", "21"}, {"read", "article.md", "--version", "--limit", "0"}, {"read", "article.md", "--version", "--limit", "nope"}, {"read", "article.md", "--version", "extra"}, {"help", "missing", "--version", "--format", "json"}} {
		_, _, err := executeForTest(t, testCommandDependencies(), args)
		if err == nil {
			t.Fatalf("executeForTest(%v) error = nil", args)
		}
	}
}

func TestExecuteRejectsUnknownCommand(t *testing.T) {
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"foo"})
	if err == nil {
		t.Fatal("executeForTest() error = nil, want unknown command error")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty stdout", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty stderr", stderr)
	}
	if !strings.Contains(err.Error(), `unknown command "foo" for "oxpio"`) {
		t.Fatalf("error = %q, want unknown command message", err.Error())
	}
}

func TestHelpRejectsInvalidFormatEvenWithCobraHelp(t *testing.T) {
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"read", "--help", "--format", "bad"})
	if err == nil || stdout != "" || stderr != "" || !strings.Contains(err.Error(), "--format must be text or json") {
		t.Fatalf("invalid help format = stdout %q, stderr %q, err %v", stdout, stderr, err)
	}
}

func TestHelpAndContextRejectStaticUnpublishedRequests(t *testing.T) {
	for _, args := range [][]string{
		{"help", "--include-unpublished"},
		{"context", "--include-unpublished"},
	} {
		stdout, stderr, err := executeForTest(t, testCommandDependencies(), args)
		if err == nil || stdout != "" || stderr != "" || !strings.Contains(err.Error(), "requires --vault") {
			t.Fatalf("executeForTest(%v) = stdout %q, stderr %q, err %v", args, stdout, stderr, err)
		}
	}
}

func TestHelpRejectsUnknownCommandInTextMode(t *testing.T) {
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"help", "missing"})
	if err == nil || stdout != "" || stderr != "" || !strings.Contains(err.Error(), `unknown command "missing"`) {
		t.Fatalf("help missing = stdout %q, stderr %q, err %v", stdout, stderr, err)
	}
}

func TestRegistryHelpDrivesCobraHelp(t *testing.T) {
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"read", "--help"})
	if err != nil || stderr != "" || !strings.Contains(stdout, "Side effects: read-only") || !strings.Contains(stdout, "--include-unpublished") {
		t.Fatalf("read help = stdout %q, stderr %q, err %v", stdout, stderr, err)
	}

	stdout, stderr, err = executeForTest(t, testCommandDependencies(), []string{"read", "--help", "--format", "json"})
	if err != nil || stderr != "" {
		t.Fatalf("read JSON help = stdout %q, stderr %q, err %v", stdout, stderr, err)
	}
	var helpEnvelope struct {
		Data struct {
			Command struct {
				Name string `json:"name"`
			} `json:"command"`
		} `json:"data"`
	}
	if decodeErr := json.Unmarshal([]byte(stdout), &helpEnvelope); decodeErr != nil || helpEnvelope.Data.Command.Name != "read" {
		t.Fatalf("read JSON help envelope = %q, err=%v", stdout, decodeErr)
	}

	stdout, stderr, err = executeForTest(t, testCommandDependencies(), []string{"config", "--help", "--format", "json"})
	if err != nil || stderr != "" {
		t.Fatalf("config JSON help = stdout %q, stderr %q, err %v", stdout, stderr, err)
	}
	var parentHelpEnvelope struct {
		Data struct {
			Commands []struct {
				Name string `json:"name"`
			} `json:"commands"`
		} `json:"data"`
	}
	if decodeErr := json.Unmarshal([]byte(stdout), &parentHelpEnvelope); decodeErr != nil || len(parentHelpEnvelope.Data.Commands) == 0 {
		t.Fatalf("config JSON help envelope = %q, err=%v", stdout, decodeErr)
	}

	stdout, stderr, err = executeForTest(t, testCommandDependencies(), []string{"--help"})
	if err != nil || stderr != "" || strings.Contains(stdout, "completion") || !strings.Contains(stdout, "OXPIO commands:") {
		t.Fatalf("root help = stdout %q, stderr %q, err %v", stdout, stderr, err)
	}
}

func TestUnknownCommandVersionFailureKeepsEmptyIdentity(t *testing.T) {
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"missing", "--version", "--format", "json"})
	if err == nil || stderr != "" {
		t.Fatalf("unknown version = stdout %q, stderr %q, err %v", stdout, stderr, err)
	}
	var envelope struct {
		Command string `json:"command"`
		OK      bool   `json:"ok"`
	}
	if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil {
		t.Fatalf("decode unknown version envelope: %v (%q)", decodeErr, stdout)
	}
	if envelope.OK || envelope.Command != "" {
		t.Fatalf("unknown version envelope = %#v", envelope)
	}
}

func TestReadRejectsExplicitZeroLimit(t *testing.T) {
	for _, limit := range []string{"0", "10001"} {
		stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"read", "article.md", "--limit", limit})
		if err == nil || stdout != "" || stderr != "" || !strings.Contains(err.Error(), "limit must be between") {
			t.Fatalf("read limit %s = stdout %q, stderr %q, err %v", limit, stdout, stderr, err)
		}
	}
}

func TestSourceMutationUsesCustomOutputBoundary(t *testing.T) {
	vault := t.TempDir()
	writeCLIConfig(t, vault)
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	original := "---\ntitle: Article\npublish: true\ntype: doc\n---\nBefore\n"
	writeValidateFile(t, vault, "article.md", original)
	writeValidateFile(t, vault, "replacement.md", "---\ntitle: Article\npublish: true\ntype: doc\n---\nAfter\n")
	output := filepath.Join(vault, "site")
	writeValidateFile(t, vault, "site/broken.md", "not strict frontmatter\n")
	digest := sha256.Sum256([]byte(original))
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"write", "article.md", "--vault", vault, "--output", output, "--content-file", "replacement.md", "--if-hash", fmt.Sprintf("%x", digest), "--dry-run", "--format", "json"})
	if err != nil || stderr != "" || !strings.Contains(stdout, `"operation":"write"`) {
		t.Fatalf("custom-output source mutation = stdout %q, stderr %q, err %v", stdout, stderr, err)
	}
}

func TestHelpUsesCustomOutputBoundary(t *testing.T) {
	vault := t.TempDir()
	writeCLIConfig(t, vault)
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeValidateFile(t, vault, "site/broken.md", "not strict frontmatter\n")
	output := filepath.Join(vault, "site")

	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"help", "--vault", vault, "--output", output, "--format", "json"})
	if err != nil || stderr != "" || !strings.Contains(stdout, `"command":"help"`) {
		t.Fatalf("help custom output = stdout %q, stderr %q, err %v", stdout, stderr, err)
	}
}

func TestCommandRegistryOutputFieldsMatchRepresentativeJSON(t *testing.T) {
	vault := t.TempDir()
	writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nlanguage: en\ntheme: base\nnavigation: []\nattachments:\n  directory: assets\n")
	writeValidateFile(t, vault, "assets/.keep", "")
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeValidateFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: doc\n---\nArticle\n")
	writeValidateFile(t, vault, "custom.css", "body {}\n")

	cases := []struct {
		name string
		args []string
	}{
		{name: "config read", args: []string{"config", "read", "--vault", vault}},
		{name: "read", args: []string{"read", "article.md", "--vault", vault}},
		{name: "theme context", args: []string{"theme", "context", "--vault", vault}},
		{name: "theme validate", args: []string{"theme", "validate", "--vault", vault}},
		{name: "theme read", args: []string{"theme", "read", "custom.css", "--vault", vault}},
	}
	for _, item := range cases {
		stdout, stderr, err := executeForTest(t, testCommandDependencies(), append(item.args, "--format", "json"))
		if err != nil || stderr != "" {
			t.Fatalf("%s = stdout %q, stderr %q, err %v", item.name, stdout, stderr, err)
		}
		var envelope struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
			t.Fatalf("%s envelope: %v (%q)", item.name, err, stdout)
		}
		spec, ok := internalapplication.FindCommand(item.name)
		if !ok {
			t.Fatalf("missing registry command %q", item.name)
		}
		for _, field := range spec.OutputFields {
			if _, ok := envelope.Data[field]; !ok {
				t.Fatalf("%s registry field %q missing from data %s", item.name, field, stdout)
			}
		}
	}
}

func TestJSONFailureContainsSanitizedDiagnosticsOnly(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, defaultConfigFilename), []byte("title: Site\nbaseURL: https://example.test/\nnavigation: []\nunknown: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"validate", "--vault", vault, "--format", "json"})
	if err == nil {
		t.Fatal("JSON validate succeeded")
	}
	if stderr != "" || strings.Contains(stdout, vault) || !strings.Contains(stdout, `"diagnostics"`) || !strings.Contains(stdout, `"ok":false`) {
		t.Fatalf("JSON failure = stdout %q, stderr %q", stdout, stderr)
	}
}

func TestJSONDiagnosticsRedactHiddenTargets(t *testing.T) {
	vault := t.TempDir()
	writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeValidateFile(t, vault, "public.md", "---\ntitle: Public\npublish: true\ntype: doc\n---\n[[private/article]]\n\n[private article](private/article.md)\n\n[[private/_index]]\n")
	writeValidateFile(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\n---\n")
	writeValidateFile(t, vault, "private/article.md", "---\ntitle: Private article\npublish: true\ntype: doc\n---\nSecret\n")
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"validate", "--vault", vault, "--format", "json"})
	if err == nil || stderr != "" || !strings.Contains(stdout, `"errors":3`) || !strings.Contains(stdout, `"warnings":0`) || !strings.Contains(stdout, `"category":"permission"`) || strings.Contains(stdout, "private/article.md") || strings.Contains(stdout, "Private article") {
		t.Fatalf("hidden JSON failure = stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
}

func TestValidateRejectsHiddenMarkdownResourceErrorsWithoutLeakingSource(t *testing.T) {
	vault := t.TempDir()
	writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeValidateFile(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\n---\n")
	writeValidateFile(t, vault, "private/article.md", "---\ntitle: Private article\npublish: true\ntype: doc\n---\n![missing](missing.png)\n")

	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"validate", "--vault", vault, "--format", "json"})
	if err == nil || stderr != "" || !strings.Contains(stdout, `"ok":false`) || !strings.Contains(stdout, `"errors":`) || strings.Contains(stdout, "private/article.md") {
		t.Fatalf("hidden Markdown validation = stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
}

func TestValidateRedactsHiddenSourceDiagnosticsBeforePlanning(t *testing.T) {
	vault := t.TempDir()
	writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeValidateFile(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\n---\n")
	writeValidateFile(t, vault, "private/draft.md", "---\ntitle: [\npublish: false\ntype: doc\n---\nHidden\n")

	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"validate", "--vault", vault, "--format", "json"})
	if err == nil || stderr != "" || !strings.Contains(stdout, `"ok":false`) || strings.Contains(stdout, "private/draft.md") {
		t.Fatalf("pre-plan hidden validation = stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
}

func TestThemeValidateRedactsHiddenSourceDiagnostics(t *testing.T) {
	vault := t.TempDir()
	writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeValidateFile(t, vault, "draft.md", "---\ntitle: Draft\npublish: false\ntype: doc\n---\n![missing](missing.png)\n")

	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"theme", "validate", "--vault", vault, "--format", "json"})
	if err == nil || stderr != "" || !strings.Contains(stdout, `"ok":false`) || strings.Contains(stdout, "draft.md") {
		t.Fatalf("theme validation = stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
}

func TestThemeValidateAcceptsOutputFlag(t *testing.T) {
	vault := t.TempDir()
	writeCLIConfig(t, vault)
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	output := filepath.Join(vault, "site")
	writeValidateFile(t, vault, "site/broken.md", "not strict frontmatter\n")
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"theme", "validate", "--vault", vault, "--output", output})
	if err != nil || stdout == "" || stderr != "" || !strings.Contains(stdout, "\"valid\": true") {
		t.Fatalf("theme validate --output = stdout %q, stderr %q, err %v", stdout, stderr, err)
	}
}

func TestValidateRedactsHiddenDraftSourceDiagnostics(t *testing.T) {
	vault := t.TempDir()
	writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeValidateFile(t, vault, "draft.md", "---\ntitle: Draft\npublish: false\ntype: doc\n---\n![missing](missing.png)\n")

	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"validate", "--vault", vault, "--format", "json"})
	if err == nil || stderr != "" || !strings.Contains(stdout, `"ok":false`) || strings.Contains(stdout, "draft.md") {
		t.Fatalf("hidden draft validation = stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
}

func TestPublicSourceHiddenResourceLinkIsReportedOnce(t *testing.T) {
	vault := t.TempDir()
	writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeValidateFile(t, vault, "public.md", "---\ntitle: Public\npublish: true\ntype: doc\n---\n[secret](private/secret.txt)\n")
	writeValidateFile(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\n---\n")
	writeValidateFile(t, vault, "private/secret.txt", "secret\n")

	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"validate", "--vault", vault, "--format", "json"})
	if err == nil || stderr != "" || !strings.Contains(stdout, `"errors":1`) || strings.Contains(stdout, "private/secret.txt") {
		t.Fatalf("hidden resource link validation = stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
}

func TestPublicSourceCannotPublishHiddenResource(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeValidateFile(t, vault, "public.md", "---\ntitle: Public\npublish: true\ntype: doc\n---\n![secret](private/secret.png)\n")
	writeValidateFile(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\n---\n")
	writeValidateFile(t, vault, "private/secret.png", "secret bytes\n")

	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"validate", "--vault", vault, "--output", output, "--format", "json"})
	if err == nil || stderr != "" || !strings.Contains(stdout, "not available to public sources") || strings.Contains(stdout, "private/secret.png") {
		t.Fatalf("hidden resource validation = stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
	if _, stderr, err = executeForTest(t, defaultCommandDependencies(), []string{"publish", "--vault", vault, "--output", output}); err == nil || !strings.Contains(stderr, "not available to public sources") {
		t.Fatalf("hidden resource publish = stderr %q, error %v", stderr, err)
	}
	if _, statErr := os.Stat(output); statErr == nil {
		t.Fatal("failed publish created formal output")
	} else if !os.IsNotExist(statErr) {
		t.Fatalf("stat failed output: %v", statErr)
	}
}

func TestJSONDiagnosticsRedactResolvedSymlinkVaultPath(t *testing.T) {
	realVault := t.TempDir()
	linkRoot := t.TempDir()
	linkedVault := filepath.Join(linkRoot, "vault")
	if err := os.Symlink(realVault, linkedVault); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realVault, defaultConfigFilename), []byte("title: Site\nbaseURL: https://example.test/\nnavigation: []\nunknown: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"validate", "--vault", linkedVault, "--format", "json"})
	if err == nil || stderr != "" || strings.Contains(stdout, realVault) || strings.Contains(stdout, linkedVault) {
		t.Fatalf("symlink JSON failure = stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
}

func TestExecuteRejectsUnknownRootFlagAndExtraArgs(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"extra", "arg"}} {
		_, _, err := executeForTest(t, testCommandDependencies(), args)
		if err == nil {
			t.Fatalf("executeForTest(%v) error = nil", args)
		}
	}
}

func executeForTest(t *testing.T, deps commandDependencies, args []string) (stdout string, stderr string, err error) {
	t.Helper()

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	err = executeWithDeps(args, deps, &stdoutBuf, &stderrBuf)

	return stdoutBuf.String(), stderrBuf.String(), err
}

func setTestPublishDependency(deps *commandDependencies, publish func(string, string, internalapplication.PublishOptions) (*internalapplication.PublishResult, error)) {
	deps.publishSite = publish
}

func testCommandDependencies() commandDependencies {
	deps := commandDependencies{
		newPreviewServer: func(outputPath string, port int) (previewServer, error) {
			return nil, fmt.Errorf("unexpected newPreviewServer call")
		},
		newFileWatcher: func() (fileWatcher, error) {
			return nil, fmt.Errorf("unexpected newFileWatcher call")
		},
	}
	return deps
}
