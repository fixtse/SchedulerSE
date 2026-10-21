package web

import (
	"crypto/rand"
	"encoding/json"
	"github.com/fixtse/SchedulerSE/internal/adapters"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTelegramSettingsSecretsAndTestDelivery(t *testing.T) {
	f := fixture(t)
	csrfToken := f.setup(t)
	var calls atomic.Int32
	var reject atomic.Bool
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/bot123456:test-secret/sendMessage" {
			t.Error("unexpected Telegram request")
		}
		var body struct {
			Chat string `json:"chat_id"`
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Chat != "-10012345" || !strings.Contains(body.Text, "test message") {
			t.Error("test message payload changed")
		}
		if reject.Load() {
			w.WriteHeader(503)
			_, _ = w.Write([]byte("private Telegram response"))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mock.Close()
	f.app.Telegram = &adapters.Telegram{Store: f.store, HTTP: adapters.NewHTTP(adapters.Config{Timeout: time.Second, TelegramURL: mock.URL})}
	form := url.Values{"csrf": {csrfToken}, "revision": {"1"}, "bot_token": {"123456:test-secret"}, "chat_id": {"-10012345"}, "enabled": {"on"}, "notification_types_present": {"1"}}
	form.Set("csrf", "forged")
	code, _, _ := f.post(t, "/settings/telegram", form)
	if code != 403 {
		t.Fatal("Telegram settings missing CSRF protection")
	}
	form.Set("csrf", csrfToken)
	test := url.Values{"csrf": {csrfToken}, "revision": {"1"}, "test_token": {rand.Text()}}
	code, _, _ = f.post(t, "/settings/telegram/test", test)
	if code != 400 || calls.Load() != 0 {
		t.Fatal("unconfigured Telegram sent a test")
	}
	code, body, _ := f.post(t, "/settings/telegram", form)
	if code != 303 || calls.Load() != 0 {
		t.Fatalf("saving settings sent a message or failed: %d %s", code, body)
	}
	settings, _ := f.store.Telegram()
	if settings.Token != "123456:test-secret" || settings.Chat != "-10012345" || !settings.Enabled || settings.Revision != 2 || settings.NotifySuccess || settings.NotifyFailure || settings.NotifyFallback {
		t.Fatal("Telegram settings not persisted")
	}
	code, body = f.get(t, "/settings/telegram")
	if code != 200 || strings.Contains(body, "test-secret") || !strings.Contains(body, "Saved token") || !strings.Contains(body, "Test Telegram") || !strings.Contains(body, `name="notify_success"`) || !strings.Contains(body, `name="notify_failure"`) || !strings.Contains(body, `name="notify_fallback"`) {
		t.Fatal("saved token exposed or settings controls missing")
	}
	match := regexp.MustCompile(`name="test_token" value="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("missing test deduplication token")
	}
	test.Set("test_token", match[1])
	code, _, _ = f.post(t, "/settings/telegram/test", test)
	if code != 409 || calls.Load() != 0 {
		t.Fatal("stale settings were tested")
	}
	code, _, _ = f.post(t, "/settings/telegram", form)
	if code != 409 {
		t.Fatal("stale Telegram edit accepted")
	}
	form.Set("revision", "2")
	form.Set("bot_token", "123456:new-secret")
	form.Set("chat_id", "invalid chat")
	code, body, _ = f.post(t, "/settings/telegram", form)
	if code != 400 || strings.Contains(body, "new-secret") || strings.Contains(body, "test-secret") {
		t.Fatal("invalid settings accepted or token exposed in error")
	}
	test.Set("revision", "2")
	test.Set("csrf", "forged")
	code, _, _ = f.post(t, "/settings/telegram/test", test)
	if code != 403 || calls.Load() != 0 {
		t.Fatal("test action missing CSRF protection")
	}
	test.Set("csrf", csrfToken)
	code, body, response := f.post(t, "/settings/telegram/test", test)
	if code != 303 || response.Header.Get("Location") != "/settings?tab=telegram&result=sent" || calls.Load() != 1 {
		t.Fatalf("test delivery failed: %d %s", code, body)
	}
	code, _, _ = f.post(t, "/settings/telegram/test", test)
	if code != 409 || calls.Load() != 1 {
		t.Fatal("duplicate test resent")
	}
	test.Set("test_token", rand.Text())
	code, _, _ = f.post(t, "/settings/telegram/test", test)
	if code != 409 || calls.Load() != 1 {
		t.Fatal("test cooldown bypassed")
	}
	if _, err := f.store.DB.Exec("UPDATE telegram_tests SET started=0"); err != nil {
		t.Fatal(err)
	}
	reject.Store(true)
	code, body, _ = f.post(t, "/settings/telegram/test", test)
	if code != 502 || calls.Load() != 2 || strings.Contains(body, "test-secret") || strings.Contains(body, "private Telegram response") {
		t.Fatal("failed test leaked secrets or was retried")
	}
	code, _, _ = f.post(t, "/settings/telegram/test", test)
	if code != 409 || calls.Load() != 2 {
		t.Fatal("uncertain Telegram send replayed")
	}
	form.Set("bot_token", "")
	form.Set("chat_id", "-10012345")
	form.Del("enabled")
	code, _, _ = f.post(t, "/settings/telegram", form)
	settings, _ = f.store.Telegram()
	if code != 303 || settings.Enabled || settings.Token != "123456:test-secret" {
		t.Fatal("disabling Telegram lost the saved token")
	}
	test.Set("revision", "3")
	code, _, _ = f.post(t, "/settings/telegram/test", test)
	if code != 400 || calls.Load() != 2 {
		t.Fatal("disabled Telegram sent a test")
	}
	form.Set("revision", "3")
	form.Set("clear_token", "on")
	form.Set("notify_success", "on")
	form.Set("notify_fallback", "on")
	code, _, _ = f.post(t, "/settings/telegram", form)
	settings, _ = f.store.Telegram()
	if code != 303 || settings.Token != "" || !settings.NotifySuccess || settings.NotifyFailure || !settings.NotifyFallback {
		t.Fatal("saved Telegram token could not be removed")
	}
	code, _ = f.get(t, "/settings/telegram/test")
	if code != 405 {
		t.Fatal("GET can send Telegram messages")
	}
	code, _, _ = f.post(t, "/logout", url.Values{"csrf": {csrfToken}})
	if code != 303 {
		t.Fatal("logout failed")
	}
	for _, path := range []string{"/settings/telegram", "/settings/telegram/test"} {
		code, _, response = f.post(t, path, test)
		if code != 303 || response.Header.Get("Location") != "/login" || calls.Load() != 2 {
			t.Fatal("unauthenticated Telegram mutation accepted")
		}
	}
}
