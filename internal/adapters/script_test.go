package adapters

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fixtse/SchedulerSE/internal/domain"
)

func TestScriptExecution(t *testing.T) {
	for _, tc := range []struct{ name, script, code string }{
		{"success", "printf done", ""},
		{"exit", "exit 7", "SCRIPT_FAILED"},
		{"pipeline", "false | true", "SCRIPT_FAILED"},
		{"errexit", "false\ntrue", "SCRIPT_FAILED"},
		{"timeout", "sleep 30 & wait", "SCRIPT_ABORTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := time.Now()
			_, err := (&HTTP{}).Post(context.Background(), domain.Job{Kind: "bash", Script: tc.script, TimeoutMS: 100})
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var failure *domain.Failure
			if !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
			if time.Since(started) > 3*time.Second {
				t.Fatal("script did not stop promptly")
			}
		})
	}
}

func TestScriptPersistence(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	job := domain.Job{ID: "script", Name: "Script", Policy: "generic", Kind: "bash", Script: "echo hello", Body: []byte(`{}`)}
	if err := store.Save(job, true); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Job(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Kind != job.Kind || saved.Script != job.Script {
		t.Fatalf("script not preserved: %+v", saved)
	}
	saved.Script = "exit 1"
	if err := store.Save(saved, false); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Job(job.ID)
	if err != nil || updated.Script != saved.Script {
		t.Fatalf("update not preserved: %+v, %v", updated, err)
	}
}

func TestScriptValidation(t *testing.T) {
	job := domain.Job{ID: "script", Name: "Script", Policy: "generic", Kind: "bash", Script: "echo hello"}
	if err := ValidateJob(job, "UTC"); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*domain.Job){
		func(j *domain.Job) { j.Script = " " },
		func(j *domain.Job) { j.Kind = "unknown" },
		func(j *domain.Job) { j.RetryCount = 1 },
		func(j *domain.Job) { j.RetryUncertain = true },
		func(j *domain.Job) { j.FallbackURL = "https://example.com" },
		func(j *domain.Job) { j.Policy = "fixtse" },
	} {
		invalid := job
		mutate(&invalid)
		if err := ValidateJob(invalid, "UTC"); err == nil {
			t.Fatalf("accepted invalid script task: %+v", invalid)
		}
	}
}
