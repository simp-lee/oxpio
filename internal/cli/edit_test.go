package cli

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	internalapplication "github.com/simp-lee/oxpio/internal/application"
	internalbuild "github.com/simp-lee/oxpio/internal/build"
	internalconfig "github.com/simp-lee/oxpio/internal/config"
	"github.com/simp-lee/oxpio/internal/model"
)

func TestEditorCommandKeepsWebEditorSeparateFromAgentEdit(t *testing.T) {
	cmd := newRootCommand(testCommandDependencies())
	if command, _, err := cmd.Find([]string{"editor"}); err != nil || command == nil {
		t.Fatalf("editor command lookup = %v, %#v", err, command)
	}
	if _, _, err := executeForTest(t, testCommandDependencies(), []string{"edit", "--setup"}); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("Agent edit accepted editor setup flag: %v", err)
	}
}

func TestEditorRejectsInvalidPortBeforeStartingServer(t *testing.T) {
	_, _, err := executeForTest(t, testCommandDependencies(), []string{"editor", "--port", "0"})
	if err == nil || !strings.Contains(err.Error(), "port must be between") {
		t.Fatalf("editor port error = %v", err)
	}
}

func TestEditorPostCommitListenerFailureDoesNotEmitSecondEnvelope(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	hash, err := internalconfig.HashArgon2idPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("title: Site\nbaseURL: https://example.test/\nnavigation: []\neditor:\n  username: admin\n  passwordHash: %s\n", hash)
	if err := os.WriteFile(filepath.Join(vault, defaultConfigFilename), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	deps := testCommandDependencies()
	setTestPublishDependency(&deps, func(_ string, output string, _ internalapplication.PublishOptions) (*internalapplication.PublishResult, error) {
		return &internalapplication.PublishResult{OutputPath: output, Published: true, Build: &internalbuild.BuildResult{Catalog: &model.SourceCatalog{}}}, nil
	})
	deps.newEditServer = func(string, string, int, string, *model.SourceCatalog) (previewServer, error) {
		return &fakePreviewServer{listenErr: errors.New("listener failed")}, nil
	}
	stdout, stderr, executeErr := executeForTest(t, deps, []string{"editor", "--vault", vault, "--output", output, "--port", strconv.Itoa(port), "--format", "json"})
	if executeErr != nil {
		t.Fatalf("listener failure error = %v", executeErr)
	}
	if strings.Count(stdout, `"schema":"oxpio.agent/v1"`) != 1 || !strings.Contains(stderr, "POST_COMMIT_WARNING") {
		t.Fatalf("listener warning output = stdout %q stderr %q", stdout, stderr)
	}
}

func TestEditorPostCommitReadyFailureIsSuccessfulWarningEnvelope(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	hash, err := internalconfig.HashArgon2idPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("title: Site\nbaseURL: https://example.test/\nnavigation: []\neditor:\n  username: admin\n  passwordHash: %s\n", hash)
	if err := os.WriteFile(filepath.Join(vault, defaultConfigFilename), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	deps := testCommandDependencies()
	setTestPublishDependency(&deps, func(_ string, output string, _ internalapplication.PublishOptions) (*internalapplication.PublishResult, error) {
		return &internalapplication.PublishResult{OutputPath: output, Published: true, Build: &internalbuild.BuildResult{Catalog: &model.SourceCatalog{}}}, nil
	})
	deps.newEditServer = func(string, string, int, string, *model.SourceCatalog) (previewServer, error) {
		return &fakeReadyEditorServer{}, nil
	}
	stdout, _, executeErr := executeForTest(t, deps, []string{"editor", "--vault", vault, "--output", output, "--port", strconv.Itoa(port), "--format", "json"})
	if executeErr != nil {
		t.Fatalf("ready failure error = %v", executeErr)
	}
	if !strings.Contains(stdout, `"ok":true`) || !strings.Contains(stdout, "POST_COMMIT_WARNING") || !strings.Contains(stdout, `"published":true`) {
		t.Fatalf("ready failure warning envelope = %s", stdout)
	}
}

type fakeReadyEditorServer struct{}

func (*fakeReadyEditorServer) EnableLiveReload()     {}
func (*fakeReadyEditorServer) ListenAndServe() error { return errors.New("unexpected listen call") }
func (*fakeReadyEditorServer) ListenAndServeWithReady(func() error) error {
	return errors.New("listener bind failed")
}
func (*fakeReadyEditorServer) NotifyReload() {}

func TestEditorPostCommitStartFailureIsSuccessfulWarningEnvelope(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	hash, err := internalconfig.HashArgon2idPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("title: Site\nbaseURL: https://example.test/\nnavigation: []\neditor:\n  username: admin\n  passwordHash: %s\n", hash)
	if err := os.WriteFile(filepath.Join(vault, defaultConfigFilename), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	deps := testCommandDependencies()
	setTestPublishDependency(&deps, func(_ string, output string, _ internalapplication.PublishOptions) (*internalapplication.PublishResult, error) {
		return &internalapplication.PublishResult{OutputPath: output, Published: true, Build: &internalbuild.BuildResult{Catalog: &model.SourceCatalog{}, OutputCleanupError: errors.New("cleanup warning")}}, nil
	})
	stdout, _, executeErr := executeForTest(t, deps, []string{"editor", "--vault", vault, "--output", output, "--port", strconv.Itoa(port), "--format", "json"})
	if executeErr != nil {
		t.Fatalf("post-commit editor start error = %v", executeErr)
	}
	if !strings.Contains(stdout, `"ok":true`) || !strings.Contains(stdout, "POST_COMMIT_WARNING") || !strings.Contains(stdout, "cleanup warning") || !strings.Contains(stdout, `"published":true`) {
		t.Fatalf("post-commit warning envelope = %s", stdout)
	}
}
