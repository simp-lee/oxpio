package cli

import (
	"strings"
	"testing"
)

func TestPublicBuildCommandIsAbsent(t *testing.T) {
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"build"})
	if err == nil || !strings.Contains(err.Error(), `unknown command "build"`) {
		t.Fatalf("build error = %v, want unknown command", err)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("build output = %q / %q, want empty", stdout, stderr)
	}
}
