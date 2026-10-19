package application

import (
	"context"
	"errors"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"time"
)

type Sync interface {
	Post(context.Context, domain.Job) (string, error)
}
type Notifications interface {
	Notify(context.Context, domain.NotificationType, string) error
}
type Runs interface {
	Begin(domain.Job, string) (bool, error)
	Finish(string, string, domain.Outcome) error
}
type Logger interface{ Event(string, map[string]any) }
type Runner struct {
	Sync          Sync
	Notifications Notifications
	Store         Runs
	Log           Logger
	Timeout       time.Duration
}

func (r *Runner) Run(ctx context.Context, j domain.Job, slot string) (*domain.Outcome, error) {
	execute, err := r.Prepare(ctx, j, slot)
	if err != nil || execute == nil {
		return nil, err
	}
	return execute()
}

// Prepare durably claims a slot before asynchronous execution is acknowledged.
// The returned function must be called exactly once when non-nil.
func (r *Runner) Prepare(ctx context.Context, j domain.Job, slot string) (func() (*domain.Outcome, error), error) {
	if ctx.Err() != nil {
		return nil, nil
	}
	ok, err := r.Store.Begin(j, slot)
	if err != nil {
		r.Log.Event("journal_error", map[string]any{"job": j.ID})
		return nil, err
	}
	if !ok {
		r.Log.Event("run_skipped", map[string]any{"job": j.ID, "slot": slot})
		return nil, nil
	}
	return func() (*domain.Outcome, error) { return r.execute(ctx, j, slot) }, nil
}

func (r *Runner) execute(ctx context.Context, j domain.Job, slot string) (*domain.Outcome, error) {
	r.Log.Event("run_started", map[string]any{"job": j.ID, "slot": slot})
	outcome := domain.Outcome{Status: "success", Notification: "none"}
	timeout := r.Timeout
	if j.TimeoutMS > 0 {
		timeout = time.Duration(j.TimeoutMS) * time.Millisecond
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	authMethod, requestErr := r.Sync.Post(requestCtx, j)
	cancel()
	alert := ""
	kind := domain.NotificationSuccess
	if requestErr != nil {
		if ctx.Err() != nil {
			outcome.Status = "interrupted"
		} else {
			outcome.Status = "failed"
			kind = domain.NotificationFailure
			var f *domain.Failure
			if !errors.As(requestErr, &f) {
				f = &domain.Failure{Code: "request_failed"}
			}
			alert = domain.ErrorAlert(j, f)
			r.Log.Event("request_failed", map[string]any{"job": j.ID, "code": f.Code, "status": f.Status})
		}
	} else if domain.IsYouTube(j) && authMethod == "api-key" {
		outcome.Status = "fallback"
		kind = domain.NotificationFallback
		alert = domain.FallbackAlert(j)
	} else {
		alert = "Task completed successfully: " + j.Name + "\nTask ID: " + j.ID
	}
	if alert != "" {
		alertCtx, stop := context.WithTimeout(ctx, 15*time.Second)
		err := r.Notifications.Notify(alertCtx, kind, alert)
		stop()
		if errors.Is(err, domain.ErrNotificationsDisabled) {
			outcome.Notification = "none"
		} else if err != nil {
			outcome.Notification = "failed"
			r.Log.Event("notification_failed", map[string]any{"job": j.ID})
		} else {
			outcome.Notification = "sent"
		}
	}
	if err := r.Store.Finish(j.ID, slot, outcome); err != nil {
		r.Log.Event("journal_error", map[string]any{"job": j.ID})
		return &outcome, err
	}
	r.Log.Event("run_finished", map[string]any{"job": j.ID, "slot": slot, "status": outcome.Status, "notification": outcome.Notification})
	return &outcome, nil
}
