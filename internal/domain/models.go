package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Job struct {
	Kind           string          `json:"kind"`
	Script         string          `json:"script"`
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	URL            string          `json:"url"`
	Authorization  string          `json:"-"`
	Body           json.RawMessage `json:"body"`
	Cron           string          `json:"cron"`
	Timezone       string          `json:"timezone"`
	TimeoutMS      int             `json:"request_timeout_ms"`
	RetryCount     int             `json:"retry_count"`
	RetryDelayMS   int             `json:"retry_delay_ms"`
	RetryUncertain bool            `json:"retry_uncertain"`
	FallbackURL    string          `json:"fallback_url"`
	FallbackAuth   string          `json:"-"`
	Enabled        bool            `json:"enabled"`
	Policy         string          `json:"policy"`
	Revision       int64           `json:"revision"`
}

type Response struct {
	AuthMethod string
	HTTPStatus int
}

type Outcome struct {
	Status, Notification, ErrorCode string
	HTTPStatus                      int
	DurationMS                      int64
	Attempts                        int
	FallbackUsed                    bool
}

type Run struct {
	Job          string `json:"job"`
	Slot         string `json:"slot"`
	Status       string `json:"status"`
	Notification string `json:"notification"`
	Started      string `json:"started"`
	Finished     string `json:"finished"`
	JobName      string `json:"job_name"`
	ErrorCode    string `json:"error_code"`
	JobRevision  int64  `json:"job_revision"`
	DurationMS   int64  `json:"duration_ms"`
	HTTPStatus   int    `json:"http_status"`
	TimeoutMS    int    `json:"timeout_ms"`
	Attempts     int    `json:"attempts"`
	FallbackUsed bool   `json:"fallback_used"`
}

type Failure struct {
	Code   string
	Status int
}

func (f *Failure) Error() string {
	if f.Status != 0 {
		return fmt.Sprintf("HTTP %d", f.Status)
	}
	return f.Code
}
func ErrorAlert(j Job, f *Failure) string {
	switch j.Policy {
	case "magnaweb":
		status := ""
		if f.Status != 0 {
			status = fmt.Sprint(f.Status)
		}
		return "Error Running Cron MagnaWeb\n" + status + " " + f.Code
	case "fixtse":
		return "Error Running Cron FixtSE\n" + f.Error()
	case "youtube-full", "youtube-incremental":
		return "Error actualizando Videos Paradigma\n" + f.Error()
	default:
		return "Error Running Cron " + j.Name + "\n" + f.Error()
	}
}
func FallbackAlert(j Job) string {
	prefix := "Error actualizando Videos Paradigma"
	if j.Policy == "youtube-full" {
		prefix = "Error Full Sync actualizando Videos Paradigma"
	}
	return prefix + "\nFallback to api-key"
}
func IsYouTube(j Job) bool { return strings.HasPrefix(j.Policy, "youtube-") }
