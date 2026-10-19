package application_test

import (
	"context"
	"github.com/fixtse/SchedulerSE/internal/adapters"
	"github.com/fixtse/SchedulerSE/internal/application"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"testing"
	"time"
)

func TestConfiguredRecovery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		code          string
		status        int
		retries       int
		uncertain     bool
		fallback      bool
		recoverAt     int
		fallbackFails bool
		wantCalls     int
		wantStatus    string
		wantFallback  bool
	}{
		{name: "default single attempt", code: "HTTP_ERROR", status: 503, wantCalls: 1, wantStatus: "failed"},
		{name: "retry recovers", code: "HTTP_ERROR", status: 503, retries: 2, recoverAt: 2, wantCalls: 2, wantStatus: "success"},
		{name: "rate limit exhausts retries", code: "HTTP_ERROR", status: 429, retries: 2, wantCalls: 3, wantStatus: "failed"},
		{name: "client error not retried", code: "HTTP_ERROR", status: 401, retries: 2, wantCalls: 1, wantStatus: "failed"},
		{name: "fallback after retries", code: "HTTP_ERROR", status: 503, retries: 1, fallback: true, wantCalls: 3, wantStatus: "fallback", wantFallback: true},
		{name: "fallback without primary auth", code: "HTTP_ERROR", status: 401, retries: 2, fallback: true, wantCalls: 2, wantStatus: "fallback", wantFallback: true},
		{name: "failed fallback not retried", code: "HTTP_ERROR", status: 503, retries: 1, fallback: true, fallbackFails: true, wantCalls: 3, wantStatus: "failed", wantFallback: true},
		{name: "timeout defaults to no recovery", code: "REQUEST_ABORTED", retries: 2, fallback: true, wantCalls: 1, wantStatus: "failed"},
		{name: "network error defaults to no recovery", code: "NETWORK_ERROR", retries: 2, fallback: true, wantCalls: 1, wantStatus: "failed"},
		{name: "redirect defaults to no recovery", code: "NETWORK_ERROR", status: 302, retries: 2, fallback: true, wantCalls: 1, wantStatus: "failed"},
		{name: "invalid successful response not repeated", code: "INVALID_JSON", status: 200, retries: 2, fallback: true, wantCalls: 1, wantStatus: "failed"},
		{name: "uncertain recovery opted in", code: "REQUEST_ABORTED", retries: 1, uncertain: true, fallback: true, wantCalls: 3, wantStatus: "fallback", wantFallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := adapters.OpenStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.DB.Close()
			j := domain.Job{ID: "test", Name: "Test", URL: "http://127.0.0.1/primary", Authorization: "primary-private", Body: []byte(`{"value":false}`), Policy: "generic", Enabled: true, RetryCount: tc.retries, RetryDelayMS: 100, RetryUncertain: tc.uncertain}
			if tc.fallback {
				j.FallbackURL = "http://127.0.0.1/fallback"
				if tc.status != 401 {
					j.FallbackAuth = "fallback-private"
				}
			}
			if err = store.Save(j, true); err != nil {
				t.Fatal(err)
			}
			j, _ = store.Job(j.ID)
			calls := 0
			notifications := &alerts{success: true}
			r := application.Runner{Store: store, Notifications: notifications, Log: &logs{}, Timeout: time.Second, Sync: syncFunc(func(ctx context.Context, actual domain.Job) (domain.Response, error) {
				calls++
				// The attempt is durable before I/O, and both overlap and duplicate
				// slots remain blocked throughout recovery.
				history, err := store.Recent()
				if err != nil || len(history) != 1 || history[0].Attempts != calls {
					t.Fatal("attempt not journaled before sending")
				}
				if ok, err := store.Begin(j, "other-slot"); err != nil || ok {
					t.Fatal("recovery released the execution lock")
				}
				if actual.TimeoutMS != 1000 || string(actual.Body) != string(j.Body) {
					t.Fatal("recovery changed timeout or payload")
				}
				if actual.URL == j.FallbackURL {
					if actual.Authorization != j.FallbackAuth || actual.Authorization == j.Authorization {
						t.Fatal("fallback received primary credentials")
					}
					if !tc.fallbackFails {
						return domain.Response{HTTPStatus: 204}, nil
					}
				} else if tc.recoverAt > 0 && calls == tc.recoverAt {
					return domain.Response{HTTPStatus: 200}, nil
				}
				return domain.Response{HTTPStatus: tc.status}, &domain.Failure{Code: tc.code, Status: tc.status}
			})}
			out, err := r.Run(context.Background(), j, "manual:test")
			if err != nil || out == nil || out.Status != tc.wantStatus || calls != tc.wantCalls || out.Attempts != calls || out.FallbackUsed != tc.wantFallback {
				t.Fatalf("recovery result: %#v calls=%d err=%v", out, calls, err)
			}
			history, err := store.Recent()
			if err != nil || history[0].Attempts != calls || history[0].FallbackUsed != tc.wantFallback {
				t.Fatal("attempt history lost")
			}
			if len(notifications.messages) != 1 {
				t.Fatal("intermediate attempts sent alerts")
			}
			wantKind := domain.NotificationFailure
			if tc.wantStatus == "success" {
				wantKind = domain.NotificationSuccess
			} else if tc.wantStatus == "fallback" {
				wantKind = domain.NotificationFallback
			}
			if notifications.kinds[0] != wantKind {
				t.Fatal("incorrect final notification type")
			}
			if repeated, err := r.Run(context.Background(), j, "manual:test"); err != nil || repeated != nil || calls != tc.wantCalls {
				t.Fatal("replayed a recovered execution")
			}
		})
	}
}

func TestCancellationDuringRetryDelay(t *testing.T) {
	store, err := adapters.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	j := domain.Job{ID: "test", Name: "Test", Enabled: true, RetryCount: 5, RetryDelayMS: 60000, FallbackURL: "http://127.0.0.1/fallback"}
	if err = store.Save(j, true); err != nil {
		t.Fatal(err)
	}
	j, _ = store.Job(j.ID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	notifications := &alerts{success: true}
	r := application.Runner{Store: store, Notifications: notifications, Log: &logs{}, Timeout: time.Second, Sync: syncFunc(func(context.Context, domain.Job) (domain.Response, error) {
		calls++
		time.AfterFunc(20*time.Millisecond, cancel)
		return domain.Response{HTTPStatus: 503}, &domain.Failure{Code: "HTTP_ERROR", Status: 503}
	})}
	started := time.Now()
	out, err := r.Run(ctx, j, "manual:cancel")
	if err != nil || out == nil || out.Status != "interrupted" || calls != 1 || len(notifications.messages) != 0 || time.Since(started) > 5*time.Second {
		t.Fatalf("cancellation did not stop recovery: %#v %v", out, err)
	}
}
