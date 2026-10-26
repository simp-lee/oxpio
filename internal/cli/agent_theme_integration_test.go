package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalagent "github.com/simp-lee/oxpio/internal/agent"
)

// TestRealAgentAndThemeWorkflow exercises the public, no-TTY protocol rather
// than calling command handlers directly. It deliberately keeps source
// mutations and theme mutations separate and proves that neither publishes
// implicitly.
func TestRealAgentAndThemeWorkflow(t *testing.T) {
	binary := buildBlackBoxBinary(t)
	vault := t.TempDir()
	writeBlackBoxFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeBlackBoxFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeBlackBoxFile(t, vault, "guide/_index.md", "---\ntitle: Guide\npublish: true\n---\nGuide\n")
	writeBlackBoxFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: doc\ntags: [go]\n---\nBody\nLine two\nLine three\n")
	writeBlackBoxFile(t, vault, "draft.md", "---\ntitle: Draft\npublish: false\ntype: doc\n---\nHidden\n")

	for _, probe := range []struct {
		args    []string
		command string
	}{
		{[]string{"help", "--agent"}, "help"},
		{[]string{"theme", "write", "--help"}, "theme write"},
		{[]string{"context", "--vault", vault}, "context"},
	} {
		envelope, stdout, code := runBlackBoxEnvelope(t, binary, probe.args, nil, probe.command)
		if code != 0 || !envelope.OK {
			t.Fatalf("protocol probe %v: code=%d envelope=%#v stdout=%q", probe.args, code, envelope, stdout)
		}
		if strings.Contains(stdout, vault) {
			t.Fatalf("protocol probe %v leaked absolute Vault path: %q", probe.args, stdout)
		}
	}

	invalidArgs, _, code := runBlackBoxEnvelope(t, binary, []string{"read", "--vault", vault}, nil, "read")
	if code != 2 || invalidArgs.OK || len(invalidArgs.Diagnostics) == 0 || invalidArgs.Diagnostics[0].Code != "INVALID_ARGUMENT" {
		t.Fatalf("invalid argument exit: code=%d envelope=%#v", code, invalidArgs)
	}
	missingSource, _, code := runBlackBoxEnvelope(t, binary, []string{"read", "missing.md", "--vault", vault}, nil, "read")
	if code != 5 || missingSource.OK || len(missingSource.Diagnostics) == 0 || missingSource.Diagnostics[0].Code != "IO_ERROR" {
		t.Fatalf("missing source exit: code=%d envelope=%#v", code, missingSource)
	}
	publishEnvelope, _, code := runBlackBoxEnvelope(t, binary, []string{"publish", "--vault", vault}, nil, "publish")
	if code != 0 || !publishEnvelope.OK {
		t.Fatalf("initial publish: code=%d envelope=%#v", code, publishEnvelope)
	}
	output := filepath.Join(vault, "public")
	beforeOutput := snapshotCLIOutput(t, output)

	var readData struct {
		Content string `json:"content"`
		Hash    string `json:"hash"`
		Offset  int    `json:"offset"`
		Limit   int    `json:"limit"`
		Lines   int    `json:"totalLines"`
		EOF     bool   `json:"eof"`
	}
	readEnvelope, _, code := runBlackBoxEnvelope(t, binary, []string{"read", "article.md", "--vault", vault}, nil, "read")
	decodeEnvelopeData(t, readEnvelope, &readData)
	if code != 0 || !readEnvelope.OK || readData.Content == "" || readData.Hash == "" || readData.Offset != 0 || readData.Limit != readData.Lines || readData.Lines < 4 || !readData.EOF {
		t.Fatalf("full read: code=%d envelope=%#v data=%#v", code, readEnvelope, readData)
	}
	limited, _, code := runBlackBoxEnvelope(t, binary, []string{"read", "article.md", "--vault", vault, "--offset", "1", "--limit", "2"}, nil, "read")
	decodeEnvelopeData(t, limited, &readData)
	if code != 0 || !limited.OK || readData.Offset != 1 || readData.Limit != 2 || readData.Lines < 4 || readData.EOF {
		t.Fatalf("limited read: code=%d data=%#v", code, readData)
	}

	for _, probe := range [][]string{
		{"find", "--vault", vault},
		{"ls", ".", "--vault", vault},
		{"grep", "Body", "--vault", vault},
		{"tags", "--vault", vault},
		{"related", "article.md", "--vault", vault},
	} {
		envelope, _, code := runBlackBoxEnvelope(t, binary, probe, nil, blackBoxExpectedCommand(probe))
		if code != 0 || !envelope.OK {
			t.Fatalf("read-only probe %v: code=%d envelope=%#v", probe, code, envelope)
		}
	}

	// Raw source stdin, followed by a JSON patch from stdin. Neither operation
	// is allowed to replace the already-published output.
	oldHash := sha256File(t, filepath.Join(vault, "article.md"))
	newSource := "---\ntitle: Article\npublish: true\ntype: doc\ntags: [go]\n---\nBody changed\nLine two\nLine three\n"
	writeEnvelope, _, code := runBlackBoxEnvelope(t, binary, []string{"write", "article.md", "--vault", vault, "--content-file", "-", "--if-hash", oldHash}, []byte(newSource), "write")
	if code != 0 || !writeEnvelope.OK || !strings.Contains(string(readBlackBoxFile(t, filepath.Join(vault, "article.md"))), "Body changed") {
		t.Fatalf("raw write: code=%d envelope=%#v", code, writeEnvelope)
	}
	assertCLIOutputUnchanged(t, output, beforeOutput, "Agent write")

	beforeDryRun := readBlackBoxFile(t, filepath.Join(vault, "article.md"))
	dryPatch := []byte(`{"edits":[{"oldText":"Body changed","newText":"Dry run"}]}`)
	writeBlackBoxFile(t, vault, "dry-patch.json", string(dryPatch))
	lockBeforeDryRun := fileExists(filepath.Join(vault, ".oxpio", "lock"))
	dryEnvelope, _, code := runBlackBoxEnvelope(t, binary, []string{"edit", "article.md", "--vault", vault, "--edits-file", "dry-patch.json", "--if-hash", sha256Hex(beforeDryRun), "--dry-run"}, nil, "edit")
	if code != 0 || !dryEnvelope.OK || !equalBytes(beforeDryRun, readBlackBoxFile(t, filepath.Join(vault, "article.md"))) {
		t.Fatalf("Agent edit dry-run: code=%d envelope=%#v", code, dryEnvelope)
	}
	assertNoCLIDurableMutation(t, vault)
	if fileExists(filepath.Join(vault, ".oxpio", "lock")) != lockBeforeDryRun {
		t.Fatal("Agent edit dry-run changed workspace lock state")
	}

	writeBlackBoxFile(t, vault, "duplicate-patch.json", `{"edits":[],"edits":[]}`)
	duplicate, _, code := runBlackBoxEnvelope(t, binary, []string{"edit", "article.md", "--vault", vault, "--edits-file", "duplicate-patch.json", "--if-hash", sha256Hex(beforeDryRun)}, nil, "edit")
	if code != 2 || duplicate.OK || len(duplicate.Diagnostics) == 0 || duplicate.Diagnostics[0].Code != "INVALID_ARGUMENT" {
		t.Fatalf("duplicate JSON exit: code=%d envelope=%#v", code, duplicate)
	}
	actualPatch := []byte(`{"edits":[{"oldText":"Body changed","newText":"Body edited"}]}`)
	actualEnvelope, _, code := runBlackBoxEnvelope(t, binary, []string{"edit", "article.md", "--vault", vault, "--edits-file", "-", "--if-hash", sha256Hex(beforeDryRun)}, actualPatch, "edit")
	if code != 0 || !actualEnvelope.OK || !strings.Contains(string(readBlackBoxFile(t, filepath.Join(vault, "article.md"))), "Body edited") {
		t.Fatalf("JSON stdin edit: code=%d envelope=%#v", code, actualEnvelope)
	}
	staleWrite := []byte(newSource)
	conflict, _, code := runBlackBoxEnvelope(t, binary, []string{"write", "article.md", "--vault", vault, "--content-file", "-", "--if-hash", sha256Hex(beforeDryRun)}, staleWrite, "write")
	if code != 3 || conflict.OK || len(conflict.Diagnostics) == 0 || conflict.Diagnostics[0].Code != "CONFLICT" {
		t.Fatalf("CAS conflict: code=%d envelope=%#v", code, conflict)
	}
	assertCLIOutputUnchanged(t, output, beforeOutput, "CAS conflict")

	// Explicit publication state changes are still source-only. Hidden sources
	// require the explicit include-unpublished capability.
	current := readBlackBoxFile(t, filepath.Join(vault, "article.md"))
	unpublish, _, code := runBlackBoxEnvelope(t, binary, []string{"write", "article.md", "--vault", vault, "--content-file", "-", "--if-hash", sha256Hex(current), "--include-unpublished", "--unpublish"}, current, "write")
	if code != 0 || !unpublish.OK || !strings.Contains(string(readBlackBoxFile(t, filepath.Join(vault, "article.md"))), "publish: false") {
		t.Fatalf("unpublish: code=%d envelope=%#v", code, unpublish)
	}
	hiddenRead, _, code := runBlackBoxEnvelope(t, binary, []string{"read", "article.md", "--vault", vault}, nil, "read")
	if code != 4 || hiddenRead.OK || len(hiddenRead.Diagnostics) == 0 || hiddenRead.Diagnostics[0].Code != "PERMISSION_DENIED" {
		t.Fatalf("hidden read: code=%d envelope=%#v", code, hiddenRead)
	}
	current = readBlackBoxFile(t, filepath.Join(vault, "article.md"))
	republish, _, code := runBlackBoxEnvelope(t, binary, []string{"write", "article.md", "--vault", vault, "--content-file", "-", "--if-hash", sha256Hex(current), "--include-unpublished", "--publish"}, current, "write")
	if code != 0 || !republish.OK {
		t.Fatalf("republish source state: code=%d envelope=%#v", code, republish)
	}
	assertCLIOutputUnchanged(t, output, beforeOutput, "source publication policy")

	// Exercise the remaining source/directory mutation adapters in the same
	// no-TTY protocol, including a raw upload.
	upload, _, code := runBlackBoxEnvelope(t, binary, []string{"upload", ".", "--vault", vault, "--file", "-", "--name", "uploaded.md", "--kind", "article", "--if-hash", "absent"}, []byte("---\ntitle: Uploaded\npublish: true\ntype: doc\n---\nUploaded\n"), "upload")
	if code != 0 || !upload.OK || !fileExists(filepath.Join(vault, "uploaded.md")) {
		t.Fatalf("upload: code=%d envelope=%#v", code, upload)
	}
	move, _, code := runBlackBoxEnvelope(t, binary, []string{"move", "article.md", "guide/moved.md", "--vault", vault, "--if-hash", sha256File(t, filepath.Join(vault, "article.md")), "--dest-if-hash", "absent"}, nil, "move")
	if code != 0 || !move.OK || !fileExists(filepath.Join(vault, "guide", "moved.md")) {
		t.Fatalf("move: code=%d envelope=%#v", code, move)
	}
	deleted, _, code := runBlackBoxEnvelope(t, binary, []string{"delete", "guide/moved.md", "--vault", vault, "--if-hash", sha256File(t, filepath.Join(vault, "guide", "moved.md"))}, nil, "delete")
	if code != 0 || !deleted.OK || fileExists(filepath.Join(vault, "guide", "moved.md")) {
		t.Fatalf("delete: code=%d envelope=%#v", code, deleted)
	}
	mkdir, _, code := runBlackBoxEnvelope(t, binary, []string{"mkdir", "scratch", "--vault", vault}, nil, "mkdir")
	if code != 0 || !mkdir.OK || !directoryExists(filepath.Join(vault, "scratch")) {
		t.Fatalf("mkdir: code=%d envelope=%#v", code, mkdir)
	}
	recover, _, code := runBlackBoxEnvelope(t, binary, []string{"recover", "--vault", vault, "--dry-run"}, nil, "recover")
	if code != 0 || !recover.OK {
		t.Fatalf("recover dry-run: code=%d envelope=%#v", code, recover)
	}
	assertCLIOutputUnchanged(t, output, beforeOutput, "all source-only mutations")

	// Theme init creates a complete editable copy without selecting it.
	minimal, _, code := runBlackBoxEnvelope(t, binary, []string{"theme", "init", "--vault", vault}, nil, "theme init")
	if code != 0 || !minimal.OK || !fileExists(filepath.Join(vault, ".oxpio", "themes", "minimal", "theme.yaml")) {
		t.Fatalf("theme init minimal: code=%d envelope=%#v", code, minimal)
	}
	for _, rel := range []string{"theme.yaml", "README.md", "THEME-API.md", "partials/example.html", "assets/style.css", "layouts/base.html", "layouts/section.html", "layouts/article.html", "layouts/tag.html", "layouts/timeline.html", "layouts/404.html"} {
		if !fileExists(filepath.Join(vault, ".oxpio", "themes", "minimal", filepath.FromSlash(rel))) {
			t.Fatalf("minimal theme missing %s", rel)
		}
	}
	beforeThemeState := snapshotCLIOutput(t, vault)
	lockBeforeThemeDryRun := fileExists(filepath.Join(vault, ".oxpio", "lock"))
	dryTheme, _, code := runBlackBoxEnvelope(t, binary, []string{"theme", "init", "scratch-theme", "--vault", vault, "--dry-run"}, nil, "theme init")
	if code != 0 || !dryTheme.OK || !jsonObjectBool(t, dryTheme, "dryRun") || fileExists(filepath.Join(vault, ".oxpio", "themes", "scratch-theme")) {
		t.Fatalf("theme init dry-run: code=%d envelope=%#v", code, dryTheme)
	}
	assertNoCLIDurableMutation(t, vault)
	if fileExists(filepath.Join(vault, ".oxpio", "lock")) != lockBeforeThemeDryRun {
		t.Fatal("theme init dry-run changed workspace lock state")
	}
	assertCLIOutputUnchanged(t, vault, beforeThemeState, "theme init dry-run")

	custom, _, code := runBlackBoxEnvelope(t, binary, []string{"theme", "init", "custom", "--vault", vault}, nil, "theme init")
	if code != 0 || !custom.OK || !strings.Contains(string(readBlackBoxFile(t, filepath.Join(vault, ".oxpio", "themes", "custom", "theme.yaml"))), "name: \"custom\"") {
		t.Fatalf("theme init custom: code=%d envelope=%#v", code, custom)
	}
	nonEmpty, _, code := runBlackBoxEnvelope(t, binary, []string{"theme", "init", "custom", "--vault", vault}, nil, "theme init")
	if code != 1 || nonEmpty.OK {
		t.Fatalf("non-empty theme init: code=%d envelope=%#v", code, nonEmpty)
	}

	baseRead, _, code := runBlackBoxEnvelope(t, binary, []string{"theme", "read", ".oxpio/themes/minimal/layouts/base.html", "--vault", vault}, nil, "theme read")
	if code != 0 || !baseRead.OK {
		t.Fatalf("theme read: code=%d envelope=%#v", code, baseRead)
	}
	var themeFile struct {
		Content string `json:"content"`
		Hash    string `json:"sha256"`
		Kind    string `json:"kind"`
		Size    int    `json:"size"`
	}
	decodeEnvelopeData(t, baseRead, &themeFile)
	if themeFile.Content == "" || len(themeFile.Hash) != sha256.Size*2 || themeFile.Kind != "text" || themeFile.Size == 0 || strings.Contains(string(baseRead.Data), vault) {
		t.Fatalf("theme read data = %#v", themeFile)
	}

	pixel := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 13, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89, 0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 'B', 0x60, 0x82}
	if err := os.WriteFile(filepath.Join(vault, "pixel.png"), pixel, 0o644); err != nil {
		t.Fatal(err)
	}
	binaryThemePath := ".oxpio/themes/minimal/assets/pixel.png"
	binaryWrite, _, code := runBlackBoxEnvelope(t, binary, []string{"theme", "write", binaryThemePath, "--vault", vault, "--content-file", "pixel.png", "--if-hash", "absent"}, nil, "theme write")
	if code != 0 || !binaryWrite.OK {
		t.Fatalf("theme binary write: code=%d envelope=%#v", code, binaryWrite)
	}
	binaryRead, _, code := runBlackBoxEnvelope(t, binary, []string{"theme", "read", binaryThemePath, "--vault", vault}, nil, "theme read")
	if code != 0 || !binaryRead.OK || !jsonRawIsNull(t, binaryRead.Data, "content") || jsonObjectString(t, binaryRead, "kind") != "binary" || jsonObjectInt(t, binaryRead, "size") != len(pixel) {
		t.Fatalf("theme binary read: code=%d envelope=%#v", code, binaryRead)
	}
	rawStdout, rawStderr, rawCode := runBlackBox(t, binary, []string{"theme", "read", binaryThemePath, "--vault", vault, "--raw", "--format", "text"}, nil)
	if rawCode != 0 || rawStderr != "" || !equalBytes([]byte(rawStdout), pixel) {
		t.Fatalf("theme binary raw read: code=%d stderr=%q bytes=%v", rawCode, rawStderr, []byte(rawStdout))
	}

	customRead, _, _ := runBlackBoxEnvelope(t, binary, []string{"theme", "read", ".oxpio/themes/custom/layouts/base.html", "--vault", vault}, nil, "theme read")
	decodeEnvelopeData(t, customRead, &themeFile)
	patch := []byte(`{"edits":[{"oldText":"{{define \"base\"}}<!doctype html>","newText":"{{define \"base\"}}<meta name=\"custom-shell\" content=\"true\">\n<!doctype html>"}]}`)
	if err := os.WriteFile(filepath.Join(vault, "theme-patch.json"), patch, 0o644); err != nil {
		t.Fatal(err)
	}
	themeConflict, _, code := runBlackBoxEnvelope(t, binary, []string{"theme", "edit", ".oxpio/themes/custom/layouts/base.html", "--vault", vault, "--edits-file", "theme-patch.json", "--if-hash", strings.Repeat("0", sha256.Size*2)}, nil, "theme edit")
	if code != 3 || themeConflict.OK || len(themeConflict.Diagnostics) == 0 || themeConflict.Diagnostics[0].Code != "CONFLICT" {
		t.Fatalf("theme CAS conflict: code=%d envelope=%#v", code, themeConflict)
	}
	themeEdit, _, code := runBlackBoxEnvelope(t, binary, []string{"theme", "edit", ".oxpio/themes/custom/layouts/base.html", "--vault", vault, "--edits-file", "theme-patch.json", "--if-hash", themeFile.Hash}, nil, "theme edit")
	if code != 0 || !themeEdit.OK || !strings.Contains(string(readBlackBoxFile(t, filepath.Join(vault, ".oxpio", "themes", "custom", "layouts", "base.html"))), "custom-shell") {
		t.Fatalf("theme edit: code=%d envelope=%#v", code, themeEdit)
	}
	validateCustom, _, code := runBlackBoxEnvelope(t, binary, []string{"theme", "validate", "--vault", vault, "--theme", "custom"}, nil, "theme validate")
	if code != 0 || !validateCustom.OK || !jsonObjectBool(t, validateCustom, "valid") {
		t.Fatalf("theme validate custom: code=%d envelope=%#v", code, validateCustom)
	}

	// Config edit and config write select the custom theme explicitly.
	configRead, _, code := runBlackBoxEnvelope(t, binary, []string{"config", "read", "--vault", vault}, nil, "config read")
	if code != 0 || !configRead.OK {
		t.Fatalf("config read: code=%d envelope=%#v", code, configRead)
	}
	var configSummary struct {
		Hash string `json:"hash"`
	}
	decodeEnvelopeData(t, configRead, &configSummary)
	writeBlackBoxFile(t, vault, "config-patch.json", `{"edits":[{"oldText":"title: Site","newText":"title: Edited site"}]}`)
	configEdit, _, code := runBlackBoxEnvelope(t, binary, []string{"config", "edit", "--vault", vault, "--edits-file", "config-patch.json", "--if-hash", configSummary.Hash}, nil, "config edit")
	if code != 0 || !configEdit.OK {
		t.Fatalf("config edit: code=%d envelope=%#v", code, configEdit)
	}
	configRead, _, _ = runBlackBoxEnvelope(t, binary, []string{"config", "read", "--vault", vault}, nil, "config read")
	decodeEnvelopeData(t, configRead, &configSummary)
	writeBlackBoxFile(t, vault, "config-custom.yaml", "title: Edited site\nbaseURL: https://example.test/\ntheme: custom\nnavigation: []\n")
	configWrite, _, code := runBlackBoxEnvelope(t, binary, []string{"config", "write", "--vault", vault, "--content-file", "config-custom.yaml", "--if-hash", configSummary.Hash}, nil, "config write")
	if code != 0 || !configWrite.OK || !strings.Contains(string(readBlackBoxFile(t, filepath.Join(vault, "oxpio.yaml"))), "theme: custom") {
		t.Fatalf("config write: code=%d envelope=%#v", code, configWrite)
	}
	themeContext, _, code := runBlackBoxEnvelope(t, binary, []string{"theme", "context", "--vault", vault}, nil, "theme context")
	if code != 0 || !themeContext.OK || jsonObjectString(t, themeContext, "selectedTheme") != "custom" || strings.Contains(string(themeContext.Data), vault) || !strings.Contains(string(themeContext.Data), ".oxpio/themes/custom/") {
		t.Fatalf("theme context: code=%d envelope=%#v", code, themeContext)
	}
	selectedValidate, _, code := runBlackBoxEnvelope(t, binary, []string{"theme", "validate", "--vault", vault}, nil, "theme validate")
	if code != 0 || !selectedValidate.OK {
		t.Fatalf("selected theme validate: code=%d envelope=%#v", code, selectedValidate)
	}

	// A dry-run is isolated and a real publish consumes the selected theme. A
	// historical staging directory is intentionally left in place to prove it
	// is never treated as a candidate transaction.
	beforeCustomPublish := snapshotCLIOutput(t, output)
	staleStage := filepath.Join(filepath.Dir(output), ".oxpio-publish-stage-old")
	if err := os.MkdirAll(staleStage, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBlackBoxFile(t, staleStage, "stale.txt", "must remain")
	dryPublish, _, code := runBlackBoxEnvelope(t, binary, []string{"publish", "--vault", vault, "--dry-run"}, nil, "publish")
	if code != 0 || !dryPublish.OK || jsonObjectBool(t, dryPublish, "published") || !fileExists(filepath.Join(staleStage, "stale.txt")) {
		t.Fatalf("publish dry-run: code=%d envelope=%#v", code, dryPublish)
	}
	assertCLIOutputUnchanged(t, output, beforeCustomPublish, "publish dry-run")
	stages, stageErr := filepath.Glob(filepath.Join(vault, ".oxpio-publish-stage-*"))
	if stageErr != nil || len(stages) != 1 || stages[0] != staleStage {
		t.Fatalf("publish dry-run staging = %v, err=%v; want only historical stage", stages, stageErr)
	}
	customPublish, _, code := runBlackBoxEnvelope(t, binary, []string{"publish", "--vault", vault}, nil, "publish")
	if code != 0 || !customPublish.OK || !jsonObjectBool(t, customPublish, "published") || !strings.Contains(string(readBlackBoxFile(t, filepath.Join(output, "index.html"))), "custom-shell") {
		t.Fatalf("custom publish: code=%d envelope=%#v", code, customPublish)
	}
	repeated, _, code := runBlackBoxEnvelope(t, binary, []string{"publish", "--vault", vault}, nil, "publish")
	if code != 0 || !repeated.OK || len(repeated.Changes) != 0 || !fileExists(filepath.Join(staleStage, "stale.txt")) {
		t.Fatalf("repeated publish: code=%d changes=%#v envelope=%#v", code, repeated.Changes, repeated)
	}

	// All successful JSON commands above must use the same envelope and keep
	// implementation paths out of their machine-facing data.
	if strings.Contains(string(customPublish.Data), vault) || strings.Contains(string(repeated.Data), vault) {
		t.Fatalf("publish data leaked absolute path: %s / %s", customPublish.Data, repeated.Data)
	}

	warningVault := t.TempDir()
	writeBlackBoxFile(t, warningVault, "oxpio.yaml", "title: Warning site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeBlackBoxFile(t, warningVault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	if err := os.MkdirAll(filepath.Join(warningVault, ".oxpio"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(warningVault, ".oxpio", "cache"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	warningPublish, _, code := runBlackBoxEnvelope(t, binary, []string{"publish", "--vault", warningVault}, nil, "publish")
	if code != 0 || !warningPublish.OK || !jsonObjectBool(t, warningPublish, "postCommitWarning") || !fileExists(filepath.Join(warningVault, "public", "index.html")) {
		t.Fatalf("CLI post-commit warning: code=%d envelope=%#v", code, warningPublish)
	}
}

func TestThemePreviewUsesIsolatedRendererAndJSONEnvelope(t *testing.T) {
	vault := t.TempDir()
	writeCLIConfig(t, vault)
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	if _, _, err := executeForTest(t, defaultCommandDependencies(), []string{"theme", "init", "minimal", "--vault", vault}); err != nil {
		t.Fatal(err)
	}
	before := snapshotCLIOutput(t, vault)
	deps := testCommandDependencies()
	server := &fakePreviewServer{}
	deps.newPreviewServer = func(string, int) (previewServer, error) { return server, nil }
	stdout, stderr, err := executeForTest(t, deps, []string{"theme", "preview", "--vault", vault, "--theme", "minimal", "--port", "19191", "--format", "json"})
	if err != nil || stderr != "" {
		t.Fatalf("theme preview: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	var envelope internalagent.Envelope
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("theme preview envelope: %v (%q)", err, stdout)
	}
	if envelope.Schema != internalagent.Schema || !envelope.OK || envelope.Command != "theme preview" || server.listenCalls != 1 {
		t.Fatalf("theme preview envelope=%#v listenCalls=%d", envelope, server.listenCalls)
	}
	assertCLIOutputUnchanged(t, vault, before, "theme preview")
}

func runBlackBoxEnvelope(t *testing.T, binary string, args []string, stdin []byte, wantCommand string) (internalagent.Envelope, string, int) {
	t.Helper()
	full := append([]string(nil), args...)
	full = append(full, "--format", "json")
	stdout, stderr, code := runBlackBox(t, binary, full, stdin)
	if stderr != "" {
		t.Fatalf("JSON command %v wrote stderr: %q", args, stderr)
	}
	assertSingleEnvelope(t, stdout, wantCommand)
	var envelope internalagent.Envelope
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode %v envelope: %v", args, err)
	}
	return envelope, stdout, code
}

func decodeEnvelopeData(t *testing.T, envelope internalagent.Envelope, target any) {
	t.Helper()
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatalf("decode envelope data: %v (%s)", err, envelope.Data)
	}
}

