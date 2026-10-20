package web

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func agentRequest(f *browserFixture, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	f.app.ServeHTTP(res, req)
	return res
}

func TestAgentAPIAuthValidationAndIsolation(t *testing.T) {
	f := fixture(t)
	if err := f.store.SetAgentToken(digest("test-api-secret")); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveAgentWebhook("https://example.com/agent", "Bearer callback-secret"); err != nil {
		t.Fatal(err)
	}
	valid := `{"id":"timer-one","kind":"timer","title":"Tea timer","after_seconds":60,"context":{"conversation_id":"chat-123"}}`
	for _, token := range []string{"", "wrong-token"} {
		if res := agentRequest(f, "POST", "/api/agent/v1/tasks", token, valid); res.Code != 401 {
			t.Fatal("unauthenticated request accepted", res.Code)
		}
	}
	// Even an authenticated browser session cannot replace the bearer token.
	session := rand.Text()
	if err := f.store.AddSession(digest(session), "csrf-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/agent/v1/tasks", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: session})
	res := httptest.NewRecorder()
	f.app.ServeHTTP(res, req)
	if res.Code != 401 {
		t.Fatal("browser session authorized API")
	}
	res = agentRequest(f, "POST", "/api/agent/v1/tasks", "test-api-secret", valid)
	if res.Code != 201 {
		t.Fatalf("create: %d %s", res.Code, res.Body.String())
	}
	res = agentRequest(f, "POST", "/api/agent/v1/tasks", "test-api-secret", valid)
	if res.Code != 409 {
		t.Fatal("duplicate ID accepted", res.Code)
	}
	for _, body := range []string{
		strings.Replace(valid, `"after_seconds":60`, `"after_seconds":-1`, 1),
		strings.Replace(valid, `"after_seconds":60`, `"after_seconds":60,"run_at":"2030-01-01T00:00:00Z"`, 1),
		strings.Replace(valid, `"after_seconds":60`, `"after_seconds":60,"interval_seconds":1`, 1),
		strings.Replace(valid, `"after_seconds":60`, `"after_seconds":60,"webhook_url":"https://evil.example"`, 1),
		strings.Replace(valid, `{"conversation_id":"chat-123"}`, `null`, 1),
		valid + `{}`,
	} {
		if res = agentRequest(f, "POST", "/api/agent/v1/tasks", "test-api-secret", body); res.Code != 400 {
			t.Fatalf("accepted invalid input: %d %s", res.Code, body)
		}
	}
	res = agentRequest(f, "GET", "/api/agent/v1/tasks/timer-one", "test-api-secret", "")
	if res.Code != 200 || !strings.Contains(res.Body.String(), "chat-123") || strings.Contains(res.Body.String(), "callback-secret") {
		t.Fatal("read missing context or leaked secret", res.Body.String())
	}
	res = agentRequest(f, "GET", "/api/agent/v1/tasks", "test-api-secret", "")
	if strings.Contains(res.Body.String(), "youtube-incremental") {
		t.Fatal("automation exposed to agent API")
	}
	if res = agentRequest(f, "DELETE", "/api/agent/v1/tasks/timer-one", "test-api-secret", ""); res.Code != 204 {
		t.Fatal("cancel failed", res.Code)
	}
	if res = agentRequest(f, "DELETE", "/api/agent/v1/tasks/timer-one", "test-api-secret", ""); res.Code != 409 {
		t.Fatal("cancelled task accepted", res.Code)
	}
	if err := f.store.SetAgentToken(""); err != nil {
		t.Fatal(err)
	}
	if res = agentRequest(f, "GET", "/api/agent/v1/tasks", "test-api-secret", ""); res.Code != 401 {
		t.Fatal("revoked token accepted")
	}
}

