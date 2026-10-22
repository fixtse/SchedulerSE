package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"

	"github.com/fixtse/SchedulerSE/internal/adapters"
)

var errBashRequirements = errors.New("Bash requirements are not met")

// Use the same checks as startup, without loading configuration, opening the
// database, or enabling Bash. Running inside Compose tests the actual container
// permissions and resource limits rather than the host's unrelated settings.
func checkBash(w io.Writer) error {
	fmt.Fprintln(w, "Checking Bash support (no settings will be changed)...")
	if runtime.GOOS != "linux" {
		fmt.Fprintln(w, "FAIL Linux: run the check inside the SchedulerSE Linux container.")
		return errBashRequirements
	}
	fmt.Fprintln(w, "PASS Linux")
	var missing []string
	for _, tool := range []string{"/bin/bash", "bwrap", "prlimit"} {
		if _, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	ready := true
	if len(missing) > 0 {
		ready = false
		fmt.Fprintf(w, "FAIL required tools: missing %s\n", strings.Join(missing, ", "))
		fmt.Fprintln(w, "  Docker: update the SchedulerSE image; these tools are included.")
		fmt.Fprintln(w, "  Native Ubuntu/Debian: sudo apt-get update && sudo apt-get install -y bash bubblewrap util-linux")
	} else {
		fmt.Fprintln(w, "PASS required tools (Bash, Bubblewrap, prlimit)")
	}
	if err := adapters.CheckScriptResourceBudget(); err != nil {
		ready = false
		fmt.Fprintf(w, "FAIL resource limits: %v\n", err)
		fmt.Fprintln(w, "  Docker: apply the memory, CPU and PID limits from compose.yaml, then recreate the container.")
		fmt.Fprintln(w, "  Native: configure the systemd limits shown in docs/bash-scripts.md.")
	} else {
		fmt.Fprintln(w, "PASS resource limits")
	}
	if len(missing) > 0 {
		fmt.Fprintln(w, "SKIP sandbox test: install the missing tools first.")
	} else if err := adapters.CheckScriptSandbox(); err != nil {
		ready = false
		fmt.Fprintf(w, "FAIL sandbox protection: %v\n", err)
		fmt.Fprintln(w, "  Check the Bubblewrap AppArmor policy on Ubuntu, or the container host's namespace policy.")
		fmt.Fprintln(w, "  Keep Bash disabled if the required protection is unavailable.")
	} else {
		fmt.Fprintln(w, "PASS sandbox protection (service network access enabled)")
	}
	if !ready {
		fmt.Fprintln(w, "Bash is not ready. Setup help: docs/bash-scripts.md")
		return errBashRequirements
	}
	fmt.Fprintln(w, "Bash is ready. Enable ENABLE_BASH in your service settings when needed.")
	return nil
}
