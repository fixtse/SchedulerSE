package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fixtse/SchedulerSE/internal/adapters"
	"github.com/fixtse/SchedulerSE/internal/application"
	"github.com/fixtse/SchedulerSE/internal/domain"
)

func TestAgentAutomationCreationAndExecution(t *testing.T) {
	f := fixture(t)
	if err := f.store.SetAgentToken(digest("agent-secret")); err != nil {
		t.Fatal(err)
	}
	calls := make(chan string, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls <- r.Header.Get("Authorization")
		w.WriteHeader(204)
	}))
	defer endpoint.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); f.app.Wait() }()
	transport := adapters.NewHTTP(adapters.Config{Timeout: time.Second})
	f.app.Runner = &application.Runner{Store: f.store, Sync: transport, Notifications: &adapters.Telegram{Store: f.store, HTTP: transport}, Log: silentLog{}, Timeout: time.Second}
	f.app.Context = ctx
	input, _ := json.Marshal(map[string]any{"id": "agent-post", "name": "Agent POST", "kind": "http", "url": endpoint.URL, "authorization": "Bearer endpoint-secret", "body": map[string]any{"value": true}})
	for _, token := range []string{"", "wrong"} {
		if res := agentRequest(f, "POST", "/api/agent/v1/automations", token, string(input)); res.Code != 401 {
			t.Fatal(res.Code)
		}
	}
	res := agentRequest(f, "POST", "/api/agent/v1/automations", "agent-secret", string(input))
	if res.Code != 201 || strings.Contains(res.Body.String(), "endpoint-secret") {
		t.Fatalf("create: %d %s", res.Code, res.Body.String())
	}
	var saved domain.Job
	if err := json.Unmarshal(res.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || saved.Enabled {
		t.Fatal("incorrect defaults", saved)
	}
	if res := agentRequest(f, "POST", "/api/agent/v1/automations", "agent-secret", string(input)); res.Code != 409 {
		t.Fatal("duplicate accepted", res.Code)
	}
	run := `{"revision":1,"run_token":"agent_execution_token_1234567890"}`
	if res := agentRequest(f, "POST", "/api/agent/v1/automations/agent-post/run", "agent-secret", run); res.Code != 202 {
		t.Fatal(res.Code, res.Body.String())
	}
	select {
	case auth := <-calls:
		if auth != "Bearer endpoint-secret" {
			t.Fatal("saved authorization missing")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("automation did not run")
	}
	if res := agentRequest(f, "POST", "/api/agent/v1/automations/agent-post/run", "agent-secret", run); res.Code != 409 {
		t.Fatal("duplicate run accepted", res.Code)
	}
	for _, path := range []string{"", "/agent-post"} {
		res := agentRequest(f, "GET", "/api/agent/v1/automations"+path, "agent-secret", "")
		if res.Code != 200 || strings.Contains(res.Body.String(), "endpoint-secret") {
			t.Fatal("read failed or leaked authorization", res.Body.String())
		}
	}
}

func TestAgentBashAutomationValidation(t *testing.T) {
	f := fixture(t)
	if err := f.store.SetAgentToken(digest("agent-secret")); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"id":"empty","name":"Empty","kind":"bash","script":""}`,
		`{"id":"retry","name":"Retry","kind":"bash","script":"true","retry_count":1}`,
		`{"id":"unknown","name":"Unknown","kind":"other"}`,
		`{"id":"extra","name":"Extra","unknown":true}`,
	} {
		if res := agentRequest(f, "POST", "/api/agent/v1/automations", "agent-secret", body); res.Code != 400 {
			t.Fatalf("invalid input accepted: %d %s", res.Code, body)
		}
	}
	res := agentRequest(f, "POST", "/api/agent/v1/automations", "agent-secret", `{"id":"script","name":"Script","kind":"bash","script":"echo hello","cron":"0 9 * * *","timezone":"UTC","enabled":true}`)
	if res.Code != 201 {
		t.Fatal(res.Code, res.Body.String())
	}
	saved, err := f.store.Job("script")
	if err != nil || saved.Kind != "bash" || saved.Script != "echo hello" || !saved.Enabled {
		t.Fatal("script not saved", saved, err)
	}
}
