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

func TestLocalPollingPickupIsDurableAndIdempotent(t *testing.T) {
	store := testStore(t)
	settings, err := store.AgentSettings()
	if err != nil || settings.DeliveryMode != "poll" {
		t.Fatalf("new install: %+v %v", settings, err)
	}
	calls := 0
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(204) }))
	defer receiver.Close()
	if err = store.SaveAgentConnection("poll", receiver.URL, "Bearer unused"); err != nil {
		t.Fatal(err)
	}
	task := domain.AgentTask{ID: "local-pickup", Kind: "timer", Title: "Local reminder", Context: json.RawMessage(`{"conversation_id":"local-thread"}`), RunAt: time.Now().Unix() - 125, IntervalSeconds: 60}
	if err = store.CreateAgentTask(task); err != nil {
		t.Fatal(err)
	}
	store.deliverAgent(context.Background(), NewHTTP(Config{}))
	if calls != 0 {
		t.Fatal("polling made an outbound callback")
	}
	events, err := store.AgentInbox("local-thread")
	if err != nil || len(events) != 1 || events[0].EventID != task.Event().EventID {
		t.Fatalf("inbox: %+v %v", events, err)
	}
	other, err := store.AgentInbox("other-thread")
	if err != nil || len(other) != 0 {
		t.Fatal("inbox lost routing filter", err)
	}
	for _, consumer := range []string{"consumer-one", "consumer-one"} {
		ok, err := store.PickupAgentTask(task.ID, task.RunAt, consumer)
		if err != nil || !ok {
			t.Fatal("pickup or retry failed", err)
		}
	}
	ok, err := store.PickupAgentTask(task.ID, task.RunAt, "consumer-two")
	if err != nil || ok {
		t.Fatal("different consumer picked up same occurrence", err)
	}
	saved, err := store.AgentTask(task.ID)
	if err != nil || saved.RunAt <= time.Now().Unix() || saved.Status != "pending" {
		t.Fatalf("recurrence: %+v %v", saved, err)
	}
	if err = store.SaveAgentConnection("webhook", receiver.URL, ""); err != nil {
		t.Fatal(err)
	}
	ok, err = store.PickupAgentTask(task.ID, task.RunAt, "consumer-one")
	if err != nil || !ok {
		t.Fatal("lost response could not be confirmed after mode switch", err)
	}
	ok, err = store.PickupAgentTask(task.ID, saved.RunAt, "consumer-one")
	if err != nil || ok {
		t.Fatal("new pickup allowed in webhook mode", err)
	}
}

func TestLocalPollingRejectsCancelledAndFutureOccurrences(t *testing.T) {
	store := testStore(t)
	for _, name := range []string{"cancelled", "future", "one-shot"} {
		task := domain.AgentTask{ID: name, Kind: "timer", Title: name, Context: json.RawMessage(`{"conversation_id":"local"}`), RunAt: time.Now().Unix() - 1}
		if name == "future" {
			task.RunAt += 3600
		}
		if err := store.CreateAgentTask(task); err != nil {
			t.Fatal(err)
		}
		if name == "cancelled" {
			if ok, err := store.CancelAgentTask(name); err != nil || !ok {
				t.Fatal("cancel failed", err)
			}
		}
		ok, err := store.PickupAgentTask(name, task.RunAt, "consumer-one")
		if err != nil || ok != (name == "one-shot") {
			t.Fatalf("pickup %s: %v %v", name, ok, err)
		}
	}
	events, err := store.AgentInbox("local")
	if err != nil || len(events) != 0 {
		t.Fatal("inbox returned consumed or future events", err)
	}
	task, err := store.AgentTask("one-shot")
	if err != nil || task.Status != "delivered" || task.HTTPStatus != 0 {
		t.Fatalf("pickup status: %+v %v", task, err)
	}
}

func TestAgentModeMigrationPreservesWebhookConnections(t *testing.T) {
	store := testStore(t)
	_, err := store.DB.Exec(`DROP TABLE agent_settings;
 CREATE TABLE agent_settings(id INTEGER PRIMARY KEY,token_hash TEXT NOT NULL,webhook_url TEXT NOT NULL,webhook_auth TEXT NOT NULL);
 INSERT INTO agent_settings VALUES(1,'saved-hash','https://example.com/hook','Bearer saved-header')`)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrateAgent(store.DB); err != nil {
		t.Fatal(err)
	}
	settings, err := store.AgentSettings()
	if err != nil || settings.DeliveryMode != "webhook" || settings.WebhookURL != "https://example.com/hook" || settings.TokenHash != "saved-hash" || settings.WebhookAuth != "Bearer saved-header" {
		t.Fatalf("migration changed connection: %+v %v", settings, err)
	}
}
