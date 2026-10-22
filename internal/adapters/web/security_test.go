package web

import (
	"net"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/fixtse/SchedulerSE/internal/domain"
)

func TestAuthLimitsArePerSourceAndIgnoreUntrustedForwarding(t *testing.T) {
	f := fixture(t)
	req := httptest.NewRequest("POST", "/login", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	for range 5 {
		if !f.app.allowAuth(req) {
			t.Fatal("early limit")
		}
	}
	req.Header.Set("X-Forwarded-For", "198.51.100.1")
	if f.app.allowAuth(req) {
		t.Fatal("spoofed forwarding bypassed limit")
	}
	req.RemoteAddr = "192.0.2.2:3456"
	if !f.app.allowAuth(req) {
		t.Fatal("one source locked out another")
	}
	_, trusted, _ := net.ParseCIDR("10.0.0.0/24")
	f.app.Config.TrustedProxies = []*net.IPNet{trusted}
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.99, 192.0.2.1, 10.0.0.2")
	if f.app.allowAuth(req) {
		t.Fatal("proxy chain did not stop at limited untrusted hop")
	}
	req.Header.Set("X-Forwarded-For", "192.0.2.3")
	if !f.app.allowAuth(req) {
		t.Fatal("trusted proxy clients share a budget")
	}
}

func TestAgentBashRequiresSeparateOptInIncludingSavedJobs(t *testing.T) {
	f := fixture(t)
	if err := f.store.SetAgentToken(digest("agent-secret")); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"blocked","name":"Blocked","kind":"bash","script":"true","enabled":true,"cron":"* * * * *"}`
	for _, enableService := range []bool{false, true} {
		f.app.Config.EnableBash = enableService
		if res := agentRequest(f, "POST", "/api/agent/v1/automations", "agent-secret", body); res.Code != 403 {
			t.Fatal("Bash created without agent opt-in", res.Code, res.Body.String())
		}
	}
	if err := f.store.Save(domain.Job{ID: "saved-script", Name: "Saved", Kind: "bash", Script: "true", Policy: "generic", Body: []byte(`{}`)}, true); err != nil {
		t.Fatal(err)
	}
	res := agentRequest(f, "POST", "/api/agent/v1/automations/saved-script/run", "agent-secret", `{"revision":1,"run_token":"security_saved_script_1234567890"}`)
	if res.Code != 403 {
		t.Fatal("saved Bash ran without opt-in", res.Code)
	}
}

func TestAgentWebhookOriginChangeCannotLeakSavedCredential(t *testing.T) {
	f := fixture(t)
	if err := f.store.SetAgentToken(digest("agent-secret")); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveAgentConnection("webhook", "https://trusted.example/hook", "Bearer saved-secret"); err != nil {
		t.Fatal(err)
	}
	res := agentRequest(f, "PUT", "/api/agent/v1/connection", "agent-secret", `{"delivery_mode":"webhook","webhook_url":"https://other.example/hook"}`)
	if res.Code != 400 {
		t.Fatal("secret could move to new origin", res.Code)
	}
	saved, _ := f.store.AgentSettings()
	if saved.WebhookURL != "https://trusted.example/hook" {
		t.Fatal("rejected mutation persisted")
	}
	res = agentRequest(f, "PUT", "/api/agent/v1/connection", "agent-secret", `{"delivery_mode":"webhook","webhook_url":"https://other.example/hook","webhook_authorization":""}`)
	if res.Code != 200 {
		t.Fatal("explicit clear rejected", res.Code)
	}
}

func TestMCPBashDisabledByDefault(t *testing.T) {
	f := fixture(t)
	if err := f.store.SetAgentToken(digest("mcp-secret")); err != nil {
		t.Fatal(err)
	}
	result := callMCP(t, f, "create_automation", map[string]any{"id": "mcp-blocked", "name": "Blocked", "kind": "bash", "script": "true"})
	if !result.IsError {
		t.Fatal("MCP bypassed default Bash policy")
	}
	if _, err := f.store.Job("mcp-blocked"); err == nil {
		t.Fatal("blocked task persisted")
	}
}

func TestBrowserWebhookChangeRequiresCredentialReplacement(t *testing.T) {
	f := fixture(t)
	token := f.setup(t)
	if err := f.store.SaveAgentWebhook("https://trusted.example/hook", "Bearer browser-saved-secret"); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf": {token}, "delivery_mode": {"webhook"}, "webhook_url": {"https://other.example/hook"}}
	code, body, _ := f.post(t, "/settings/agent", form)
	if code != 400 || strings.Contains(body, "browser-saved-secret") {
		t.Fatal("browser origin change was allowed or leaked secret", code)
	}
	saved, _ := f.store.AgentSettings()
	if saved.WebhookURL != "https://trusted.example/hook" {
		t.Fatal("rejected change persisted")
	}
	form.Set("clear_webhook_authorization", "on")
	code, _, _ = f.post(t, "/settings/agent", form)
	if code != 200 {
		t.Fatal("explicit browser credential clear rejected", code)
	}
}
