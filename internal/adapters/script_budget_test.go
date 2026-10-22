package adapters

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScriptRequiresAggregateResourceBudget(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "service")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := checkScriptBudgetAt(root, child); err == nil {
		t.Fatal("unbounded sandbox permitted")
	}
	for name, value := range map[string]string{"memory.max": "536870912", "pids.max": "128", "cpu.max": "100000 100000"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkScriptBudgetAt(root, child); err != nil {
		t.Fatal("ancestor limits ignored", err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory.max"), []byte("max"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkScriptBudgetAt(root, child); err == nil {
		t.Fatal("unbounded memory permitted")
	}
}
