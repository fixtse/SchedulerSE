package domain

import (
	"encoding/json"
	"time"
)

// AgentTask is independent of automation jobs and their execution history.
type AgentTask struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"`
	Title           string          `json:"title"`
	Message         string          `json:"message"`
	Context         json.RawMessage `json:"context"`
	RunAt           int64           `json:"run_at_unix"`
	IntervalSeconds int64           `json:"interval_seconds"`
	Status          string          `json:"status"`
	HTTPStatus      int             `json:"http_status"`
	LastError       string          `json:"last_error"`
}

type AgentSettings struct {
	TokenHash   string
	WebhookURL  string
	WebhookAuth string
}

func (t AgentTask) DueUTC() string {
	return time.Unix(t.RunAt, 0).UTC().Format("02 Jan 2006 · 15:04:05 UTC")
}
