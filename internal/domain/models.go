package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Job struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	URL           string          `json:"url"`
	Authorization string          `json:"-"`
	Body          json.RawMessage `json:"body"`
	Cron          string          `json:"cron"`
	Timezone      string          `json:"timezone"`
	TimeoutMS     int             `json:"request_timeout_ms"`
	Enabled       bool            `json:"enabled"`
	Policy        string          `json:"policy"`
	Revision      int64           `json:"revision"`
}

type Outcome struct{ Status, Notification string }
type Run struct{ Job, Slot, Status, Notification, Started, Finished string }
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
