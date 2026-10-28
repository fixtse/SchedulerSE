package web

import (
	"crypto/rand"
	"encoding/json"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"net/url"
	"strings"
	"testing"
	"time"
)

func registerPairing(t *testing.T, f *browserFixture) (string, string, string) {
	t.Helper()
	id := "b8741730-c0b1-46ea-9bb5-f0cb1b65414d"
	secret, token := rand.Text(), "sse_"+rand.Text()
	raw, _ := json.Marshal(map[string]string{"id": id, "secret_hash": digest(secret), "token_hash": digest(token), "callback_url": "https://zero.example/v1/triggers/schedulerse-events", "callback_authorization": "Bearer " + strings.Repeat("a", 64)})
	res := agentRequest(f, "POST", "/api/pairing/v1/requests", "", string(raw))
	if res.Code != 201 {
		t.Fatalf("register %d %s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), secret) || strings.Contains(res.Body.String(), token) {
		t.Fatal("registration leaked credentials")
	}
	return id, secret, token
}
func TestZeroPairingApprovalScopePersistenceAndRevocation(t *testing.T) {
	f := fixture(t)
	f.setup(t)
	id, secret, token := registerPairing(t, f)
	if res := agentRequest(f, "GET", "/api/agent/v1/connection", token, ""); res.Code != 401 {
		t.Fatal("pending credential authorized API", res.Code)
	}
	if res := agentRequest(f, "GET", "/api/pairing/v1/requests/"+id, "wrong", ""); res.Code != 404 {
		t.Fatal("pairing existence leaked")
	}
	_, page := f.get(t, "/pairing/"+id)
	if strings.Contains(page, secret) || strings.Contains(page, token) || strings.Contains(page, strings.Repeat("a", 64)) {
		t.Fatal("approval page leaked secret")
	}
	if code, _, _ := f.post(t, "/pairing/"+id, url.Values{"action": {"approve"}}); code != 403 {
		t.Fatal("approval bypassed CSRF", code)
	}
	if code, body, _ := f.post(t, "/pairing/"+id, url.Values{"csrf": {csrf(t, page)}, "action": {"approve"}}); code != 303 {
		t.Fatalf("approve %d %s", code, body)
	}
	status := agentRequest(f, "GET", "/api/pairing/v1/requests/"+id, secret, "")
	if status.Code != 200 || !strings.Contains(status.Body.String(), `"state":"approved"`) {
		t.Fatal("approval status", status.Code, status.Body.String())
	}
	// Creating another Server over the same store keeps approved credentials.
	f.app = New(f.store, f.app.Config, func() {}, "test-bootstrap-token")
	if res := agentRequest(f, "GET", "/api/agent/v1/connection", token, ""); res.Code != 200 || strings.Contains(res.Body.String(), strings.Repeat("a", 64)) {
		t.Fatal("paired connection access or secret masking", res.Code)
	}
	for _, path := range []string{"/api/agent/v1/automations", "/api/agent/v1/tasks", "/api/agent/v1/mcp", "/api/agent/v1/inbox?conversation_id=other"} {
		if res := agentRequest(f, "GET", path, token, ""); res.Code != 403 {
			t.Fatal("paired privilege escalation", path, res.Code)
		}
	}
	if res := agentRequest(f, "PUT", "/api/agent/v1/connection", token, `{"delivery_mode":"poll"}`); res.Code != 403 {
		t.Fatal("paired token replaced delivery", res.Code)
	}
	valid := `{"id":"zero-rule-one-0","kind":"reminder","title":"Zero reminder","message":"test","after_seconds":60,"context":{"integration":"zero","rule_id":"rule-one","generation":0}}`
	if res := agentRequest(f, "POST", "/api/agent/v1/tasks", token, valid); res.Code != 201 {
		t.Fatal("paired create", res.Code, res.Body.String())
	}
	if res := agentRequest(f, "POST", "/api/agent/v1/tasks", token, strings.Replace(valid, `"integration":"zero"`, `"integration":"other"`, 1)); res.Code != 403 {
		t.Fatal("unscoped task accepted", res.Code)
	}
	if err := f.store.CreateAgentTask(domain.AgentTask{ID: "other", Kind: "reminder", Title: "Other", Context: json.RawMessage(`{}`), RunAt: time.Now().Add(time.Hour).Unix(), Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if res := agentRequest(f, "DELETE", "/api/agent/v1/tasks/other", token, ""); res.Code != 403 {
		t.Fatal("paired credential cancelled foreign task", res.Code)
	}
	if res := agentRequest(f, "GET", "/api/agent/v1/tasks/zero-rule-one-0", token, ""); res.Code != 200 {
		t.Fatal("paired inspect", res.Code)
	}
	_, page = f.get(t, "/pairing/"+id)
	if code, _, _ := f.post(t, "/pairing/"+id, url.Values{"csrf": {csrf(t, page)}, "action": {"revoke"}}); code != 303 {
		t.Fatal("revoke failed", code)
	}
	if res := agentRequest(f, "GET", "/api/agent/v1/connection", token, ""); res.Code != 401 {
		t.Fatal("revoked token accepted")
	}
	settings, _ := f.store.AgentSettings()
	task, _ := f.store.AgentTask("zero-rule-one-0")
	if settings.WebhookURL != "https://zero.example/v1/triggers/schedulerse-events" || task.Status != "pending" {
		t.Fatal("revocation changed delivery or schedules")
	}
}
func TestZeroPairingNeverTakesOverAnExistingConnection(t *testing.T) {
	for _, mode := range []string{"webhook", "poll"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			f.setup(t)
			if err := f.store.SetAgentToken(digest("existing-secret")); err != nil {
				t.Fatal(err)
			}
			if err := f.store.SaveAgentConnection(mode, "https://existing.example/hook", "Bearer existing-header"); err != nil {
				t.Fatal(err)
			}
			id, _, token := registerPairing(t, f)
			_, page := f.get(t, "/pairing/"+id)
			code, _, _ := f.post(t, "/pairing/"+id, url.Values{"csrf": {csrf(t, page)}, "action": {"approve"}})
			if code != 409 {
				t.Fatal("connection overwritten", code)
			}
			saved, _ := f.store.AgentSettings()
			if saved.DeliveryMode != mode || saved.TokenHash != digest("existing-secret") || saved.WebhookURL != "https://existing.example/hook" || saved.WebhookAuth != "Bearer existing-header" {
				t.Fatal("existing connection changed")
			}
			if res := agentRequest(f, "GET", "/api/agent/v1/connection", token, ""); res.Code != 401 {
				t.Fatal("conflicting pairing activated")
			}
		})
	}
}
func TestZeroPairingExpiresAndReturnsToRequestAfterLogin(t *testing.T) {
	f := fixture(t)
	sessionCSRF := f.setup(t)
	id, _, _ := registerPairing(t, f)
	if _, err := f.store.DB.Exec(`UPDATE agent_pairings SET expires_at=? WHERE id=?`, time.Now().Add(-time.Second).Unix(), id); err != nil {
		t.Fatal(err)
	}
	_, page := f.get(t, "/pairing/"+id)
	if !strings.Contains(page, "expired") {
		t.Fatal("expired approval offered")
	}
	if code, _, _ := f.post(t, "/pairing/"+id, url.Values{"csrf": {sessionCSRF}, "action": {"approve"}}); code != 409 {
		t.Fatal("expired request approved", code)
	}
	// A logged-out browser remembers only a validated local request ID.
	if code, _, _ := f.post(t, "/logout", url.Values{"csrf": {sessionCSRF}}); code != 303 {
		t.Fatal("logout failed")
	}
	if code, _ := f.get(t, "/pairing/"+id); code != 303 {
		t.Fatal("unauthenticated approval page accepted")
	}
	_, page = f.get(t, "/login")
	code, _, res := f.post(t, "/login", url.Values{"csrf": {csrf(t, page)}, "username": {"owner"}, "password": {"correct-horse-battery"}})
	if code != 303 || res.Header.Get("Location") != "/pairing/"+id {
		t.Fatal("login lost pairing destination", code, res.Header.Get("Location"))
	}
}
