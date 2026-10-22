package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckBashDoesNotLoadServiceStateOrRequireEnablement(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "must-not-create")
	t.Setenv("DATA_DIR", state)
	t.Setenv("ENABLE_BASH", "true")
	t.Setenv("ENABLE_AGENT_BASH", "true")
	t.Setenv("TELEGRAM_API_URL", "invalid-config-must-not-be-loaded")
	t.Setenv("PATH", dir) // No sandbox tools; the check must report actionable failures.
	out, err := os.CreateTemp(dir, "output")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	args, stdout := os.Args, os.Stdout
	defer func() { os.Args, os.Stdout = args, stdout }()
	os.Args, os.Stdout = []string{"schedulerse", "check-bash"}, out
	err = run()
	if !errors.Is(err, errBashRequirements) {
		t.Fatalf("expected requirement failure, got %v", err)
	}
	data, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PASS Linux", "FAIL required tools", "resource limits", "SKIP sandbox test", "apt-get install", "Bash is not ready"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %q in %s", want, data)
		}
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("checker accessed writable service state", err)
	}
}
