package adapters

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fixtse/SchedulerSE/internal/domain"
)

func requireSandbox(t *testing.T) {
	t.Helper()
	if err := CheckScriptSandbox(); err != nil {
		if os.Getenv("REQUIRE_SCRIPT_SANDBOX") == "true" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
}

func TestScriptDisabledByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must-not-exist")
	_, err := NewHTTP(Config{}).Post(context.Background(), domain.Job{Kind: "bash", Script: "touch '" + path + "'"})
	if err == nil || err.Error() != "SCRIPT_DISABLED" {
		t.Fatalf("expected disabled, got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("script ran")
	}
}

func TestScriptSandboxIsolation(t *testing.T) {
	requireSandbox(t)
	dir := t.TempDir()
	fixture := filepath.Join(dir, "secret")
	if err := os.WriteFile(fixture, []byte("synthetic secret"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUDIT_SECRET", "synthetic secret")
	t.Setenv("BASH_ENV", fixture)
	script := "test -z \"${AUDIT_SECRET:-}\"; test -z \"${BASH_ENV:-}\"; test ! -e '" + fixture + "'; test ! -e /data; test ! -e /home; test ! -e /proc/" + strconv.Itoa(os.Getpid()) + "/environ; touch /tmp/ok; test -f /tmp/ok; readlink /proc/self/ns/net"
	cmd, err := scriptCommand(context.Background(), script)
	if err != nil {
		t.Fatal(err)
	}
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	host, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) == host {
		t.Fatal("host network namespace exposed")
	}
	unchanged, _ := os.ReadFile(fixture)
	if string(unchanged) != "synthetic secret" {
		t.Fatal("host file changed")
	}
}

func TestScriptSandboxKillsDetachedChildren(t *testing.T) {
	requireSandbox(t)
	for _, timeout := range []bool{false, true} {
		t.Run(strconv.FormatBool(timeout), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			tail := "sleep 0.05; exit 0"
			if timeout {
				tail = "sleep 30"
			}
			cmd, err := scriptCommand(ctx, "setsid bash -c 'sleep 1; echo escaped' </dev/null &\n"+tail)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			cmd.Stdout = &output
			started := time.Now()
			err = cmd.Run()
			if timeout && ctx.Err() == nil {
				t.Fatalf("expected timeout: %v", err)
			}
			if !timeout && err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "escaped") || time.Since(started) > 900*time.Millisecond {
				t.Fatal("detached child outlived run", output.String())
			}
		})
	}
}

func TestScriptSandboxFileBudget(t *testing.T) {
	requireSandbox(t)
	_, err := runScript(context.Background(), domain.Job{TimeoutMS: 3000, Script: "dd if=/dev/zero of=/tmp/large bs=1M count=17 status=none"})
	if err == nil {
		t.Fatal("file size budget not enforced")
	}
}

func TestSandboxProbeReportsHelperDiagnostic(t *testing.T) {
	dir := t.TempDir()
	helper := "#!/bin/sh\nprintf '%s\\n' 'bwrap: namespace denied by test policy' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "bwrap"), []byte(helper), 0700); err != nil {
		t.Fatal(err)
	}
	// The failing helper never invokes prlimit; keep this diagnostic test
	// independent of installed sandbox dependencies.
	if err := os.WriteFile(filepath.Join(dir, "prlimit"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := CheckScriptSandbox()
	if err == nil || !strings.Contains(err.Error(), "bwrap: namespace denied by test policy") {
		t.Fatalf("missing helper diagnostic: %v", err)
	}
}

func TestSandboxProbeDiagnosticIsBounded(t *testing.T) {
	var diagnostic sandboxDiagnostic
	input := []byte(strings.Repeat("x", 8192))
	for range 3 {
		if n, err := diagnostic.Write(input); n != len(input) || err != nil {
			t.Fatal("diagnostic changed writer semantics", n, err)
		}
	}
	if len(diagnostic) != 4096 {
		t.Fatal("unbounded diagnostic", len(diagnostic))
	}
}
