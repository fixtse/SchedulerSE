package application

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fixtse/SchedulerSE/internal/domain"
)

// executionAlert includes operational metadata only, never payloads, script source,
// endpoint URLs (which can contain credentials), output, or authorization headers.
func executionAlert(summary string, j domain.Job, slot string, started, finished time.Time, outcome domain.Outcome) string {
	zone := time.UTC
	if j.Timezone != "" {
		if location, err := time.LoadLocation(j.Timezone); err == nil {
			zone = location
		}
	}
	trigger := "Scheduled"
	if strings.HasPrefix(slot, "manual:") {
		trigger = "Manual"
	}
	taskType := "HTTP POST"
	if j.Kind == "bash" {
		taskType = "Bash script"
	}
	result := "Success"
	if outcome.Status == "failed" {
		result = "Failed"
	}
	if outcome.Status == "fallback" {
		result = "Completed with fallback"
	}
	var text strings.Builder
	fmt.Fprintf(&text, "%s\n\nResult: %s\nTask: %s\nTask ID: %s\nType: %s\nTrigger: %s\nStarted: %s\nFinished: %s\nTimezone: %s\nExecution duration: %s\nTimeout per attempt: %s\nAttempts: %d\nTask revision: %d",
		summary, result, j.Name, j.ID, taskType, trigger,
		started.In(zone).Format(time.RFC3339), finished.In(zone).Format(time.RFC3339), zone.String(),
		finished.Sub(started).Round(time.Millisecond), time.Duration(j.TimeoutMS)*time.Millisecond, outcome.Attempts, j.Revision)
	if outcome.HTTPStatus > 0 {
		fmt.Fprintf(&text, "\nHTTP status: %d %s", outcome.HTTPStatus, http.StatusText(outcome.HTTPStatus))
	}
	if outcome.FallbackUsed {
		text.WriteString("\nFallback endpoint: used")
	}
	if domain.IsYouTube(j) && outcome.Status == "fallback" && !outcome.FallbackUsed {
		text.WriteString("\nAuthentication fallback: API key")
	}
	if outcome.ErrorCode != "" {
		fmt.Fprintf(&text, "\nError code: %s", outcome.ErrorCode)
		if explanation := failureExplanation(outcome.ErrorCode); explanation != "" {
			fmt.Fprintf(&text, "\nDetails: %s", explanation)
		}
	}
	return text.String()
}

func failureExplanation(code string) string {
	switch code {
	case "SCRIPT_FAILED":
		return "The script exited with a nonzero status."
	case "SCRIPT_START_FAILED":
		return "The script could not be started. Check Bash availability and filesystem permissions."
	case "SCRIPT_ABORTED":
		return "The script exceeded its execution timeout or was cancelled."
	case "REQUEST_ABORTED":
		return "The request exceeded its timeout or was cancelled; the endpoint may already have processed it."
	case "NETWORK_ERROR":
		return "The request could not complete, or a redirect was refused; the endpoint may already have processed it."
	case "HTTP_ERROR":
		return "The endpoint returned an unsuccessful HTTP status."
	case "INVALID_JSON":
		return "The endpoint returned an invalid JSON response."
	case "response_too_large":
		return "The response exceeded the 1 MB limit."
	case "request_failed":
		return "Execution failed without a recognized error code."
	}
	return ""
}
