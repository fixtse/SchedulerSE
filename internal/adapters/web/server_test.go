package web

import (
	"context"
	"crypto/rand"
	"github.com/fixtse/SchedulerSE/internal/adapters"
	"github.com/fixtse/SchedulerSE/internal/application"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"golang.org/x/crypto/bcrypt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var csrfPattern = regexp.MustCompile(`name="csrf"\s+value="([^"]+)"`)

type browserFixture struct {
	app     *Server
	server  *httptest.Server
	client  *http.Client
	store   *adapters.Store
	reloads int
}

func fixture(t *testing.T) *browserFixture {
	t.Helper()
	dir := t.TempDir()
	s, err := adapters.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Seed([]domain.Job{{ID: "youtube-incremental", Name: "YouTube incremental sync", URL: "https://example.com/sync", Authorization: "Bearer private-header", Body: []byte(`{"fullSync":"false"}`), Policy: "youtube-incremental", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	f := &browserFixture{store: s}
	f.app = New(s, adapters.Config{DataDir: dir, Timezone: "Europe/Madrid", Timeout: 120 * time.Second}, func() { f.reloads++ }, "test-bootstrap-token")
	f.server = httptest.NewServer(f.app)
	jar, _ := cookiejar.New(nil)
	// Production-cost bcrypt is much slower under the race detector on CI runners.
	f.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 30 * time.Second}
	t.Cleanup(func() { f.server.Close(); s.DB.Close() })
	return f
}
func (f *browserFixture) get(t *testing.T, path string) (int, string) {
	t.Helper()
	r, err := f.client.Get(f.server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}
func csrf(t *testing.T, body string) string {
	t.Helper()
	m := csrfPattern.FindStringSubmatch(body)
	if len(m) != 2 {
		t.Fatal("missing CSRF form")
	}
	return m[1]
}
func (f *browserFixture) post(t *testing.T, path string, form url.Values) (int, string, *http.Response) {
	t.Helper()
	res, err := f.client.PostForm(f.server.URL+path, form)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body), res
}
func (f *browserFixture) setup(t *testing.T) string {
	t.Helper()
	_, body := f.get(t, "/setup")
	code, body, res := f.post(t, "/setup", url.Values{"csrf": {csrf(t, body)}, "setup_token": {"test-bootstrap-token"}, "username": {"owner"}, "password": {"correct-horse-battery"}, "confirm_password": {"correct-horse-battery"}})
	if code != 303 {
		t.Fatalf("setup: %d %s", code, body)
	}
	cookies := res.Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == "session" {
			found = true
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Fatal("unsafe session cookie")
			}
		}
	}
	if !found {
		t.Fatal("no session")
	}
	_, body = f.get(t, "/")
	return csrf(t, body)
}
func TestFirstRunAuthSessionsAndCSRF(t *testing.T) {
	// Keep password hashing tests sequential to avoid CPU contention under -race.
	f := fixture(t)
	response, err := f.client.Get(f.server.URL + "/setup")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.Header.Get("Referrer-Policy") != "same-origin" {
		t.Fatal("form navigation needs a same-origin Origin header")
	}
	code, _ := f.get(t, "/")
	if code != 303 {
		t.Fatal("first request should redirect to setup")
	}
	_, page := f.get(t, "/setup")
	token := csrf(t, page)
	code, _, _ = f.post(t, "/setup", url.Values{"csrf": {token}, "setup_token": {"wrong"}})
	if code != 400 {
		t.Fatal("unverified setup accepted")
	}
	code, _, _ = f.post(t, "/setup", url.Values{"csrf": {"forged"}, "setup_token": {"test-bootstrap-token"}})
	if code != 403 {
		t.Fatal("setup CSRF accepted")
	}
	sessionCSRF := f.setup(t)
	username, hash, err := f.store.Account()
	if err != nil || username != "owner" || string(hash) == "correct-horse-battery" || bcrypt.CompareHashAndPassword(hash, []byte("correct-horse-battery")) != nil {
		t.Fatal("password storage invalid")
	}
	code, _ = f.get(t, "/setup")
	if code != 303 {
		t.Fatal("setup can be reclaimed")
	}
	code, _, _ = f.post(t, "/jobs/youtube-incremental/toggle", url.Values{"csrf": {"wrong"}, "revision": {"1"}})
	if code != 403 {
		t.Fatal("mutation CSRF accepted")
	}
	req, _ := http.NewRequest("POST", f.server.URL+"/logout", strings.NewReader(url.Values{"csrf": {sessionCSRF}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://attacker.example")
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("cross-site logout accepted")
	}
	code, _, _ = f.post(t, "/logout", url.Values{"csrf": {sessionCSRF}})
	if code != 303 {
		t.Fatal("logout failed")
	}
	code, _ = f.get(t, "/")
	if code != 303 {
		t.Fatal("logout session retained")
	}
	_, page = f.get(t, "/login")
	token = csrf(t, page)
	code, _, _ = f.post(t, "/login", url.Values{"csrf": {token}, "username": {"owner"}, "password": {"incorrect"}})
	if code != 401 {
		t.Fatal("wrong login accepted")
	}
	code, _, _ = f.post(t, "/login", url.Values{"csrf": {token}, "username": {"owner"}, "password": {"correct-horse-battery"}})
	if code != 303 {
		t.Fatal("correct login rejected")
	}
	_, _ = f.store.DB.Exec("UPDATE sessions SET expires=0")
	code, _ = f.get(t, "/")
	if code != 303 {
		t.Fatal("expired session accepted")
	}
}
func TestManagementPersistenceSecretsAndConcurrentChanges(t *testing.T) {
	f := fixture(t)
	token := f.setup(t)
	_, body := f.get(t, "/jobs/youtube-incremental")
	if strings.Contains(body, "private-header") {
		t.Fatal("stored secret reflected")
	}
	form := url.Values{"csrf": {token}, "id": {"new-job"}, "name": {"<script>alert(1)</script>"}, "url": {"http://127.0.0.1/mock"}, "authorization": {"Bearer new-secret"}, "body": {`{"fullSync":false,"count":1}`}, "cron": {"*/15 * * * *"}, "request_timeout_ms": {"300000"}}
	code, body, _ := f.post(t, "/jobs/new", form)
	if code != 303 {
		t.Fatalf("create: %d %s", code, body)
	}
	job, err := f.store.Job("new-job")
	if err != nil || job.Enabled || job.Authorization != "Bearer new-secret" || job.TimeoutMS != 300000 {
		t.Fatalf("job %#v %v", job, err)
	}
	_, body = f.get(t, "/")
	if !strings.Contains(body, "OVERVIEW / 2 AUTOMATIONS") || strings.Contains(body, "Scheduler is active") || strings.Contains(body, "OVERVIEW / Europe/Madrid") {
		t.Fatal("dashboard must show the automation count without the scheduler banner")
	}
	if strings.Contains(body, "<script>") || strings.Contains(body, "new-secret") {
		t.Fatal("unsafe dashboard rendering")
	}
	code, _, _ = f.post(t, "/jobs/new", form)
	if code != 409 {
		t.Fatal("duplicate ID accepted")
	}
	_, body = f.get(t, "/jobs/new-job")
	if !strings.Contains(body, `name="request_timeout_ms"`) || !strings.Contains(body, `value="300000"`) || !strings.Contains(body, `placeholder="120000"`) {
		t.Fatal("saved timeout or inherited default missing in editor")
	}
	form.Set("revision", strconv.FormatInt(job.Revision, 10))
	for _, invalid := range []string{"oops", "99", "600001", "-1", "2.5", "999999999999999999999999999999"} {
		form.Set("request_timeout_ms", invalid)
		code, _, _ = f.post(t, "/jobs/new-job", form)
		if code != 400 {
			t.Fatalf("invalid timeout %q accepted: %d", invalid, code)
		}
	}
	// Older clients that omit the setting must retain the saved override.
	form.Del("request_timeout_ms")
	form.Set("name", "Updated")
	form.Set("authorization", "")
	form.Set("revision", strconv.FormatInt(job.Revision, 10))
	form.Set("enabled", "on")
	code, body, _ = f.post(t, "/jobs/new-job", form)
	if code != 303 {
		t.Fatalf("edit: %d %s", code, body)
	}
	updated, _ := f.store.Job("new-job")
	if !updated.Enabled || updated.Name != "Updated" || updated.Authorization != "Bearer new-secret" || updated.TimeoutMS != 300000 {
		t.Fatal("edit lost saved state")
	}
	code, _, _ = f.post(t, "/jobs/new-job", form)
	if code != 409 {
		t.Fatal("stale edit overwrote latest version")
	}
	code, _, _ = f.post(t, "/jobs/new-job/toggle", url.Values{"csrf": {token}, "revision": {strconv.FormatInt(updated.Revision, 10)}})
	if code != 303 {
		t.Fatal("disable failed")
	}
	disabled, _ := f.store.Job("new-job")
	if disabled.Enabled {
		t.Fatal("disable not persisted")
	}
	form.Set("revision", strconv.FormatInt(disabled.Revision, 10))
	form.Set("request_timeout_ms", "")
	form.Set("clear_authorization", "on")
	form.Del("enabled")
	code, _, _ = f.post(t, "/jobs/new-job", form)
	if code != 303 {
		t.Fatal("clear header failed")
	}
	cleared, _ := f.store.Job("new-job")
	if cleared.Authorization != "" || cleared.TimeoutMS != 0 {
		t.Fatal("header not cleared or timeout not reset")
	}
	// Built-in policy comes from storage, never from a submitted form.
	bad := url.Values{"csrf": {token}, "name": {"Incremental"}, "url": {"https://example.com/sync"}, "body": {`{"fullSync":"false"}`}, "cron": {"* * * * *"}, "revision": {"1"}, "policy": {"generic"}}
	code, _, _ = f.post(t, "/jobs/youtube-incremental", bad)
	if code != 303 {
		t.Fatal("manual task could not be scheduled")
	}
	converted, _ := f.store.Job("youtube-incremental")
	if converted.Cron != "* * * * *" || converted.Policy != "youtube-incremental" {
		t.Fatal("schedule or legacy policy changed")
	}
	bad.Set("revision", strconv.FormatInt(converted.Revision, 10))
	bad.Set("cron", "")
	bad.Set("body", `{"fullSync":false}`)
	code, _, _ = f.post(t, "/jobs/youtube-incremental", bad)
	if code != 400 {
		t.Fatal("payload type changed")
	}
	if f.reloads != 5 {
		t.Fatalf("expected reload after each mutation, got %d", f.reloads)
	}
}
func TestAuthRateLimitAndHealth(t *testing.T) {
	f := fixture(t)
	for range 5 {
		if !f.app.allowAuth(httptest.NewRequest("POST", "/login", nil)) {
			t.Fatal("early rate limit")
		}
	}
	if f.app.allowAuth(httptest.NewRequest("POST", "/login", nil)) {
		t.Fatal("rate limit missing")
	}
	code, body := f.get(t, "/health")
	if code != 200 || body != `{"status":"ok","schedulerEnabled":true}` {
		t.Fatalf("health %d %s", code, body)
	}
	req := httptest.NewRequest("GET", "http://example.com/health", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	w := httptest.NewRecorder()
	f.app.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatal("health exposed externally")
	}
	f.app.Config.CookieSecure = true
	w = httptest.NewRecorder()
	f.app.cookie(w, "session", "token", 123)
	if !strings.Contains(w.Header().Get("Set-Cookie"), "Secure") {
		t.Fatal("secure cookie option ignored")
	}
}

type silentLog struct{}

func (silentLog) Event(string, map[string]any) {}

func TestScheduleManagementAndManualExecution(t *testing.T) {
	f := fixture(t)
	token := f.setup(t)
	calls := make(chan string, 4)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	ctx, cancel := context.WithCancel(context.Background())
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Error("task method changed")
		}
		body, _ := io.ReadAll(r.Body)
		calls <- string(body)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer mock.Close()
	defer func() { unblock(); cancel(); f.app.Wait() }()
	transport := adapters.NewHTTP(adapters.Config{Timeout: 5 * time.Second})
	f.app.Runner = &application.Runner{Store: f.store, Sync: transport, Notifications: &adapters.Telegram{Store: f.store, HTTP: transport}, Log: silentLog{}, Timeout: 5 * time.Second}
	f.app.Context = ctx
	form := url.Values{"csrf": {token}, "id": {"friendly"}, "name": {"Friendly"}, "url": {mock.URL}, "body": {`{"count":1,"fullSync":false}`}, "schedule_mode": {"weekly"}, "schedule_time": {"09:30"}, "schedule_days": {"1", "5"}, "timezone": {"America/Lima"}}
	code, body, _ := f.post(t, "/jobs/new", form)
	if code != 303 {
		t.Fatalf("schedule create: %d %s", code, body)
	}
	j, _ := f.store.Job("friendly")
	if j.Cron != "30 9 * * 1,5" || j.Timezone != "America/Lima" || j.Enabled {
		t.Fatal("builder did not persist schedule and timezone")
	}
	_, body = f.get(t, "/jobs/friendly")
	for _, fragment := range []string{`value="weekly" selected`, `value="America/Lima" selected`, `value="09:30"`, `action="/jobs/friendly/run"`} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("saved schedule not reflected: %s", fragment)
		}
	}
	form.Set("timezone", "invalid/zone")
	form.Set("revision", "1")
	code, body, _ = f.post(t, "/jobs/friendly", form)
	if code != 400 || !strings.Contains(body, `value="09:30"`) {
		t.Fatal("invalid timezone accepted or schedule fields lost")
	}
	form.Set("timezone", "UTC")
	code, _, _ = f.post(t, "/jobs/friendly", form)
	if code != 303 {
		t.Fatal("timezone edit failed")
	}
	code, _, _ = f.post(t, "/jobs/friendly", form)
	if code != 409 {
		t.Fatal("stale timezone edit accepted")
	}
	run := url.Values{"csrf": {token}, "revision": {"2"}, "run_token": {rand.Text()}}
	run.Set("csrf", "forged")
	code, _, _ = f.post(t, "/jobs/friendly/run", run)
	if code != 403 {
		t.Fatal("manual execution missing CSRF protection")
	}
	run.Set("csrf", token)
	run.Set("revision", "1")
	code, _, _ = f.post(t, "/jobs/friendly/run", run)
	if code != 409 {
		t.Fatal("stale manual execution accepted")
	}
	run.Set("revision", "2")
	code, body, _ = f.post(t, "/jobs/friendly/run", run)
	if code != 303 {
		t.Fatalf("manual disabled run: %d %s", code, body)
	}
	select {
	case payload := <-calls:
		if payload != `{"count":1,"fullSync":false}` {
			t.Fatal("payload types changed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manual run never reached mock endpoint")
	}
	code, _, _ = f.post(t, "/jobs/friendly/run", run)
	if code != 409 {
		t.Fatal("duplicate form accepted")
	}
	original := run.Get("run_token")
	run.Set("run_token", rand.Text())
	code, _, _ = f.post(t, "/jobs/friendly/run", run)
	if code != 409 {
		t.Fatal("overlapping manual run accepted")
	}
	unblock()
	deadline := time.Now().Add(3 * time.Second)
	for {
		runs, err := f.store.Recent()
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == 1 && runs[0].Status == "success" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("manual execution not recorded: %v", runs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	run.Set("run_token", original)
	code, _, _ = f.post(t, "/jobs/friendly/run", run)
	if code != 409 {
		t.Fatal("completed manual run replayed")
	}
	// A legacy saved task can also run manually, preserving its string payload.
	incremental, _ := f.store.Job("youtube-incremental")
	incremental.URL = mock.URL
	incremental.Authorization = "Bearer local-test-credential"
	incremental.Enabled = false
	if err := f.store.Save(incremental, false); err != nil {
		t.Fatal(err)
	}
	run.Set("run_token", rand.Text())
	code, _, _ = f.post(t, "/jobs/youtube-incremental/run", run)
	if code != 400 {
		t.Fatal("manual built-in bypassed credential requirements")
	}
	if err := f.store.ImportTelegram("123456:test-only", "12345"); err != nil {
		t.Fatal(err)
	}
	run.Set("run_token", rand.Text())
	code, body, _ = f.post(t, "/jobs/youtube-incremental/run", run)
	if code != 303 {
		t.Fatalf("incremental run: %d %s", code, body)
	}
	select {
	case payload := <-calls:
		if payload != `{"fullSync":"false"}` {
			t.Fatal("incremental payload changed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("incremental run never reached mock")
	}
	code, _ = f.get(t, "/jobs/friendly/run")
	if code != 405 {
		t.Fatal("GET can trigger execution")
	}
	code, _, _ = f.post(t, "/logout", url.Values{"csrf": {token}})
	if code != 303 {
		t.Fatal("logout failed")
	}
	code, _, response := f.post(t, "/jobs/friendly/run", run)
	if code != 303 || response.Header.Get("Location") != "/login" {
		t.Fatal("unauthenticated manual execution accepted")
	}
}

func TestManualBashAutomationOmitsSchedulingControls(t *testing.T) {
	f := fixture(t)
	f.app.Config.EnableBash = true
	token := f.setup(t)
	form := url.Values{"csrf": {token}, "id": {"manual-script"}, "name": {"Manual script"}, "kind": {"bash"}, "script": {"echo hello"}, "schedule_mode": {"manual"}, "enabled": {"on"}, "recovery_settings": {"1"}, "retry_count": {"3"}, "fallback_url": {"https://example.com/fallback"}}
	code, body, _ := f.post(t, "/jobs/new", form)
	if code != 303 {
		t.Fatalf("create manual script: %d %s", code, body)
	}
	job, err := f.store.Job("manual-script")
	if err != nil || job.Enabled || job.RetryCount != 0 || job.FallbackURL != "" {
		t.Fatalf("manual script retained scheduling or HTTP recovery: %#v %v", job, err)
	}
	_, body = f.get(t, "/")
	for _, card := range strings.Split(body, `<article class="job-card">`) {
		if !strings.Contains(card, `/jobs/manual-script"`) {
			continue
		}
		card = strings.Split(card, "</article>")[0]
		if strings.Contains(card, "/toggle") || strings.Contains(card, "Disabled") || !strings.Contains(card, "Manual only") || !strings.Contains(card, "/run") {
			t.Fatal("manual card must offer Run now without scheduling controls", card)
		}
	}
}
