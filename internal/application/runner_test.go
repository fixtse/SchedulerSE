package application_test

import (
	"context"
	"errors"
	"github.com/fixtse/SchedulerSE/internal/adapters"
	"github.com/fixtse/SchedulerSE/internal/application"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"strings"
	"testing"
	"time"
)

type syncFunc func(context.Context, domain.Job) (string, error)

func (f syncFunc) Post(ctx context.Context, j domain.Job) (string, error) { return f(ctx, j) }

type alerts struct {
	messages []string
	fail     bool
}

func (a *alerts) Send(_ context.Context, text string) error {
	a.messages = append(a.messages, text)
	if a.fail {
		return errors.New("private-token")
	}
	return nil
}

type logs struct{ events []string }

func (l *logs) Event(e string, d map[string]any) {
	l.events = append(l.events, e)
	for _, v := range d {
		if text, ok := v.(string); ok {
			l.events = append(l.events, text)
		}
	}
}
func TestRunnerContracts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		policy, auth, code, wantStatus, wantAlert string
		httpStatus                                int
		notifyFails                               bool
	}{
		{policy: "youtube-full", auth: "oauth", wantStatus: "success"},
		{policy: "youtube-full", auth: "api-key", wantStatus: "fallback", wantAlert: "Error Full Sync actualizando Videos Paradigma\nFallback to api-key"},
		{policy: "youtube-incremental", auth: "api-key", wantStatus: "fallback", wantAlert: "Error actualizando Videos Paradigma\nFallback to api-key"},
		{policy: "fixtse", auth: "api-key", wantStatus: "success"},
		{policy: "fixtse", code: "HTTP_ERROR", httpStatus: 503, wantStatus: "failed", wantAlert: "Error Running Cron FixtSE\nHTTP 503"},
		{policy: "magnaweb", code: "HTTP_ERROR", httpStatus: 503, wantStatus: "failed", wantAlert: "Error Running Cron MagnaWeb\n503 HTTP_ERROR"},
		{policy: "youtube-full", code: "HTTP_ERROR", httpStatus: 503, wantStatus: "failed", wantAlert: "Error actualizando Videos Paradigma\nHTTP 503"},
		{policy: "generic", code: "private", wantStatus: "failed", wantAlert: "Error Running Cron Test\nrequest_failed", notifyFails: true},
	} {
		t.Run(tc.policy+tc.code+tc.auth, func(t *testing.T) {
			t.Parallel()
			store, err := adapters.OpenStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.DB.Close()
			j := domain.Job{ID: "test", Name: "Test", Enabled: true, Policy: tc.policy, Body: []byte(`{}`)}
			if err = store.Seed([]domain.Job{j}); err != nil {
				t.Fatal(err)
			}
			j, _ = store.Job("test")
			calls := 0
			a := &alerts{fail: tc.notifyFails}
			l := &logs{}
			r := application.Runner{Store: store, Notifications: a, Log: l, Timeout: time.Second, Sync: syncFunc(func(context.Context, domain.Job) (string, error) {
				calls++
				if tc.code == "private" {
					return "", errors.New("private-token")
				}
				if tc.code != "" {
					return "", &domain.Failure{Code: tc.code, Status: tc.httpStatus}
				}
				return tc.auth, nil
			})}
			out, err := r.Run(context.Background(), j, "slot")
			if err != nil || out == nil || out.Status != tc.wantStatus {
				t.Fatalf("outcome: %#v %v", out, err)
			}
			if tc.wantAlert == "" {
				if len(a.messages) != 0 {
					t.Fatal(a.messages)
				}
			} else if len(a.messages) != 1 || a.messages[0] != tc.wantAlert {
				t.Fatal(a.messages)
			}
			if tc.notifyFails && out.Notification != "failed" {
				t.Fatal(out)
			}
			if again, _ := r.Run(context.Background(), j, "slot"); again != nil || calls != 1 {
				t.Fatal("uncertain request replayed")
			}
			if strings.Contains(strings.Join(l.events, " "), "private-token") {
				t.Fatal("secret logged")
			}
		})
	}
}
func TestShutdownInterruptsWithoutAlert(t *testing.T) {
	t.Parallel()
	store, err := adapters.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	j := domain.Job{ID: "test", Name: "Test", Enabled: true, Policy: "generic", Body: []byte(`{}`)}
	_ = store.Seed([]domain.Job{j})
	j, _ = store.Job("test")
	started := make(chan struct{})
	a := &alerts{}
	r := application.Runner{Store: store, Notifications: a, Log: &logs{}, Timeout: time.Second, Sync: syncFunc(func(ctx context.Context, _ domain.Job) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *domain.Outcome)
	go func() { out, _ := r.Run(ctx, j, "one"); done <- out }()
	<-started
	cancel()
	out := <-done
	if out == nil || out.Status != "interrupted" || len(a.messages) != 0 {
		t.Fatalf("shutdown %#v %v", out, a.messages)
	}
}

func TestDisabledNotificationsRemainSilent(t *testing.T) {
	t.Parallel()
	store, err := adapters.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	j := domain.Job{ID: "optional-alerts", Name: "Optional alerts", Enabled: true, Policy: "generic", Body: []byte(`{}`)}
	if err = store.Save(j, true); err != nil {
		t.Fatal(err)
	}
	j, _ = store.Job(j.ID)
	// No HTTP adapter is needed: disabled settings must return before any I/O.
	notifications := &adapters.Telegram{Store: store}
	runner := application.Runner{Store: store, Notifications: notifications, Log: &logs{}, Timeout: time.Second, Sync: syncFunc(func(context.Context, domain.Job) (string, error) {
		return "", &domain.Failure{Code: "HTTP_ERROR", Status: 503}
	})}
	out, err := runner.Run(context.Background(), j, "manual:test")
	if err != nil || out == nil || out.Status != "failed" || out.Notification != "none" {
		t.Fatalf("disabled notifications changed outcome: %v %v", out, err)
	}
}

func TestRunnerUsesPerJobTimeout(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		timeoutMS int
		want      time.Duration
	}{
		{"shorter", 100, 100 * time.Millisecond},
		{"longer", 600000, 10 * time.Minute},
		{"inherited", 0, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := adapters.OpenStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.DB.Close()
			j := domain.Job{ID: tc.name, Name: tc.name, Enabled: true, Policy: "generic", TimeoutMS: tc.timeoutMS}
			if err = store.Save(j, true); err != nil {
				t.Fatal(err)
			}
			j, _ = store.Job(j.ID)
			calls := 0
			runner := application.Runner{Store: store, Notifications: &alerts{}, Log: &logs{}, Timeout: time.Minute, Sync: syncFunc(func(ctx context.Context, _ domain.Job) (string, error) {
				calls++
				deadline, ok := ctx.Deadline()
				remaining := time.Until(deadline)
				if !ok || remaining <= 0 || remaining > tc.want || tc.want-remaining > time.Second {
					t.Errorf("request deadline %s; expected %s budget", remaining, tc.want)
				}
				return "", nil
			})}
			out, err := runner.Run(context.Background(), j, "manual:timeout")
			if err != nil || out == nil || out.Status != "success" || calls != 1 {
				t.Fatalf("timeout run: %v %v, calls=%d", out, err, calls)
			}
		})
	}
}
