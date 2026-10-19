package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRecoveryEditorAndSchedulingManualTasks(t *testing.T) {
	f := fixture(t)
	if err := f.store.CreateAccount("owner", []byte("unused-test-hash")); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddSession(digest("recovery-session"), "recovery-csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	serverURL, _ := url.Parse(f.server.URL)
	f.client.Jar.SetCookies(serverURL, []*http.Cookie{{Name: "session", Value: "recovery-session", Path: "/"}})
	code, body := f.get(t, "/jobs/youtube-incremental")
	if code != 200 || !strings.Contains(body, `<select name="schedule_mode">`) || !strings.Contains(body, `name="retry_count"`) || strings.Contains(body, "private-header") {
		t.Fatal("manual task has no scheduling or recovery controls")
	}
	form := url.Values{"csrf": {"recovery-csrf"}, "revision": {"1"}, "name": {"Incremental"}, "url": {"https://example.com/sync"}, "body": {`{"fullSync":"false"}`}, "schedule_mode": {"daily"}, "schedule_time": {"12:30"}, "timezone": {"Europe/Madrid"}, "recovery_settings": {"1"}, "retry_count": {"2"}, "retry_delay_ms": {"250"}, "retry_uncertain": {"on"}, "fallback_url": {"https://backup.example.com/sync"}, "fallback_authorization": {"fallback-private"}}
	code, _, _ = f.post(t, "/jobs/youtube-incremental", form)
	if code != 303 {
		t.Fatalf("could not configure recovery and schedule: %d", code)
	}
	j, _ := f.store.Job("youtube-incremental")
	if j.Cron != "30 12 * * *" || j.Timezone != "Europe/Madrid" || j.Policy != "youtube-incremental" || j.RetryCount != 2 || j.RetryDelayMS != 250 || !j.RetryUncertain || j.FallbackAuth != "fallback-private" || j.Authorization != "Bearer private-header" {
		t.Fatal("schedule or recovery settings not persisted")
	}
	code, body = f.get(t, "/jobs/youtube-incremental")
	if code != 200 || strings.Contains(body, "fallback-private") || strings.Contains(body, "private-header") || !strings.Contains(body, `name="clear_fallback_authorization"`) {
		t.Fatal("saved fallback secret exposed or missing")
	}
	form.Set("fallback_authorization", "")
	form.Set("retry_count", "3")
	code, _, _ = f.post(t, "/jobs/youtube-incremental", form)
	if code != 409 {
		t.Fatal("stale revision changed recovery settings")
	}
	form.Set("revision", strconv.FormatInt(j.Revision, 10))
	form.Set("retry_count", "6")
	code, body, _ = f.post(t, "/jobs/youtube-incremental", form)
	if code != 400 || strings.Contains(body, "fallback-private") || strings.Contains(body, "private-header") {
		t.Fatal("invalid settings accepted or validation exposed secrets")
	}
	form.Set("retry_count", "0")
	form.Del("retry_uncertain")
	form.Set("fallback_url", "")
	form.Set("clear_fallback_authorization", "on")
	code, _, _ = f.post(t, "/jobs/youtube-incremental", form)
	if code != 303 {
		t.Fatal("could not disable recovery")
	}
	j, _ = f.store.Job(j.ID)
	if j.RetryCount != 0 || j.RetryUncertain || j.FallbackURL != "" || j.FallbackAuth != "" {
		t.Fatal("recovery was not disabled")
	}
}
