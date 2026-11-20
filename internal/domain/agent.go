package domain

import (
	"encoding/json"
	"time"
)

// AgentTask is independent of automation jobs and their execution history.
type AgentTask struct {
	ID                   string          `json:"id"`
	Kind                 string          `json:"kind"`
	Title                string          `json:"title"`
	Message              string          `json:"message"`
	Context              json.RawMessage `json:"context"`
	RunAt                int64           `json:"run_at_unix"`
	IntervalSeconds      int64           `json:"interval_seconds"`
	CheckIntervalSeconds int64           `json:"check_interval_seconds,omitempty"`
	CompletionRequested  bool            `json:"completion_requested,omitempty"`
	ArchivedAt           int64           `json:"archived_at_unix,omitempty"`
	Status               string          `json:"status"`
	HTTPStatus           int             `json:"http_status"`
	LastError            string          `json:"last_error"`
}

type AgentSettings struct {
	DeliveryMode string
	TokenHash    string
	WebhookURL   string
	WebhookAuth  string
}

func (t AgentTask) DueUTC() string {
	return time.Unix(t.RunAt, 0).UTC().Format("02 Jan 2006 · 15:04:05 UTC")
}

// AgentEvent identifies a single occurrence, including recurring schedules.
type AgentEvent struct {
	Event       string    `json:"event"`
	EventID     string    `json:"event_id"`
	Phase       string    `json:"phase,omitempty"`
	ScheduledAt string    `json:"scheduled_at"`
	Task        AgentTask `json:"task"`
}

func (t AgentTask) Event() AgentEvent {
	at := time.Unix(t.RunAt, 0).UTC().Format(time.RFC3339)
	var phase string
	if t.CheckIntervalSeconds > 0 {
		phase = "check"
		if t.CompletionRequested {
			phase = "completion"
		}
	}
	return AgentEvent{Phase: phase, Event: "schedule.due", EventID: t.ID + ":" + at, ScheduledAt: at, Task: t}
}
