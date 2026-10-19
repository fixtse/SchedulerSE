package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fixtse/SchedulerSE/internal/domain"
)

func TestAgentDeliveryAndRecurrence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		interval int64
		want     string
	}{
		{"one-shot", 204, 0, "delivered"},
		{"repeat", 200, 60, "pending"},
		{"failure-stops-repeat", 503, 60, "failed"},
		{"redirect-rejected", 307, 0, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := testStore(t)
			var calls atomic.Int32
			task := domain.AgentTask{ID: "agent-one", Kind: "reminder", Title: "Water plants", Context: json.RawMessage(`{"conversation_id":"chat-123"}`), RunAt: time.Now().Unix() - 125, IntervalSeconds: tc.interval}
			receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer receiver-secret" {
					t.Error("callback credentials or method missing")
				}
				var event struct {
					Event   string           `json:"event"`
					EventID string           `json:"event_id"`
					Task    domain.AgentTask `json:"task"`
				}
				if json.NewDecoder(r.Body).Decode(&event) != nil {
					t.Error("invalid callback")
				}
				if event.Event != "schedule.due" || event.EventID != "agent-one:"+time.Unix(task.RunAt, 0).UTC().Format(time.RFC3339) || string(event.Task.Context) != string(task.Context) {
					t.Errorf("callback lost routing context: %+v", event)
				}
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(tc.status)
			}))
			defer receiver.Close()
			if err := store.SaveAgentWebhook(receiver.URL, "Bearer receiver-secret"); err != nil {
				t.Fatal(err)
			}
			if err := store.CreateAgentTask(task); err != nil {
				t.Fatal(err)
			}
			transport := NewHTTP(Config{})
			store.deliverAgent(context.Background(), transport)
			saved, err := store.AgentTask(task.ID)
			if err != nil || saved.Status != tc.want || saved.HTTPStatus != tc.status {
				t.Fatalf("saved=%+v error=%v", saved, err)
			}
			if tc.want == "pending" && (saved.RunAt <= time.Now().Unix() || (saved.RunAt-task.RunAt)%tc.interval != 0) {
				t.Fatal("recurrence did not skip missed slots")
			}
			store.deliverAgent(context.Background(), transport)
			if calls.Load() != 1 {
				t.Fatal("delivery was retried or redirected")
			}
			jobs, err := store.Jobs()
			if err != nil || len(jobs) != 0 {
				t.Fatal("agent schedule appeared in automations")
			}
		})
	}
}

func TestAgentCancelPauseAndRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	task := domain.AgentTask{ID: "cancel-me", Kind: "timer", Title: "Timer", Context: json.RawMessage(`{}`), RunAt: time.Now().Unix() - 1}
	if err = store.CreateAgentTask(task); err != nil {
		t.Fatal(err)
	}
	store.deliverAgent(context.Background(), NewHTTP(Config{})) // No configured webhook: remain pending.
	saved, _ := store.AgentTask(task.ID)
	if saved.Status != "pending" {
		t.Fatal("missing webhook consumed task")
	}
	ok, err := store.CancelAgentTask(task.ID)
	if err != nil || !ok {
		t.Fatal("cancel failed", err)
	}
	task.ID = "interrupted"
	if err = store.CreateAgentTask(task); err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB.Exec(`UPDATE agent_tasks SET status='delivering' WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	task.ID = "pending"
	if err = store.CreateAgentTask(task); err != nil {
		t.Fatal(err)
	}
	if err = store.DB.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	if err = store.Recover(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"cancel-me": "cancelled", "interrupted": "interrupted", "pending": "pending"} {
		saved, err = store.AgentTask(id)
		if err != nil || saved.Status != want {
			t.Fatalf("restart %s: %+v %v", id, saved, err)
		}
	}
}
