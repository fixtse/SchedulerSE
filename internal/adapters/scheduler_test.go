package adapters

import (
	"context"
	"github.com/fixtse/SchedulerSE/internal/application"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"testing"
	"time"
)

func TestDefaultsDSTAndValidation(t *testing.T) {
	for _, key := range []string{"TZ", "REQUEST_TIMEOUT_MS", "COOKIE_SECURE", "TELEGRAM_API_URL"} {
		t.Setenv(key, "")
	}
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Timezone != "Europe/Madrid" {
		t.Fatal("defaults changed")
	}
	daily, err := ParseSchedule("0 0 * * *", c.Timezone)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse(time.RFC3339, "2026-10-24T21:00:00Z")
	first := daily.Next(start)
	second := daily.Next(first)
	if second.Sub(first) != 25*time.Hour {
		t.Fatalf("DST changed: %s %s", first, second)
	}
	quarter, _ := ParseSchedule("*/15 * * * *", c.Timezone)
	start, _ = time.Parse(time.RFC3339, "2026-10-16T12:01:00Z")
	if quarter.Next(start).UTC().Format(time.RFC3339) != "2026-10-16T12:15:00Z" {
		t.Fatal("quarter hour changed")
	}
	for _, spec := range []string{"bad", "CRON_TZ=UTC * * * * *", "@every 1s", "0 0 31 2 *"} {
		if _, err := ParseSchedule(spec, c.Timezone); err == nil {
			t.Errorf("accepted %q", spec)
		}
	}
	for _, endpoint := range []string{"http://public.example/sync", "https://user:secret@example.com", "https://example.com/#secret", "ftp://example.com"} {
		if ValidateEndpoint(endpoint) == nil {
			t.Error("unsafe endpoint accepted")
		}
	}
	legacy := domain.Job{ID: "legacy", Name: "Legacy", URL: "http://127.0.0.1/mock", Body: []byte(`{"fullSync":"false"}`), Cron: "* * * * *", Policy: "youtube-incremental"}
	if err := ValidateJob(legacy, c.Timezone); err != nil {
		t.Fatal("manual task could not be scheduled:", err)
	}
	t.Setenv("COOKIE_SECURE", "yes")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("invalid flag accepted")
	}
}

type scheduleSync struct{ calls chan string }

func (f scheduleSync) Post(_ context.Context, j domain.Job) (domain.Response, error) {
	f.calls <- j.ID
	return domain.Response{HTTPStatus: 200}, nil
}

type quietLog struct{}

func (quietLog) Event(string, map[string]any) {}

type quietNotifications struct{}

