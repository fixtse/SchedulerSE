package adapters

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Shell support requires an externally enforced aggregate budget as well as
// per-process rlimits. Inspect ancestors to support delegated/systemd cgroups.
func CheckScriptResourceBudget() error {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return errors.New("Bash requires bounded cgroup v2 resources")
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "0::/") {
			root := "/sys/fs/cgroup"
			path := filepath.Join(root, strings.TrimPrefix(line, "0::"))
			return checkScriptBudgetAt(root, path)
		}
	}
	return errors.New("Bash requires bounded cgroup v2 resources")
}

func checkScriptBudgetAt(root, path string) error {
	root, path = filepath.Clean(root), filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return errors.New("invalid cgroup path")
	}
	memory, pids, cpu := false, false, false
	for {
		for filename, ceiling := range map[string]uint64{"memory.max": 512 << 20, "pids.max": 128} {
			raw, err := os.ReadFile(filepath.Join(path, filename))
			if err != nil {
				continue
			}
			value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
			if err == nil && value > 0 && value <= ceiling {
				if filename == "memory.max" {
					memory = true
				} else {
					pids = true
				}
			}
		}
		raw, err := os.ReadFile(filepath.Join(path, "cpu.max"))
		if err == nil {
			parts := strings.Fields(string(raw))
			if len(parts) == 2 {
				quota, e1 := strconv.ParseUint(parts[0], 10, 64)
				period, e2 := strconv.ParseUint(parts[1], 10, 64)
				if e1 == nil && e2 == nil && quota > 0 && period > 0 && quota <= period {
					cpu = true
				}
			}
		}
		if path == root {
			break
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	if !memory || !pids || !cpu {
		return errors.New("Bash requires cgroup v2 limits: memory <=512 MiB, PIDs <=128 and CPU <=1 core")
	}
	return nil
}
