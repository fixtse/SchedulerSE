package web

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"io"
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
	if !strings.Contains(page, `id="setup-guide"`) {
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

func TestAgentDeliveryDownloadRequiresAuthentication(t *testing.T) {
	f := fixture(t)
	code, _ := f.get(t, "/settings/agent/delivery.zip")
	if code != 303 {
		t.Fatal("browser delivery kit download did not require sign-in", code)
	}
	if res := agentRequest(f, "GET", "/api/agent/v1/delivery.zip", "", ""); res.Code != 401 {
		t.Fatal("API delivery kit download did not require token", res.Code)
	}
	if err := f.store.SetAgentToken(digest("download-token")); err != nil {
		t.Fatal(err)
	}
	res := agentRequest(f, "GET", "/api/agent/v1/delivery.zip", "download-token", "")
	if res.Code != 200 || res.Header().Get("Content-Type") != "application/zip" || !strings.HasPrefix(res.Body.String(), "PK") {
		t.Fatal("authenticated delivery kit download failed", res.Code)
	}
	if !strings.Contains(res.Header().Get("Content-Disposition"), "schedulerse-delivery.zip") {
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

func TestAgentTokenCanConfigureDeliveryWithoutBrowserSession(t *testing.T) {
	f := fixture(t)
	if err := f.store.SetAgentToken(digest("setup-api-token")); err != nil {
		t.Fatal(err)
	}
	path := "/api/agent/v1/connection"
	if res := agentRequest(f, "PUT", path, "", `{"delivery_mode":"poll"}`); res.Code != 401 {
		t.Fatal("unauthenticated delivery setup accepted")
	}
	input := `{"delivery_mode":"webhook","webhook_url":"https://agent.example.com/callback","webhook_authorization":"Bearer callback-private-secret"}`
	res := agentRequest(f, "PUT", path, "setup-api-token", input)
	if res.Code != 200 || strings.Contains(res.Body.String(), "callback-private-secret") || !strings.Contains(res.Body.String(), `"has_webhook_authorization":true`) {
		t.Fatal("configuration failed or exposed authorization", res.Code, res.Body.String())
	}
	res = agentRequest(f, "GET", path, "setup-api-token", "")
	if res.Code != 200 || strings.Contains(res.Body.String(), "callback-private-secret") || !strings.Contains(res.Body.String(), "agent.example.com") {
		t.Fatal("configuration discovery failed", res.Code, res.Body.String())
	}
	if res = agentRequest(f, "PUT", path, "setup-api-token", `{"delivery_mode":"poll"}`); res.Code != 200 {
		t.Fatal("automatic polling configuration failed", res.Code)
	}
	saved, err := f.store.AgentSettings()
	if err != nil || saved.DeliveryMode != "poll" || saved.WebhookAuth != "Bearer callback-private-secret" {
		t.Fatalf("omitted credentials were not preserved: %+v %v", saved, err)
	}
	for _, body := range []string{
		`{"delivery_mode":"invalid"}`,
		`{"delivery_mode":"webhook","webhook_url":"http://public.example.com/hook"}`,
		`{"delivery_mode":"webhook","webhook_url":""}`,
		`{"delivery_mode":"poll","telegram_token":"outside-scope"}`,
		`{"delivery_mode":"poll","webhook_authorization":"Bearer invalid\r\nheader"}`,
	} {
		if res = agentRequest(f, "PUT", path, "setup-api-token", body); res.Code != 400 {
			t.Fatalf("accepted invalid setup: %d %s", res.Code, body)
		}
	}
	if res = agentRequest(f, "PUT", path, "setup-api-token", `{"delivery_mode":"poll","webhook_authorization":""}`); res.Code != 200 {
		t.Fatal("explicit credential removal failed")
	}
	saved, err = f.store.AgentSettings()
	if err != nil || saved.WebhookAuth != "" {
		t.Fatal("credential removal not saved", err)
	}
}

func TestDeliveryDownloadCapturesBrowserOrigin(t *testing.T) {
	f := fixture(t)
	if err := f.store.SetAgentToken(digest("origin-api-token")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, host, origin, referer, forwarded, want string
		secure                                       bool
	}{
		{name: "HTTP IP and port", host: "192.0.2.10:8789", want: "http://192.0.2.10:8789"},
		{name: "HTTPS browser behind proxy", host: "scheduler.example.com:9443", origin: "https://scheduler.example.com:9443", want: "https://scheduler.example.com:9443"},
		{name: "same host referer", host: "scheduler.example.com", referer: "https://scheduler.example.com/settings/agent", want: "https://scheduler.example.com"},
		{name: "secure proxy fallback", host: "scheduler.example.com", secure: true, want: "https://scheduler.example.com"},
		{name: "ignore forwarded origin", host: "schedulerse:8789", forwarded: "https", want: "http://schedulerse:8789"},
		{name: "IPv6 and port", host: "[2001:db8::1]:8789", origin: "http://[2001:db8::1]:8789", want: "http://[2001:db8::1]:8789"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.app.Config.CookieSecure = tc.secure
			req := httptest.NewRequest("GET", "/api/agent/v1/delivery.zip?origin="+url.QueryEscape(tc.origin), nil)
			req.Host = tc.host
			req.Header.Set("Authorization", "Bearer origin-api-token")
			req.Header.Set("Referer", tc.referer)
			req.Header.Set("X-Forwarded-Proto", tc.forwarded)
			res := httptest.NewRecorder()
			f.app.ServeHTTP(res, req)
			if res.Code != 200 {
				t.Fatal("delivery kit download failed", res.Code, res.Body.String())
			}
			archive, err := zip.NewReader(bytes.NewReader(res.Body.Bytes()), int64(res.Body.Len()))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, file := range archive.File {
				if file.Name == "schedulerse-delivery/connection.json" {
					reader, err := file.Open()
					if err != nil {
						t.Fatal(err)
					}
					var metadata map[string]string
					err = json.NewDecoder(reader).Decode(&metadata)
					reader.Close()
					if err != nil || metadata["service_url"] != tc.want || strings.Contains(fmt.Sprint(metadata), "origin-api-token") {
						t.Fatalf("incorrect or secret-bearing origin: %+v %v", metadata, err)
					}
					found = true
				}
			}
			if !found {
				t.Fatal("missing personalized connection metadata")
			}
		})
	}
	f.app.Config.CookieSecure = false
	for _, origin := range []string{"https://attacker.example", "http://example.com/path", "http://user@example.com", "http://example.com?token=secret", "ftp://example.com", "http://example.com#fragment"} {
		res := agentRequest(f, "GET", "/api/agent/v1/delivery.zip?origin="+url.QueryEscape(origin), "origin-api-token", "")
		if res.Code != 400 {
			t.Fatalf("accepted invalid download origin %q: %d", origin, res.Code)
		}
	}
}

func TestAgentSetupGuideIsReadableInSettings(t *testing.T) {
	f := fixture(t)
	if code, _ := f.get(t, "/settings/agent/guide"); code != 303 {
		t.Fatal("guide did not require sign-in", code)
	}
	if err := f.store.CreateAccount("owner", []byte("unused-test-hash")); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddSession(digest("guide-session"), "guide-csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse(f.server.URL)
	f.client.Jar.SetCookies(base, []*http.Cookie{{Name: "session", Value: "guide-session", Path: "/"}})
	response, err := f.client.Get(f.server.URL + "/settings/agent")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") || response.Header.Get("Content-Disposition") != "" {
		t.Fatal("guide was not displayed as an app page", response.StatusCode, response.Header)
	}
	if !strings.Contains(string(body), "Set up your agent") || !strings.Contains(string(body), "guide.md") || !strings.Contains(string(body), "conversation_id") || !strings.Contains(string(body), `href="/settings/agent/delivery.zip"`) {
		t.Fatal("guide omitted setup instructions, download or API reference")
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("authenticated guide became cacheable")
	}
	response, err = f.client.Get(f.server.URL + "/settings/agent/guide.md")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	markdown, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || string(markdown) != agentGuide || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/markdown") || !strings.Contains(response.Header.Get("Content-Disposition"), "attachment") {
		t.Fatal("Markdown reference download failed", response.StatusCode)
	}
	if !strings.Contains(string(body), `id="setup-guide"`) || !strings.Contains(string(body), `id="agent-token"`) || !strings.Contains(string(body), `action="/settings/agent/token"`) || !strings.Contains(string(body), `action="/settings/agent"`) {
		t.Fatal("merged guide omitted token or delivery controls")
	}
	response, err = f.client.Get(f.server.URL + "/settings/agent/guide")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 303 || response.Header.Get("Location") != "/settings/agent#setup-guide" {
		t.Fatal("old guide URL did not redirect to merged settings page", response.StatusCode, response.Header)
	}
}