func (quietNotifications) Notify(context.Context, domain.NotificationType, string) error {
	return domain.ErrNotificationsDisabled
}
func TestSchedulerReloadAndShutdown(t *testing.T) {
	s := testStore(t)
	j := domain.Job{ID: "test", Name: "Test", URL: "http://127.0.0.1/mock", Body: []byte(`{}`), Cron: "* * * * * *", Enabled: false, Policy: "generic"}
	if err := s.Seed([]domain.Job{j}); err != nil {
		t.Fatal(err)
	}
	calls := make(chan string, 8)
	r := &application.Runner{Sync: scheduleSync{calls}, Notifications: quietNotifications{}, Store: s, Log: quietLog{}, Timeout: time.Second}
	scheduler := NewScheduler(s, r, "UTC", quietLog{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { scheduler.Serve(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	j, _ = s.Job("test")
	j.Enabled = true
	if err := s.Save(j, false); err != nil {
		t.Fatal(err)
	}
	scheduler.Reload()
	select {
	case <-calls:
	case <-time.After(3 * time.Second):
		t.Fatal("reload did not schedule")
	}
	j, _ = s.Job("test")
	j.Enabled = false
	if err := s.Save(j, false); err != nil {
		t.Fatal(err)
	}
	scheduler.Reload()
	select {
	case <-calls:
		t.Fatal("disabled job executed")
	case <-time.After(1100 * time.Millisecond):
	}
}

func TestExecutionRequiresBuiltInCredentials(t *testing.T) {
	j := domain.Job{ID: "youtube-full", Policy: "youtube-full"}
	if ValidateRunnable(j, domain.TelegramSettings{}) == nil {
		t.Fatal("missing authorization accepted")
	}
	j.Authorization = "Bearer test-only"
	if ValidateRunnable(j, domain.TelegramSettings{}) == nil {
		t.Fatal("missing alert credentials accepted")
	}
	if err := ValidateRunnable(j, domain.TelegramSettings{Token: "test-only", Chat: "test-only", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	j.Policy = "generic"
	j.ID = "new"
	j.Name = "New"
	j.Body = []byte(`{}`)
	j.URL = "http://127.0.0.1/mock"
	if ValidateJob(j, "UTC") == nil {
		t.Fatal("reserved route ID accepted")
	}
}

func TestPerJobTimezoneAndDST(t *testing.T) {
	j := domain.Job{ID: "zone", Name: "Zone", URL: "http://127.0.0.1/mock", Body: []byte(`{}`), Cron: "0 9 * * *", Timezone: "America/Lima", Policy: "generic"}
	if err := ValidateJob(j, "Europe/Madrid"); err != nil {
		t.Fatal(err)
	}
	schedule, err := ParseSchedule(j.Cron, JobTimezone(j, "Europe/Madrid"))
	if err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse(time.RFC3339, "2026-10-24T00:00:00Z")
	if got := schedule.Next(start).UTC().Format(time.RFC3339); got != "2026-10-24T14:00:00Z" {
		t.Fatalf("job timezone ignored: %s", got)
	}
	j.Timezone = "Europe/Madrid"
	j.Cron = "0 0 * * *"
	schedule, _ = ParseSchedule(j.Cron, JobTimezone(j, "UTC"))
	first := schedule.Next(start)
	if schedule.Next(first).Sub(first) != 25*time.Hour {
		t.Fatal("job timezone did not follow DST")
	}
	j.Timezone = ""
	if JobTimezone(j, "Europe/Madrid") != "Europe/Madrid" {
		t.Fatal("legacy timezone changed")
	}
	for _, zone := range []string{"not/a-zone", "UTC * * * * *", "Local"} {
		j.Timezone = zone
		j.Cron = ""
		if ValidateJob(j, "UTC") == nil {
			t.Fatalf("accepted invalid timezone %q", zone)
		}
	}
}

func TestOldTaskEnvironmentNoLongerConfiguresJobs(t *testing.T) {
	t.Setenv("COOKIE_SECURE", "false")
	t.Setenv("REQUEST_TIMEOUT_MS", "120000")
	t.Setenv("TZ", "Europe/Madrid")
	t.Setenv("TELEGRAM_API_URL", "http://127.0.0.1/mock")
	for _, key := range []string{"YOUTUBE_SYNC_URL", "YOUTUBE_FULL_CRON", "YOUTUBE_INCREMENTAL_CRON", "FIXTSE_CRON_URL", "FIXTSE_CRON", "MAGNAWEB_CRON_URL", "MAGNAWEB_CRON"} {
		t.Setenv(key, "obsolete-value")
	}
	if _, err := LoadConfig(); err != nil {
		t.Fatalf("obsolete task environment affected startup: %v", err)
	}
}

func TestObsoleteSchedulerSwitchIsIgnored(t *testing.T) {
	for _, value := range []string{"", "false", "true", "invalid"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("SCHEDULER_ENABLED", value)
			t.Setenv("COOKIE_SECURE", "false")
			t.Setenv("TZ", "UTC")
			t.Setenv("REQUEST_TIMEOUT_MS", "120000")
			t.Setenv("TELEGRAM_API_URL", "http://127.0.0.1/mock")
			if _, err := LoadConfig(); err != nil {
				t.Fatalf("obsolete scheduler switch affected configuration: %v", err)
			}
		})
	}
}

func TestRequestTimeoutValidation(t *testing.T) {
	j := domain.Job{ID: "timeout", Name: "Timeout", URL: "http://127.0.0.1/mock", Body: []byte(`{}`), Policy: "generic"}
	for _, value := range []int{0, 100, 120000, 600000} {
		j.TimeoutMS = value
		if err := ValidateJob(j, "UTC"); err != nil {
			t.Fatalf("valid timeout %d rejected: %v", value, err)
		}
	}
	for _, value := range []int{-1, 1, 99, 600001} {
		j.TimeoutMS = value
		if ValidateJob(j, "UTC") == nil {
			t.Fatalf("invalid timeout %d accepted", value)
		}
	}
}
