package adapters

import (
	"context"
	"database/sql"
	"errors"
	"github.com/fixtse/SchedulerSE/internal/application"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"sync"
	"time"
)

type Scheduler struct {
	Store    *Store
	Runner   *application.Runner
	Timezone string
	Log      application.Logger
	reload   chan struct{}
}

func NewScheduler(s *Store, r *application.Runner, tz string, log application.Logger) *Scheduler {
	return &Scheduler{Store: s, Runner: r, Timezone: tz, Log: log, reload: make(chan struct{}, 1)}
}
func (s *Scheduler) Reload() {
	select {
	case s.reload <- struct{}{}:
	default:
	}
}

// One timer sleeps until the next slot. Reloading computes future slots only.
func (s *Scheduler) Serve(ctx context.Context) {
	var active sync.WaitGroup
	defer active.Wait()
	for ctx.Err() == nil {
		timezone := s.Timezone
		settings, settingsErr := s.Store.AppSettings()
		if settingsErr == nil {
			timezone = settings.Timezone
		} else if !errors.Is(settingsErr, sql.ErrNoRows) {
			s.Log.Event("settings_error", nil)
			select {
			case <-ctx.Done():
				return
			case <-s.reload:
			case <-time.After(5 * time.Second):
			}
			continue
		}
		jobs, err := s.Store.Jobs()
		if err != nil {
			s.Log.Event("scheduler_error", nil)
		}
		telegram, settingsErr := s.Store.Telegram()
		if settingsErr != nil {
			s.Log.Event("notification_settings_error", nil)
		}
		type scheduled struct {
			job domain.Job
			at  time.Time
		}
		upcoming := []scheduled{}
		var next time.Time
		now := time.Now()
		for _, j := range jobs {
			if !j.Enabled || j.Cron == "" {
				continue
			}
			if settingsErr != nil || ValidateRunnable(j, telegram) != nil {
				s.Log.Event("job_not_ready", map[string]any{"job": j.ID})
				continue
			}
			spec, parseErr := ParseSchedule(j.Cron, JobTimezone(j, timezone))
			if parseErr != nil {
				s.Log.Event("scheduler_error", map[string]any{"job": j.ID})
				continue
			}
			at := spec.Next(now)
			if at.IsZero() {
				continue
			}
			upcoming = append(upcoming, scheduled{j, at})
			if next.IsZero() || at.Before(next) {
				next = at
			}
		}
		delay := 24 * time.Hour
		if !next.IsZero() {
			delay = time.Until(next)
		}
		if err != nil || settingsErr != nil {
			delay = time.Second * 5
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.reload:
			timer.Stop()
			continue
		case <-timer.C:
			// Do not backfill a slot after host suspension or a long scheduling pause.
			if time.Since(next) > time.Second {
				continue
			}
			telegram, settingsErr = s.Store.Telegram()
			if settingsErr != nil {
				s.Log.Event("notification_settings_error", nil)
				continue
			}
			for _, item := range upcoming {
				if !item.at.Equal(next) || ValidateRunnable(item.job, telegram) != nil {
					continue
				}
				active.Add(1)
				go func(j domain.Job, slot string) { defer active.Done(); _, _ = s.Runner.Run(ctx, j, slot) }(item.job, next.UTC().Format(time.RFC3339))
			}
		}
	}
}
