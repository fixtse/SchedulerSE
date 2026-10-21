package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fixtse/SchedulerSE/internal/adapters"
)

func TestGeneralSettingsApplyAndSurviveRestart(t *testing.T) {
	f := fixture(t)
	csrf := f.setup(t)
	form := url.Values{"csrf": {csrf}, "timezone": {"UTC"}, "request_timeout_ms": {"5000"}}
	code, body, _ := f.post(t, "/settings", form)
	if code != 200 || !strings.Contains(body, "Settings saved") {
		t.Fatal("save settings", code, body)
	}
	if f.app.settings().Timezone != "UTC" || f.app.defaultTimeout() != 5*time.Second || f.reloads != 1 {
		t.Fatal("defaults did not apply immediately", f.app.settings(), f.reloads)
	}
	_, body = f.get(t, "/jobs/new")
	if !strings.Contains(body, `placeholder="5000"`) || !strings.Contains(body, `value="UTC" selected`) {
		t.Fatal("new editor did not use new defaults")
	}
	restarted := New(f.store, adapters.Config{Timezone: "Europe/Madrid", Timeout: 120 * time.Second}, func() {}, "")
	if restarted.settings() != f.app.settings() {
		t.Fatal("settings did not survive restart")
	}
	form.Set("request_timeout_ms", "0")
	if code, _, _ = f.post(t, "/settings", form); code != 400 {
		t.Fatal("invalid timeout accepted", code)
	}
	if f.app.defaultTimeout() != 5*time.Second {
		t.Fatal("invalid save changed defaults")
	}
	form.Set("request_timeout_ms", "5000")
	form.Set("csrf", "wrong")
	if code, _, _ = f.post(t, "/settings", form); code != 403 {
		t.Fatal("settings save bypassed CSRF", code)
	}
}

func TestSecureCookieSettingRequiresHTTPSAndReissuesSession(t *testing.T) {
	f := fixture(t)
	csrf := f.setup(t)
	form := url.Values{"csrf": {csrf}, "timezone": {"UTC"}, "request_timeout_ms": {"120000"}, "cookie_secure": {"on"}}
	if code, _, _ := f.post(t, "/settings", form); code != 400 {
		t.Fatal("secure cookies enabled from HTTP", code)
	}
	request := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(form.Encode()))
	origin, _ := url.Parse(f.server.URL)
	request.Host = origin.Host
	request.Header.Set("Origin", "https://"+origin.Host)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range f.client.Jar.Cookies(origin) {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	f.app.ServeHTTP(response, request)
	if response.Code != 200 || !f.app.settings().CookieSecure {
		t.Fatal("secure cookie setting failed", response.Code, response.Body.String())
	}
	found := false
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "session" {
			found = true
			if !cookie.Secure || !cookie.HttpOnly || cookie.MaxAge <= 0 {
				t.Fatal("session attributes not preserved", cookie)
			}
		}
	}
	if !found {
		t.Fatal("current session cookie was not updated")
	}
}

func TestSettingsTabsShareNavigationAndKeepForms(t *testing.T) {
	f := fixture(t)
	f.setup(t)
	for _, tc := range []struct{ path, selected, action string }{
		{"/settings", "/settings", "/settings"},
		{"/settings?tab=telegram", "/settings?tab=telegram", "/settings/telegram"},
		{"/settings?tab=agent", "/settings?tab=agent", "/settings/agent"},
	} {
		code, body := f.get(t, tc.path)
		if code != 200 || !strings.Contains(body, `aria-label="Settings"`) || !strings.Contains(body, `href="`+tc.selected+`" aria-current="page"`) || !strings.Contains(body, `action="`+tc.action+`"`) {
			t.Fatalf("settings tab %s lost navigation or its form: %d", tc.path, code)
		}
		if strings.Count(body, "<h1>") != 1 || !strings.Contains(body, "<h1>Settings") {
			t.Fatal("settings must share a single page heading")
		}
	}
}
