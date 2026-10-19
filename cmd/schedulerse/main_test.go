package main

import (
	"encoding/json"
	"github.com/fixtse/SchedulerSE/internal/adapters"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"os"
	"testing"
)

func TestPreviewEmptyAndPersistedTasks(t *testing.T) {
	for _, saved := range []bool{false, true} {
		name := "empty"
		if saved {
			name = "persisted"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if saved {
				s, err := adapters.OpenStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				err = s.Save(domain.Job{ID: "existing", Name: "Existing", URL: "http://127.0.0.1/mock", Authorization: "Bearer test-only", Body: []byte(`{"count":1}`), Cron: "0 9 * * *", Timezone: "America/Lima", Enabled: true, Policy: "generic", TimeoutMS: 300000}, true)
				s.DB.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			output, err := os.CreateTemp(t.TempDir(), "plan")
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			originalArgs, originalStdout := os.Args, os.Stdout
			defer func() { os.Args, os.Stdout = originalArgs, originalStdout }()
			os.Args, os.Stdout = []string{"schedulerse", "plan"}, output
			if err = preview(adapters.Config{DataDir: dir, Timezone: "Europe/Madrid"}); err != nil {
				t.Fatal(err)
			}
			if _, err = output.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			var plan struct {
				SchedulerEnabled bool
				Jobs             []struct {
					ID, Cron, Timezone string
					Enabled            bool
					RequestTimeoutMS   int
				}
			}
			if err = json.NewDecoder(output).Decode(&plan); err != nil {
				t.Fatal(err)
			}
			if !plan.SchedulerEnabled {
				t.Fatal("scheduler must always be enabled in the plan")
			}
			if !saved && (plan.Jobs == nil || len(plan.Jobs) != 0) {
				t.Fatal("fresh plan must contain an empty task list")
			}
			if saved && (len(plan.Jobs) != 1 || plan.Jobs[0].ID != "existing" || plan.Jobs[0].Cron != "0 9 * * *" || plan.Jobs[0].Timezone != "America/Lima" || !plan.Jobs[0].Enabled || plan.Jobs[0].RequestTimeoutMS != 300000) {
				t.Fatal("persisted task missing or changed in plan")
			}
			os.Args = []string{"schedulerse", "dry-run", "missing"}
			if err = preview(adapters.Config{DataDir: dir, Timezone: "Europe/Madrid"}); err == nil {
				t.Fatal("unknown task accepted")
			}
		})
	}
}