func jsonObjectBool(t *testing.T, envelope internalagent.Envelope, field string) bool {
	t.Helper()
	var object map[string]json.RawMessage
	decodeEnvelopeData(t, envelope, &object)
	var value bool
	if err := json.Unmarshal(object[field], &value); err != nil {
		t.Fatalf("decode bool %s: %v", field, err)
	}
	return value
}

func jsonObjectString(t *testing.T, envelope internalagent.Envelope, field string) string {
	t.Helper()
	var object map[string]json.RawMessage
	decodeEnvelopeData(t, envelope, &object)
	var value string
	if err := json.Unmarshal(object[field], &value); err != nil {
		t.Fatalf("decode string %s: %v", field, err)
	}
	return value
}

func jsonObjectInt(t *testing.T, envelope internalagent.Envelope, field string) int {
	t.Helper()
	var object map[string]json.RawMessage
	decodeEnvelopeData(t, envelope, &object)
	var value int
	if err := json.Unmarshal(object[field], &value); err != nil {
		t.Fatalf("decode int %s: %v", field, err)
	}
	return value
}

func jsonRawIsNull(t *testing.T, data json.RawMessage, field string) bool {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("decode object: %v", err)
	}
	return string(object[field]) == "null"
}

func assertNoCLIDurableMutation(t *testing.T, vault string) {
	t.Helper()
	name := filepath.Join(vault, ".oxpio", "transactions", "current.json")
	if _, err := os.Lstat(name); err == nil {
		t.Fatalf("unexpected durable state %q", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat durable state %q: %v", name, err)
	}
}

func readBlackBoxFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	return sha256Hex(readBlackBoxFile(t, path))
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func equalBytes(left, right []byte) bool { return string(left) == string(right) }
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
