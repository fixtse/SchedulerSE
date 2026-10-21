package adapters

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/fixtse/SchedulerSE/internal/domain"
)

// Script output is deliberately discarded: scripts may print credentials.
func runScript(ctx context.Context, j domain.Job) (domain.Response, error) {
	if j.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(j.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	dir, err := os.MkdirTemp("", "schedulerse-script-")
	if err != nil {
		return domain.Response{}, &domain.Failure{Code: "SCRIPT_START_FAILED"}
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, "bash", "--noprofile", "--norc", "-e", "-o", "pipefail")
	cmd.Stdin = strings.NewReader(j.Script)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	if err = cmd.Start(); err != nil {
		return domain.Response{}, &domain.Failure{Code: "SCRIPT_START_FAILED"}
	}
	err = cmd.Wait()
	// Do not leave background children running after the script exits.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if ctx.Err() != nil {
		return domain.Response{}, &domain.Failure{Code: "SCRIPT_ABORTED"}
	}
	if err != nil {
		return domain.Response{}, &domain.Failure{Code: "SCRIPT_FAILED"}
	}
	return domain.Response{}, nil
}
