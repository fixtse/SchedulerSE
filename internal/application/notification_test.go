package application

import (
	"strings"
	"testing"
	"time"

	"github.com/fixtse/SchedulerSE/internal/domain"
)

func TestExecutionAlertDetails(t *testing.T) {
	started := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	job := domain.Job{ID: "daily", Name: "Daily report", Kind: "bash", Timezone: "Europe/Madrid", TimeoutMS: 5000, Revision: 3,
		URL: "https://user:secret@example.com/private?token=secret", Authorization: "private-auth", Script: "private-script", Body: []byte(`{"secret":"private-body"}`)}
	outcome := domain.Outcome{Status: "failed", ErrorCode: "SCRIPT_FAILED", Attempts: 1}
	message := executionAlert("Task failed", job, "manual:one", started, started.Add(1250*time.Millisecond), outcome)
	for _, want := range []string{"Result: Failed", "Task: Daily report", "Task ID: daily", "Type: Bash script", "Trigger: Manual", "Started: 2026-10-22T12:00:00+02:00", "Finished: 2026-10-22T12:00:01+02:00", "Timezone: Europe/Madrid", "Execution duration: 1.25s", "Timeout per attempt: 5s", "Attempts: 1", "Task revision: 3", "Error code: SCRIPT_FAILED", "nonzero status"} {
		if !strings.Contains(message, want) {
			t.Errorf("missing %q in %s", want, message)
		}
	}
	for _, secret := range []string{"secret", "private-auth", "private-script", "private-body", "HTTP status:"} {
		if strings.Contains(message, secret) {
			t.Errorf("unexpected %q in alert", secret)
		}
	}
}

func TestExecutionAlertHTTPFallback(t *testing.T) {
	started := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	message := executionAlert("Fallback completed", domain.Job{TimeoutMS: 1000, Timezone: "invalid"}, "scheduled-slot", started, started.Add(time.Second), domain.Outcome{Status: "fallback", HTTPStatus: 204, Attempts: 3, FallbackUsed: true})
	for _, want := range []string{"Result: Completed with fallback", "Type: HTTP POST", "Trigger: Scheduled", "Timezone: UTC", "HTTP status: 204 No Content", "Attempts: 3", "Fallback endpoint: used"} {
		if !strings.Contains(message, want) {
			t.Errorf("missing %q in %s", want, message)
		}
	}
	if strings.Contains(message, "Error code:") {
		t.Fatal("successful fallback reported an error")
	}
}