func TestAgentSettingsTokenLifecycleAndCSRF(t *testing.T) {
	f := fixture(t)
	// Install a session directly to avoid unrelated expensive password hashing.
	session, csrfToken := rand.Text(), rand.Text()
	if err := f.store.CreateAccount("owner", []byte("unused-test-hash")); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddSession(digest(session), csrfToken, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse(f.server.URL)
	f.client.Jar.SetCookies(base, []*http.Cookie{{Name: "session", Value: session, Path: "/"}})
	code, _, _ := f.post(t, "/settings/agent/token", url.Values{"action": {"generate"}})
	if code != 403 {
		t.Fatal("token generated without CSRF", code)
	}
	code, page, _ := f.post(t, "/settings/agent/token", url.Values{"csrf": {csrfToken}, "action": {"generate"}})
	if code != 200 {
		t.Fatal("token generation failed", code, page)
	}
	settings, err := f.store.AgentSettings()
	if err != nil || settings.TokenHash == "" || strings.Contains(page, settings.TokenHash) {
		t.Fatal("hash storage or rendering failed")
	}
	tokenPattern := regexp.MustCompile(`value="(sse_[A-Za-z0-9]+)"`)
	match := tokenPattern.FindStringSubmatch(page)
	if len(match) != 2 || digest(match[1]) != settings.TokenHash {
		t.Fatal("generated token does not match stored digest")
	}
	_, page = f.get(t, "/settings/agent")
	if strings.Contains(page, match[1]) {
		t.Fatal("token displayed again")
	}
	if !strings.Contains(page, "agent setup guide") {
		t.Fatal("missing setup guide")
	}
	code, _, _ = f.post(t, "/settings/agent", url.Values{"csrf": {csrfToken}, "webhook_url": {"https://example.com/hook"}, "webhook_authorization": {"Bearer private-callback"}})
	if code != 200 {
		t.Fatal("webhook save failed", code)
	}
	_, page = f.get(t, "/settings/agent")
	if strings.Contains(page, "private-callback") {
		t.Fatal("callback credential leaked")
	}
	_, page = f.get(t, "/")
	if !strings.Contains(page, `href="/agent"`) {
		t.Fatal("missing agent tab")
	}
	_, page = f.get(t, "/agent")
	if strings.Contains(page, "YouTube incremental sync") {
		t.Fatal("automations appeared in agent tab")
	}
	res := agentRequest(f, "GET", "/api/agent/v1/tasks", match[1], "")
	if res.Code != 200 {
		t.Fatal("new token rejected")
	}
	var tasks []any
	if json.Unmarshal(res.Body.Bytes(), &tasks) != nil || len(tasks) != 0 {
		t.Fatal("unexpected agent tasks")
	}
	code, _, _ = f.post(t, "/settings/agent/token", url.Values{"csrf": {csrfToken}, "action": {"revoke"}})
	if code != 200 {
		t.Fatal("revoke failed")
	}
	if res = agentRequest(f, "GET", "/api/agent/v1/tasks", match[1], ""); res.Code != 401 {
		t.Fatal("revoked token still works")
	}
}

func TestAgentSkillDownloadRequiresAuthentication(t *testing.T) {
	f := fixture(t)
	code, _ := f.get(t, "/settings/agent/skill.zip")
	if code != 303 {
		t.Fatal("browser skill download did not require sign-in", code)
	}
	if res := agentRequest(f, "GET", "/api/agent/v1/skill.zip", "", ""); res.Code != 401 {
		t.Fatal("API skill download did not require token", res.Code)
	}
	if err := f.store.SetAgentToken(digest("download-token")); err != nil {
		t.Fatal(err)
	}
	res := agentRequest(f, "GET", "/api/agent/v1/skill.zip", "download-token", "")
	if res.Code != 200 || res.Header().Get("Content-Type") != "application/zip" || !strings.HasPrefix(res.Body.String(), "PK") {
		t.Fatal("authenticated skill download failed", res.Code)
	}
	if !strings.Contains(res.Header().Get("Content-Disposition"), "schedulerse-agent.zip") {
		t.Fatal("missing download filename")
	}
}

func TestLocalAgentInboxWorksWithoutWebhook(t *testing.T) {
	f := fixture(t)
	if err := f.store.SetAgentToken(digest("local-api-token")); err != nil {
		t.Fatal(err)
	}
	valid := `{"id":"local-reminder","kind":"reminder","title":"Test local delivery","after_seconds":60,"context":{"conversation_id":"local-chat"}}`
	res := agentRequest(f, "POST", "/api/agent/v1/tasks", "local-api-token", valid)
	if res.Code != 201 {
		t.Fatalf("local task required webhook: %d %s", res.Code, res.Body.String())
	}
	if res = agentRequest(f, "POST", "/api/agent/v1/tasks", "local-api-token", strings.Replace(valid, `{"conversation_id":"local-chat"}`, `{}`, 1)); res.Code != 400 {
		t.Fatal("polling accepted unroutable task", res.Code)
	}
	at := time.Now().Unix() - 1
	if _, err := f.store.DB.Exec(`UPDATE agent_tasks SET run_at=? WHERE id='local-reminder'`, at); err != nil {
		t.Fatal(err)
	}
	if res = agentRequest(f, "GET", "/api/agent/v1/inbox?conversation_id=local-chat", "", ""); res.Code != 401 {
		t.Fatal("unauthenticated inbox accepted")
	}
	res = agentRequest(f, "GET", "/api/agent/v1/inbox?conversation_id=local-chat", "local-api-token", "")
	var events []domain.AgentEvent
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &events) != nil || len(events) != 1 || events[0].Task.ID != "local-reminder" {
		t.Fatal("missing due event", res.Code, res.Body.String())
	}
	ack := fmt.Sprintf(`{"run_at_unix":%d,"consumer_id":"stable-random-consumer-identity-123"}`, at)
	for range 2 {
		res = agentRequest(f, "POST", "/api/agent/v1/inbox/local-reminder/ack", "local-api-token", ack)
		if res.Code != 204 {
			t.Fatal("pickup/retry failed", res.Code, res.Body.String())
		}
	}
	other := strings.Replace(ack, "stable-random-consumer-identity-123", "other-random-consumer-identity-456", 1)
	if res = agentRequest(f, "POST", "/api/agent/v1/inbox/local-reminder/ack", "local-api-token", other); res.Code != 409 {
		t.Fatal("another consumer received same occurrence", res.Code)
	}
	res = agentRequest(f, "GET", "/api/agent/v1/inbox?conversation_id=local-chat", "local-api-token", "")
	if res.Code != 200 || strings.TrimSpace(res.Body.String()) != "[]" {
		t.Fatal("picked-up event remained in inbox", res.Code, res.Body.String())
	}
	if err := f.store.SaveAgentWebhook("https://example.com/hook", ""); err != nil {
		t.Fatal(err)
	}
	if res = agentRequest(f, "GET", "/api/agent/v1/inbox?conversation_id=local-chat", "local-api-token", ""); res.Code != 409 {
		t.Fatal("inbox active in webhook mode", res.Code)
	}
	if res = agentRequest(f, "POST", "/api/agent/v1/inbox/local-reminder/ack", "local-api-token", ack); res.Code != 204 {
		t.Fatal("pickup retry lost after switching mode", res.Code)
	}
}
