package adapters

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/fixtse/SchedulerSE/internal/domain"
)

// No host root, /data, home, environment, sockets, or network are exposed.
// PID 1 exiting destroys the whole PID namespace, including setsid descendants.
func sandboxArgs(limit string) []string {
	args := []string{"--unshare-all", "--unshare-user", "--disable-userns", "--die-with-parent", "--new-session", "--clearenv", "--cap-drop", "ALL"}
	for _, path := range []string{"/usr/bin", "/usr/lib", "/usr/lib64", "/bin", "/lib", "/lib64"} {
		if _, err := os.Stat(path); err == nil {
			args = append(args, "--ro-bind", path, path)
		}
	}
	return append(args, "--proc", "/proc", "--dev", "/dev", "--size", "16777216", "--tmpfs", "/tmp", "--remount-ro", "/", "--chdir", "/tmp", "--setenv", "PATH", "/usr/bin:/bin", "--setenv", "HOME", "/tmp", "--as-pid-1", "--", limit, "--as=268435456", "--cpu=30", "--fsize=16777216", "--nofile=128", "--nproc=64", "--", "/bin/bash", "--noprofile", "--norc", "-e", "-o", "pipefail")
}

func scriptCommand(ctx context.Context, script string) (*exec.Cmd, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, err
	}
	limit, err := exec.LookPath("prlimit")
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bwrap, sandboxArgs(limit)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	cmd.Dir = "/"
	cmd.Stdin = strings.NewReader(script)
	cmd.WaitDelay = time.Second
	return cmd, nil
}

// Probe the same isolation and resource limits used for real executions. Never
// fall back to executing Bash directly when user namespaces are unavailable.
func CheckScriptSandbox() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, err := scriptCommand(ctx, "true")
	if err != nil {
		return errors.New("Bash requires bubblewrap and prlimit")
	}
	if cmd.Run() != nil {
		return errors.New("Bash sandbox unavailable: check unprivileged user namespaces and container policy")
	}
	return nil
}

// Output is discarded. Every run has a hard deadline even outside the Runner.
func runScript(ctx context.Context, j domain.Job) (domain.Response, error) {
	timeout := 120 * time.Second
	if j.TimeoutMS > 0 && j.TimeoutMS <= 600000 {
		timeout = time.Duration(j.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd, err := scriptCommand(ctx, j.Script)
	if err != nil {
		return domain.Response{}, &domain.Failure{Code: "SCRIPT_SANDBOX_UNAVAILABLE"}
	}
	err = cmd.Run()
	if ctx.Err() != nil {
		return domain.Response{}, &domain.Failure{Code: "SCRIPT_ABORTED"}
	}
	if err != nil {
		return domain.Response{}, &domain.Failure{Code: "SCRIPT_FAILED"}
	}
	return domain.Response{}, nil
}
